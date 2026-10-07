package cc

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/db"
	"github.com/alexberardi/jarvis-server/internal/platform/scheduler"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

func TestMemoryExtraction(t *testing.T) {
	me := newMemEnv(t)
	ctx := context.Background()
	me.save(memWrite{UserID: i64(7), HouseholdID: voiceHH, Content: "Has a brother named Mike", Category: "fact"})
	if _, err := me.m.embedSweep(ctx); err != nil {
		t.Fatal(err)
	}
	t1 := me.addTranscript(7, voiceHH, "set a timer, I'm grilling steaks for my brother Mike", "Timer set.")
	me.exec(`UPDATE cc_conversation_transcripts SET tool_calls_json = ? WHERE id = ?`,
		`[{"id": "c1", "type": "function", "function": {"name": "set_timer", "arguments": "{}"}, "failure_message": null}]`, t1)
	me.addTranscript(7, voiceHH, "dentist on friday", "")
	me.addTranscript(8, voiceHH, "sam talks", "")
	me.addTranscript(11, voiceHH, "not a member here", "")

	if err := me.m.extractionTick(ctx); err != nil {
		t.Fatal(err)
	}
	// One job per member pair; a non-member's transcripts are never mined.
	if n := me.count(`SELECT count(*) FROM platform_jobs WHERE type = ?`, memExtractJob); n != 2 {
		t.Fatalf("jobs %d", n)
	}
	if me.count(`SELECT count(*) FROM cc_conversation_transcripts WHERE user_id = 11 AND extraction_job_id IS NULL`) != 1 {
		t.Fatal("non-member transcript claimed")
	}
	// A second tick claims nothing new (dedup key + stamps).
	if err := me.m.extractionTick(ctx); err != nil {
		t.Fatal(err)
	}
	if n := me.count(`SELECT count(*) FROM platform_jobs WHERE type = ?`, memExtractJob); n != 2 {
		t.Fatalf("re-claimed: %d jobs", n)
	}
	var job7 int64
	if err := me.d.Read.QueryRow(`SELECT id FROM platform_jobs WHERE dedup_key = ?`, extractDedupKey(7, voiceHH)).Scan(&job7); err != nil {
		t.Fatal(err)
	}
	j, _ := me.queuedJob(memExtractJob)
	for j.ID != job7 {
		me.setJobState(j.ID, "done")
		j, _ = me.queuedJob(memExtractJob)
	}

	me.eng.say("<think>Mike is a brother, already stored.</think>\n```json\n[" +
		`{"category": "fact", "key": "brother", "content": "Has a brother named Mike"},` +
		`{"category": "preference", "key": "cooking_style", "content": "Enjoys grilling"},` +
		`{"category": "note", "key": "dentist", "content": "Dentist appointment Friday", "ttl_days": 7}` +
		"]\n```")
	if _, err := me.m.runExtraction(ctx, j); err != nil {
		t.Fatal(err)
	}
	req := me.eng.last()
	if req["temperature"] != 0.0 {
		t.Fatalf("extraction must be greedy: %v", req["temperature"])
	}
	msgs := messagesOf(req)
	if msgs[0]["content"] != extractionSystemPrompt {
		t.Fatal("system prompt")
	}
	user := msgs[1]["content"].(string)
	want := "Existing memories for this user:\n- Has a brother named Mike\n\nRecent conversations:\n" +
		"User: set a timer, I'm grilling steaks for my brother Mike\nJarvis: Timer set.\n[Tools called: set_timer]\n---\nUser: dentist on friday"
	if user != want {
		t.Fatalf("user message\n%q\nwant\n%q", user, want)
	}
	// The duplicate is skipped by vector; the others are passive, vectors stored, TTL applied.
	if me.count(`SELECT count(*) FROM cc_user_memories WHERE content = 'Has a brother named Mike'`) != 1 {
		t.Fatal("near-duplicate saved")
	}
	if me.count(`SELECT count(*) FROM cc_user_memories WHERE source = 'passive' AND user_id = 7 AND embedding_model = 'minilm-a'`) != 2 {
		t.Fatal("passive memories")
	}
	if me.count(`SELECT count(*) FROM cc_user_memories WHERE key = 'dentist' AND expires_at = ?`, dbTime(me.clock().AddDate(0, 0, 7))) != 1 {
		t.Fatal("ttl_days")
	}
	if me.count(`SELECT count(*) FROM cc_conversation_transcripts WHERE user_id = 7 AND is_processed = 1`) != 2 {
		t.Fatal("transcripts not processed")
	}
}

func TestMemoryExtractionReleaseTombstoneGates(t *testing.T) {
	me := newMemEnv(t)
	ctx := context.Background()
	me.addTranscript(7, voiceHH, "my cat is Tom", "")
	if err := me.m.extractionTick(ctx); err != nil {
		t.Fatal(err)
	}
	j, ok := me.queuedJob(memExtractJob)
	if !ok {
		t.Fatal("no job")
	}
	// A still-queued job keeps its transcripts, however old they get (fixes 04 §8.8).
	me.advance(2 * time.Hour)
	if err := me.m.releaseStaleExtractions(ctx); err != nil {
		t.Fatal(err)
	}
	if me.count(`SELECT count(*) FROM cc_conversation_transcripts WHERE extraction_job_id IS NOT NULL`) != 1 {
		t.Fatal("live job's transcripts released")
	}
	// A job that failed for good releases them, and the next tick re-claims.
	me.setJobState(j.ID, "failed")
	if err := me.m.extractionTick(ctx); err != nil {
		t.Fatal(err)
	}
	j2, ok := me.queuedJob(memExtractJob)
	if !ok || j2.ID == j.ID {
		t.Fatal("not re-claimed")
	}

	// Tombstone (D20): the user is purged while the job runs; its result is dropped.
	if err := me.d.Tx(ctx, func(tx *sql.Tx) error { return me.m.PurgeUser(ctx, tx, 7) }); err != nil {
		t.Fatal(err)
	}
	before := len(me.eng.requests())
	if _, err := me.m.runExtraction(ctx, j2); err != nil {
		t.Fatal(err)
	}
	if len(me.eng.requests()) != before || me.count(`SELECT count(*) FROM cc_user_memories`) != 0 {
		t.Fatal("a purged user's job ran or re-created memories")
	}

	// Purge racing a running job: the purge commits after the LLM answered, before the save.
	me.addTranscript(8, voiceHH, "I love hiking", "")
	if err := me.m.extractionTick(ctx); err != nil {
		t.Fatal(err)
	}
	j3, _ := me.queuedJob(memExtractJob)
	me.eng.say(`[{"content": "Loves hiking"}]`)
	me.emb.mu.Lock()
	me.emb.hook = func() {
		if err := me.d.Tx(ctx, func(tx *sql.Tx) error { return me.m.PurgeUser(ctx, tx, 8) }); err != nil {
			t.Error(err)
		}
	}
	me.emb.mu.Unlock()
	if _, err := me.m.runExtraction(ctx, j3); err != nil {
		t.Fatal(err)
	}
	if me.count(`SELECT count(*) FROM cc_user_memories`) != 0 {
		t.Fatal("late result re-created a purged user's memory")
	}

	// D19 gates: extraction off for the household means no jobs.
	me.set(settingExtractionEnabled, false, settings.Scope{HouseholdID: voiceHH})
	me.addTranscript(9, voiceHH, "power user talks", "")
	if err := me.m.extractionTick(ctx); err != nil {
		t.Fatal(err)
	}
	if n := me.count(`SELECT count(*) FROM platform_jobs WHERE dedup_key = ?`, extractDedupKey(9, voiceHH)); n != 0 {
		t.Fatal("extraction off still enqueued")
	}
}

func TestMemoryCleanup(t *testing.T) {
	me := newMemEnv(t)
	ctx := context.Background()
	past := me.clock().Add(-time.Minute)
	future := me.clock().Add(time.Hour)
	me.save(memWrite{HouseholdID: voiceHH, Content: "expired", ExpiresAt: &past})
	me.save(memWrite{HouseholdID: voiceHH, Content: "fresh", ExpiresAt: &future})
	inactive := me.save(memWrite{UserID: i64(7), HouseholdID: voiceHH, Content: "deactivated"})
	me.exec(`UPDATE cc_user_memories SET is_active = 0 WHERE id = ?`, inactive)
	me.addTranscript(7, voiceHH, "old", "")
	me.advance(8 * 24 * time.Hour)
	me.addTranscript(7, voiceHH, "new", "")
	me.save(memWrite{HouseholdID: voiceHH, Content: "permanent"})
	if err := me.m.memoryCleanup(ctx); err != nil {
		t.Fatal(err)
	}
	var left []string
	rows, _ := me.d.Read.Query(`SELECT content FROM cc_user_memories ORDER BY id`)
	for rows.Next() {
		var c string
		_ = rows.Scan(&c)
		left = append(left, c)
	}
	rows.Close()
	if strings.Join(left, ",") != "permanent" {
		t.Fatalf("left %v", left) // "fresh" expired too: the clock moved 8 days
	}
	if me.count(`SELECT count(*) FROM cc_conversation_transcripts WHERE user_message = 'new'`) != 1 ||
		me.count(`SELECT count(*) FROM cc_conversation_transcripts`) != 1 {
		t.Fatal("transcript TTL")
	}
}

func TestMemoryLoopsScheduled(t *testing.T) {
	me := newMemEnv(t)
	ctx := context.Background()
	if err := db.Migrate(ctx, me.d, scheduler.MigrationModule, scheduler.Migrations()); err != nil {
		t.Fatal(err)
	}
	me.m.deps.Scheduler = scheduler.New(me.d, me.q, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := me.m.startMemory(ctx); err != nil {
		t.Fatal(err)
	}
	if err := me.m.startMemory(ctx); err != nil { // idempotent across restarts
		t.Fatal(err)
	}
	for _, name := range []string{memExtractTickJob, memEmbedJob, memCleanupJob} {
		if me.count(`SELECT count(*) FROM platform_triggers WHERE name = ? AND job_type = ?`, name, name) != 1 {
			t.Errorf("trigger %s", name)
		}
	}
}
