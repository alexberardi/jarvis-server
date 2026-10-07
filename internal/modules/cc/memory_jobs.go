package cc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
	"github.com/alexberardi/jarvis-server/internal/platform/scheduler"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// Memory's background work (docs/cc/04 §2.3, §3.4-3.5, §11 "Jobs"; D27 loops are scheduler
// triggers; LD8 LLM work runs on the background label through the durable queue):
//
//   - cc.memory_extract_tick (every 300 s): releases transcripts whose extraction job is gone,
//     then claims each (user, household)'s unprocessed transcripts for one cc.memory_extract
//     job, stamping them with the job id in the same transaction as the enqueue.
//   - cc.memory_extract: the background LLM extracts facts; they are deduplicated against the
//     user's memories by vector and saved as passive memories.
//   - cc.memory_embed (every 60 s, D11): embeds every active memory without a vector of the
//     current embedding model, which is also the LD6 re-embed after a model change.
//   - cc.memory_cleanup (every 30 min): hard-deletes expired and deactivated memories (D40
//     04.Q6) and transcripts past memory.transcript_ttl_days.

const (
	memExtractTickJob = "cc.memory_extract_tick"
	memExtractJob     = "cc.memory_extract"
	memEmbedJob       = "cc.memory_embed"
	memCleanupJob     = "cc.memory_cleanup"
)

const (
	extractionBatchSize  = 20   // transcripts per extraction job
	extractionProfileMax = 1000 // "existing memories" context for dedup
	embedSweepBatch      = 100
	embedSweepMaxBatches = 10 // per tick: an LD6 model switch re-embeds 1000 rows a minute
)

func (m *Module) registerMemoryJobs() {
	q := m.deps.Queue
	if q == nil {
		return
	}
	q.Register(memExtractTickJob, queue.Handler{Run: m.runExtractionTick, MaxAttempts: 1})
	q.Register(memExtractJob, queue.Handler{Run: m.runExtraction, MaxAttempts: 3, Lease: 15 * time.Minute})
	q.Register(memEmbedJob, queue.Handler{Run: m.runEmbedSweep, MaxAttempts: 1, Lease: 15 * time.Minute})
	q.Register(memCleanupJob, queue.Handler{Run: m.runMemoryCleanup, MaxAttempts: 1})
}

// startMemory schedules the memory loops.
func (m *Module) startMemory(ctx context.Context) error {
	if m.deps.Scheduler == nil || m.deps.Queue == nil {
		return nil
	}
	for _, t := range []scheduler.Trigger{
		{Name: memExtractTickJob, Kind: scheduler.KindInterval, JobType: memExtractTickJob,
			Spec: scheduler.Spec{Every: 300 * time.Second}},
		{Name: memEmbedJob, Kind: scheduler.KindInterval, JobType: memEmbedJob,
			Spec: scheduler.Spec{Every: 60 * time.Second, StartNow: true}},
		{Name: memCleanupJob, Kind: scheduler.KindInterval, JobType: memCleanupJob,
			Spec: scheduler.Spec{Every: 30 * time.Minute, StartNow: true}},
	} {
		if err := m.deps.Scheduler.Ensure(ctx, t); err != nil {
			return err
		}
	}
	return nil
}

// --- extraction: the tick ---

// extractPayload is a cc.memory_extract job.
type extractPayload struct {
	UserID      int64  `json:"user_id"`
	HouseholdID string `json:"household_id"`
}

func extractDedupKey(userID int64, hh string) string {
	return "extract:" + strconv.FormatInt(userID, 10) + ":" + hh
}

func (m *Module) runExtractionTick(ctx context.Context, _ queue.Job) ([]byte, error) {
	return nil, m.extractionTick(ctx)
}

func (m *Module) extractionTick(ctx context.Context) error {
	if err := m.releaseStaleExtractions(ctx); err != nil {
		return err
	}
	rows, err := m.deps.DB.Read.QueryContext(ctx, `SELECT DISTINCT user_id, household_id FROM cc_conversation_transcripts
		WHERE is_processed = 0 AND extraction_job_id IS NULL`)
	if err != nil {
		return err
	}
	type pair struct {
		uid int64
		hh  string
	}
	var pairs []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.uid, &p.hh); err != nil {
			rows.Close()
			return err
		}
		pairs = append(pairs, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, p := range pairs {
		// D19: extraction is per household; with it off nothing new is mined (the rows age
		// out with the transcript TTL).
		if !m.extractionEnabled(ctx, p.hh) {
			continue
		}
		// A user who is no longer a member of the household is never mined for it.
		if _, member, err := m.Auth.HouseholdRole(ctx, p.uid, p.hh); err != nil || !member {
			continue
		}
		if err := m.claimExtraction(ctx, p.uid, p.hh); err != nil {
			m.deps.Log.Warn("cc: extraction enqueue failed", "user_id", p.uid, "err", err)
		}
	}
	return nil
}

func (m *Module) extractionEnabled(ctx context.Context, hh string) bool {
	sc := settings.Scope{HouseholdID: hh}
	return m.settings.Bool(ctx, settingMemoryEnabled, sc) && m.settings.Bool(ctx, settingExtractionEnabled, sc)
}

// claimExtraction enqueues one job for the pair's oldest unprocessed transcripts and stamps
// them with its id, atomically. A live job for the pair (dedup key) means nothing to do.
func (m *Module) claimExtraction(ctx context.Context, uid int64, hh string) error {
	payload, _ := json.Marshal(extractPayload{UserID: uid, HouseholdID: hh})
	enqueued := false
	err := m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id FROM cc_conversation_transcripts
			WHERE user_id = ? AND household_id = ? AND is_processed = 0 AND extraction_job_id IS NULL
			ORDER BY created_at, id LIMIT ?`, uid, hh, extractionBatchSize)
		if err != nil {
			return err
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil || len(ids) == 0 {
			return err
		}
		jobID, err := m.deps.Queue.EnqueueTx(ctx, tx, memExtractJob, payload, queue.Options{DedupKey: extractDedupKey(uid, hh)})
		if errors.Is(err, queue.ErrDuplicate) {
			return nil
		}
		if err != nil {
			return err
		}
		stamp := strconv.FormatInt(jobID, 10)
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx, `UPDATE cc_conversation_transcripts SET extraction_job_id = ? WHERE id = ?`, stamp, id); err != nil {
				return err
			}
		}
		enqueued = true
		return nil
	})
	if err == nil && enqueued {
		m.deps.Queue.Notify(memExtractJob)
	}
	return err
}

// releaseStaleExtractions un-stamps transcripts whose job is no longer queued or running
// (failed for good, cancelled, or purged): by the job's state, not the transcript's age
// (fixes 04 §8.8, where a still-queued job's transcripts were re-claimed after 30 minutes).
func (m *Module) releaseStaleExtractions(ctx context.Context) error {
	rows, err := m.deps.DB.Read.QueryContext(ctx, `SELECT DISTINCT extraction_job_id FROM cc_conversation_transcripts
		WHERE is_processed = 0 AND extraction_job_id IS NOT NULL`)
	if err != nil {
		return err
	}
	var stamps []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			rows.Close()
			return err
		}
		stamps = append(stamps, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, s := range stamps {
		live := false
		if id, err := strconv.ParseInt(s, 10, 64); err == nil {
			info, err := m.deps.Queue.Get(ctx, id)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			live = err == nil && info.Type == memExtractJob && (info.State == queue.Queued || info.State == queue.Running)
		}
		if live {
			continue
		}
		if _, err := m.deps.DB.Write.ExecContext(ctx, `UPDATE cc_conversation_transcripts SET extraction_job_id = NULL
			WHERE extraction_job_id = ? AND is_processed = 0`, s); err != nil {
			return err
		}
	}
	return nil
}

// --- extraction: the job ---

type transcriptForExtraction struct {
	user      string
	assistant sql.NullString
	toolCalls sql.NullString
}

func (m *Module) runExtraction(ctx context.Context, job queue.Job) ([]byte, error) {
	var p extractPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return nil, queue.Permanent(err)
	}
	stamp := strconv.FormatInt(job.ID, 10)
	ts, err := m.claimedTranscripts(ctx, stamp)
	if err != nil {
		return nil, err
	}
	if len(ts) == 0 { // purged (D20) or released: nothing to do
		return nil, nil
	}
	if !m.extractionEnabled(ctx, p.HouseholdID) {
		return nil, m.markExtractionProcessed(ctx, m.deps.DB.Write, stamp)
	}
	if m.LLM == nil {
		return nil, errors.New("no llm module")
	}
	existing, err := m.profileText(ctx, m.deps.DB.Read, p.UserID, p.HouseholdID, extractionProfileMax)
	if err != nil {
		return nil, err
	}
	if existing == "" {
		existing = "None stored yet."
	}
	temp, think := 0.0, 0
	resp, err := m.LLM.Chat(ctx, llm.ChatRequest{
		Label:           llm.LabelBackground,
		Temperature:     &temp,  // greedy: structured, factual, stable run to run
		ReasoningBudget: &think, // thinking off: it only burns the shared GPU here
		Messages: []llm.Message{
			{Role: "system", Content: llm.TextContent(extractionSystemPrompt)},
			{Role: "user", Content: llm.TextContent(extractionUserMessage(existing, ts))},
		},
	})
	if err != nil {
		return nil, err // retried; after the last attempt the tick releases the transcripts
	}
	mems := parseExtractionResponse(resp.Content)

	// Embed up front (best effort) to skip near-duplicates of the user's memories and store
	// the vector with the row.
	var vecs [][]byte
	model := ""
	if len(mems) > 0 {
		texts := make([]string, len(mems))
		for i, mm := range mems {
			texts[i] = mm.content
		}
		ectx, cancel := context.WithTimeout(ctx, time.Minute)
		vecs, model, err = m.embedTexts(ectx, texts)
		cancel()
		if err != nil {
			m.deps.Log.Warn("cc: extraction dedup embed failed, saving without dedup", "err", err)
			vecs = make([][]byte, len(mems))
		}
	}
	saved, skipped := 0, 0
	err = m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		// The tombstone: if the user was purged (or the rows released) meanwhile, the
		// transcripts this job claimed are gone and its result is dropped.
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM cc_conversation_transcripts
			WHERE extraction_job_id = ? AND is_processed = 0`, stamp).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		uid := p.UserID
		for i, mm := range mems {
			if vecs[i] != nil {
				dup, err := m.similarDuplicate(ctx, tx, memScope{HouseholdID: p.HouseholdID, UserID: &uid},
					unpackVector(vecs[i]), model, 0)
				if err != nil {
					return err
				}
				if dup != 0 {
					skipped++
					continue
				}
			}
			res, err := m.saveMemory(ctx, tx, memWrite{UserID: &uid, HouseholdID: p.HouseholdID, Content: mm.content,
				Category: mm.category, Key: mm.key, Source: "passive", ExpiresAt: mm.expiresAt(m.now()),
				Vec: vecs[i], Model: model})
			if err != nil {
				return err
			}
			if res.Skipped {
				skipped++
			} else {
				saved++
			}
		}
		return m.markExtractionProcessed(ctx, tx, stamp)
	})
	if err != nil {
		return nil, err
	}
	m.deps.Log.Info("cc: memory extraction done", "user_id", p.UserID, "transcripts", len(ts),
		"extracted", len(mems), "saved", saved, "deduped", skipped)
	return nil, nil
}

func (m *Module) claimedTranscripts(ctx context.Context, stamp string) ([]transcriptForExtraction, error) {
	rows, err := m.deps.DB.Read.QueryContext(ctx, `SELECT user_message, assistant_message, tool_calls_json
		FROM cc_conversation_transcripts WHERE extraction_job_id = ? AND is_processed = 0 ORDER BY created_at, id`, stamp)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []transcriptForExtraction
	for rows.Next() {
		var t transcriptForExtraction
		if err := rows.Scan(&t.user, &t.assistant, &t.toolCalls); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (m *Module) markExtractionProcessed(ctx context.Context, q dbtx, stamp string) error {
	_, err := q.ExecContext(ctx, `UPDATE cc_conversation_transcripts SET is_processed = 1, processed_at = ?
		WHERE extraction_job_id = ?`, dbTime(m.now()), stamp)
	return err
}

// extractionUserMessage is _enqueue_extraction's user turn: existing memories, then each
// transcript as "User: …", "\nJarvis: …" and "\n[Tools called: a, b]", joined by "\n---\n".
func extractionUserMessage(existing string, ts []transcriptForExtraction) string {
	lines := make([]string, len(ts))
	for i, t := range ts {
		line := "User: " + t.user
		if t.assistant.Valid && t.assistant.String != "" {
			line += "\nJarvis: " + t.assistant.String
		}
		if t.toolCalls.Valid && t.toolCalls.String != "" {
			if names := toolCallNames(t.toolCalls.String); len(names) > 0 {
				line += "\n[Tools called: " + strings.Join(names, ", ") + "]"
			}
		}
		lines[i] = line
	}
	return "Existing memories for this user:\n" + existing + "\n\nRecent conversations:\n" + strings.Join(lines, "\n---\n")
}

// toolCallNames reads the tool names of a stored tool_calls list: the flat {"name": …}
// shape legacy mobile wrote, or the OpenAI {"function": {"name": …}} shape voice turns write
// (legacy read only the first, so voice calls showed as "?").
func toolCallNames(raw string) []string {
	v, err := pyjson.Loads(raw)
	if err != nil {
		return nil
	}
	list, _ := v.([]any)
	var names []string
	for _, e := range list {
		o, ok := e.(*pyjson.Object)
		if !ok {
			continue
		}
		name := "?"
		if n, ok := o.Get("name"); ok {
			name = pyjson.Str(n)
		} else if f, ok := o.Get("function"); ok {
			if fo, ok := f.(*pyjson.Object); ok {
				if n, ok := fo.Get("name"); ok {
					name = pyjson.Str(n)
				}
			}
		}
		names = append(names, name)
	}
	return names
}

// extractedMemory is one validated item of the extractor's output.
type extractedMemory struct {
	content, category, key string
	ttlDays                any
}

// expiresAt is _expires_at_from_ttl: absent or unparseable means permanent (fail safe:
// keep rather than expire); an integer (or integer string, or a float truncated) is days.
func (e extractedMemory) expiresAt(now time.Time) *time.Time {
	var days int64
	switch x := e.ttlDays.(type) {
	case *big.Int:
		if !x.IsInt64() || x.Int64() > 1e6 || x.Int64() < -1e6 { // past datetime's range: keep it
			return nil
		}
		days = x.Int64()
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil
		}
		if x > 1e6 || x < -1e6 {
			return nil
		}
		days = int64(x)
	case bool:
		if x {
			days = 1
		}
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		if err != nil {
			return nil
		}
		days = n
	default:
		return nil
	}
	t := now.Add(time.Duration(days) * 24 * time.Hour)
	return &t
}

var (
	extractThinkRE = regexp.MustCompile(`<think>[\s\S]*?</think>`)
	extractFenceRE = regexp.MustCompile("```(?:json)?\\s*\\n?")
	extractArrayRE = regexp.MustCompile(`\[[\s\S]*\]`)
)

// parseExtractionResponse is _parse_extraction_response: strip closed <think> blocks; salvage
// a truncated one from the first "["; strip code fences; parse the JSON array (or the first
// [...] span); keep objects with a non-empty string content.
func parseExtractionResponse(content string) []extractedMemory {
	cleaned := strings.TrimSpace(extractThinkRE.ReplaceAllString(content, ""))
	if strings.Contains(cleaned, "<think>") {
		if i := strings.Index(cleaned, "["); i >= 0 {
			cleaned = cleaned[i:]
		} else {
			cleaned = ""
		}
	}
	cleaned = strings.TrimSpace(extractFenceRE.ReplaceAllString(cleaned, ""))
	v, err := pyjson.Loads(cleaned)
	if err != nil {
		match := extractArrayRE.FindString(cleaned)
		if match == "" {
			return nil
		}
		if v, err = pyjson.Loads(match); err != nil {
			return nil
		}
	}
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	var out []extractedMemory
	for _, e := range list {
		o, ok := e.(*pyjson.Object)
		if !ok {
			continue
		}
		c, _ := o.Get("content")
		cs, ok := c.(string)
		if !ok || cs == "" {
			continue
		}
		mm := extractedMemory{content: cs, category: "general"}
		if cat, ok := o.Get("category"); ok {
			if s, ok := cat.(string); ok {
				mm.category = s
			}
		}
		if k, ok := o.Get("key"); ok {
			if s, ok := k.(string); ok {
				mm.key = s
			}
		}
		if t, ok := o.Get("ttl_days"); ok {
			mm.ttlDays = t
		}
		out = append(out, mm)
	}
	return out
}

// --- the embedding sweep (D11) and re-embed (LD6) ---

func (m *Module) runEmbedSweep(ctx context.Context, _ queue.Job) ([]byte, error) {
	_, err := m.embedSweep(ctx)
	return nil, err
}

// embedSweep embeds active rows that have no vector of the current embedding model. With no
// embeddings engine it does nothing (recall stays on keyword search). It returns how many
// rows it embedded.
func (m *Module) embedSweep(ctx context.Context) (int, error) {
	if m.LLM == nil {
		return 0, nil
	}
	cur, err := m.LLM.Embed(ctx, nil)
	if err != nil {
		m.deps.Log.Debug("cc: embedding sweep skipped: no embeddings engine", "err", err)
		return 0, nil
	}
	total := 0
	for range embedSweepMaxBatches {
		rows, err := m.deps.DB.Read.QueryContext(ctx, `SELECT id, content FROM cc_user_memories
			WHERE is_active = 1 AND (embedding IS NULL OR embedding_model IS NULL OR embedding_model != ?)
			ORDER BY id LIMIT ?`, cur.Model, embedSweepBatch)
		if err != nil {
			return total, err
		}
		var ids []int64
		var texts []string
		for rows.Next() {
			var id int64
			var c string
			if err := rows.Scan(&id, &c); err != nil {
				rows.Close()
				return total, err
			}
			ids, texts = append(ids, id), append(texts, c)
		}
		rows.Close()
		if err := rows.Err(); err != nil || len(ids) == 0 {
			return total, err
		}
		vecs, model, err := m.embedTexts(ctx, texts)
		if err != nil {
			m.deps.Log.Warn("cc: embedding sweep failed", "err", err)
			return total, nil
		}
		if model != cur.Model { // the engine switched mid-sweep: the next tick starts over
			return total, nil
		}
		n := 0
		err = m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
			for i, id := range ids {
				if vecs[i] == nil {
					continue
				}
				if err := setVector(ctx, tx, id, texts[i], vecs[i], model); err != nil {
					return err
				}
				n++
			}
			return nil
		})
		total += n
		if err != nil {
			return total, err
		}
		if n == 0 || len(ids) < embedSweepBatch { // done, or nothing embeddable left
			break
		}
	}
	if total > 0 {
		m.deps.Log.Info("cc: embedded memories", "count", total, "model", cur.Model)
	}
	return total, nil
}

// --- cleanup ---

func (m *Module) runMemoryCleanup(ctx context.Context, _ queue.Job) ([]byte, error) {
	return nil, m.memoryCleanup(ctx)
}

// memoryCleanup hard-deletes expired memories and deactivated ones (only a mobile PUT
// is_active=false makes those now), and transcripts past their TTL.
func (m *Module) memoryCleanup(ctx context.Context) error {
	now := m.now()
	if _, err := m.deps.DB.Write.ExecContext(ctx, `DELETE FROM cc_user_memories
		WHERE is_active = 0 OR (expires_at IS NOT NULL AND expires_at <= ?)`, dbTime(now)); err != nil {
		return err
	}
	days := m.settings.Int(ctx, settingTranscriptTTLDays, settings.Scope{})
	if days <= 0 {
		return nil
	}
	_, err := m.deps.DB.Write.ExecContext(ctx, `DELETE FROM cc_conversation_transcripts WHERE created_at < ?`,
		dbTime(now.Add(-time.Duration(days)*24*time.Hour)))
	return err
}
