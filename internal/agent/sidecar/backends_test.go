package sidecar

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// fakeHost answers the child's backend requests from a table and records what it was asked.
type fakeHost struct {
	mu      sync.Mutex
	answers map[string]any
	calls   []string
	params  map[string]map[string]any
	fail    bool
}

func (h *fakeHost) Answer(_ context.Context, method string, raw json.RawMessage) (any, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, method)
	var p map[string]any
	_ = json.Unmarshal(raw, &p)
	if h.params == nil {
		h.params = map[string]map[string]any{}
	}
	h.params[method] = p
	if h.fail {
		return nil, errors.New("host down")
	}
	a, ok := h.answers[method]
	if !ok {
		return nil, errors.New("unexpected " + method)
	}
	return a, nil
}

// The Pi's built-in commands reach the agent through the SDK's backends: every helper becomes
// one request to the Go side, and its answer is what the command gets back.
func TestSidecarBackendsReachTheHost(t *testing.T) {
	py := python(t)
	t.Setenv("PYTHONIOENCODING", "cp1252") // Windows' Python: the protocol must stay UTF-8 anyway
	ctx := context.Background()
	timer := map[string]any{"timer_id": "ab12cd34", "label": "pasta", "duration_seconds": 90, "remaining_seconds": 89}
	h := &fakeHost{answers: map[string]any{
		"llm.chat_text":               "Why did the chicken…",
		"cc.post":                     map[string]any{"ok": true},
		"timer.set":                   "ab12cd34",
		"timer.get":                   timer,
		"timer.list_active":           []any{timer},
		"timer.find_by_label":         "ab12cd34",
		"timer.cancel":                true,
		"timer.clear_all":             2,
		"audio.set_volume_percent":    true,
		"audio.adjust_volume_percent": 30,
		"audio.set_muted":             true,
		"audio.speak":                 true,
		"alerts.pending": []any{map[string]any{"id": "a1", "source_agent": "news", "title": "Storm", "summary": "s",
			"priority": 3, "created_at": "2026-10-18T09:00:00+00:00", "expires_at": "2099-01-01T00:00:00+00:00",
			"dedupe_key": nil, "category": "weather", "target_user_id": nil}},
		"alerts.count":  1,
		"alerts.remove": 1,
		"alerts.flush":  []any{},
		"inbox.post":    "ok",
		"signals.emit":  "ok",
	}}
	logs := &syncWriter{w: &strings.Builder{}}
	sc, err := StartWith(ctx, py, "testdata/backends", slog.New(slog.NewTextHandler(logs, nil)), Options{Host: h})
	if err != nil {
		t.Fatal(err)
	}
	uid := int64(7)
	out, ok := sc.Call(ctx, "everything", nil, node.Request{UserID: &uid})
	node.TakeSignals(out)
	sc.Close()
	if !ok || out["success"] != true {
		t.Fatalf("call = %v", out)
	}
	b, _ := json.Marshal(out)
	for _, want := range []string{`"llm":"Why did the chicken…"`, `"cc":{"ok":true}`, `"timer_set":"ab12cd34"`,
		`"timer_get":{"duration_seconds":90,"label":"pasta","remaining_seconds":89,"timer_id":"ab12cd34"}`,
		`"timer_find":"ab12cd34"`, `"timer_cancel":true`, `"timer_clear":2`, `"volume_set":true`, `"volume_adjust":30`,
		`"muted":true`, `"spoke":true`, `"alerts":[["Storm",3,2026]]`, `"alerts_count":1`, `"alerts_removed":1`,
		`"alerts_flushed":0`, `"inbox":"ok"`, `"signal":"ok"`, `"echo":{"text":"inner","user":7}`, `"missing":null`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("output lacks %s:\n%s", want, b)
		}
	}
	if p := h.params["timer.set"]; p["seconds"] != 90.0 || p["label"] != "pasta" {
		t.Errorf("timer.set params = %v", p)
	}
	if p := h.params["cc.post"]; p["path"] != "/api/v0/node/thing" || p["json"].(map[string]any)["a"] != 1.0 {
		t.Errorf("cc.post params = %v", p)
	}
	if p := h.params["alerts.remove"]; len(p["ids"].([]any)) != 1 || p["ids"].([]any)[0] != "a1" {
		t.Errorf("alerts.remove params = %v", p)
	}
	if p := h.params["inbox.post"]; p["command_name"] != "everything" || p["title"] != "List" || p["body"] != "milk" ||
		p["target_type"] != "household" {
		t.Errorf("inbox.post params = %v", p)
	}
	if p := h.params["signals.emit"]; p["payload"].(map[string]any)["signal"].(map[string]any)["kind"] != "presence.seen" {
		t.Errorf("signals.emit params = %v", p)
	}
	if p := h.params["audio.adjust_volume_percent"]; p["delta"] != -10.0 {
		t.Errorf("audio.adjust params = %v", p)
	}
	// The SDK's stdlib logger reaches the agent's log, all of it by the time Close returns.
	if !strings.Contains(logs.String(), "everything WARNING: to stderr") {
		t.Fatalf("log lacks the command's warning:\n%s", logs.String())
	}
}

// A host that fails every request: the commands get the SDK's defaults, and the call succeeds.
func TestSidecarBackendFailuresAreSafeDefaults(t *testing.T) {
	py := python(t)
	ctx := context.Background()
	h := &fakeHost{fail: true}
	sc, err := StartWith(ctx, py, "testdata/backends", slog.New(slog.NewTextHandler(io.Discard, nil)), Options{Host: h})
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()
	out, _ := sc.Call(ctx, "failing", nil, node.Request{})
	node.TakeSignals(out)
	if out["success"] != true || out["llm"] != nil || out["timer"] != nil || out["count"] != 0.0 || out["inbox"] != "http_error" {
		t.Fatalf("call = %v", out)
	}
}

// Without a host (the desktop agent) no backend is registered: the helpers answer their
// defaults without asking the agent.
func TestSidecarWithoutHostRegistersNoBackends(t *testing.T) {
	py := python(t)
	ctx := context.Background()
	sc, err := Start(ctx, py, "testdata/backends", slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()
	out, _ := sc.Call(ctx, "failing", nil, node.Request{})
	node.TakeSignals(out)
	if out["success"] != true || out["inbox"] != "no_backend" || out["timer"] != nil {
		t.Fatalf("call = %v", out)
	}
}

type syncWriter struct {
	mu sync.Mutex
	w  *strings.Builder
}

func (s *syncWriter) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.String()
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}
