package cc

import (
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// assertStrictOK checks a wire request against the two rules the Qwen 3.5 template raises on
// (ID12, A10 F10): a system message anywhere but first, and no user message.
func assertStrictOK(t *testing.T, req map[string]any) []map[string]any {
	t.Helper()
	msgs := messagesOf(req)
	user := false
	for i, m := range msgs {
		if m["role"] == "system" && i > 0 {
			t.Fatalf("system message at %d: %v", i, msgs)
		}
		user = user || m["role"] == "user"
	}
	if !user {
		t.Fatalf("no user message: %v", msgs)
	}
	return msgs
}

// On a strict-template endpoint the warmup, a turn (speaker block etc.) and a native tool
// round trip all reach the engine with one leading system message and a user message; the
// conversation's own history is unchanged. (Nag placement is covered by the llm fold tests.)
func TestStrictTemplateFold(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_5_9B)
	ve.eng.strict.Store(true)

	ve.start("s1", weatherTool)
	conv := ve.m.convs.get("s1")
	warm := assertStrictOK(t, ve.eng.requests()[0])
	if len(warm) != 2 || warm[0]["content"] != conv.messages[0].Content || warm[1]["content"] != llm.PrimeUserMessage {
		t.Fatalf("warmup %v", warm)
	}

	ve.eng.say("It's noon.")
	ve.turn("/api/v0/voice/command", "s1", "what time is it", nil).want(200)
	msgs := assertStrictOK(t, ve.eng.requests()[1])
	last := msgs[len(msgs)-1]["content"].(string)
	if !strings.HasPrefix(last, "<system>\n") || !strings.Contains(last, "\n</system>\n\nwhat time is it") {
		t.Fatalf("turn user message %q", last)
	}
	if msgs[0]["content"] != conv.messages[0].Content {
		t.Fatal("system prompt changed")
	}

	// Native tool call → continue with the result: tool messages pass through.
	ve.eng.push(engineReply{toolCalls: []map[string]any{{"id": "call_1", "type": "function",
		"function": map[string]any{"name": "get_weather", "arguments": "{}"}}}, finish: "tool_calls"})
	id := ve.turn("/api/v0/voice/command", "s1", "weather", nil).want(200).json()["tool_calls"].([]any)[0].(map[string]any)["id"]
	ve.eng.say("Rainy.")
	ve.do("POST", "/api/v0/voice/command/continue", map[string]any{"conversation_id": "s1",
		"tool_results": []any{map[string]any{"tool_call_id": id, "output": map[string]any{"message": "Rainy."}}}}, ve.node.h()).want(200)
	reqs := ve.eng.requests()
	msgs = assertStrictOK(t, reqs[len(reqs)-1])
	if msgs[len(msgs)-1]["role"] != "tool" {
		t.Fatalf("continue: last message %v", msgs[len(msgs)-1])
	}

	// The cached history is untouched: no folded text, the per-turn blocks still stripped.
	for _, m := range ve.m.convs.get("s1").messages {
		if strings.Contains(m.Content, "<system>") {
			t.Fatalf("history holds folded text: %q", m.Content)
		}
	}

	// Without the flag nothing changes: the warmup is the system prompt alone.
	ve.eng.strict.Store(false)
	n := len(ve.eng.requests())
	ve.start("s2", weatherTool)
	if w := messagesOf(ve.eng.requests()[n]); len(w) != 1 || w[0]["role"] != "system" {
		t.Fatalf("unflagged warmup %v", w)
	}
}

// G1 holds on a strict endpoint: the warmup's system prompt is the fixture's, byte for byte.
func TestG1ThroughStrictWarmup(t *testing.T) {
	inputs := goldenPrompts(t, "_inputs.json")
	nodeDev := og2(og2(inputs, "tool_sets").(*pyjson.Object), "node_dev").([]any)
	fx := goldenPrompts(t, prompts.Qwen3_5_9B+"__real_node.json")
	var keys []string
	for _, k := range og2(og2(og2(fx, "inputs").(*pyjson.Object), "node_context").(*pyjson.Object), "date_keys").([]any) {
		keys = append(keys, k.(string))
	}
	ve := newVoiceEnv(t, prompts.Qwen3_5_9B, func(m *Module) { m.tools = servertools.NewRegistry(); m.dateKeys = keys })
	ve.eng.strict.Store(true)
	ve.do("POST", "/api/v0/conversation/start", realNodeRequest(t, fx, nodeDev), ve.node.h()).want(200)
	assertWarmPrompt(t, ve, "g1", og2(fx, "system_prompt").(string))
	assertStrictOK(t, ve.eng.requests()[0])
}
