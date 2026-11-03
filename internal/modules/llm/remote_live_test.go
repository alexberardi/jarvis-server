package llm

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/livetest"
)

// TestRemoteLive talks to the real cloud APIs: every provider with a JARVIS_TEST_<NAME>_KEY
// (livetest; CI: the cloud-live workflow). Each gets the requests jarvisd makes of a remote
// label, so a parameter a provider refuses ("Unsupported parameter: 'max_tokens'"), a
// thinking level it doesn't take or a history it won't accept fails here, not at a user.
func TestRemoteLive(t *testing.T) {
	ps := livetest.Providers()
	if len(ps) == 0 {
		t.Skip("set JARVIS_TEST_{ANTHROPIC,OPENAI,GEMINI}_KEY to run against the cloud APIs")
	}
	for _, p := range ps {
		t.Run(p.Name, func(t *testing.T) {
			remoteLiveChecks(t, &livetest.Busy{}, Endpoint{BaseURL: p.BaseURL, APIKey: p.Key, Model: p.Model, Remote: true, API: p.API})
		})
	}
}

func remoteLiveChecks(t *testing.T, busy *livetest.Busy, ep Endpoint) {
	s := NewService(ServiceConfig{Resolver: &fakeResolver{eps: map[string]Endpoint{LabelLive: ep}, err: map[string]error{}},
		HTTPClient: busy.Client(), Log: quietLog()})
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	tools := []Tool{{Type: "function", Function: ToolFunction{Name: "get_weather", Description: "Current weather for a city",
		Parameters: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`)}}}
	weatherQ := []Message{msg("system", "Always use the get_weather tool for weather questions."), msg("user", "What's the weather in Paris?")}
	weatherResult := TextContent(`{"weather":"sunny, 21C"}`)

	// wantToolCall checks a tool call is well formed: an id, get_weather, a JSON object with a city.
	wantToolCall := func(t *testing.T, calls []ToolCall) {
		t.Helper()
		if len(calls) == 0 || calls[0].ID == "" || calls[0].Function.Name != "get_weather" {
			busy.Fatalf(t, "no get_weather call: %+v", calls)
		}
		var args struct {
			City string `json:"city"`
		}
		if err := json.Unmarshal([]byte(calls[0].Function.Arguments), &args); err != nil || args.City == "" {
			busy.Fatalf(t, "arguments %q: %v", calls[0].Function.Arguments, err)
		}
	}

	t.Run("chat", func(t *testing.T) {
		r, err := s.Chat(ctx, ChatRequest{Messages: []Message{msg("system", "Answer in one word."), msg("user", "What colour is the sky on a clear day?")},
			MaxTokens: ptr(32)})
		if err != nil {
			busy.Fatalf(t, "%v", err)
		}
		if r.Content == "" || r.FinishReason != "stop" || r.Usage.PromptTokens == 0 || r.Usage.CompletionTokens == 0 {
			busy.Fatalf(t, "%+v", r)
		}
		t.Logf("chat: %q %+v", r.Content, r.Usage)
	})

	// cc turns thinking off (budget 0) for voice: each provider's lowest setting must be accepted.
	t.Run("thinking off", func(t *testing.T) {
		r, err := s.Chat(ctx, ChatRequest{Messages: []Message{msg("user", "Say hello.")}, MaxTokens: ptr(64), ReasoningBudget: ptr(0), Temperature: ptr(0.4)})
		if err != nil {
			busy.Fatalf(t, "%v", err)
		}
		if r.Content == "" {
			busy.Fatalf(t, "empty answer: %+v", r)
		}
	})

	t.Run("tool round trip", func(t *testing.T) {
		first, err := s.Chat(ctx, ChatRequest{Messages: weatherQ, Tools: tools, MaxTokens: ptr(256)})
		if err != nil {
			busy.Fatalf(t, "%v", err)
		}
		if first.FinishReason != "tool_calls" {
			busy.Fatalf(t, "finish %q: %+v", first.FinishReason, first)
		}
		wantToolCall(t, first.ToolCalls)
		msgs := append(append([]Message{}, weatherQ...), Message{Role: "assistant", Content: TextContent(first.Content), ToolCalls: first.ToolCalls})
		for _, tc := range first.ToolCalls {
			msgs = append(msgs, Message{Role: "tool", ToolCallID: tc.ID, Content: weatherResult})
		}
		final, err := s.Chat(ctx, ChatRequest{Messages: msgs, Tools: tools, MaxTokens: ptr(256)})
		if err != nil {
			busy.Fatalf(t, "%v", err)
		}
		if final.FinishReason != "stop" || len(final.ToolCalls) != 0 || final.Content == "" {
			busy.Fatalf(t, "final %+v", final)
		}
		t.Logf("round trip: %q", final.Content)
	})

	// cc's history keeps no vendor data on tool calls, and an earlier turn may have come from
	// another model: a tool call without a Gemini thought signature must still be accepted.
	t.Run("history tool call from elsewhere", func(t *testing.T) {
		msgs := append(append([]Message{}, weatherQ...),
			Message{Role: "assistant", Content: TextContent(""), ToolCalls: []ToolCall{{ID: "call_hist1", Type: "function",
				Function: FunctionCall{Name: "get_weather", Arguments: `{"city":"Paris"}`}}}},
			Message{Role: "tool", ToolCallID: "call_hist1", Content: weatherResult},
			msg("assistant", "It's sunny and 21C in Paris."),
			msg("user", "And in Rome?"))
		r, err := s.Chat(ctx, ChatRequest{Messages: msgs, Tools: tools, MaxTokens: ptr(256)})
		if err != nil {
			busy.Fatalf(t, "%v", err)
		}
		wantToolCall(t, r.ToolCalls)
	})

	t.Run("stream tool call", func(t *testing.T) {
		ch, err := s.Stream(ctx, ChatRequest{Messages: weatherQ, Tools: tools, MaxTokens: ptr(256)})
		if err != nil {
			busy.Fatalf(t, "%v", err)
		}
		var last Frame
		for f := range ch {
			last = f
		}
		if !last.Done || last.Err != "" || last.FinishReason != "tool_calls" || len(last.ToolCalls) == 0 {
			busy.Fatalf(t, "final frame %+v", last)
		}
		tc := last.ToolCalls[0]
		if tc.ID == nil || *tc.ID == "" || tc.Name == nil || *tc.Name != "get_weather" || !json.Valid([]byte(tc.Arguments)) {
			busy.Fatalf(t, "tool call %+v", tc)
		}
		t.Logf("streamed tool call: %s %s", *tc.Name, tc.Arguments)
	})

	t.Run("stream text", func(t *testing.T) {
		ch, err := s.Stream(ctx, ChatRequest{Messages: []Message{msg("user", "Count from one to five in words.")}, MaxTokens: ptr(128)})
		if err != nil {
			busy.Fatalf(t, "%v", err)
		}
		text := ""
		var last Frame
		for f := range ch {
			text += f.Delta
			last = f
		}
		if !last.Done || last.Err != "" || text == "" {
			busy.Fatalf(t, "streamed %q, final frame %+v", text, last)
		}
	})
}
