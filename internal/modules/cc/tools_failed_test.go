package cc

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

const typeTextTool = `[{"type":"function","function":{"name":"desktop_type_text","description":"Type text on the user's computer.",` +
	`"parameters":{"type":"object","properties":{"text":{"type":"string","description":"The words"},` +
	`"app":{"type":"string","description":"The app"}},"required":["text"]}},"keywords":["type"]}]`

// omeriteFailure is what the desktop agent returns when no window is called "Omerite"
// (internal/agent/dictate TestMisheardAppWithNothingNearFailsLoudly).
var omeriteFailure = map[string]any{"success": false, "error_code": "window_not_found", "open_apps": []any{"Alacritty", "Firefox"},
	"error": `Nothing was typed: no open window matches "Omerite". Open apps: Alacritty, Firefox. ` +
		"Tell the user nothing was typed and ask which app they meant."}

func TestAllToolsFailed(t *testing.T) {
	obj := func(s string) any { v, _ := pyjson.Loads(s); return v }
	for _, c := range []struct {
		outs []any
		want bool
	}{
		{nil, false},
		{[]any{obj(`{"success": false, "error": "x"}`)}, true},
		{[]any{`{"success": false}`, obj(`{"success": false}`)}, true}, // a JSON string counts
		{[]any{obj(`{"success": false}`), obj(`{"success": true}`)}, false},
		{[]any{obj(`{"answer": "yes"}`)}, false}, // a validation answer has no success
		{[]any{"plain text"}, false},
		{[]any{obj(`{"success": "false"}`)}, false},
	} {
		var rs []toolResult
		for _, o := range c.outs {
			rs = append(rs, toolResult{Output: o})
		}
		if got := allToolsFailed(rs); got != c.want {
			t.Errorf("%v: %v, want %v", c.outs, got, c.want)
		}
	}
}

// The real failure: the model was told nothing was typed and said "Done" anyway. The result
// reaches the model as the node sent it, a note follows when every call failed, and the trace
// records the outcome (never the typed words).
func TestContinueWhenEveryToolFailed(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_5_9B)
	ve.start("f1", typeTextTool)
	ve.eng.push(engineReply{toolCalls: []map[string]any{{"id": "call_d1", "type": "function",
		"function": map[string]any{"name": "desktop_type_text", "arguments": `{"app": "Omerite", "text": "I need to take the dogs for a walk"}`}}},
		finish: "tool_calls"})
	r := ve.turn("/api/v0/voice/command", "f1", "Type into Omerite that I need to take the dogs for a walk.", nil).want(200).json()
	if r["stop_reason"] != "tool_calls" {
		t.Fatalf("turn %v", r)
	}
	ve.eng.say("Nothing was typed: I couldn't find Omerite. Which app did you mean?")
	ve.do("POST", "/api/v0/voice/command/continue", map[string]any{"conversation_id": "f1",
		"tool_results": []any{map[string]any{"tool_call_id": "call_d1", "output": omeriteFailure}}}, ve.node.h()).want(200)

	msgs := messagesOf(ve.eng.last())
	tool := -1
	for i, m := range msgs {
		if m["role"] == "tool" {
			tool = i
		}
	}
	if tool < 0 {
		t.Fatal("no tool message")
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(msgs[tool]["content"].(string)), &got); err != nil ||
		got["error"] != omeriteFailure["error"] || got["success"] != false || got["error_code"] != "window_not_found" {
		t.Fatalf("tool message %q (%v)", msgs[tool]["content"], err)
	}
	if tool != len(msgs)-2 || !strings.Contains(msgs[len(msgs)-1]["content"].(string), toolsFailedNote) {
		t.Fatalf("no failure note after the result: %v", msgs[tool:])
	}

	names, by := ve.traceSpans(ve.traceIDs("voice_command_continue", 1)[0])
	meta, _ := by["continue_conversation"]["metadata"].(map[string]any)
	b, _ := json.Marshal(meta)
	if string(b) != `{"tool_results":[{"error_code":"window_not_found","success":false,"tool":"desktop_type_text"}]}` {
		t.Fatalf("continue span %s (spans %v)", b, names)
	}

	// The note is per round: the next turn doesn't carry it.
	ve.eng.say("Okay.")
	ve.turn("/api/v0/voice/command", "f1", "never mind", nil).want(200)
	for _, m := range messagesOf(ve.eng.last()) {
		if c, _ := m["content"].(string); strings.Contains(c, "[TOOLS_FAILED]") {
			t.Fatal("the failure note outlived its round")
		}
	}
}

func TestContinueWithASuccessHasNoNote(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_5_9B)
	ve.start("f2", typeTextTool)
	ve.eng.push(engineReply{toolCalls: []map[string]any{
		{"id": "call_a", "type": "function", "function": map[string]any{"name": "desktop_type_text", "arguments": `{"text": "hi"}`}},
		{"id": "call_b", "type": "function", "function": map[string]any{"name": "desktop_type_text", "arguments": `{"app": "x", "text": "hi"}`}},
	}, finish: "tool_calls"})
	ve.turn("/api/v0/voice/command", "f2", "type hi twice", nil).want(200)
	ve.eng.say("Typed it once; the second app wasn't found.")
	ve.do("POST", "/api/v0/voice/command/continue", map[string]any{"conversation_id": "f2", "tool_results": []any{
		map[string]any{"tool_call_id": "call_a", "output": map[string]any{"success": true, "typed_chars": 2}},
		map[string]any{"tool_call_id": "call_b", "output": omeriteFailure},
	}}, ve.node.h()).want(200)
	for _, m := range messagesOf(ve.eng.last()) {
		if c, _ := m["content"].(string); strings.Contains(c, "[TOOLS_FAILED]") {
			t.Fatal("note with a success in the round")
		}
	}
	_, by := ve.traceSpans(ve.traceIDs("voice_command_continue", 1)[0])
	b, _ := json.Marshal(by["continue_conversation"]["metadata"])
	if string(b) != `{"tool_results":[{"success":true,"tool":"desktop_type_text"},{"error_code":"window_not_found","success":false,"tool":"desktop_type_text"}]}` {
		t.Fatalf("continue span %s", b)
	}
}

func TestContinueStreamTextPathNote(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_8B)
	ve.start("f3", typeTextTool)
	ve.eng.say(`<tool_call>{"name": "desktop_type_text", "arguments": {"app": "Omerite", "text": "walk the dogs"}}</tool_call>`)
	id := ve.turn("/api/v0/voice/command/stream", "f3", "type into omerite walk the dogs", nil).want(202).json()["tool_calls"].([]any)[0].(map[string]any)["id"]
	ve.eng.say("Nothing was typed. Which app did you mean?")
	ve.do("POST", "/api/v0/voice/command/continue/stream", map[string]any{"conversation_id": "f3",
		"tool_results": []any{map[string]any{"tool_call_id": id, "output": omeriteFailure}}}, ve.node.h()).want(200)
	msgs := messagesOf(ve.eng.last())
	if last := msgs[len(msgs)-1]; last["role"] != "system" || last["content"] != toolsFailedNote {
		t.Fatalf("stream text path: last %v", last)
	}
	if !strings.Contains(msgs[len(msgs)-3]["content"].(string), `Tell the user nothing was typed and ask which app they meant.", "error_code": "window_not_found"`) {
		t.Fatalf("results message %q", msgs[len(msgs)-3]["content"])
	}
	_, by := ve.traceSpans(ve.traceIDs("voice_command_continue", 1)[0])
	b, _ := json.Marshal(by["continue_stream_dispatch"]["metadata"])
	if !strings.Contains(string(b), `"error_code":"window_not_found"`) || strings.Contains(string(b), "dogs") {
		t.Fatalf("stream span %s", b)
	}
}

// A chat turn that runs the tool on a node over MQTT: the node hop's span says how the call
// went (never what was typed), and the model gets the failure note.
func TestChatNodeToolFailureTraced(t *testing.T) {
	ce := newChatEnv(t, prompts.Qwen3_5_9B)
	out, _ := json.Marshal(map[string]any{"output": omeriteFailure})
	ce.pub.toolReply = string(out)
	cid := ce.warm("tok-7")
	ce.eng.push(engineReply{toolCalls: []map[string]any{{"id": "call_c1", "type": "function",
		"function": map[string]any{"name": "get_weather", "arguments": `{"city": "the dogs walk"}`}}}})
	ce.eng.say("Nothing was typed. Which app did you mean?")
	ce.chat("tok-7", map[string]any{"message": "Type into Omerite that I need to take the dogs for a walk.", "conversation_id": cid})
	msgs := messagesOf(ce.eng.last())
	if c, _ := msgs[len(msgs)-1]["content"].(string); !strings.Contains(c, toolsFailedNote) {
		t.Fatalf("no failure note: %v", msgs[len(msgs)-1])
	}
	_, by := ce.traceSpans(ce.traceIDs("mobile_chat", 1)[0])
	b, _ := json.Marshal(by["mqtt_tool_get_weather"]["metadata"])
	if string(b) != `{"command":"get_weather","error_code":"window_not_found","success":false}` {
		t.Fatalf("node span %s", b)
	}
	b, _ = json.Marshal(by["continue_conversation"]["metadata"])
	if string(b) != `{"tool_results":[{"error_code":"window_not_found","success":false,"tool":"get_weather"}]}` {
		t.Fatalf("continue span %s", b)
	}
}
