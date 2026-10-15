package cc

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
)

// A desktop agent's tools, as it sends them: each carries keywords (internal/agent/node Spec).
const desktopTools = `[
 {"type": "function", "function": {"name": "desktop_open", "description": "Open an app.",
  "parameters": {"type": "object", "properties": {"target": {"type": "string"}}, "required": ["target"]}},
  "keywords": ["open", "launch", "start", "app", "computer", "desktop"]},
 {"type": "function", "function": {"name": "desktop_type_text", "description": "Type text.",
  "parameters": {"type": "object", "properties": {"text": {"type": "string"}, "app": {"type": "string"}}, "required": ["text"]}},
  "keywords": ["type", "dictate", "write", "keyboard"]}
]`

// lastIsNag reports whether the newest model request ends with a system nudge tagged tag.
func lastIsNag(ve *voiceEnv, tag string) bool {
	msgs := messagesOf(ve.eng.last())
	last := msgs[len(msgs)-1]
	s, _ := last["content"].(string)
	return last["role"] == "system" && strings.HasPrefix(s, tag)
}

func nativeCall(id, name, args string) engineReply {
	return engineReply{toolCalls: []map[string]any{{"id": id, "type": "function",
		"function": map[string]any{"name": name, "arguments": args}}}}
}

// "Remember that my parking spot is B12." on a desktop agent got "…B12 noted." and no call: the
// agent's tool keywords made the keyword half strict, and no keyword said "remember". The
// memory tools' own keywords now count.
func TestFollowThroughRememberWithKeywordedTools(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_5_9B)
	ve.stt.recognition = true // memory tools offered
	ve.start("m1", desktopTools)
	ve.eng.say("Got it, Jordan. Parking spot B12 noted.")
	ve.eng.push(nativeCall("call_m1", "remember", `{"content": "parking spot is B12"}`))
	ve.eng.say("I couldn't save that, because I'm not sure who's speaking.")
	ve.turn("/api/v0/voice/command", "m1", "Remember that my parking spot is B12.", nil).want(200)
	reqs := ve.eng.requests()
	msgs := messagesOf(reqs[len(reqs)-2])
	if last := msgs[len(msgs)-1]; last["role"] != "system" || !strings.HasPrefix(last["content"].(string), nagMustCall) {
		t.Fatalf("no must-call nudge before the retry: %v", last)
	}
}

// The must-call retry is spent and the reply still says "noted": one more nudge, to call
// remember.
func TestFollowThroughUnsavedAfterMustCall(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_5_9B)
	ve.stt.recognition = true
	ve.start("m5", desktopTools)
	ve.eng.say("Got it, Jordan. I've noted that your parking spot is B12.")
	ve.eng.say("Got it, Jordan — I'll remember that your parking spot is B12.")
	ve.eng.push(nativeCall("call_m5", "remember", `{"content": "parking spot is B12"}`))
	ve.eng.say("I couldn't save that, because I'm not sure who's speaking.")
	ve.turn("/api/v0/voice/command", "m5", "Remember that my parking spot is B12.", nil).want(200)
	reqs := ve.eng.requests()
	msgs := messagesOf(reqs[len(reqs)-2]) // the call that made the remember call
	if last := msgs[len(msgs)-1]; last["role"] != "system" || !strings.HasPrefix(last["content"].(string), nagUnsaved) {
		t.Fatalf("remember not called after the not-saved nudge: %v", last)
	}
	for _, r := range reqs {
		for _, m := range messagesOf(r) {
			if s, _ := m["content"].(string); m["role"] == "system" && strings.HasPrefix(s, nagNoMemoryTool) {
				t.Fatal("remember is offered: no no-memory-tool nudge")
			}
		}
	}
}

// With no tool that stores it, a memory request must not be answered "noted".
func TestFollowThroughNoMemoryTool(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_5_9B)
	ve.stt.recognition = false // no voice ID: no memory tools
	ve.start("m2", desktopTools)
	ve.eng.say("Got it, Jordan. Parking spot B12 noted.")
	ve.eng.say("I can't remember that for you here.")
	b := ve.turn("/api/v0/voice/command", "m2", "Remember that my parking spot is B12.", nil).want(200).json()
	if b["assistant_message"] != "I can't remember that for you here." || !lastIsNag(ve, nagNoMemoryTool) {
		t.Fatalf("reply %v", b["assistant_message"])
	}
}

// "Not in my memory" without a search: one nudge to call recall.
func TestFollowThroughRecallFirst(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_5_9B)
	ve.stt.recognition = true
	ve.start("m3", desktopTools)
	ve.eng.say("Not in my memory, Jordan. Want me to jot that down?")
	ve.eng.push(nativeCall("call_m3", "recall", `{"query": "parking spot"}`))
	ve.eng.say("I don't have your parking spot saved.")
	b := ve.turn("/api/v0/voice/command", "m3", "Do you remember where my parking spot is?", nil).want(200).json()
	if b["assistant_message"] != "I don't have your parking spot saved." {
		t.Fatalf("reply %v", b["assistant_message"])
	}
	reqs := ve.eng.requests()
	msgs := messagesOf(reqs[len(reqs)-2])
	if last := msgs[len(msgs)-1]; last["role"] != "system" || !strings.HasPrefix(last["content"].(string), nagRecallFirst) {
		t.Fatalf("no recall nudge: %v", last)
	}
	// After the search, the same denial is the answer: no second nudge (3 model calls in all).
	if n := len(reqs); n < 3 {
		t.Fatalf("%d model calls", n)
	}
}

// "Open Omerite and type hello there." opened the app, then told the user to type: the
// continue gets one nudge and calls desktop_type_text.
func TestFollowThroughTypeAfterOpen(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_5_9B)
	ve.start("m4", desktopTools)
	ve.eng.push(nativeCall("call_o1", "desktop_open", `{"target": "Omerite"}`))
	r := ve.turn("/api/v0/voice/command/stream", "m4", "Open Omerite and type hello there.", nil).want(202).json()
	if r["stop_reason"] != "tool_calls" {
		t.Fatalf("%v", r)
	}
	ve.eng.say(`Got it, Jordan. I've opened Omawrite for you. Now, type "hello there" to get started.`)
	ve.eng.push(nativeCall("call_t1", "desktop_type_text", `{"text": "hello there", "app": "Omerite"}`))
	b := ve.do("POST", "/api/v0/voice/command/continue", map[string]any{"conversation_id": "m4",
		"tool_results": []any{map[string]any{"tool_call_id": "call_o1", "output": map[string]any{"success": true, "matched_app": "Omawrite"}}}},
		ve.node.h()).want(200).json()
	calls, _ := b["tool_calls"].([]any)
	if b["stop_reason"] != "tool_calls" || len(calls) != 1 || !strings.Contains(jsonOf(calls[0]), "desktop_type_text") {
		t.Fatalf("continue %v", b)
	}
	if !lastIsNag(ve, nagUnfinished) {
		t.Fatal("no unfinished-request nudge before the call")
	}

	// Once the typing ran, the reply is spoken as is.
	before := len(ve.eng.requests())
	ve.eng.say("Done, hello there is typed in Omawrite.")
	b = ve.do("POST", "/api/v0/voice/command/continue", map[string]any{"conversation_id": "m4",
		"tool_results": []any{map[string]any{"tool_call_id": "call_t1", "output": map[string]any{"success": true}}}},
		ve.node.h()).want(200).json()
	if b["assistant_message"] != "Done, hello there is typed in Omawrite." || len(ve.eng.requests())-before != 1 {
		t.Fatalf("after typing %v (%d model calls)", b, len(ve.eng.requests())-before)
	}
}

func TestFollowThroughNagQuiet(t *testing.T) {
	conv := &conversation{serverNames: map[string]bool{"remember": true, "recall": true}}
	user := chatMsg{Role: "user", Content: "x"}
	for _, c := range []struct{ utt, reply string }{
		{"Remember that my parking spot is B12.", "Do you mean the garage spot?"},  // a question, no claim
		{"Do you remember where my parking spot is?", "Your parking spot is B12."}, // answered
		{"What's the weather like?", "I don't have access to the weather."},        // not about the user
		{"Open Omerite and type hello there.", "Sure."},                            // no typing tool offered
		{"", "Not in my memory."}, // no utterance
	} {
		if nag := followThroughNag(conv, c.utt, c.reply, []chatMsg{user}); nag != "" {
			t.Errorf("%q / %q: %q", c.utt, c.reply, nag)
		}
	}
	// A search already ran this turn: its answer stands.
	ran := []chatMsg{user, {Role: "assistant", ToolCalls: nil}, {Role: "tool", Content: `{"results": []}`}}
	if nag := followThroughNag(conv, "Do you remember where my parking spot is?", "Not in my memory.", ran); nag != "" {
		t.Errorf("after recall: %q", nag)
	}
}

func jsonOf(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// The must-call retry got recall instead of remember, then "Got it": remember still hasn't run.
func TestFollowThroughUnsavedAfterOtherTool(t *testing.T) {
	conv := &conversation{serverNames: map[string]bool{"remember": true, "recall": true}}
	msgs := []chatMsg{{Role: "user", Content: "Remember that my locker code is 4471."},
		{Role: "assistant", ToolCalls: []parse.ToolCall{{Function: parse.FunctionCall{Name: "recall"}}}},
		{Role: "tool", Content: `{"status": "no_results"}`}}
	if nag := followThroughNag(conv, "Remember that my locker code is 4471.", "Got it, Jordan — your locker code is 4471.", msgs); nag != unsavedRetry {
		t.Fatalf("nag %q", nag)
	}
	msgs[1].ToolCalls[0].Function.Name = "remember"
	if nag := followThroughNag(conv, "Remember that my locker code is 4471.", "Got it, Jordan.", msgs); nag != "" {
		t.Fatalf("after remember: %q", nag)
	}
}
