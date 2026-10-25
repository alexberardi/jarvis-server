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
