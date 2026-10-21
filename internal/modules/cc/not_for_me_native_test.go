package cc

import (
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
)

// With native tool calling, not_for_me is a tool the model calls (decision
// 2026-10-20-prompt-diet), offered on every turn and taking the sentinel's silent path.
func TestNativeNotForMeToolOffered(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_5_9B)
	ve.start("nfm0", searchTools)
	ve.eng.say("Sure.")
	ve.turn("/api/v0/voice/command", "nfm0", "look up x", nil).want(200)
	reqs := ve.eng.requests()
	if len(reqs) == 0 {
		t.Fatal("no request")
	}
	tools, _ := reqs[len(reqs)-1]["tools"].([]any)
	f := toolByName(tools, notForMeTool)
	if f == nil {
		t.Fatalf("not_for_me not offered: %s", jsonOf(tools))
	}
	if req := jsonOf(f["parameters"].(map[string]any)["required"]); req != "null" && req != "[]" {
		t.Fatalf("not_for_me requires %s", req)
	}
	// The warmup sends the same tools, so the prefix cache holds.
	if jsonOf(reqs[0]["tools"]) != jsonOf(reqs[len(reqs)-1]["tools"]) {
		t.Fatal("warmup and turn tools differ")
	}
}

// A turn whose only call is not_for_me ends silently, nothing reaches the node, and the history
// keeps the plain sentinel (no tool call left without a result).
func TestNativeNotForMeCallStopsSilently(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_5_9B)
	ve.start("nfm1", searchTools)
	ve.eng.push(nativeCall("call_n1", notForMeTool, `{}`))
	b := ve.turn("/api/v0/voice/command", "nfm1", "mom can you grab the milk", nil).want(200).json()
	if b["stop_reason"] != "not_for_me" {
		t.Fatalf("stop %v: %v", b["stop_reason"], b)
	}
	if calls, _ := b["tool_calls"].([]any); len(calls) != 0 {
		t.Fatalf("calls reached the node: %v", calls)
	}
	if msg, _ := b["assistant_message"].(string); strings.TrimSpace(msg) != "" {
		t.Fatalf("spoke %q", msg)
	}

	ve.eng.say("Done.")
	ve.turn("/api/v0/voice/command", "nfm1", "look up x", nil).want(200)
	reqs := ve.eng.requests()
	for _, m := range reqs[len(reqs)-1]["messages"].([]any) {
		mm := m.(map[string]any)
		if strings.Contains(jsonOf(mm["tool_calls"]), notForMeTool) {
			t.Fatalf("history kept the not_for_me call: %s", jsonOf(mm))
		}
	}
}

// not_for_me next to a real call is dropped; the real call runs.
func TestNativeNotForMeWithAnotherCallIsDropped(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_5_9B)
	ve.start("nfm2", searchTools)
	ve.eng.push(engineReply{toolCalls: []map[string]any{
		{"id": "call_a", "type": "function", "function": map[string]any{"name": notForMeTool, "arguments": "{}"}},
		{"id": "call_b", "type": "function", "function": map[string]any{"name": "web_lookup", "arguments": `{"query":"x"}`}},
	}})
	b := ve.turn("/api/v0/voice/command", "nfm2", "look up x", nil).want(200).json()
	calls, _ := b["tool_calls"].([]any)
	if b["stop_reason"] != "tool_calls" || len(calls) != 1 {
		t.Fatalf("%v", b)
	}
	if name := calls[0].(map[string]any)["function"].(map[string]any)["name"]; name != "web_lookup" {
		t.Fatalf("ran %v", name)
	}
}

// The old sentinel still works (text providers, and a model that writes it anyway).
func TestNativeNotForMeSentinelStillWorks(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_5_9B)
	ve.start("nfm3", searchTools)
	ve.eng.say(parse.NotForMeLiteral)
	b := ve.turn("/api/v0/voice/command", "nfm3", "and then she said", nil).want(200).json()
	if b["stop_reason"] != "not_for_me" {
		t.Fatalf("%v", b)
	}
}
