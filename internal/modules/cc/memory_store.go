package cc

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// Memory and knowledge (docs/cc/04): the cc_user_memories store, its vectors, the User
// Profile text (D43), semantic recall (M1, LD6), and the D20 purge. Routes live in
// memory_routes.go, the voice tools in memory_tools.go, the queue jobs in memory_jobs.go.
//
// Scope: a row with a user_id is that user's, in that household only (the same user in two
// households has disjoint memories); user_id NULL is a household row (agent context, or
// mobile scope=household). Every query filters household_id.
//
// Vectors (LD6): float32 little-endian, L2-normalised, tagged with the embedding model that
// produced them. Search compares only vectors of the current model (cosine = dot product);
// a row whose vector is missing or stale is found by keyword search until the embedding
// sweep re-embeds it.

// Setting keys owned by memory (D11: only keys something reads are declared).
const (
	settingPinnedMaxChars         = "memory.pinned_max_chars"
	settingRecallThreshold        = "memory.recall_similarity_threshold"
	settingRecallMaxResults       = "memory.recall_max_results"
	settingTranscriptTTLDays      = "memory.transcript_ttl_days"
	settingAdvancedContext        = "model.advanced_context"
	settingAgentContextEnabled    = "memory.agent_context_enabled"
	settingAgentContextMaxResults = "memory.agent_context_max_results"
	settingAgentContextMaxChars   = "memory.agent_context_max_chars"
	settingAgentContextThreshold  = "memory.agent_context_similarity_threshold"
)

// Content dedup threshold (extraction and inject), calibrated for all-MiniLM-L6-v2.
const memoryDedupThreshold = 0.9

// memoryDefinitions are the memory settings (legacy settings_definitions.py). memory.enabled,
// memory.recall_enabled and memory.extraction_enabled are declared with the voice settings.
func memoryDefinitions() []settings.Definition {
	return []settings.Definition{
		{Key: settingPinnedMaxChars, Category: "memory", Type: settings.Int, Default: int64(500),
			Description: "Maximum characters for the User Profile block in the prompt (pinned memories first, then " +
				"the rest by category priority and recency)"},
		{Key: settingRecallThreshold, Category: "memory", Type: settings.Float, Default: 0.3,
			Description: "Minimum cosine similarity for recall results (0-1)"},
		{Key: settingRecallMaxResults, Category: "memory", Type: settings.Int, Default: int64(5),
			Description: "Maximum number of recall results returned"},
		{Key: settingTranscriptTTLDays, Category: "memory", Type: settings.Int, Default: int64(7),
			Description: "Days to retain conversation transcripts before cleanup"},
		{Key: settingAdvancedContext, Category: "model", Type: settings.Bool, Default: false,
			Description: "Enable proactive context injection — the background agents' weather, calendar, news, and " +
				"reminder memories are injected into the prompt so the assistant can answer 'how's today looking?' " +
				"proactively. Agent memories are always stored (D40 04.Q10); this gates only the per-turn injection."},
		{Key: settingAgentContextEnabled, Category: "memory", Type: settings.Bool, Default: true,
			Description: "Enable agent-injected context retrieval during voice commands"},
		{Key: settingAgentContextMaxResults, Category: "memory", Type: settings.Int, Default: int64(5),
			Description: "Maximum number of agent context items to inject into the prompt"},
		{Key: settingAgentContextMaxChars, Category: "memory", Type: settings.Int, Default: int64(500),
			Description: "Maximum characters for agent context in the prompt"},
		{Key: settingAgentContextThreshold, Category: "memory", Type: settings.Float, Default: 0.25,
			Description: "Minimum cosine similarity for agent context vector search (0-1)"},
	}
}

// dbtx is *sql.DB or *sql.Tx.
type dbtx interface {
	ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

// memoryRow is one cc_user_memories row (without its vector).
type memoryRow struct {
	ID          int64
	UserID      *int64
	HouseholdID string
	Category    string
	Key         *string
	Content     string
	Source      string
	IsActive    bool
	IsPinned    bool
	CreatedAt   string
	UpdatedAt   string
	ExpiresAt   *string
}

const memoryCols = `id, user_id, household_id, category, key, content, source, is_active, is_pinned, created_at, updated_at, expires_at`

type rowScanner interface{ Scan(dest ...any) error }

func scanMemory(sc rowScanner, extra ...any) (memoryRow, error) {
	var r memoryRow
	var uid sql.NullInt64
	var key, exp sql.NullString
	dest := append([]any{&r.ID, &uid, &r.HouseholdID, &r.Category, &key, &r.Content, &r.Source, &r.IsActive,
		&r.IsPinned, &r.CreatedAt, &r.UpdatedAt, &exp}, extra...)
	if err := sc.Scan(dest...); err != nil {
		return r, err
	}
	if uid.Valid {
		v := uid.Int64
		r.UserID = &v
	}
	if key.Valid {
		r.Key = &key.String
	}
	if exp.Valid {
		r.ExpiresAt = &exp.String
	}
	return r, nil
}

func memoryByID(ctx context.Context, q dbtx, id int64) (memoryRow, error) {
	return scanMemory(q.QueryRowContext(ctx, `SELECT `+memoryCols+` FROM cc_user_memories WHERE id = ?`, id))
}

// --- vectors ---

// packVector L2-normalises v and encodes it as float32 little-endian. nil for an empty or
// zero vector.
func packVector(v []float64) []byte {
	var norm float64
	for _, x := range v {
		norm += x * x
	}
	if len(v) == 0 || norm == 0 || math.IsNaN(norm) || math.IsInf(norm, 0) {
		return nil
	}
	norm = math.Sqrt(norm)
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(float32(x/norm)))
	}
	return b
}

func unpackVector(b []byte) []float32 {
	if len(b) == 0 || len(b)%4 != 0 {
		return nil
	}
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v
}

// cosine of two normalised vectors; -2 when they can't be compared.
func cosine(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return -2
	}
	var s float64
	for i := range a {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}

// embedTimeout bounds an embedding call made on behalf of a request or tool.
const embedTimeout = 10 * time.Second

// embedTexts embeds texts through the llm module (LD6/LD7). It returns packed vectors (nil
// entries for unusable ones) and the model tag; an error when no embeddings engine exists.
func (m *Module) embedTexts(ctx context.Context, texts []string) ([][]byte, string, error) {
	if m.LLM == nil {
		return nil, "", errors.New("no llm module")
	}
	res, err := m.LLM.Embed(ctx, texts)
	if err != nil {
		return nil, "", err
	}
	if len(res.Vectors) != len(texts) {
		return nil, "", errors.New("embeddings: count mismatch")
	}
	out := make([][]byte, len(texts))
	for i, v := range res.Vectors {
		out[i] = packVector(v)
	}
	return out, res.Model, nil
}

// embedOne embeds a single text: (vector, model) or (nil, "") on any failure.
func (m *Module) embedOne(ctx context.Context, text string, timeout time.Duration) ([]float32, string) {
	ectx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	vecs, model, err := m.embedTexts(ectx, []string{text})
	if err != nil || len(vecs) != 1 || vecs[0] == nil {
		if err != nil {
			m.deps.Log.Debug("cc: embedding unavailable, keyword search only", "err", err)
		}
		return nil, ""
	}
	return unpackVector(vecs[0]), model
}

// --- candidates and search ---

// memScope selects one memory pool: a user's own rows, or the household rows (UserID nil).
type memScope struct {
	HouseholdID string
	UserID      *int64
	Category    string // "" = any
}

func (s memScope) where(now time.Time) (string, []any) {
	q := `household_id = ? AND is_active = 1 AND (expires_at IS NULL OR expires_at > ?)`
	args := []any{s.HouseholdID, dbTime(now)}
	if s.UserID != nil {
		q += ` AND user_id = ?`
		args = append(args, *s.UserID)
	} else {
		q += ` AND user_id IS NULL`
	}
	if s.Category != "" {
		q += ` AND category = ?`
		args = append(args, s.Category)
	}
	return q, args
}

type memCandidate struct {
	memoryRow
	vec   []float32
	model string
}

// candidates loads a pool with its vectors (brute force: thousands of rows per household
// score in well under a millisecond).
func (m *Module) memCandidates(ctx context.Context, q dbtx, sc memScope) ([]memCandidate, error) {
	where, args := sc.where(m.now())
	rows, err := q.QueryContext(ctx, `SELECT `+memoryCols+`, embedding, COALESCE(embedding_model, '')
		FROM cc_user_memories WHERE `+where+` ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []memCandidate
	for rows.Next() {
		var c memCandidate
		var emb []byte
		r, err := scanMemory(rows, &emb, &c.model)
		if err != nil {
			return nil, err
		}
		c.memoryRow = r
		c.vec = unpackVector(emb)
		out = append(out, c)
	}
	return out, rows.Err()
}

// scoredMemory is a search hit.
type scoredMemory struct {
	memoryRow
	Score float64
}

// vectorHits scores the candidates with a current-model vector against qvec.
func vectorHits(cands []memCandidate, qvec []float32, model string, threshold float64) []scoredMemory {
	var out []scoredMemory
	for _, c := range cands {
		if c.model != model || c.vec == nil {
			continue
		}
		if s := cosine(qvec, c.vec); s >= threshold {
			out = append(out, scoredMemory{c.memoryRow, s})
		}
	}
	sortScored(out)
	return out
}

// substringHits is search_memories_substring: words longer than 2 characters, any word as a
// case-insensitive substring, scored by matched words / total words.
func substringHits(cands []memCandidate, query string) []scoredMemory {
	var words []string
	for _, w := range strings.Fields(strings.ToLower(query)) {
		if utf8.RuneCountInString(w) > 2 {
			words = append(words, w)
		}
	}
	if len(words) == 0 {
		return nil
	}
	var out []scoredMemory
	for _, c := range cands {
		content := strings.ToLower(c.Content)
		n := 0
		for _, w := range words {
			if strings.Contains(content, w) {
				n++
			}
		}
		if n > 0 {
			out = append(out, scoredMemory{c.memoryRow, float64(n) / float64(len(words))})
		}
	}
	sortScored(out)
	return out
}

func sortScored(s []scoredMemory) {
	sort.SliceStable(s, func(i, j int) bool { return s[i].Score > s[j].Score })
}

// searchMemories is recall's search (and agent context's): vector hits over rows embedded by
// the current model; keyword hits over the rows without a current vector (LD6), or over every
// row when the vector search found nothing above threshold or embeddings are down (M1). The
// union keeps vector hits first. method is "vector", "substring" or "vector+substring".
func (m *Module) searchMemories(ctx context.Context, sc memScope, query string, limit int, threshold float64,
	embedWithin time.Duration) ([]scoredMemory, string, error) {
	cands, err := m.memCandidates(ctx, m.deps.DB.Read, sc)
	if err != nil {
		return nil, "", err
	}
	if len(cands) == 0 {
		return nil, "vector", nil
	}
	qvec, model := m.embedOne(ctx, query, embedWithin)
	var hits []scoredMemory
	pool := cands
	if qvec != nil {
		hits = vectorHits(cands, qvec, model, threshold)
		if len(hits) > 0 {
			pool = nil
			for _, c := range cands {
				if c.model != model || c.vec == nil {
					pool = append(pool, c)
				}
			}
		}
	}
	seen := map[int64]bool{}
	var out []scoredMemory
	for _, h := range hits {
		seen[h.ID] = true
		out = append(out, h)
	}
	sub := 0
	for _, h := range substringHits(pool, query) {
		if !seen[h.ID] {
			seen[h.ID] = true
			out = append(out, h)
			sub++
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	method := "vector"
	switch {
	case len(hits) == 0 && sub > 0, qvec == nil:
		method = "substring"
	case len(hits) > 0 && sub > 0:
		method = "vector+substring"
	}
	return out, method, nil
}

// similarDuplicate returns the id of an active, unexpired row in sc (other than exclude)
// whose current-model vector is within the dedup threshold of vec, or 0.
func (m *Module) similarDuplicate(ctx context.Context, q dbtx, sc memScope, vec []float32, model string, exclude int64) (int64, error) {
	cands, err := m.memCandidates(ctx, q, sc)
	if err != nil {
		return 0, err
	}
	best, bestID := -2.0, int64(0)
	for _, c := range cands {
		if c.ID == exclude || c.model != model {
			continue
		}
		if s := cosine(vec, c.vec); s >= memoryDedupThreshold && s > best {
			best, bestID = s, c.ID
		}
	}
	return bestID, nil
}

// --- the User Profile (D43) ---

// categoryPriority is _CATEGORY_PRIORITY.
func categoryPriority(c string) int {
	switch c {
	case "preference":
		return 0
	case "fact":
		return 1
	case "note":
		return 2
	case "general":
		return 3
	}
	return 99
}

// profileText is get_memories_for_prompt changed by D43: pinned first, then unpinned, each by
// category priority then most recently updated, as "- content" lines added greedily until the
// next would exceed maxChars (Python len: code points). Expired rows never appear.
func (m *Module) profileText(ctx context.Context, q dbtx, userID int64, householdID string, maxChars int) (string, error) {
	uid := userID
	where, args := memScope{HouseholdID: householdID, UserID: &uid}.where(m.now())
	rows, err := q.QueryContext(ctx, `SELECT `+memoryCols+` FROM cc_user_memories WHERE `+where, args...)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var mems []memoryRow
	for rows.Next() {
		r, err := scanMemory(rows)
		if err != nil {
			return "", err
		}
		mems = append(mems, r)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	sort.SliceStable(mems, func(i, j int) bool {
		a, b := mems[i], mems[j]
		if a.IsPinned != b.IsPinned {
			return a.IsPinned
		}
		if pa, pb := categoryPriority(a.Category), categoryPriority(b.Category); pa != pb {
			return pa < pb
		}
		if ta, tb := parseTS(a.UpdatedAt), parseTS(b.UpdatedAt); !ta.Equal(tb) {
			return ta.After(tb)
		}
		return a.ID > b.ID
	})
	var lines []string
	total := 0
	for _, r := range mems {
		line := "- " + r.Content
		n := utf8.RuneCountInString(line)
		if total+n+1 > maxChars {
			break
		}
		lines = append(lines, line)
		total += n + 1
	}
	return strings.Join(lines, "\n"), nil
}

// memoryProfile is the MemoryProfile the voice pipeline uses by default (Module.Memory).
type memoryProfile struct{ m *Module }

// ProfileText is the User Profile block's memory lines for an identified speaker ("" on any
// error: the turn goes on without them).
func (p memoryProfile) ProfileText(ctx context.Context, userID int64, householdID string) string {
	if userID == 0 || householdID == "" {
		return ""
	}
	max := int(p.m.settings.Int(ctx, settingPinnedMaxChars, settings.Scope{HouseholdID: householdID}))
	s, err := p.m.profileText(ctx, p.m.deps.DB.Read, userID, householdID, max)
	if err != nil {
		p.m.deps.Log.Warn("cc: profile load failed", "user_id", userID, "err", err)
		return ""
	}
	return s
}

// --- writes ---

// memWrite is one save_memory call.
type memWrite struct {
	UserID      *int64
	HouseholdID string
	Content     string
	Category    string
	Key         string // "" = no key: always inserts
	Source      string
	Pinned      bool
	ExpiresAt   *time.Time
	// Vec/Model store a vector computed for exactly this content (optional).
	Vec   []byte
	Model string
}

type saveResult struct {
	ID      int64
	Updated bool // a key matched an existing row
	Skipped bool // a passive write met a protected row (D40 04.Q7)
}

// saveMemory is save_memory: upsert by (household, key, user — IS NULL for household rows)
// among active rows, else insert. Changed by D40 04.Q7: a passive write never overwrites a
// pinned, ui-authored or permanent row, and only a ui write (mobile) changes is_pinned. A
// content change drops the stale vector so recall can't match the old text.
func (m *Module) saveMemory(ctx context.Context, q dbtx, w memWrite) (saveResult, error) {
	now := dbTime(m.now())
	var exp any
	if w.ExpiresAt != nil {
		exp = dbTime(*w.ExpiresAt)
	}
	var uid any
	if w.UserID != nil {
		uid = *w.UserID
	}
	var vec, model any
	if w.Vec != nil {
		vec, model = w.Vec, w.Model
	}
	if w.Key != "" {
		cond := `user_id IS NULL`
		args := []any{w.HouseholdID, w.Key}
		if w.UserID != nil {
			cond = `user_id = ?`
			args = append(args, *w.UserID)
		}
		var id int64
		var content, source string
		var pinned bool
		var oldExp sql.NullString
		err := q.QueryRowContext(ctx, `SELECT id, content, source, is_pinned, expires_at FROM cc_user_memories
			WHERE household_id = ? AND key = ? AND is_active = 1 AND `+cond+` ORDER BY id LIMIT 1`, args...).
			Scan(&id, &content, &source, &pinned, &oldExp)
		switch {
		case err == nil:
			if w.Source == "passive" && (pinned || source == "ui" || !oldExp.Valid) {
				return saveResult{ID: id, Updated: true, Skipped: true}, nil
			}
			if w.Source == "ui" {
				pinned = w.Pinned
			}
			set := `content = ?, category = ?, source = ?, is_pinned = ?, expires_at = ?, updated_at = ?`
			sargs := []any{w.Content, w.Category, w.Source, pinned, exp, now}
			switch {
			case w.Vec != nil:
				set += `, embedding = ?, embedding_model = ?`
				sargs = append(sargs, vec, model)
			case content != w.Content:
				set += `, embedding = NULL, embedding_model = NULL`
			}
			if _, err := q.ExecContext(ctx, `UPDATE cc_user_memories SET `+set+` WHERE id = ?`, append(sargs, id)...); err != nil {
				return saveResult{}, err
			}
			return saveResult{ID: id, Updated: true}, nil
		case !errors.Is(err, sql.ErrNoRows):
			return saveResult{}, err
		}
	}
	var key any
	if w.Key != "" {
		key = w.Key
	}
	var id int64
	err := q.QueryRowContext(ctx, `INSERT INTO cc_user_memories
		(user_id, household_id, category, key, content, source, is_active, is_pinned, embedding, embedding_model,
		 created_at, updated_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?) RETURNING id`,
		uid, w.HouseholdID, w.Category, key, w.Content, w.Source, w.Pinned, vec, model, now, now, exp).Scan(&id)
	return saveResult{ID: id}, err
}

// setVector stores a vector for a row, unless its content changed since it was embedded.
func setVector(ctx context.Context, q dbtx, id int64, content string, vec []byte, model string) error {
	_, err := q.ExecContext(ctx, `UPDATE cc_user_memories SET embedding = ?, embedding_model = ?
		WHERE id = ? AND content = ?`, vec, model, id, content)
	return err
}

// deleteCharacterization drops a person's synthesized view so it can't outlive what they
// asked to forget (D30; synthesis itself is dormant).
func deleteCharacterization(ctx context.Context, q dbtx, userID int64, householdID string) error {
	_, err := q.ExecContext(ctx, `DELETE FROM cc_person_characterizations WHERE user_id = ? AND household_id = ?`,
		userID, householdID)
	return err
}

// forgetMemories hard-deletes the user's active rows in the household whose content contains
// match, case-insensitively (forget_memory's ILIKE %match%; D40 04.Q6 makes it a hard
// delete), and their characterization when anything went.
func (m *Module) forgetMemories(ctx context.Context, userID int64, householdID, match string) (int, error) {
	needle := strings.ToLower(match)
	if needle == "" {
		return 0, nil
	}
	count := 0
	err := m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id, content FROM cc_user_memories
			WHERE user_id = ? AND household_id = ? AND is_active = 1`, userID, householdID)
		if err != nil {
			return err
		}
		var ids []int64
		for rows.Next() {
			var id int64
			var content string
			if err := rows.Scan(&id, &content); err != nil {
				rows.Close()
				return err
			}
			if strings.Contains(strings.ToLower(content), needle) {
				ids = append(ids, id)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx, `DELETE FROM cc_user_memories WHERE id = ?`, id); err != nil {
				return err
			}
		}
		count = len(ids)
		if count > 0 {
			return deleteCharacterization(ctx, tx, userID, householdID)
		}
		return nil
	})
	return count, err
}

// --- D20 ---

// purgeMemoryUser is memory's part of account deletion (D20), inside the deletion's
// transaction: the user's memories in every household, their characterizations, their
// personal settings, and the traces of every conversation they spoke in (legacy collected
// those through transcripts; traces also carry user_id now). In-flight extraction jobs drop
// their result: they find their transcripts gone.
func purgeMemoryUser(ctx context.Context, tx *sql.Tx, userID int64) error {
	for _, q := range []string{
		`DELETE FROM cc_request_traces WHERE conversation_id IN
			(SELECT conversation_id FROM cc_conversation_transcripts WHERE user_id = ?)`,
		`DELETE FROM cc_user_memories WHERE user_id = ?`,
		`DELETE FROM cc_person_characterizations WHERE user_id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, q, userID); err != nil {
			return err
		}
	}
	// Personal settings (legacy purged settings.user_id). The table belongs to the settings
	// service and may not exist if its migration failed; that is not a purge failure.
	if _, err := tx.ExecContext(ctx, `DELETE FROM cc_settings WHERE user_id = ?`, userID); err != nil &&
		!strings.Contains(err.Error(), "no such table") {
		return err
	}
	return nil
}

// purgeMemoryUserHousehold deletes a user's data for one household, for when they leave it (D20):
// their memories, transcripts and characterization there. Their other households are kept.
// Module.PurgeUserHousehold (households.go) calls it.
func purgeMemoryUserHousehold(ctx context.Context, tx *sql.Tx, userID int64, householdID string) error {
	for _, q := range []string{
		`DELETE FROM cc_user_memories WHERE user_id = ? AND household_id = ?`,
		`DELETE FROM cc_conversation_transcripts WHERE user_id = ? AND household_id = ?`,
		`DELETE FROM cc_person_characterizations WHERE user_id = ? AND household_id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, q, userID, householdID); err != nil {
			return err
		}
	}
	return nil
}
