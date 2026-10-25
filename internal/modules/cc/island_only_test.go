package cc

import (
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
)

// A desktop command whose result the node's island showed (island_only): the continue makes no
// LLM call, a voice node gets nothing to speak but the exchange stays open (its follow-up
// window: "…restore it"), and the history keeps
// what was shown, so "it" in the next turn has something to refer to (DESKTOP-COMMANDS.md Q9).
func TestIslandOnlyResultsEndTheTurnSilently(t *testing.T) {
	for _, model := range []string{prompts.Qwen3_5_9B, prompts.Qwen3_8B} { // native, text
		ve := newVoiceEnv(t, model)
		ve.start("c-island", weatherTool)
		if model == prompts.Qwen3_5_9B {
			ve.eng.push(engineReply{toolCalls: []map[string]any{{"id": "call_w1", "type": "function",
				"function": map[string]any{"name": "get_weather", "arguments": `{}`}}}})
		} else {
			ve.eng.say(`<tool_call>{"name": "get_weather", "arguments": {}}</tool_call>`)
		}
		r := ve.turn("/api/v0/voice/command/stream", "c-island", "maximize firefox", nil).want(202).json()
		id := r["tool_calls"].([]any)[0].(map[string]any)["id"]
		before := len(ve.eng.requests())
		b := ve.do("POST", "/api/v0/voice/command/continue", map[string]any{"conversation_id": "c-island",
			"tool_results": []any{map[string]any{"tool_call_id": id, "output": map[string]any{
				"success": true, "action": "maximize", "island": "Maximized Firefox", "island_only": true}}}}, ve.node.h()).want(200).json()
		if b["assistant_message"] != "" || b["end_of_exchange"] == true || b["stop_reason"] != "complete" {
			t.Fatalf("%s: continue %v", model, b)
		}
		if n := len(ve.eng.requests()); n != before {
			t.Fatalf("%s: %d LLM calls for an island-only result", model, n-before)
		}
		conv := ve.m.convs.get("c-island")
		if last := conv.messages[len(conv.messages)-1]; last.Role != "assistant" || last.Content != "Maximized Firefox" {
			t.Fatalf("%s: history ends %+v", model, last)
		}
	}
}

// One result without island_only (a question, or another tool) means the round is answered as
// usual.
func TestMixedRoundIsAnsweredAsUsual(t *testing.T) {
	ok, _ := islandNotes([]toolResult{
		{Output: map[string]any{"island_only": true, "island": "Tab 3"}},
		{Output: map[string]any{"windows": []any{}}},
	})
	if ok != nil {
		t.Fatalf("notes %v", ok)
	}
	if _, all := islandNotes(nil); all {
		t.Fatal("no results counted as island-only")
	}
}

// A list on the island to pick from (choice): still no LLM call and nothing to speak, but the
// exchange stays open, so the node's choice window can send "the second one".
func TestIslandChoiceKeepsTheExchangeOpen(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_5_9B)
	ve.start("c-choice", weatherTool)
	ve.eng.push(engineReply{toolCalls: []map[string]any{{"id": "call_c1", "type": "function",
		"function": map[string]any{"name": "get_weather", "arguments": `{}`}}}})
	ve.turn("/api/v0/voice/command/stream", "c-choice", "switch to the terminal", nil).want(202)
	before := len(ve.eng.requests())
	b := ve.do("POST", "/api/v0/voice/command/continue", map[string]any{"conversation_id": "c-choice",
		"tool_results": []any{map[string]any{"tool_call_id": "call_c1", "output": map[string]any{
			"success": false, "error_code": "ambiguous_window", "island": "Which one?  1. Terminal — build  2. Terminal — logs",
			"island_only": true, "choice": true, "numbered": []any{"Terminal — build", "Terminal — logs"}}}}}, ve.node.h()).want(200).json()
	if b["assistant_message"] != "" || b["end_of_exchange"] == true || len(ve.eng.requests()) != before {
		t.Fatalf("choice continue %v (%d LLM calls)", b, len(ve.eng.requests())-before)
	}
}
