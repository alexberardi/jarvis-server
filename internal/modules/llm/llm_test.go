package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/jsonmode"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/platform/db"
	"github.com/alexberardi/jarvis-server/internal/platform/module"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

type env struct {
	t      *testing.T
	m      *Module
	srv    *httptest.Server
	engine *fakeEngine
	res    *fakeResolver
	q      *queue.Queue
	ctx    context.Context
}

func setup(t *testing.T, opts ...func(*Module)) *env {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "jarvis.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := db.Migrate(ctx, d, queue.MigrationModule, queue.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, d, "llm", Migrations()); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	q := queue.New(d, log)
	q.PollInterval = 10 * time.Millisecond
	eng := newFakeEngine()
	t.Cleanup(eng.close)
	res := &fakeResolver{eps: map[string]Endpoint{
		LabelLive:       {BaseURL: eng.srv.URL, Model: "qwen-live.gguf", ContextLength: 4096},
		LabelBackground: {BaseURL: eng.srv.URL + "/v1/", Model: "qwen-bg.gguf"},
		LabelEmbeddings: {BaseURL: eng.srv.URL, Model: "all-MiniLM-L6-v2", Embeddings: true},
	}, err: map[string]error{}}
	m := &Module{Auth: fakeAuth{}, Resolver: res, Version: "test", AppID: "jarvisd", AppKey: "k",
		chatBackoff: func(int) time.Duration { return 20 * time.Millisecond }}
	for _, o := range opts {
		o(m)
	}
	mux := http.NewServeMux()
	m.Register(mux, module.Deps{DB: d, Log: log, Queue: q})
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	q.Start(ctx)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &env{t: t, m: m, srv: srv, engine: eng, res: res, q: q, ctx: ctx}
}

var appHeaders = map[string]string{"X-Jarvis-App-Id": "app", "X-Jarvis-App-Key": "secret"}

type resp struct {
	status int
	header http.Header
	body   []byte
}

func (r resp) json(t *testing.T) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(r.body, &v); err != nil {
		t.Fatalf("body %q: %v", r.body, err)
	}
	return v
}

func (e *env) do(method, path string, body any, headers map[string]string) resp {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return resp{res.StatusCode, res.Header, b}
}

func (e *env) post(path string, body any) resp { return e.do("POST", path, body, appHeaders) }

func chat(prompt string, extra map[string]any) map[string]any {
	r := map[string]any{"model": "live", "messages": []any{map[string]any{"role": "user", "content": prompt}}}
	for k, v := range extra {
		r[k] = v
	}
	return r
}

func errType(t *testing.T, r resp) (string, string) {
	t.Helper()
	d, _ := r.json(t)["detail"].(map[string]any)
	e, _ := d["error"].(map[string]any)
	typ, _ := e["type"].(string)
	msg, _ := e["message"].(string)
	return typ, msg
}

// --- non-stream ---

func TestChatShape(t *testing.T) {
	e := setup(t)
	r := e.post("/v1/chat/completions", chat("Reply with OK.", nil))
	if r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	v := r.json(t)
	if !regexp.MustCompile(`^chatcmpl-[0-9a-f]{8}$`).MatchString(v["id"].(string)) || v["object"] != "chat.completion" || v["model"] != "live" {
		t.Fatalf("envelope %v", v)
	}
	if v["date_keys"] != nil {
		t.Fatalf("date_keys %v, want null", v["date_keys"])
	}
	ch := v["choices"].([]any)[0].(map[string]any)
	msg := ch["message"].(map[string]any)
	if msg["content"] != "OK" || msg["tool_calls"] != nil || msg["tool_call_id"] != nil || ch["finish_reason"] != "stop" {
		t.Fatalf("choice %v", ch)
	}
	if u := v["usage"].(map[string]any); u["total_tokens"] != float64(14) || len(u) != 3 {
		t.Fatalf("usage %v", u)
	}
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(r.header.Get("X-Request-Id")) {
		t.Fatalf("X-Request-Id %q", r.header.Get("X-Request-Id"))
	}
	// The engine got the label's model, the default temperature and the live thinking default.
	b := e.engine.last()
	if b["model"] != "qwen-live.gguf" || b["temperature"] != 0.7 || b["stream"] != false {
		t.Fatalf("payload %v", b)
	}
	if kw := b["chat_template_kwargs"].(map[string]any); kw["enable_thinking"] != false || b["reasoning_budget"] != float64(0) {
		t.Fatalf("thinking %v %v", b["chat_template_kwargs"], b["reasoning_budget"])
	}
}

func TestChatModelLabels(t *testing.T) {
	e := setup(t)
	for model, want := range map[string]string{"gpt-4o": "live", "background": "background", "Background": "Background", "LIVE": "LIVE"} {
		r := e.post("/v1/chat/completions", chat("hi", map[string]any{"model": model}))
		if got := r.json(t)["model"]; got != want {
			t.Errorf("model %q echoed %v, want %q", model, got, want)
		}
	}
	e.post("/v1/chat/completions", chat("hi", map[string]any{"model": "background"}))
	if b := e.engine.last(); b["model"] != "qwen-bg.gguf" || b["chat_template_kwargs"].(map[string]any)["enable_thinking"] != true {
		t.Fatalf("background payload %v", b)
	}
}

func TestChatTemperatureZeroHonoured(t *testing.T) {
	e := setup(t)
	e.post("/v1/chat/completions", chat("hi", map[string]any{"temperature": 0, "top_p": 0.5, "seed": 3, "max_tokens": 8}))
	b := e.engine.last()
	if b["temperature"] != float64(0) || b["top_p"] != 0.5 || b["seed"] != float64(3) || b["max_tokens"] != float64(8) {
		t.Fatalf("payload %v", b)
	}
}

func TestThinkingControl(t *testing.T) {
	e := setup(t)
	e.post("/v1/chat/completions", chat("hi", map[string]any{"reasoning_budget": 512}))
	if b := e.engine.last(); b["chat_template_kwargs"].(map[string]any)["enable_thinking"] != true || b["reasoning_budget"] != float64(512) {
		t.Fatalf("payload %v", b)
	}
	// A blank label default sends nothing: the engine's launch flags decide.
	if err := e.m.settings.Set(e.ctx, "llm.live.reasoning_budget", "server", settings.Scope{}); err != nil {
		t.Fatal(err)
	}
	e.post("/v1/chat/completions", chat("hi", nil))
	if b := e.engine.last(); b["chat_template_kwargs"] != nil || b["reasoning_budget"] != nil {
		t.Fatalf("payload %v", b)
	}
}

func TestDateKeys(t *testing.T) {
	e := setup(t)
	r := e.post("/v1/chat/completions", chat("Remind me tomorrow morning to call mom.", map[string]any{"include_date_context": true}))
	if dk := r.json(t)["date_keys"].([]any); len(dk) != 1 || dk[0] != "tomorrow_morning" {
		t.Fatalf("date_keys %v", dk)
	}
	r = e.post("/v1/chat/completions", chat("Reply with OK.", map[string]any{"include_date_context": true}))
	if dk, ok := r.json(t)["date_keys"].([]any); !ok || len(dk) != 0 {
		t.Fatalf("date_keys %v, want []", r.json(t)["date_keys"])
	}
	// D8: no user text still gives [] (legacy: null).
	body := map[string]any{"model": "live", "include_date_context": true, "messages": []any{map[string]any{"role": "system", "content": "x"}}}
	if dk, ok := e.post("/v1/chat/completions", body).json(t)["date_keys"].([]any); !ok || len(dk) != 0 {
		t.Fatal("want [] for no user text")
	}
}

func TestUserText(t *testing.T) {
	s := func(x string) *Content { return TextContent(x) }
	msgs := []Message{
		{Role: "user", Content: s("first")},
		{Role: "user", Content: &Content{Parts: []Part{{Type: "image_url", ImageURL: &ImageURL{URL: "data:x"}}, {Type: "text", Text: "second"}}}},
		{Role: "user", Content: &Content{Parts: []Part{{Type: "image_url"}}}},
		{Role: "user"},
		{Role: "assistant", Content: s("nope")},
	}
	if got := userText(msgs); got != "second" {
		t.Fatalf("userText = %q", got)
	}
}

func TestValidation(t *testing.T) {
	e := setup(t)
	for body, loc := range map[string]string{`{"model":"live"}`: "messages", `{"messages":[{"role":"user","content":"hi"}]}`: "model"} {
		req, _ := http.NewRequest("POST", e.srv.URL+"/v1/chat/completions", strings.NewReader(body))
		for k, v := range appHeaders {
			req.Header.Set(k, v)
		}
		res, _ := http.DefaultClient.Do(req)
		var out struct {
			Detail []struct {
				Loc []any `json:"loc"`
			} `json:"detail"`
		}
		json.NewDecoder(res.Body).Decode(&out)
		res.Body.Close()
		if res.StatusCode != 422 || len(out.Detail) == 0 || out.Detail[0].Loc[1] != loc {
			t.Errorf("%s: %d %v", body, res.StatusCode, out)
		}
	}
	r := e.post("/v1/chat/completions", map[string]any{"model": "live", "messages": []any{map[string]any{"role": "user",
		"content": []any{map[string]any{"type": "video"}}}}})
	if r.status != 422 {
		t.Fatalf("bad part: %d %s", r.status, r.body)
	}
}

func TestAppAuth(t *testing.T) {
	e := setup(t)
	for _, p := range []string{"/v1/chat/completions", "/v1/chat/completions/cancel/x", "/v1/embeddings", "/internal/queue/enqueue"} {
		if r := e.do("POST", p, map[string]any{}, nil); r.status != 401 || r.json(t)["detail"] != "Missing app credentials" {
			t.Errorf("%s no creds: %d %s", p, r.status, r.body)
		}
		if r := e.do("POST", p, map[string]any{}, map[string]string{"X-Jarvis-App-Id": "app", "X-Jarvis-App-Key": "bad"}); r.status != 401 || r.json(t)["detail"] != "Invalid app credentials" {
			t.Errorf("%s bad creds: %d %s", p, r.status, r.body)
		}
	}
}

func TestNotLoaded(t *testing.T) {
	e := setup(t)
	e.res.set(LabelLive, Endpoint{Model: "m"}, &NotReadyError{State: StateLoading})
	for _, stream := range []bool{false, true} {
		r := e.post("/v1/chat/completions", chat("hi", map[string]any{"stream": stream}))
		if typ, msg := errType(t, r); r.status != 503 || typ != "model_not_loaded" || msg != "live model is loading: not loaded yet" {
			t.Fatalf("stream=%v: %d %s", stream, r.status, r.body)
		}
	}
}

// A10 F22: after a restart or upgrade the live model reloads for 5-10 s; in-process voice and
// chat calls (WithReadyWait) wait for it, bounded, instead of failing at once.
func TestReadyWait(t *testing.T) {
	e := setup(t)
	old := readyPoll
	readyPoll = 5 * time.Millisecond
	t.Cleanup(func() { readyPoll = old })
	good := e.res.eps[LabelLive]
	req := ChatRequest{Label: LabelLive, Messages: []Message{{Role: "user", Content: TextContent("hi")}}}
	svc := e.m.Service()

	e.res.set(LabelLive, good, &NotReadyError{State: StateLoading, Reason: "starting"})
	go func() {
		time.Sleep(60 * time.Millisecond)
		e.res.set(LabelLive, good, nil)
	}()
	if _, err := svc.Chat(WithReadyWait(e.ctx, 10*time.Second), req); err != nil {
		t.Fatalf("waited chat: %v", err)
	}

	// Still loading when the wait runs out: 503 that says so.
	e.res.set(LabelLive, good, &NotReadyError{State: StateLoading, Reason: "starting"})
	start := time.Now()
	_, err := svc.Chat(WithReadyWait(e.ctx, 40*time.Millisecond), req)
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 503 || !strings.Contains(ae.Message, "still loading after waiting 40ms") {
		t.Fatalf("timeout: %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("wait not bounded")
	}
	// Without it (the HTTP API) and for a failed model, no wait.
	for _, c := range []struct {
		ctx context.Context
		err error
	}{
		{e.ctx, &NotReadyError{State: StateLoading}},
		{WithReadyWait(e.ctx, time.Hour), &NotReadyError{State: StateFailed, Reason: "exit 1"}},
	} {
		e.res.set(LabelLive, good, c.err)
		start := time.Now()
		if _, err := svc.Chat(c.ctx, req); err == nil || time.Since(start) > time.Second {
			t.Fatalf("%v: err %v after %v", c.err, err, time.Since(start))
		}
	}
	// Streams wait too.
	e.res.set(LabelLive, good, &NotReadyError{State: StateLoading})
	go func() {
		time.Sleep(30 * time.Millisecond)
		e.res.set(LabelLive, good, nil)
	}()
	ch, err := svc.Stream(WithReadyWait(e.ctx, 10*time.Second), req)
	if err != nil {
		t.Fatalf("waited stream: %v", err)
	}
	for range ch {
	}
}

func TestUpstreamErrorPassesThrough(t *testing.T) {
	e := setup(t)
	e.engine.script(fakeReply{Status: 400, Body: `{"error":{"code":400,"message":"request exceeds the available context size","n_prompt_tokens":9000}}`})
	r := e.post("/v1/chat/completions", chat("hi", nil))
	if typ, msg := errType(t, r); r.status != 400 || typ != "invalid_request_error" || !strings.Contains(msg, "n_prompt_tokens") {
		t.Fatalf("%d %s", r.status, r.body)
	}
}

// --- images (LD4) ---

const png1x1 = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="

func imageMsgs(url string) []any {
	return []any{map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "text", "text": "What is this?"},
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}},
	}}}
}

func TestImages(t *testing.T) {
	e := setup(t)
	for _, stream := range []bool{false, true} {
		r := e.post("/v1/chat/completions", chat("", map[string]any{"messages": imageMsgs(png1x1), "stream": stream}))
		want := "Model 'live' does not support images. Use a vision-capable model instead."
		if typ, msg := errType(t, r); r.status != 400 || typ != "invalid_request_error" || msg != want {
			t.Fatalf("stream=%v: %d %s", stream, r.status, r.body)
		}
	}
	r := e.post("/v1/chat/completions", chat("", map[string]any{"messages": imageMsgs("https://x/y.png")}))
	if _, msg := errType(t, r); r.status != 400 || !strings.HasPrefix(msg, "Only data URLs") {
		t.Fatalf("%d %s", r.status, r.body)
	}
	e.res.set(LabelLive, Endpoint{BaseURL: e.engine.srv.URL, Model: "vl", Vision: true}, nil)
	if r := e.post("/v1/chat/completions", chat("", map[string]any{"messages": imageMsgs(png1x1)})); r.status != 200 {
		t.Fatalf("vision: %d %s", r.status, r.body)
	}
	parts := e.engine.last()["messages"].([]any)[0].(map[string]any)["content"].([]any)
	if parts[1].(map[string]any)["image_url"].(map[string]any)["url"] != png1x1 {
		t.Fatalf("image not forwarded: %v", parts)
	}
}

// --- tools (D8: native history forwarded) ---

func TestToolsAndHistory(t *testing.T) {
	e := setup(t)
	e.engine.script(fakeReply{ToolCalls: []map[string]any{{"id": "call_1", "type": "function",
		"function": map[string]any{"name": "get_weather", "arguments": `{"city":"Paris"}`}}}})
	tools := []any{map[string]any{"type": "function", "strict": true, "function": map[string]any{"name": "get_weather",
		"parameters": map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}}}}}
	history := []any{
		map[string]any{"role": "user", "content": "weather?"},
		map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{"id": "c0", "type": "function",
			"function": map[string]any{"name": "get_weather", "arguments": "{}"}}}},
		map[string]any{"role": "tool", "tool_call_id": "c0", "content": "sunny"},
	}
	r := e.post("/v1/chat/completions", map[string]any{"model": "live", "messages": history, "tools": tools, "tool_choice": "auto"})
	ch := r.json(t)["choices"].([]any)[0].(map[string]any)
	tc := ch["message"].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)
	if ch["finish_reason"] != "tool_calls" || tc["id"] != "call_1" || tc["function"].(map[string]any)["arguments"] != `{"city":"Paris"}` {
		t.Fatalf("choice %v", ch)
	}
	b := e.engine.last()
	if b["tool_choice"] != "auto" || b["tools"].([]any)[0].(map[string]any)["strict"] != true {
		t.Fatalf("tools not forwarded verbatim: %v", b["tools"])
	}
	msgs := b["messages"].([]any)
	if msgs[1].(map[string]any)["tool_calls"] == nil || msgs[2].(map[string]any)["tool_call_id"] != "c0" {
		t.Fatalf("tool history dropped: %v", msgs)
	}
}

// --- JSON mode ---

func TestJSONModeRepair(t *testing.T) {
	e := setup(t)
	e.engine.script(fakeReply{Content: "Sure! {\"a\":1,\"a\":2}"})
	r := e.post("/v1/chat/completions", chat("give json", map[string]any{"response_format": map[string]any{"type": "json_object"}}))
	if got := r.json(t)["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"]; got != `{"a": [1, 2]}` {
		t.Fatalf("content %q", got)
	}
	msgs := e.engine.last()["messages"].([]any)
	if msgs[0].(map[string]any)["role"] != "system" || msgs[0].(map[string]any)["content"] != jsonmode.SystemMessage {
		t.Fatalf("instruction not injected: %v", msgs[0])
	}
	if _, has := e.engine.last()["response_format"]; has {
		t.Fatal("response_format must not reach the engine (no grammar, 04 §3.2)")
	}
}

func TestJSONModeRetry(t *testing.T) {
	e := setup(t)
	schema := map[string]any{"type": "object", "required": []any{"facts"}, "properties": map[string]any{"facts": map[string]any{"type": "array"}}}
	rf := map[string]any{"type": "json_object", "json_schema": schema}
	e.engine.script(fakeReply{Content: `{"other": 1}`}, fakeReply{Content: `{"facts": ["x"]}`})
	r := e.post("/v1/chat/completions", chat("q", map[string]any{"response_format": rf, "temperature": 0, "max_tokens": 600, "reasoning_budget": 0}))
	if got := r.json(t)["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"]; got != `{"facts": ["x"]}` {
		t.Fatalf("content %q (%s)", got, r.body)
	}
	retry := e.engine.last()
	msgs := retry["messages"].([]any)
	turn := msgs[len(msgs)-1].(map[string]any)["content"].(string)
	want, _ := jsonmode.CorrectionTurn(`{"other": 1}`, mustLoads(t, `{"type": "object", "required": ["facts"], "properties": {"facts": {"type": "array"}}}`))
	if turn != want || retry["temperature"] != 0.3 || retry["max_tokens"] != float64(8192) || retry["tools"] != nil {
		t.Fatalf("retry payload: temp %v max %v turn %q", retry["temperature"], retry["max_tokens"], turn)
	}
	if retry["chat_template_kwargs"].(map[string]any)["enable_thinking"] != false {
		t.Fatal("retry lost the thinking budget")
	}

	e.engine.script(fakeReply{Content: "nope"}, fakeReply{Content: "still nope"})
	r = e.post("/v1/chat/completions", chat("q", map[string]any{"response_format": rf}))
	// "nope" repairs to the JSON string "nope" (repair_unescaped_quotes), which then fails the
	// schema: the legacy message carries that first schema error.
	if typ, msg := errType(t, r); r.status != 500 || typ != "invalid_response_error" || msg != "Model returned invalid JSON response. $ expected type object, got str" {
		t.Fatalf("%d %s", r.status, r.body)
	}
}

// --- streaming ---

type sseFrame map[string]any

func readFrames(t *testing.T, body io.Reader) ([]sseFrame, []string) {
	t.Helper()
	var frames []sseFrame
	var raw []string
	sc := bufio.NewScanner(body)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			t.Fatalf("bad line %q", line)
		}
		raw = append(raw, line[6:])
		var f sseFrame
		if err := json.Unmarshal([]byte(line[6:]), &f); err != nil {
			t.Fatalf("frame %q: %v", line, err)
		}
		frames = append(frames, f)
	}
	return frames, raw
}

func TestStreamFrames(t *testing.T) {
	e := setup(t)
	e.engine.script(fakeReply{Content: "One two café"})
	req, _ := http.NewRequest("POST", e.srv.URL+"/v1/chat/completions", strings.NewReader(
		`{"model":"live","stream":true,"include_date_context":true,"temperature":0,"reasoning_budget":0,"messages":[{"role":"user","content":"count tomorrow"}]}`))
	for k, v := range appHeaders {
		req.Header.Set(k, v)
	}
	req.Header.Set("X-Request-Id", "rid-1")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	h := res.Header
	if res.StatusCode != 200 || !strings.HasPrefix(h.Get("Content-Type"), "text/event-stream") || h.Get("X-Request-Id") != "rid-1" ||
		h.Get("Cache-Control") != "no-cache" || h.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("%d %v", res.StatusCode, h)
	}
	frames, raw := readFrames(t, res.Body)
	var content string
	for _, f := range frames[:len(frames)-1] {
		content += f["delta"].(string)
	}
	last := frames[len(frames)-1]
	if last["done"] != true || last["content"] != content || content != "One two café" || last["tool_calls"] != nil ||
		last["finish_reason"] != "stop" || last["date_keys"] != nil {
		t.Fatalf("final %v (content %q)", last, content)
	}
	if u := last["usage"].(map[string]any); u["total_tokens"] != float64(14) {
		t.Fatalf("usage %v", u)
	}
	if !strings.Contains(raw[len(raw)-2], `\u00e9`) || !strings.HasPrefix(raw[0], `{"delta": `) {
		t.Fatalf("frames not json.dumps-shaped: %q", raw)
	}
	// D8: the stream applies thinking control and temperature 0 like non-stream.
	b := e.engine.last()
	if b["temperature"] != float64(0) || b["chat_template_kwargs"].(map[string]any)["enable_thinking"] != false ||
		b["stream_options"].(map[string]any)["include_usage"] != true {
		t.Fatalf("stream payload %v", b)
	}
}

func TestStreamToolCalls(t *testing.T) {
	e := setup(t)
	e.engine.script(fakeReply{ToolCalls: []map[string]any{{"id": "call_9", "function": map[string]any{"name": "f", "arguments": "{}"}}}})
	r := e.post("/v1/chat/completions", chat("x", map[string]any{"stream": true, "tools": []any{map[string]any{"type": "function", "function": map[string]any{"name": "f"}}}}))
	frames, _ := readFrames(t, bytes.NewReader(r.body))
	last := frames[len(frames)-1]
	calls, ok := last["tool_calls"].([]any)
	if !ok || last["finish_reason"] != "tool_calls" || calls[0].(map[string]any)["id"] != "call_9" {
		t.Fatalf("final %v", last)
	}
	if e.engine.last()["tools"] == nil {
		t.Fatal("tools dropped on the stream path")
	}
}

func TestStreamUpstreamErrorFrame(t *testing.T) {
	e := setup(t)
	e.engine.script(fakeReply{Status: 500, Body: "boom"})
	r := e.post("/v1/chat/completions", chat("x", map[string]any{"stream": true}))
	frames, _ := readFrames(t, bytes.NewReader(r.body))
	if r.status != 200 || len(frames) != 1 || frames[0]["error"] != "HTTP 500: boom" {
		t.Fatalf("%d %v", r.status, frames)
	}
}

func TestStreamCancel(t *testing.T) {
	e := setup(t)
	e.engine.streamDelay = 30 * time.Millisecond
	e.engine.streamText = strings.Repeat("tick ", 200)
	req, _ := http.NewRequest("POST", e.srv.URL+"/v1/chat/completions", strings.NewReader(
		`{"model":"live","stream":true,"messages":[{"role":"user","content":"count"}]}`))
	for k, v := range appHeaders {
		req.Header.Set(k, v)
	}
	req.Header.Set("X-Request-Id", "cancel-me")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	br := bufio.NewReader(res.Body)
	if line, _ := br.ReadString('\n'); !strings.HasPrefix(line, `data: {"delta"`) {
		t.Fatalf("first line %q", line)
	}
	if r := e.post("/v1/chat/completions/cancel/cancel-me", nil); r.status != 200 || r.json(t)["status"] != "cancelling" || r.json(t)["request_id"] != "cancel-me" {
		t.Fatalf("cancel: %d %s", r.status, r.body)
	}
	frames, raw := readFrames(t, br)
	if raw[len(raw)-1] != `{"cancelled": true}` {
		t.Fatalf("last frame %q", raw[len(raw)-1])
	}
	for _, f := range frames {
		if f["done"] != nil {
			t.Fatal("a cancelled stream must not send a done frame")
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		r := e.post("/v1/chat/completions/cancel/cancel-me", nil)
		if r.status == 404 {
			if typ, msg := errType(t, r); typ != "not_found" || msg != "No active stream with request id 'cancel-me'" {
				t.Fatalf("404 body %s", r.body)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stream id still registered")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestStreamSupersede(t *testing.T) {
	e := setup(t)
	e.engine.streamDelay = 20 * time.Millisecond
	e.engine.streamText = strings.Repeat("x ", 100)
	svc := e.m.Service()
	first, err := svc.Stream(e.ctx, ChatRequest{Label: "live", RequestID: "same", Messages: []Message{{Role: "user", Content: TextContent("a")}}})
	if err != nil {
		t.Fatal(err)
	}
	<-first // one delta
	if _, err := svc.Stream(e.ctx, ChatRequest{Label: "live", RequestID: "same", Messages: []Message{{Role: "user", Content: TextContent("b")}}}); err != nil {
		t.Fatal(err)
	}
	var last Frame
	for f := range first {
		last = f
	}
	if !last.Cancelled {
		t.Fatalf("superseded stream ended with %+v", last)
	}
	svc.Cancel("same")
}

// --- background concurrency ---

func TestBackgroundSerialised(t *testing.T) {
	e := setup(t)
	e.engine.gate = make(chan struct{})
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.m.Service().Chat(e.ctx, ChatRequest{Label: "background", Messages: []Message{{Role: "user", Content: TextContent("x")}}})
		}()
	}
	time.Sleep(100 * time.Millisecond)
	if n := e.engine.active.Load(); n != 1 {
		t.Fatalf("%d background calls at once, want 1", n)
	}
	for range 3 {
		e.engine.gate <- struct{}{}
	}
	wg.Wait()
	if p := e.engine.peak.Load(); p != 1 {
		t.Fatalf("peak %d", p)
	}
}

// --- embeddings ---

func TestEmbeddings(t *testing.T) {
	e := setup(t)
	r := e.post("/v1/embeddings", map[string]any{"input": []string{"turn on the lights", "what time is it"}})
	v := r.json(t)
	data := v["data"].([]any)
	if r.status != 200 || len(data) != 2 || v["model"] != "all-MiniLM-L6-v2" || v["object"] != "list" {
		t.Fatalf("%d %s", r.status, r.body)
	}
	for i, d := range data {
		dm := d.(map[string]any)
		var norm float64
		for _, x := range dm["embedding"].([]any) {
			norm += x.(float64) * x.(float64)
		}
		if dm["index"] != float64(i) || dm["object"] != "embedding" || norm < 0.999 || norm > 1.001 {
			t.Fatalf("item %d: %v (norm %v)", i, dm, norm)
		}
	}
	if u := v["usage"].(map[string]any); u["prompt_tokens"] != float64(8) {
		t.Fatalf("usage %v", u)
	}
	r = e.post("/v1/embeddings", map[string]any{"input": []string{}, "model": "x"})
	if v := r.json(t); len(v["data"].([]any)) != 0 || v["model"] != "x" || v["usage"].(map[string]any)["total_tokens"] != float64(0) {
		t.Fatalf("empty: %s", r.body)
	}
	if r := e.post("/v1/embeddings", map[string]any{}); r.status != 422 {
		t.Fatalf("missing input: %d", r.status)
	}
}

// --- discovery and health ---

func TestDiscovery(t *testing.T) {
	e := setup(t)
	models := e.do("GET", "/v1/models", nil, nil).json(t)["data"].([]any)
	if len(models) != 2 || models[0].(map[string]any)["context_length"] != float64(4096) || models[1].(map[string]any)["context_length"] != nil {
		t.Fatalf("models %v", models)
	}
	// Shared engine → one entry.
	e.res.set(LabelBackground, Endpoint{BaseURL: e.engine.srv.URL, Model: "qwen-live.gguf"}, nil)
	if n := len(e.do("GET", "/v1/models", nil, nil).json(t)["data"].([]any)); n != 1 {
		t.Fatalf("%d entries for a shared engine", n)
	}
	eng := e.do("GET", "/v1/engine", nil, nil).json(t)
	if eng["inference_engine"] != "llama_cpp" || eng["allows_caching"] != true {
		t.Fatalf("engine %v", eng)
	}
	dk := e.do("GET", "/v1/adapters/date-keys", nil, nil)
	if !bytes.HasPrefix(dk.body, []byte(`{"version":"2.0","static_keys":["after_dinner",`)) || dk.json(t)["adapter_trained"] != false {
		t.Fatalf("date-keys %s", dk.body)
	}
}

func TestHealth(t *testing.T) {
	e := setup(t)
	r := e.do("GET", "/health", nil, nil)
	v := r.json(t)
	if r.status != 200 || v["status"] != "healthy" || v["version"] != "test" || len(v) != 3 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	ms := v["model_service"].(map[string]any)
	if ms["aliases"].(map[string]any)["live"] != "qwen-live.gguf" || ms["models"].([]any)[0] != "qwen-live.gguf" {
		t.Fatalf("model_service %v", ms)
	}
	e.res.set(LabelLive, Endpoint{}, &NotReadyError{State: StateLoading})
	if r := e.do("GET", "/health", nil, nil); r.status != 200 || r.json(t)["status"] != "initializing" {
		t.Fatalf("loading: %d %s", r.status, r.body)
	}
	e.m.notReadySince = time.Now().Add(-loadingGrace - time.Second)
	if r := e.do("GET", "/health", nil, nil); r.status != 503 {
		t.Fatalf("past grace: %d %s", r.status, r.body)
	}
	e.res.set(LabelLive, Endpoint{}, &NotReadyError{State: StateFailed, Reason: "bad gguf"})
	if r := e.do("GET", "/health", nil, nil); r.status != 503 || r.json(t)["status"] != "degraded" {
		t.Fatalf("failed: %d %s", r.status, r.body)
	}
	e.res.set(LabelLive, Endpoint{}, &NotReadyError{State: StateNotConfigured})
	if r := e.do("GET", "/health", nil, nil); r.status != 200 || r.json(t)["status"] != "not_configured" {
		t.Fatalf("fresh install: %d %s", r.status, r.body)
	}
	if v := e.do("GET", "/v1/health", nil, nil).json(t); v["version"] != nil {
		t.Fatal("/v1/health has no version (legacy)")
	}
}

func mustLoads(t *testing.T, s string) any {
	t.Helper()
	v, err := pyjson.Loads(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestInProcessClientSkipsAppAuth(t *testing.T) {
	e := setup(t)
	c := e.m.InProcessClient()
	res, err := c.Post(InProcessBaseURL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"live","messages":[{"role":"user","content":"hi"}],"max_tokens":5}`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("in-process chat: %d %s", res.StatusCode, b)
	}
	// The same request over the network without credentials is still refused.
	nr, err := http.Post(e.srv.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"live","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	nr.Body.Close()
	if nr.StatusCode != 401 {
		t.Fatalf("network request without creds: %d", nr.StatusCode)
	}
}
