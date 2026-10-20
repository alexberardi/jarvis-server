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
