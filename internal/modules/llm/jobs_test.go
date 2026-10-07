package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// callbackSink records POSTed callbacks.
type callbackSink struct {
	srv    *httptest.Server
	mu     sync.Mutex
	got    []map[string]any
	header []http.Header
	status []int // scripted statuses, then 200
}

func newSink(t *testing.T) *callbackSink {
	s := &callbackSink{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var v map[string]any
		json.Unmarshal(b, &v)
		s.mu.Lock()
		code := 200
		if len(s.status) > 0 {
			code, s.status = s.status[0], s.status[1:]
		}
		if code == 200 {
			s.got = append(s.got, v)
			s.header = append(s.header, r.Header.Clone())
		}
		s.mu.Unlock()
		w.WriteHeader(code)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *callbackSink) wait(t *testing.T, n int) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.mu.Lock()
		got := append([]map[string]any{}, s.got...)
		s.mu.Unlock()
		if len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d callbacks, want %d", len(got), n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func enqueueBody(id, cb string, mut func(map[string]any)) map[string]any {
	j := map[string]any{
		"job_id": id, "job_type": "chat", "created_at": time.Now().UTC().Format(time.RFC3339), "priority": "normal",
		"trace_id": "trace-" + id, "idempotency_key": id, "job_type_version": "v1", "ttl_seconds": 120,
		"metadata": map[string]any{"type": "memory", "z": 1, "a": 2},
		"request": map[string]any{
			"model":    "background",
			"messages": []any{map[string]any{"role": "user", "content": "Reply with the word OK."}},
			"sampling": map[string]any{"temperature": 0, "max_tokens": 5}, "reasoning_budget": 0,
		},
		"callback": map[string]any{"url": cb, "auth_type": "bearer", "token": "cb-token"},
	}
	if mut != nil {
		mut(j)
	}
	return j
}

func TestEnqueueAndCallback(t *testing.T) {
	e := setup(t)
	sink := newSink(t)
	r := e.post("/internal/queue/enqueue", enqueueBody("j1", sink.srv.URL+"/cb", nil))
	if v := r.json(t); r.status != 200 || v["accepted"] != true || v["job_id"] != "j1" || v["deduped"] != false {
		t.Fatalf("%d %s", r.status, r.body)
	}
	if v := e.post("/internal/queue/enqueue", enqueueBody("j1", sink.srv.URL+"/cb", nil)).json(t); v["deduped"] != true {
		t.Fatalf("second enqueue not deduped: %v", v)
	}
	got := sink.wait(t, 1)
	env := got[0]
	if env["job_id"] != "j1" || env["job_type"] != "chat" || env["status"] != "succeeded" || env["error"] != nil ||
		env["result"].(map[string]any)["content"] != "OK" || env["metadata"].(map[string]any)["z"] != float64(1) {
		t.Fatalf("envelope %v", env)
	}
	if len(env) != 8 {
		t.Fatalf("envelope keys %v", env)
	}
	h := sink.header[0]
	if h.Get("Authorization") != "Bearer cb-token" || h.Get("X-Trace-Id") != "trace-j1" || h.Get("X-Jarvis-App-Id") != "jarvisd" {
		t.Fatalf("headers %v", h)
	}
	// The job ran on background with the request's sampling (temperature 0 honoured).
	b := e.engine.last()
	if b["model"] != "qwen-bg.gguf" || b["temperature"] != float64(0) || b["max_tokens"] != float64(5) {
		t.Fatalf("payload %v", b)
	}
	// Still deduped after completion, within the TTL window.
	if v := e.post("/internal/queue/enqueue", enqueueBody("j1", sink.srv.URL+"/cb", nil)).json(t); v["deduped"] != true {
		t.Fatal("dedupe window must outlive the job")
	}
	// The raw envelope keeps the legacy key order.
	if !strings.HasPrefix(string(mustMarshal(buildEnvelope(JobResult{JobID: "x"}))), `{"job_id":"x","job_type":"","finished_at":`) {
		t.Fatal("envelope key order")
	}
}

// testClock is a settable clock safe to read from queue workers.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

// newClock installs a fake clock on the service (queue workers read it concurrently).
func newClock(e *env) *testClock {
	c := &testClock{t: time.Now()}
	e.m.Service().setNow(c.now)
	return c
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func mustMarshal(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func TestEnqueueValidation(t *testing.T) {
	e := setup(t)
	cb := "http://127.0.0.1:9/cb"
	check := func(name string, body map[string]any, status int, code string) {
		t.Helper()
		r := e.post("/internal/queue/enqueue", body)
		if r.status != status {
			t.Fatalf("%s: %d %s", name, r.status, r.body)
		}
		if code != "" {
			d := r.json(t)["detail"].(map[string]any)["error"].(map[string]any)
			if d["code"] != code || d["type"] != "invalid_request_error" || d["message"] == "" {
				t.Fatalf("%s: %s", name, r.body)
			}
		}
	}
	check("expired", enqueueBody("e1", cb, func(j map[string]any) {
		j["created_at"] = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
		j["ttl_seconds"] = 60
	}), 400, "expired")
	check("invalid", enqueueBody("e2", cb, func(j map[string]any) { j["request"] = map[string]any{"model": "background"} }), 400, "invalid_request")
	check("schema", enqueueBody("e3", cb, func(j map[string]any) {
		j["request"].(map[string]any)["response_format"] = map[string]any{"type": "json_object"}
	}), 400, "missing_schema")
	check("callback", enqueueBody("e4", cb, func(j map[string]any) { delete(j, "callback") }), 422, "")
	r := e.post("/internal/queue/enqueue", enqueueBody("e5", cb, func(j map[string]any) {
		j["request"].(map[string]any)["response_format"] = map[string]any{"type": "json_object"}
	}))
	if _, msg := errType(t, r); msg != "json_schema required when response_format.type=json_object" {
		t.Fatalf("message %q", msg)
	}
}

// D8: a job past its TTL when it runs completes with an "expired" failure callback.
func TestExpiredJobCallsBack(t *testing.T) {
	e := setup(t)
	sink := newSink(t)
	svc := e.m.Service()
	clock := newClock(e)
	if _, _, err := svc.Enqueue(e.ctx, Job{JobID: "x1", JobType: "chat", TTL: time.Minute, CreatedAt: clock.now(),
		Request:  ChatRequest{Messages: []Message{{Role: "user", Content: TextContent("hi")}}},
		Callback: &Callback{URL: sink.srv.URL}}); err != nil {
		t.Fatal(err)
	}
	// It can't be expired at enqueue; make the worker see it late.
	clock.add(2 * time.Minute)
	env := sink.wait(t, 1)[0]
	if env["status"] != "failed" || env["error"].(map[string]any)["code"] != "expired" || env["result"] != nil {
		t.Fatalf("envelope %v", env)
	}
	if len(e.engine.requests()) != 0 {
		t.Fatal("an expired job must not run")
	}
}

func TestJobRetriesThenFails(t *testing.T) {
	e := setup(t)
	sink := newSink(t)
	sink.status = []int{503} // the first delivery attempt fails too
	e.engine.script(fakeReply{Status: 500, Body: "engine crashed"}, fakeReply{Status: 500, Body: "engine crashed again"})
	e.post("/internal/queue/enqueue", enqueueBody("r1", sink.srv.URL, nil))
	env := sink.wait(t, 1)[0]
	if env["status"] != "failed" || env["error"].(map[string]any)["message"] != "engine crashed again" ||
		env["error"].(map[string]any)["code"] != "internal_server_error" {
		t.Fatalf("envelope %v", env)
	}
	if n := len(e.engine.requests()); n != 2 {
		t.Fatalf("%d engine calls, want 2 (one retry)", n)
	}
}

func TestJobRetrySucceeds(t *testing.T) {
	e := setup(t)
	sink := newSink(t)
	e.engine.script(fakeReply{Status: 502, Body: "bad gateway"})
	e.post("/internal/queue/enqueue", enqueueBody("r2", sink.srv.URL, nil))
	if env := sink.wait(t, 1)[0]; env["status"] != "succeeded" {
		t.Fatalf("envelope %v", env)
	}
}

func TestJob4xxNotRetried(t *testing.T) {
	e := setup(t)
	sink := newSink(t)
	e.engine.script(fakeReply{Status: 400, Body: "context overflow"})
	e.post("/internal/queue/enqueue", enqueueBody("r3", sink.srv.URL, nil))
	env := sink.wait(t, 1)[0]
	if env["status"] != "failed" || env["error"].(map[string]any)["code"] != "invalid_request_error" || len(e.engine.requests()) != 1 {
		t.Fatalf("envelope %v, %d calls", env, len(e.engine.requests()))
	}
}

func TestInProcessNotify(t *testing.T) {
	e := setup(t)
	svc := e.m.Service()
	got := make(chan JobResult, 1)
	svc.OnComplete("memory.extract", func(_ context.Context, r JobResult) error {
		got <- r
		return nil
	})
	id, deduped, err := svc.Enqueue(e.ctx, Job{Request: ChatRequest{Label: "live", Messages: []Message{{Role: "user", Content: TextContent("hi")}}},
		Metadata: json.RawMessage(`{"transcript":7}`), Notify: "memory.extract"})
	if err != nil || deduped || id == "" {
		t.Fatal(id, deduped, err)
	}
	select {
	case r := <-got:
		if r.Status != "succeeded" || r.Content == nil || *r.Content != "OK" || string(r.Metadata) != `{"transcript":7}` || r.JobID != id {
			t.Fatalf("result %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no completion")
	}
	if e.engine.last()["model"] != "qwen-bg.gguf" {
		t.Fatal("queued work must run on background, never live")
	}
}

func TestPurgeDedupe(t *testing.T) {
	e := setup(t)
	svc := e.m.Service()
	clock := newClock(e)
	if _, _, err := svc.Enqueue(e.ctx, Job{JobID: "p", TTL: time.Second, Request: ChatRequest{Messages: []Message{{Role: "user", Content: TextContent("x")}}}}); err != nil {
		t.Fatal(err)
	}
	clock.add(2 * time.Second)
	if _, err := svc.runPurge(e.ctx, queueJob()); err != nil {
		t.Fatal(err)
	}
	var n int
	svc.db.Read.QueryRow(`SELECT count(*) FROM llm_dedupe`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d dedupe rows after purge", n)
	}
	// And a new enqueue of the same pair after the window is accepted.
	if _, deduped, _ := svc.Enqueue(e.ctx, Job{JobID: "p", TTL: time.Second, Request: ChatRequest{Messages: []Message{{Role: "user", Content: TextContent("x")}}}}); deduped {
		t.Fatal("deduped after the window")
	}
}

func TestParseCreatedAt(t *testing.T) {
	now := time.Unix(1000, 0)
	for in, want := range map[string]int64{
		"2026-10-06T12:00:00Z": 1791288000, "2026-10-06T12:00:00+00:00": 1791288000, "2026-10-06T12:00:00.5Z": 1791288000,
		"1791288000": 1791288000, "garbage": 1000,
	} {
		if got := parseCreatedAt(in, now).Unix(); got != want {
			t.Errorf("parseCreatedAt(%q) = %d, want %d", in, got, want)
		}
	}
}
