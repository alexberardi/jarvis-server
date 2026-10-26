package llm

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
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
// remote Gemini gets no cap and "minimal" effort for thinking off; sampling is kept.
func TestPayloadRemoteGemini(t *testing.T) {
	s := NewService(ServiceConfig{Log: quietLog()})
	for budget, effort := range map[int]string{0: `"reasoning_effort":"minimal"`, -1: ""} {
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
