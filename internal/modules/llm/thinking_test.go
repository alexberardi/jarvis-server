package llm

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// An engine that refuses reasoning_budget (transformers serve: 422 for any field outside
// OpenAI's schema) gets the thinking switch by chat_template_kwargs alone.
func TestPayloadWithoutReasoningBudget(t *testing.T) {
	s := NewService(ServiceConfig{Log: quietLog()})
	req := &ChatRequest{Messages: []Message{msg("user", "hi")}, ReasoningBudget: ptr(0)}
	for _, c := range []struct {
		ep   Endpoint
		want string
	}{
		{Endpoint{Model: "live"}, `"chat_template_kwargs":{"enable_thinking":false},"reasoning_budget":0}`},
		{Endpoint{Model: "/m/SmolLM2", NoReasoningBudget: true}, `"chat_template_kwargs":{"enable_thinking":false}}`},
	} {
		b, err := json.Marshal(s.payload(context.Background(), c.ep, LabelLive, req, req.Messages, false))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(string(b), c.want) {
			t.Errorf("%+v: %s", c.ep, b)
		}
	}
}

// llama-server ignores reasoning_budget; a capped budget (N>0) also goes out under its own
// name, reasoning_budget_tokens. -1 (uncapped) and 0 (off) don't.
func TestPayloadReasoningBudgetTokens(t *testing.T) {
	s := NewService(ServiceConfig{Log: quietLog()})
	for _, c := range []struct {
		budget int
		want   string
	}{
		{256, `"chat_template_kwargs":{"enable_thinking":true},"reasoning_budget":256,"reasoning_budget_tokens":256}`},
		{-1, `"chat_template_kwargs":{"enable_thinking":true},"reasoning_budget":-1}`},
		{0, `"chat_template_kwargs":{"enable_thinking":false},"reasoning_budget":0}`},
	} {
		req := &ChatRequest{Messages: []Message{msg("user", "hi")}, ReasoningBudget: ptr(c.budget)}
		b, err := json.Marshal(s.payload(context.Background(), Endpoint{Model: "live"}, LabelLive, req, req.Messages, false))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(string(b), c.want) {
			t.Errorf("budget %d: %s", c.budget, b)
		}
	}
}

// A remote (OpenAI and its kin) refuses llama-server's thinking fields ("Unknown parameter:
// 'chat_template_kwargs'") and, on newer models, max_tokens ("Use 'max_completion_tokens'").
func TestPayloadRemoteSendsNoThinkingFields(t *testing.T) {
	s := NewService(ServiceConfig{Log: quietLog()})
	for _, budget := range []int{0, -1, 256} {
		req := &ChatRequest{Messages: []Message{msg("user", "hi")}, ReasoningBudget: ptr(budget), MaxTokens: ptr(50)}
		b, err := json.Marshal(s.payload(context.Background(), Endpoint{Model: "gpt", Remote: true}, LabelLive, req, req.Messages, false))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "chat_template_kwargs") || strings.Contains(string(b), "reasoning_budget") ||
			strings.Contains(string(b), `"max_tokens"`) || !strings.Contains(string(b), `"max_completion_tokens":50`) {
			t.Errorf("budget %d: %s", budget, b)
		}
	}
}

// OpenAI's reasoning models take only the default temperature and top_p ("Unsupported value:
// 'temperature' does not support 0.4 with this model"), and a short cap leaves them no room to
// answer after thinking.
func TestPayloadRemoteReasoningModelSendsNoSampling(t *testing.T) {
	s := NewService(ServiceConfig{Log: quietLog()})
	for model, fixed := range map[string]bool{"gpt-5-mini": true, "openai/o4-mini": true, "o3": true, "gpt-4o": false, "llama-3.3-70b": false} {
		req := &ChatRequest{Messages: []Message{msg("user", "hi")}, Temperature: ptr(0.4), TopP: ptr(0.9), MaxTokens: ptr(256), ReasoningBudget: ptr(0)}
		b, err := json.Marshal(s.payload(context.Background(), Endpoint{Model: model, Remote: true}, LabelLive, req, req.Messages, false))
		if err != nil {
			t.Fatal(err)
		}
		body := string(b)
		if has := strings.Contains(body, `"temperature":0.4`) && strings.Contains(body, `"top_p":0.9`) &&
			strings.Contains(body, `"max_completion_tokens":256`) && !strings.Contains(body, "reasoning_effort"); has == fixed {
			t.Errorf("%s: %s", model, body)
		}
		if fixed && !strings.Contains(body, `"reasoning_effort":"low"`) {
			t.Errorf("%s: thinking off but no low effort: %s", model, body)
		}
	}
	// A local engine keeps an explicit 0.
	req := &ChatRequest{Messages: []Message{msg("user", "hi")}, Temperature: ptr(0.0)}
	if b, _ := json.Marshal(s.payload(context.Background(), Endpoint{Model: "o3"}, LabelLive, req, req.Messages, false)); !strings.Contains(string(b), `"temperature":0`) {
		t.Errorf("local: %s", b)
	}
}

// Gemini counts thinking against the cap and 2.5 Pro / 3.x can't turn thinking off, so a
// remote Gemini gets no cap and "low" effort for thinking off; sampling is kept.
func TestPayloadRemoteGemini(t *testing.T) {
	s := NewService(ServiceConfig{Log: quietLog()})
	for budget, effort := range map[int]string{0: `"reasoning_effort":"low"`, -1: ""} {
		req := &ChatRequest{Messages: []Message{msg("user", "hi")}, Temperature: ptr(0.4), MaxTokens: ptr(256), ReasoningBudget: ptr(budget)}
		b, err := json.Marshal(s.payload(context.Background(), Endpoint{Model: "models/gemini-2.5-flash", Remote: true}, LabelLive, req, req.Messages, false))
		if err != nil {
			t.Fatal(err)
		}
		body := string(b)
		if strings.Contains(body, "max_completion_tokens") || strings.Contains(body, `"max_tokens"`) || !strings.Contains(body, `"temperature":0.4`) ||
			strings.Contains(body, "reasoning_effort") != (effort != "") || !strings.Contains(body, effort) {
			t.Errorf("budget %d: %s", budget, body)
		}
	}
}

func TestEndpointURLKeepsGooglesBase(t *testing.T) {
	for base, want := range map[string]string{
		"https://generativelanguage.googleapis.com/v1beta/openai": "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions",
		"https://api.openai.com/v1":                               "https://api.openai.com/v1/chat/completions",
		"http://127.0.0.1:8080":                                   "http://127.0.0.1:8080/v1/chat/completions",
	} {
		if got := endpointURL(base, "/chat/completions"); got != want {
			t.Errorf("%s → %s", base, got)
		}
	}
}

// Gemini 3 refuses a history tool call without a thought signature: one it sent is kept,
// any other gets Google's stand-in, and the caller's messages are left alone.
func TestPayloadGeminiThoughtSignatures(t *testing.T) {
	s := NewService(ServiceConfig{Log: quietLog()})
	real := json.RawMessage(`{"google":{"thought_signature":"abc"}}`)
	msgs := []Message{msg("user", "add milk"),
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "1", Type: "function", Function: FunctionCall{Name: "remember", Arguments: "{}"}},
			{ID: "2", Type: "function", Function: FunctionCall{Name: "todo", Arguments: "{}"}, ExtraContent: real}}},
		{Role: "tool", ToolCallID: "1", Content: TextContent("ok")}}
	req := &ChatRequest{Messages: msgs}
	b, _ := json.Marshal(s.payload(context.Background(), Endpoint{Model: "gemini-3.1-flash-lite", Remote: true}, LabelLive, req, msgs, false))
	if !strings.Contains(string(b), `"extra_content":{"google":{"thought_signature":"skip_thought_signature_validator"}}`) ||
		!strings.Contains(string(b), `"thought_signature":"abc"`) {
		t.Fatalf("%s", b)
	}
	if msgs[1].ToolCalls[0].ExtraContent != nil {
		t.Fatal("caller's history was changed")
	}
	b, _ = json.Marshal(s.payload(context.Background(), Endpoint{Model: "gpt-4o", Remote: true}, LabelLive, req, msgs, false))
	if strings.Contains(string(b), "skip_thought_signature_validator") {
		t.Fatalf("non-Gemini got a signature: %s", b)
	}
}

// A remote's transient refusal (Gemini's 503 "experiencing high demand", a 429) is retried;
// a local engine's and a 400 are not.
func TestRemoteTransientErrorsRetry(t *testing.T) {
	old := remoteRetryDelays
	remoteRetryDelays = []time.Duration{0, 0}
	t.Cleanup(func() { remoteRetryDelays = old })
	ok := canned{body: `{"choices":[{"message":{"content":"hi"},"finish_reason":"stop"}]}`}
	busy := canned{status: 503, body: `{"error":{"message":"This model is currently experiencing high demand."}}`}
	chat := func(rec *recorder, remote bool) error {
		ep := Endpoint{BaseURL: rec.srv.URL, Model: "gemini-3.1-flash-lite", Remote: remote}
		s := NewService(ServiceConfig{Resolver: &fakeResolver{eps: map[string]Endpoint{LabelLive: ep}, err: map[string]error{}}, Log: quietLog()})
		_, err := s.Chat(context.Background(), ChatRequest{Messages: []Message{msg("user", "hi")}})
		return err
	}
	if rec := newRecorder(t, busy, canned{status: 429}, ok); chat(rec, true) != nil || rec.count() != 3 {
		t.Fatalf("remote: %d requests", rec.count())
	}
	if rec := newRecorder(t, busy, busy, busy, ok); chat(rec, true) == nil || rec.count() != 3 {
		t.Fatalf("remote gives up after 2 retries: %d requests", rec.count())
	}
	if rec := newRecorder(t, canned{status: 400}, ok); chat(rec, true) == nil || rec.count() != 1 {
		t.Fatalf("a 400 is not retried: %d requests", rec.count())
	}
	if rec := newRecorder(t, busy, ok); chat(rec, false) == nil || rec.count() != 1 {
		t.Fatalf("a local engine is not retried: %d requests", rec.count())
	}
}

// Gemini's thought signature on a returned tool call is kept, so it goes back as sent.
func TestToolCallKeepsExtraContent(t *testing.T) {
	rec := newRecorder(t, canned{body: `{"choices":[{"message":{"content":null,"tool_calls":[{"id":"c1","type":"function",` +
		`"function":{"name":"get_weather","arguments":"{}"},"extra_content":{"google":{"thought_signature":"sig"}}}]},"finish_reason":"tool_calls"}]}`})
	ep := Endpoint{BaseURL: rec.srv.URL, Model: "gemini-3.1-flash-lite", Remote: true}
	s := NewService(ServiceConfig{Resolver: &fakeResolver{eps: map[string]Endpoint{LabelLive: ep}, err: map[string]error{}}, Log: quietLog()})
	r, err := s.Chat(context.Background(), ChatRequest{Messages: []Message{msg("user", "weather?")}})
	if err != nil || len(r.ToolCalls) != 1 || string(r.ToolCalls[0].ExtraContent) != `{"google":{"thought_signature":"sig"}}` {
		t.Fatalf("%+v %v", r, err)
	}
}
