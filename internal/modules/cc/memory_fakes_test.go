package cc

import (
	"context"
	"database/sql"
	"errors"
	"hash/fnv"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/db"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
)

// fakeEmbedder is a deterministic bag-of-words embedder: texts sharing words are similar,
// identical texts score 1. model is the tag it reports (LD6).
type fakeEmbedder struct {
	mu    sync.Mutex
	model string
	down  bool
	calls int
	hook  func() // runs (once) before the next Embed call
}

const fakeEmbedDims = 256

func (f *fakeEmbedder) Embed(_ context.Context, texts []string) (llm.Embeddings, error) {
	f.mu.Lock()
	hook := f.hook
	f.hook = nil
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return llm.Embeddings{}, errors.New("no embeddings engine")
	}
	f.calls++
	out := make([][]float64, len(texts))
	for i, t := range texts {
		v := make([]float64, fakeEmbedDims)
		for _, w := range strings.Fields(strings.ToLower(t)) {
			h := fnv.New32a()
			_, _ = h.Write([]byte(strings.Trim(w, ".,!?'\"")))
			v[h.Sum32()%fakeEmbedDims]++
		}
		out[i] = v
	}
	return llm.Embeddings{Vectors: out, Model: f.model}, nil
}

func (f *fakeEmbedder) set(model string, down bool) {
	f.mu.Lock()
	f.model, f.down = model, down
	f.mu.Unlock()
}

// memLLM is the real llm.Service (chat against the fake engine) with fake embeddings.
type memLLM struct {
	*llm.Service
	emb *fakeEmbedder
}

func (l memLLM) Embed(ctx context.Context, texts []string) (llm.Embeddings, error) {
	return l.emb.Embed(ctx, texts)
}

// memEnv is the voice environment plus fake embeddings, a durable queue and more users:
// 7 alex and 8 sam (members), 9 a power user, 10 an admin in voiceHH; 11 in another
// household.
type memEnv struct {
	*voiceEnv
	emb *fakeEmbedder
	q   *queue.Queue
}

const otherHH = "hh-other"

func newMemEnv(t *testing.T) *memEnv {
	t.Helper()
	emb := &fakeEmbedder{model: "minilm-a"}
	ve := newVoiceEnv(t, prompts.Qwen3_5_9B, func(m *Module) {
		m.LLM = memLLM{m.LLM.(*llm.Service), emb}
	})
	ctx := context.Background()
	if err := db.Migrate(ctx, ve.d, queue.MigrationModule, queue.Migrations()); err != nil {
		t.Fatal(err)
	}
	q := queue.New(ve.d, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ve.m.deps.Queue = q
	ve.m.registerMemoryJobs()
	ve.auth.addUser(9, voiceHH, authn.RolePowerUser)
	ve.auth.addUser(10, voiceHH, authn.RoleAdmin)
	ve.auth.addUser(11, otherHH, authn.RoleAdmin)
	return &memEnv{voiceEnv: ve, emb: emb, q: q}
}

func i64(v int64) *int64 { return &v }

// save stores a memory directly; it returns the id.
func (me *memEnv) save(w memWrite) int64 {
	me.t.Helper()
	if w.Category == "" {
		w.Category = "general"
	}
	if w.Source == "" {
		w.Source = "voice"
	}
	res, err := me.m.saveMemory(context.Background(), me.d.Write, w)
	if err != nil {
		me.t.Fatal(err)
	}
	return res.ID
}

func (me *memEnv) count(q string, args ...any) int {
	me.t.Helper()
	var n int
	if err := me.d.Read.QueryRow(q, args...).Scan(&n); err != nil {
		me.t.Fatal(err)
	}
	return n
}

func (me *memEnv) exec(q string, args ...any) {
	me.t.Helper()
	if _, err := me.d.Write.Exec(q, args...); err != nil {
		me.t.Fatal(err)
	}
}

// addTranscript inserts a transcript row at the env's clock.
func (me *memEnv) addTranscript(uid int64, hh, user, assistant string) int64 {
	me.t.Helper()
	var id int64
	err := me.d.Write.QueryRow(`INSERT INTO cc_conversation_transcripts (user_id, household_id, conversation_id,
		user_message, assistant_message, created_at) VALUES (?, ?, 'conv', ?, ?, ?) RETURNING id`,
		uid, hh, user, assistant, dbTime(me.clock())).Scan(&id)
	if err != nil {
		me.t.Fatal(err)
	}
	return id
}

// queuedJob returns the newest job of a type as the queue would hand it to a worker.
func (me *memEnv) queuedJob(jobType string) (queue.Job, bool) {
	me.t.Helper()
	var j queue.Job
	err := me.d.Read.QueryRow(`SELECT id, type, payload, COALESCE(dedup_key, '') FROM platform_jobs
		WHERE type = ? AND state = 'queued' ORDER BY id DESC LIMIT 1`, jobType).Scan(&j.ID, &j.Type, &j.Payload, &j.DedupKey)
	if errors.Is(err, sql.ErrNoRows) {
		return j, false
	}
	if err != nil {
		me.t.Fatal(err)
	}
	j.Attempt = 1
	return j, true
}

func (me *memEnv) setJobState(id int64, state string) {
	me.exec(`UPDATE platform_jobs SET state = ? WHERE id = ?`, state, id)
}
