package cc

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/livetest"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// liveResolver serves one remote endpoint for every label, as a remote live label does.
type liveResolver struct{ ep llm.Endpoint }

func (r liveResolver) Resolve(context.Context, string) (llm.Endpoint, error) { return r.ep, nil }

// TestCloudLive runs real voice turns through the command center against each cloud
// provider with a key (livetest; CI: the cloud-live workflow): the Cloud prompt, the dev
// node's whole tool set plus the server tools, a client tool round trip on the blocking
// routes, then a second one on the streaming routes with the first still in the history.
// It fails where a user would: a tool schema the provider refuses, a malformed tool call, a
// history it won't take back (Gemini's thought signatures) or a stream that parses empty.
func TestCloudLive(t *testing.T) {
	ps := livetest.Providers()
	if len(ps) == 0 {
		t.Skip("set JARVIS_TEST_{ANTHROPIC,OPENAI,GEMINI}_KEY to run against the cloud APIs")
	}
	inputs := goldenPrompts(t, "_inputs.json")
	nodeDev := og2(og2(inputs, "tool_sets").(*pyjson.Object), "node_dev").([]any)
	var weather []any
	if err := json.Unmarshal([]byte(weatherTool), &weather); err != nil {
		t.Fatal(err)
	}
	clientTools := pyjson.Compact(append(append([]any{}, nodeDev...), weather...))

	for _, p := range ps {
		t.Run(p.Name, func(t *testing.T) {
			busy := &livetest.Busy{}
			res := liveResolver{llm.Endpoint{BaseURL: p.BaseURL, APIKey: p.Key, Model: p.Model, Remote: true, API: p.API}}
			ve := newVoiceEnv(t, prompts.Cloud, func(m *Module) {
				m.LLM, m.Endpoints = llm.NewService(llm.ServiceConfig{Resolver: res, HTTPClient: busy.Client()}), res.Resolve
			})
			ve.start("live", clientTools)

			// Blocking: a client tool call, then the formatting call after its result.
			b := ve.turn("/api/v0/voice/command", "live", "add milk to my todo list", nil).want(200).json()
			call := liveToolCall(t, busy, b, "todo_list")
			b = ve.do("POST", "/api/v0/voice/command/continue", map[string]any{"conversation_id": "live",
				"tool_results": []any{map[string]any{"tool_call_id": call["id"], "output": map[string]any{"success": true, "items": []string{"milk"}}}}},
				ve.node.h()).want(200).json()
			if msg, _ := b["assistant_message"].(string); msg == "" {
				busy.Fatalf(t, "no answer after the todo result: %v", b)
			}
			t.Logf("todo: %v", b["assistant_message"])

			// Streaming, with the todo round in the history. Any tool will do: a model may look the
			// weather up with quick_search instead (gpt-5-nano does); the call must be well formed.
			s := ve.turn("/api/v0/voice/command/stream", "live", "what's the weather in Paris today?", nil).want(202).json()
			call = liveToolCall(t, busy, s, "")
			a := ve.do("POST", "/api/v0/voice/command/continue/stream", map[string]any{"conversation_id": "live",
				"tool_results": []any{map[string]any{"tool_call_id": call["id"], "output": map[string]any{"conditions": "sunny", "temp_c": 21}}}},
				ve.node.h()).want(200)
			if len(a.body) == 0 {
				busy.Fatalf(t, "the streamed answer after the weather result was empty")
			}
			t.Logf("weather: %s", a.body)
		})
	}
}

// liveToolCall is the response's call to name (any call for ""), with JSON-object arguments,
// or a fatal error.
func liveToolCall(t *testing.T, busy *livetest.Busy, r map[string]any, name string) map[string]any {
	t.Helper()
	calls, _ := r["tool_calls"].([]any)
	for _, c := range calls {
		call, _ := c.(map[string]any)
		fn, _ := call["function"].(map[string]any)
		if name != "" && fn["name"] != name {
			continue
		}
		var args map[string]any
		if id, _ := call["id"].(string); id == "" || json.Unmarshal([]byte(fn["arguments"].(string)), &args) != nil {
			busy.Fatalf(t, "malformed %s call: %v", fn["name"], call)
		}
		return call
	}
	busy.Fatalf(t, "no %s call (stop %v): %v", name, r["stop_reason"], r)
	return nil
}
