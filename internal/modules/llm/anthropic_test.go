package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// recorder is a canned upstream: it records each request and answers the next scripted reply.
type recorder struct {
	srv *httptest.Server

	mu      sync.Mutex
	paths   []string
	headers []http.Header
	bodies  []string
	replies []canned
}

type canned struct {
	status int
	ctype  string
	body   string
}

func newRecorder(t *testing.T, replies ...canned) *recorder {
	t.Helper()
	r := &recorder{replies: replies}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.paths = append(r.paths, req.URL.Path)
		r.headers = append(r.headers, req.Header.Clone())
		r.bodies = append(r.bodies, string(b))
		var c canned
		if len(r.replies) > 0 {
			c, r.replies = r.replies[0], r.replies[1:]
		}
		r.mu.Unlock()
		if c.ctype == "" {
			c.ctype = "application/json"
		}
		if c.status == 0 {
			c.status = 200
		}
		w.Header().Set("Content-Type", c.ctype)
		w.WriteHeader(c.status)
		io.WriteString(w, c.body)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *recorder) last() (string, http.Header, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := len(r.paths) - 1
	return r.paths[n], r.headers[n], r.bodies[n]
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.paths)
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// anthropicService resolves the live label (and embeddings) to rec as an Anthropic remote.
func anthropicService(rec *recorder, base, model string) *Service {
	ep := Endpoint{BaseURL: rec.srv.URL + base, APIKey: "sk-ant-test", Model: model, Remote: true, API: APIAnthropic, Vision: true}
	emb := ep
	emb.Embeddings = true
	res := &fakeResolver{eps: map[string]Endpoint{LabelLive: ep, LabelEmbeddings: emb}, err: map[string]error{}}
	return NewService(ServiceConfig{Resolver: res, Log: quietLog()})
}

const messageOK = `{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"Hi."}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":2}}`

func ptr[T any](v T) *T { return &v }

func msg(role, text string) Message { return Message{Role: role, Content: TextContent(text)} }

// The request body golden: system hoisting, a leading assistant turn dropped, the tool round
// trip with parallel results merged into one user message, tools and tool_choice translated,
// no sampling knobs, max_tokens defaulted, thinking disabled.
func TestAnthropicRequestGolden(t *testing.T) {
	rec := newRecorder(t, canned{body: messageOK})
	s := anthropicService(rec, "", "claude-sonnet-test")
	req := ChatRequest{
		Messages: []Message{
			msg("assistant", "Hello! How can I help?"),
			msg("system", "You are Jarvis."),
			{Role: "user", Content: &Content{Parts: []Part{{Type: "text", Text: "weather in"}, {Type: "text", Text: ""}, {Type: "text", Text: " Paris and Rome?"}}}},
			msg("system", "Be brief."),
			{Role: "assistant", Content: TextContent("Checking."), ToolCalls: []ToolCall{
				{ID: "call_a", Type: "function", Function: FunctionCall{Name: "get_weather", Arguments: `{"city": "Paris"}`}},
				{ID: "call_b", Type: "function", Function: FunctionCall{Name: "get_weather", Arguments: `{"city":`}},
				{ID: "call_c", Type: "function", Function: FunctionCall{Name: "now", Arguments: ""}},
			}},
			{Role: "tool", ToolCallID: "call_a", Content: TextContent("sunny")},
			{Role: "tool", ToolCallID: "call_b", Content: TextContent("rain")},
			{Role: "tool", ToolCallID: "call_c", Content: nil},
			msg("user", ""),
			msg("user", "thanks"),
		},
		Temperature: ptr(0.2), TopP: ptr(0.9), Seed: ptr(int64(7)), ReasoningBudget: ptr(-1),
		Tools: []Tool{
			{Type: "function", Function: ToolFunction{Name: "get_weather", Description: "Weather for a city",
				Parameters: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`)}},
			{Type: "function", Function: ToolFunction{Name: "now"}},
		},
		ToolChoice: json.RawMessage(`"required"`),
	}
	if _, err := s.Chat(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	_, _, body := rec.last()
	want := `{"model":"claude-sonnet-test","system":"You are Jarvis.\n\nBe brief.","messages":[` +
		`{"role":"user","content":[{"type":"text","text":"weather in"},{"type":"text","text":" Paris and Rome?"}]},` +
		`{"role":"assistant","content":[{"type":"text","text":"Checking."},` +
		`{"type":"tool_use","id":"call_a","name":"get_weather","input":{"city":"Paris"}},` +
		`{"type":"tool_use","id":"call_b","name":"get_weather","input":{}},` +
		`{"type":"tool_use","id":"call_c","name":"now","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_a","content":"sunny"},` +
		`{"type":"tool_result","tool_use_id":"call_b","content":"rain"},` +
		`{"type":"tool_result","tool_use_id":"call_c","content":""},` +
		`{"type":"text","text":"thanks"}]}],` +
		`"max_tokens":4096,"stream":false,"thinking":{"type":"disabled"},` +
		`"tools":[{"name":"get_weather","description":"Weather for a city","input_schema":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}},` +
		`{"name":"now","input_schema":{"type":"object","properties":{}}}],` +
		`"tool_choice":{"type":"any"}}`
	if body != want {
		t.Fatalf("body\n got %s\nwant %s", body, want)
	}
}

func TestAnthropicMaxTokensAndThinking(t *testing.T) {
	for _, tc := range []struct {
		model    string
		max      *int
		thinking bool
		wantMax  float64
	}{
		{"claude-sonnet-test", ptr(300), true, 300},
		{"claude-sonnet-test", ptr(0), true, 4096},
		{"claude-fable-test", nil, false, 4096},
		{"Claude-Mythos-Test", nil, false, 4096},
	} {
		rec := newRecorder(t, canned{body: messageOK})
		s := anthropicService(rec, "", tc.model)
		if _, err := s.Chat(context.Background(), ChatRequest{Messages: []Message{msg("user", "hi")}, MaxTokens: tc.max}); err != nil {
			t.Fatal(err)
		}
		_, _, raw := rec.last()
		var b map[string]any
		json.Unmarshal([]byte(raw), &b)
		_, has := b["thinking"]
		if has != tc.thinking || b["max_tokens"] != tc.wantMax {
			t.Errorf("%s: thinking sent %v, max_tokens %v in %s", tc.model, has, b["max_tokens"], raw)
		}
		for _, k := range []string{"temperature", "top_p", "seed", "chat_template_kwargs", "reasoning_budget", "stream_options"} {
			if _, ok := b[k]; ok {
				t.Errorf("%s: %s sent", tc.model, k)
			}
		}
	}
}

func TestAnthropicHeadersAndPath(t *testing.T) {
	for _, base := range []string{"", "/", "/v1", "/v1/"} {
		rec := newRecorder(t, canned{body: messageOK})
		s := anthropicService(rec, base, "claude-sonnet-test")
		if _, err := s.Chat(context.Background(), ChatRequest{Messages: []Message{msg("user", "hi")}}); err != nil {
			t.Fatal(err)
		}
		path, h, _ := rec.last()
		if path != "/v1/messages" || h.Get("x-api-key") != "sk-ant-test" || h.Get("anthropic-version") != "2023-06-01" ||
			h.Get("Authorization") != "" || h.Get("Content-Type") != "application/json" {
			t.Errorf("base %q: path %s headers %v", base, path, h)
		}
	}
}

func TestAnthropicImages(t *testing.T) {
	rec := newRecorder(t, canned{body: messageOK})
	s := anthropicService(rec, "", "claude-sonnet-test")
	_, err := s.Chat(context.Background(), ChatRequest{Messages: []Message{{Role: "user", Content: &Content{Parts: []Part{
		{Type: "text", Text: "what is this?"},
		{Type: "image_url", ImageURL: &ImageURL{URL: "data:image/png;base64,iVBORw0KGgo="}},
	}}}}})
	if err != nil {
		t.Fatal(err)
	}
	_, _, body := rec.last()
	if !strings.Contains(body, `{"type":"text","text":"what is this?"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}}`) {
		t.Fatalf("image not translated: %s", body)
	}
}

func TestAnthropicToolChoice(t *testing.T) {
	for in, want := range map[string]string{
		`"auto"`:     `{"type":"auto"}`,
		`"none"`:     `{"type":"none"}`,
		`"required"`: `{"type":"any"}`,
		`"bogus"`:    `{"type":"auto"}`,
		`{"type":"function","function":{"name":"get_weather"}}`: `{"type":"tool","name":"get_weather"}`,
		`{"type":"function"}`:        `{"type":"any"}`,
		`{"type":"tool"}`:            `{"type":"any"}`,
		`{"type":"tool","name":"x"}`: `{"type":"tool","name":"x"}`,
		`{"type":"any"}`:             `{"type":"any"}`,
		`{"type":"unknown"}`:         `{"type":"auto"}`,
		`null`:                       ``,
		``:                           ``,
	} {
		if got := string(anthropicToolChoice(json.RawMessage(in))); got != want {
			t.Errorf("%s: got %s want %s", in, got, want)
		}
	}
}

func TestAnthropicToolAlreadyAnthropicShaped(t *testing.T) {
	var tools []Tool
	if err := json.Unmarshal([]byte(`[{"name":"x","description":"d","input_schema":{"type":"object"}},{"type":"function","function":{"name":"y","parameters":"bad"}},{"type":"function","function":{}}]`), &tools); err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(anthropicTools(tools))
	want := `[{"name":"x","description":"d","input_schema":{"type":"object"}},{"name":"y","input_schema":{"type":"object","properties":{}}}]`
	if string(got) != want {
		t.Fatalf("got %s", got)
	}
}

func TestAnthropicToolMessageNeedsID(t *testing.T) {
	rec := newRecorder(t)
	s := anthropicService(rec, "", "claude-sonnet-test")
	req := ChatRequest{Messages: []Message{msg("user", "hi"), {Role: "tool", Content: TextContent("x")}}}
	_, err := s.Chat(context.Background(), req)
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 400 || !strings.Contains(ae.Message, "tool_call_id") {
		t.Fatalf("chat: %v", err)
	}
	if _, err := s.Stream(context.Background(), req); !errors.As(err, &ae) || ae.Status != 400 {
		t.Fatalf("stream: %v", err)
	}
	if _, err := s.Chat(context.Background(), ChatRequest{Messages: []Message{msg("system", "only")}}); !errors.As(err, &ae) || ae.Status != 400 {
		t.Fatalf("system only: %v", err)
	}
	if rec.count() != 0 {
		t.Fatalf("sent %d requests", rec.count())
	}
}

func TestAnthropicNonStream(t *testing.T) {
	rec := newRecorder(t, canned{body: `{"type":"message","content":[` +
		`{"type":"thinking","thinking":"hmm","signature":"sig"},` +
		`{"type":"text","text":"Let me check "},` +
		`{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{"city": "Paris"}},` +
		`{"type":"text","text":"both."},` +
		`{"type":"tool_use","id":"toolu_2","name":"now","input":{}}],` +
		`"stop_reason":"tool_use","usage":{"input_tokens":10,"cache_read_input_tokens":100,"cache_creation_input_tokens":20,"output_tokens":7}}`})
	s := anthropicService(rec, "", "claude-sonnet-test")
	resp, err := s.Chat(context.Background(), ChatRequest{Messages: []Message{msg("user", "hi")},
		Tools: []Tool{{Function: ToolFunction{Name: "get_weather"}}, {Function: ToolFunction{Name: "now"}}}})
	if err != nil {
		t.Fatal(err)
	}
	want := []ToolCall{
		{ID: "toolu_1", Type: "function", Function: FunctionCall{Name: "get_weather", Arguments: `{"city":"Paris"}`}},
		{ID: "toolu_2", Type: "function", Function: FunctionCall{Name: "now", Arguments: `{}`}},
	}
	if resp.Content != "Let me check both." || resp.FinishReason != "tool_calls" || len(resp.ToolCalls) != 2 ||
		resp.ToolCalls[0] != want[0] || resp.ToolCalls[1] != want[1] {
		t.Fatalf("%+v", resp)
	}
	if resp.Usage != (Usage{PromptTokens: 130, CompletionTokens: 7, TotalTokens: 137}) {
		t.Fatalf("usage %+v", resp.Usage)
	}
}

func TestAnthropicFinishReason(t *testing.T) {
	for in, want := range map[string]string{"tool_use": "tool_calls", "max_tokens": "length",
		"model_context_window_exceeded": "length", "refusal": "content_filter", "end_turn": "stop",
		"stop_sequence": "stop", "pause_turn": "stop", "": "stop"} {
		if got := finishReason(in); got != want {
			t.Errorf("%q: %s", in, got)
		}
	}
}

func TestAnthropicErrorBody(t *testing.T) {
	rec := newRecorder(t, canned{status: 400,
		body: `{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens: Field required"},"request_id":"req_1"}`})
	s := anthropicService(rec, "", "claude-sonnet-test")
	_, err := s.Chat(context.Background(), ChatRequest{Messages: []Message{msg("user", "hi")}})
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 400 || ae.Message != "invalid_request_error: max_tokens: Field required" {
		t.Fatalf("%v", err)
	}
	if got := anthropicErrorMessage([]byte("upstream timeout")); got != "upstream timeout" {
		t.Fatalf("non-JSON body: %q", got)
	}
}

// sse renders events as the Messages API streams them: an event line and a data line each.
func sse(events ...string) string {
	var sb strings.Builder
	for _, e := range events {
		var typ struct {
			Type string `json:"type"`
		}
		json.Unmarshal([]byte(e), &typ)
		sb.WriteString("event: " + typ.Type + "\ndata: " + e + "\n\n")
	}
	return sb.String()
}

// Interleaved text and two tool calls whose input arrives in fragments; a thinking block and
// pings in between; message_delta repeats input_tokens without the cache counters.
var interleavedStream = sse(
	`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"usage":{"input_tokens":12,"cache_read_input_tokens":200,"cache_creation_input_tokens":30,"output_tokens":1}}}`,
	`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"secret plan"}}`,
	`{"type":"content_block_stop","index":0}`,
	`{"type":"ping"}`,
	`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
	`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Checking "}}`,
	`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"both."}}`,
	`{"type":"content_block_stop","index":1}`,
	`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_a","name":"get_weather","input":{}}}`,
	`{"type":"content_block_start","index":3,"content_block":{"type":"tool_use","id":"toolu_b","name":"get_weather","input":{}}}`,
	`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":""}}`,
	`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"ci"}}`,
	`{"type":"content_block_delta","index":3,"delta":{"type":"input_json_delta","partial_json":"{\"city\": \"Ro"}}`,
	`{"type":"ping"}`,
	`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"ty\": \"Paris\"}"}}`,
	`{"type":"content_block_delta","index":3,"delta":{"type":"input_json_delta","partial_json":"me\"}"}}`,
	`{"type":"content_block_stop","index":2}`,
	`{"type":"content_block_stop","index":3}`,
	`{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"input_tokens":12,"output_tokens":41}}`,
	`{"type":"message_stop"}`,
)

func TestAnthropicStreamInterleaved(t *testing.T) {
	var deltas []string
	res, err := parseAnthropicStream(strings.NewReader(interleavedStream), func(d string) bool {
		deltas = append(deltas, d)
		return true
	}, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(deltas, "|") != "Checking |both." || res.Content != "Checking both." || res.FinishReason != "tool_calls" {
		t.Fatalf("deltas %q, %+v", deltas, res)
	}
	if len(res.ToolCalls) != 2 {
		t.Fatalf("tool calls %+v", res.ToolCalls)
	}
	for i, want := range []struct{ id, args string }{{"toolu_a", `{"city":"Paris"}`}, {"toolu_b", `{"city":"Rome"}`}} {
		tc := res.ToolCalls[i]
		if *tc.ID != want.id || *tc.Name != "get_weather" || tc.Type != "function" || tc.Arguments != want.args {
			t.Errorf("call %d: %s %s %q", i, *tc.ID, *tc.Name, tc.Arguments)
		}
	}
	// The cache counters from message_start survive a message_delta that repeats only
	// input_tokens (#75 replaced the prompt count with the uncached 12).
	if res.Usage != (Usage{PromptTokens: 242, CompletionTokens: 41, TotalTokens: 283}) {
		t.Fatalf("usage %+v", res.Usage)
	}
}

func TestAnthropicStreamUsageCumulativeDelta(t *testing.T) {
	stream := sse(
		`{"type":"message_start","message":{"usage":{"input_tokens":0,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"Hi"}}`,
		`{"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"input_tokens":15,"cache_read_input_tokens":50,"cache_creation_input_tokens":5,"output_tokens":9}}`,
		`{"type":"message_stop"}`,
	)
	var deltas []string
	res, err := parseAnthropicStream(strings.NewReader(stream), func(d string) bool { deltas = append(deltas, d); return true }, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	if res.Usage != (Usage{PromptTokens: 70, CompletionTokens: 9, TotalTokens: 79}) || res.FinishReason != "length" ||
		res.Content != "Hi" || len(deltas) != 1 || res.ToolCalls != nil {
		t.Fatalf("%+v %q", res, deltas)
	}
}

func TestAnthropicStreamErrors(t *testing.T) {
	midError := sse(
		`{"type":"message_start","message":{"usage":{"input_tokens":3}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hel"}}`,
		`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`,
	)
	if _, err := parseAnthropicStream(strings.NewReader(midError), func(string) bool { return true }, quietLog()); err == nil ||
		err.Error() != "Anthropic stream error (overloaded_error): Overloaded" {
		t.Fatalf("error event: %v", err)
	}
	cut := sse(
		`{"type":"message_start","message":{"usage":{"input_tokens":3}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hel"}}`,
	)
	if _, err := parseAnthropicStream(strings.NewReader(cut), func(string) bool { return true }, quietLog()); !errors.Is(err, errNoMessageStop) {
		t.Fatalf("cut stream: %v", err)
	}
	if _, err := parseAnthropicStream(strings.NewReader(interleavedStream), func(string) bool { return false }, quietLog()); !errors.Is(err, context.Canceled) {
		t.Fatalf("stopped reader: %v", err)
	}
}

// collect drains a stream's frames, with a hang guard.
func collect(t *testing.T, ch <-chan Frame) []Frame {
	t.Helper()
	var out []Frame
	guard := time.After(30 * time.Second)
	for {
		select {
		case f, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, f)
		case <-guard:
			t.Fatal("stream hung")
		}
	}
}

func TestAnthropicServiceStream(t *testing.T) {
	rec := newRecorder(t,
		canned{ctype: "text/event-stream", body: interleavedStream},
		canned{ctype: "text/event-stream", body: sse(
			`{"type":"message_start","message":{"usage":{"input_tokens":3}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hel"}}`,
			`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)},
	)
	s := anthropicService(rec, "/v1", "claude-sonnet-test")
	ch, err := s.Stream(context.Background(), ChatRequest{Messages: []Message{msg("user", "hi")}, Temperature: ptr(0.3)})
	if err != nil {
		t.Fatal(err)
	}
	frames := collect(t, ch)
	last := frames[len(frames)-1]
	if len(frames) != 3 || frames[0].Delta != "Checking " || !last.Done || last.Content != "Checking both." ||
		len(last.ToolCalls) != 2 || last.FinishReason != "tool_calls" || last.Usage.PromptTokens != 242 {
		t.Fatalf("frames %+v", frames)
	}
	path, h, body := rec.last()
	var b map[string]any
	json.Unmarshal([]byte(body), &b)
	if path != "/v1/messages" || h.Get("x-api-key") != "sk-ant-test" || b["stream"] != true || b["temperature"] != nil {
		t.Fatalf("%s %v %s", path, h, body)
	}

	ch, err = s.Stream(context.Background(), ChatRequest{Messages: []Message{msg("user", "hi")}})
	if err != nil {
		t.Fatal(err)
	}
	frames = collect(t, ch)
	last = frames[len(frames)-1]
	if len(frames) != 2 || frames[0].Delta != "Hel" || last.Done || last.Err != "Anthropic stream error (overloaded_error): Overloaded" {
		t.Fatalf("error frames %+v", frames)
	}
}

func TestAnthropicEmbeddingsRejected(t *testing.T) {
	rec := newRecorder(t)
	s := anthropicService(rec, "", "claude-sonnet-test")
	_, err := s.Embed(context.Background(), []string{"hello"})
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != http.StatusServiceUnavailable || !strings.Contains(ae.Message, "Anthropic API has no embeddings") {
		t.Fatalf("%v", err)
	}
	if rec.count() != 0 {
		t.Fatal("embeddings request sent to Anthropic")
	}
}

// The OpenAI path is untouched: same body, bearer auth, no Anthropic headers.
func TestOpenAIPathUnchanged(t *testing.T) {
	rec := newRecorder(t, canned{body: `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`})
	ep := Endpoint{BaseURL: rec.srv.URL, APIKey: "sk-openai", Model: "gpt-test", Remote: true}
	s := NewService(ServiceConfig{Resolver: &fakeResolver{eps: map[string]Endpoint{LabelLive: ep}, err: map[string]error{}}, Log: quietLog()})
	_, err := s.Chat(context.Background(), ChatRequest{
		Messages:    []Message{msg("system", "sys"), msg("user", "hi")},
		Temperature: ptr(0.2), MaxTokens: ptr(50), ReasoningBudget: ptr(0),
		Tools:      []Tool{{Type: "function", Function: ToolFunction{Name: "now"}}},
		ToolChoice: json.RawMessage(`"auto"`),
	})
	if err != nil {
		t.Fatal(err)
	}
	path, h, body := rec.last()
	want := `{"model":"gpt-test","messages":[{"role":"system","content":"sys"},{"role":"user","content":"hi"}],` +
		`"temperature":0.2,"stream":false,"max_tokens":50,"tools":[{"type":"function","function":{"name":"now"}}],` +
		`"tool_choice":"auto","chat_template_kwargs":{"enable_thinking":false},"reasoning_budget":0}`
	if path != "/v1/chat/completions" || body != want {
		t.Fatalf("%s\n got %s\nwant %s", path, body, want)
	}
	if h.Get("Authorization") != "Bearer sk-openai" || h.Get("x-api-key") != "" || h.Get("anthropic-version") != "" {
		t.Fatalf("headers %v", h)
	}
}

// TestAnthropicLive talks to the real API. It is skipped unless JARVIS_TEST_ANTHROPIC_KEY is
// set (CI: the anthropic-live workflow, from the ANTHROPIC_API_KEY secret). The model is
// JARVIS_TEST_ANTHROPIC_MODEL, default claude-haiku-4-5-20251001; max_tokens stays small.
func TestAnthropicLive(t *testing.T) {
	key := os.Getenv("JARVIS_TEST_ANTHROPIC_KEY")
	if key == "" {
		t.Skip("set JARVIS_TEST_ANTHROPIC_KEY to run against api.anthropic.com")
	}
	model := os.Getenv("JARVIS_TEST_ANTHROPIC_MODEL")
	if model == "" {
		model = "claude-haiku-4-5-20251001"
	}
	ep := Endpoint{BaseURL: "https://api.anthropic.com", APIKey: key, Model: model, Remote: true, API: APIAnthropic}
	s := NewService(ServiceConfig{Resolver: &fakeResolver{eps: map[string]Endpoint{LabelLive: ep}, err: map[string]error{}}, Log: quietLog()})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	tools := []Tool{{Type: "function", Function: ToolFunction{Name: "get_weather", Description: "Current weather for a city",
		Parameters: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`)}}}
	weatherQ := []Message{msg("system", "Always use the get_weather tool for weather questions."), msg("user", "What's the weather in Paris?")}

	t.Run("chat", func(t *testing.T) {
		r, err := s.Chat(ctx, ChatRequest{Messages: []Message{msg("system", "Answer in one word."), msg("user", "What colour is the sky on a clear day?")},
			MaxTokens: ptr(32)})
		if err != nil {
			t.Fatal(err)
		}
		if r.Content == "" || r.FinishReason != "stop" || r.Usage.PromptTokens == 0 || r.Usage.CompletionTokens == 0 {
			t.Fatalf("%+v", r)
		}
		t.Logf("chat: %q %+v", r.Content, r.Usage)
	})

	t.Run("tool round trip", func(t *testing.T) {
		first, err := s.Chat(ctx, ChatRequest{Messages: weatherQ, Tools: tools, MaxTokens: ptr(256)})
		if err != nil {
			t.Fatal(err)
		}
		if first.FinishReason != "tool_calls" || len(first.ToolCalls) == 0 || first.ToolCalls[0].Function.Name != "get_weather" {
			t.Fatalf("no tool call: %+v", first)
		}
		var args struct {
			City string `json:"city"`
		}
		if err := json.Unmarshal([]byte(first.ToolCalls[0].Function.Arguments), &args); err != nil || args.City == "" {
			t.Fatalf("arguments %q: %v", first.ToolCalls[0].Function.Arguments, err)
		}
		msgs := append(append([]Message{}, weatherQ...), Message{Role: "assistant", Content: TextContent(first.Content), ToolCalls: first.ToolCalls})
		for _, tc := range first.ToolCalls {
			msgs = append(msgs, Message{Role: "tool", ToolCallID: tc.ID, Content: TextContent(`{"weather":"sunny, 21C"}`)})
		}
		final, err := s.Chat(ctx, ChatRequest{Messages: msgs, Tools: tools, MaxTokens: ptr(256)})
		if err != nil {
			t.Fatal(err)
		}
		if final.FinishReason != "stop" || len(final.ToolCalls) != 0 || final.Content == "" {
			t.Fatalf("final %+v", final)
		}
		t.Logf("round trip: %q", final.Content)
	})

	t.Run("stream tool call", func(t *testing.T) {
		ch, err := s.Stream(ctx, ChatRequest{Messages: weatherQ, Tools: tools, MaxTokens: ptr(256)})
		if err != nil {
			t.Fatal(err)
		}
		var last Frame
		for f := range ch {
			last = f
		}
		if !last.Done || last.FinishReason != "tool_calls" || len(last.ToolCalls) == 0 || last.Usage.PromptTokens == 0 {
			t.Fatalf("final frame %+v", last)
		}
		tc := last.ToolCalls[0]
		if tc.ID == nil || *tc.ID == "" || tc.Name == nil || *tc.Name != "get_weather" || !json.Valid([]byte(tc.Arguments)) {
			t.Fatalf("tool call %+v", tc)
		}
		t.Logf("streamed tool call: %s %s", *tc.Name, tc.Arguments)
	})
}
