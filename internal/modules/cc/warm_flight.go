package cc

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/llm"
)

// warmFlight shares identical warm-ups and remembers the recent ones (decision
// 2026-10-20-prompt-diet). Conversations opening together (a voice turn and the app's chats) used
// to send one cold warm-up each for the same prefix; on an engine that serves one request at a
// time (mlx_lm.server) a voice turn then waited behind all of them.
type warmFlight struct {
	// path, when set, is where the recent warm-ups are kept across restarts (0600; they hold
	// the household's system prompt and tools, no conversation).
	path     string
	mu       sync.Mutex
	inFlight map[string]*warmCall
	recent   []llm.ChatRequest // newest last, distinct prefixes
}

type warmCall struct {
	done chan struct{}
	err  error
}

// maxRecentWarm is how many distinct prefixes a restarted engine is warmed with again.
const maxRecentWarm = 4

// warmKey identifies a warm-up by what reaches the engine's prefix cache (priority aside).
func warmKey(r llm.ChatRequest) string {
	r.Priority = ""
	b, _ := json.Marshal(r)
	sum := sha256.Sum256(b)
	return string(sum[:])
}

// do runs fn for req unless an identical warm-up is already in flight, in which case it waits for
// that one and returns its result. A successful warm-up is remembered for rewarm.
func (w *warmFlight) do(ctx context.Context, req llm.ChatRequest, fn func(context.Context) error) error {
	key := warmKey(req)
	w.mu.Lock()
	if w.inFlight == nil {
		w.inFlight = map[string]*warmCall{}
	}
	if c, ok := w.inFlight[key]; ok {
		w.mu.Unlock()
		select {
		case <-c.done:
			return c.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	c := &warmCall{done: make(chan struct{})}
	w.inFlight[key] = c
	w.mu.Unlock()

	c.err = fn(ctx)

	w.mu.Lock()
	delete(w.inFlight, key)
	if c.err == nil {
		w.recent = slices.DeleteFunc(w.recent, func(r llm.ChatRequest) bool { return warmKey(r) == key })
		w.recent = append(w.recent, req)
		if len(w.recent) > maxRecentWarm {
			w.recent = w.recent[len(w.recent)-maxRecentWarm:]
		}
		w.saveLocked()
	}
	w.mu.Unlock()
	close(c.done)
	return c.err
}

// load reads the saved warm-ups; a missing or unreadable file is none.
func (w *warmFlight) load() {
	if w.path == "" {
		return
	}
	b, err := os.ReadFile(w.path)
	if err != nil {
		return
	}
	var reqs []llm.ChatRequest
	if json.Unmarshal(b, &reqs) != nil {
		return
	}
	if len(reqs) > maxRecentWarm {
		reqs = reqs[len(reqs)-maxRecentWarm:]
	}
	w.mu.Lock()
	w.recent = reqs
	w.mu.Unlock()
}

// saveLocked writes the recent warm-ups (w.mu held); failures only cost a cold first turn.
func (w *warmFlight) saveLocked() {
	if w.path == "" {
		return
	}
	b, err := json.Marshal(w.recent)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(w.path), 0o700); err != nil {
		return
	}
	tmp := w.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, w.path)
}

// recentWarm returns the remembered warm-ups, newest first.
func (w *warmFlight) recentWarm() []llm.ChatRequest {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := slices.Clone(w.recent)
	slices.Reverse(out)
	return out
}

// rewarmTimeout bounds each re-warm after an engine restart.
const rewarmTimeout = 5 * time.Minute

// EngineReady is the llm module's OnEngineReady: when the live engine (re)starts, its prefix
// cache is empty, so the recent warm-ups are sent again, newest first, at background priority
// so a live turn goes first. Other labels' engines need nothing from command-center.
func (m *Module) EngineReady(labels []string) {
	if m.LLM == nil || !slices.Contains(labels, llm.LabelLive) {
		return
	}
	reqs := m.warms.recentWarm()
	if len(reqs) == 0 {
		return
	}
	go func() {
		for _, r := range reqs {
			ctx, cancel := context.WithTimeout(context.Background(), rewarmTimeout)
			err := m.warms.do(ctx, r, func(ctx context.Context) error {
				bg := r
				bg.Priority = llm.PriorityBackground
				_, err := m.LLM.Chat(ctx, bg)
				return err
			})
			cancel()
			if err != nil {
				m.deps.Log.Warn("cc: re-warm after engine start failed", "err", err)
			}
		}
		m.deps.Log.Info("cc: re-warmed after engine start", "prefixes", len(reqs))
	}()
}
