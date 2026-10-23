package cc

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/watches"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
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
	// #435: the 9B answered the not-saved nudge in prose again; the retry after it requires a
	// tool call. The first call and the must-call retry stay auto.
	for i, want := range map[int]string{len(reqs) - 4: "auto", len(reqs) - 3: "auto", len(reqs) - 2: "required"} {
		if got := reqs[i]["tool_choice"]; got != want {
			t.Errorf("request %d: tool_choice %v, want %s", i, got, want)
		}
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

// Prod's text path (Qwen3_14B, qwen3.8-27b): the continue after desktop_open re-enters the loop
// while the typing is pending, so the same nudge fires (#56; the text path's continue was one
// formatting call with no tools, which answered "Done.").
func TestFollowThroughTypeAfterOpenTextPath(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_14B)
	ve.start("t4", desktopTools)
	ve.eng.say(`<tool_call>{"name": "desktop_open", "arguments": {"target": "neovim"}}</tool_call>`)
	r := ve.turn("/api/v0/voice/command/stream", "t4", "open neovim and type hello world", nil).want(202).json()
	calls, _ := r["tool_calls"].([]any)
	if r["stop_reason"] != "tool_calls" || len(calls) != 1 {
		t.Fatalf("%v", r)
	}
	openID := calls[0].(map[string]any)["id"]

	// The stream continue hands this one to the blocking continue (the loop).
	ve.do("POST", "/api/v0/voice/command/continue/stream", map[string]any{"conversation_id": "t4",
		"tool_results": []any{map[string]any{"tool_call_id": openID, "output": map[string]any{"success": true}}}},
		ve.node.h()).want(202)

	ve.eng.say("Done.")
	ve.eng.say(`<tool_call>{"name": "desktop_type_text", "arguments": {"text": "hello world", "app": "neovim"}}</tool_call>`)
	b := ve.do("POST", "/api/v0/voice/command/continue", map[string]any{"conversation_id": "t4",
		"tool_results": []any{map[string]any{"tool_call_id": openID, "output": map[string]any{"success": true}}}},
		ve.node.h()).want(200).json()
	calls, _ = b["tool_calls"].([]any)
	if b["stop_reason"] != "tool_calls" || len(calls) != 1 || !strings.Contains(jsonOf(calls[0]), "desktop_type_text") {
		t.Fatalf("continue %v", b)
	}
	if !lastIsNag(ve, nagUnfinished) {
		t.Fatal("no unfinished-request nudge before the call")
	}
	typeID := calls[0].(map[string]any)["id"]

	// Once the typing ran, the continue is the text path's formatting call again.
	before := len(ve.eng.requests())
	ve.eng.say("Typed hello world in neovim.")
	b = ve.do("POST", "/api/v0/voice/command/continue", map[string]any{"conversation_id": "t4",
		"tool_results": []any{map[string]any{"tool_call_id": typeID, "output": map[string]any{"success": true}}}},
		ve.node.h()).want(200).json()
	if b["assistant_message"] != "Typed hello world in neovim." || len(ve.eng.requests())-before != 1 {
		t.Fatalf("after typing %v (%d model calls)", b, len(ve.eng.requests())-before)
	}
	if req := ve.eng.last(); req["tools"] != nil {
		t.Fatalf("formatting call offered tools: %v", req["tools"])
	}
}

// qwen3.8-27b once answered the nudge with a sentence and then the call as a bare object, no
// <tool_call> tags: on the text path's loop that is the call (an offered tool only).
func TestFollowThroughTypeAfterOpenBareCall(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_14B)
	ve.start("t5", desktopTools)
	ve.eng.say(`<tool_call>{"name": "desktop_open", "arguments": {"target": "neovim"}}</tool_call>`)
	r := ve.turn("/api/v0/voice/command/stream", "t5", "open neovim and type hello world", nil).want(202).json()
	openID := r["tool_calls"].([]any)[0].(map[string]any)["id"]
	ve.eng.say("Done.")
	ve.eng.say("I'll type that now.\n{\"name\": \"desktop_type_text\", \"arguments\": {\"text\": \"hello world\", \"app\": \"neovim\"}}")
	b := ve.do("POST", "/api/v0/voice/command/continue", map[string]any{"conversation_id": "t5",
		"tool_results": []any{map[string]any{"tool_call_id": openID, "output": map[string]any{"success": true}}}},
		ve.node.h()).want(200).json()
	calls, _ := b["tool_calls"].([]any)
	if b["stop_reason"] != "tool_calls" || len(calls) != 1 || !strings.Contains(jsonOf(calls[0]), `hello world`) ||
		!strings.Contains(jsonOf(calls[0]), "desktop_type_text") {
		t.Fatalf("continue %v", b)
	}
}

func TestTrailingBareCall(t *testing.T) {
	offered := func(n string) bool { return n == "desktop_type_text" }
	for _, c := range []struct {
		in, name, prose string
	}{
		{"I'll type that now.\n{\"name\": \"desktop_type_text\", \"arguments\": {\"text\": \"hi\"}}", "desktop_type_text", "I'll type that now."},
		{"Sure: {\"name\": \"desktop_type_text\", \"arguments\": {}}  ", "desktop_type_text", "Sure:"},
		{"I'll run {\"name\": \"rm_rf\", \"arguments\": {}}", "", ""},                  // not offered
		{"{\"name\": \"desktop_type_text\", \"arguments\": {}} and then some", "", ""}, // not trailing
		{"The config is {\"name\": \"desktop_type_text\"}", "", ""},                    // no arguments
		{"Nothing to see here.", "", ""},
	} {
		call, prose, ok := trailingBareCall(c.in, offered)
		if ok != (c.name != "") || (ok && (call.Function.Name != c.name || prose != c.prose)) {
			t.Errorf("%q: %v %q %v", c.in, call, prose, ok)
		}
	}
}

// #221: "stop watching the example.com one" answered "I've stopped watching it" with no
// cancel_watch call this turn (list_watches ran the turn before) gets one nudge to call it.
func TestFollowThroughNotCancelled(t *testing.T) {
	conv := &conversation{serverNames: map[string]bool{"list_watches": true, "cancel_watch": true}}
	utt := "stop watching the example.com one"
	msgs := []chatMsg{{Role: "user", Content: "what are you watching?"},
		{Role: "assistant", ToolCalls: []parse.ToolCall{{Function: parse.FunctionCall{Name: "list_watches"}}}},
		{Role: "tool", Content: `{"watches": []}`}, {Role: "assistant", Content: "You're watching one thing."},
		{Role: "user", Content: utt}}
	if nag := followThroughNag(conv, utt, "Okay, I've stopped watching the example.com price.", msgs); nag != notCancelledRetry {
		t.Fatalf("nag %q", nag)
	}
	if nag := followThroughNag(conv, utt, "Which one do you mean?", msgs); nag != "" {
		t.Fatalf("question: %q", nag)
	}
	// Spent once per run.
	nagged := append(slices.Clone(msgs), sysMsg(notCancelledRetry))
	if nag := followThroughNag(conv, utt, "Stopped it.", nagged); nag != "" {
		t.Fatalf("second nag %q", nag)
	}
	ran := append(slices.Clone(msgs), chatMsg{Role: "assistant", ToolCalls: []parse.ToolCall{{Function: parse.FunctionCall{Name: "cancel_watch"}}}},
		chatMsg{Role: "tool", Content: `{"status": "cancelled"}`})
	if nag := followThroughNag(conv, utt, "Stopped watching price on example.com item.", ran); nag != "" {
		t.Fatalf("after cancel_watch: %q", nag)
	}
	// Without the watch tools offered, nothing to nudge toward.
	if nag := followThroughNag(&conversation{serverNames: map[string]bool{}}, utt, "I've stopped watching it.", msgs); nag != "" {
		t.Fatalf("no tool: %q", nag)
	}
}

// #221 through the engine: the reply claims the watch stopped without cancel_watch; the nudge
// asks for the call, which runs and cancels the watch, and the reply comes after its result.
func TestFollowThroughNotCancelledEngine(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_5_9B, func(m *Module) { m.DevSpeakerHints = true })
	ve.stt.recognition = true // the watch tools are offered to a speaker the household can recognise
	ve.set(settingWatchesEnabled, true, settings.Scope{HouseholdID: voiceHH})
	uid := int64(7)
	w, err := ve.m.watches.Create(context.Background(), watches.Watch{HouseholdID: voiceHH, UserID: &uid,
		Title: "price on example.com item", SourceKind: watches.SourceWeb,
		Source:    watches.Source{URL: "https://www.example.com/item", Extract: "the price"},
		Condition: priceBelow100, FirePolicy: watches.FireOnce, CadenceSeconds: 3600})
	if err != nil {
		t.Fatal(err)
	}
	ve.start("w1", weatherTool)
	ve.eng.say("Okay, I've stopped watching the example.com price.")
	ve.eng.push(nativeCall("call_w1", "cancel_watch", `{"watch": "the example.com one"}`))
	ve.eng.say("Stopped watching price on example.com item.")
	b := ve.turn("/api/v0/voice/command", "w1", "stop watching the example.com one",
		map[string]any{"speaker_user_id": 7}).want(200).json()
	nagged, result := false, ""
	for _, r := range ve.eng.requests() {
		for _, m := range messagesOf(r) {
			s, _ := m["content"].(string)
			nagged = nagged || m["role"] == "system" && strings.HasPrefix(s, nagNotCancelled)
			if m["role"] == "tool" {
				result = s
			}
		}
	}
	got, err := ve.m.watches.Get(context.Background(), voiceHH, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !nagged || !strings.Contains(result, `"cancelled"`) || got.State != watches.StateDone ||
		b["assistant_message"] != "Stopped watching price on example.com item." {
		t.Fatalf("nagged %v, result %s, state %s, reply %v", nagged, result, got.State, b["assistant_message"])
	}
}

// #435: "Got it, Jordan — your locker code is 4471. <exchange_complete/>" with no remember call
// skipped every follow-through nudge (the exchange-complete marker counted as terminal).
func TestFollowThroughUnsavedWithExchangeComplete(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_5_9B)
	ve.stt.recognition = true
	ve.start("m6", desktopTools)
	ve.eng.say("Got it, Jordan. Your locker code is 4471. <exchange_complete/>")
	ve.eng.push(nativeCall("call_m6", "remember", `{"content": "locker code is 4471"}`))
	ve.eng.say("I couldn't save that, because I'm not sure who's speaking.")
	ve.turn("/api/v0/voice/command", "m6", "Remember that my locker code is 4471.", nil).want(200)
	reqs := ve.eng.requests()
	msgs := messagesOf(reqs[len(reqs)-2])
	if last := msgs[len(msgs)-1]; last["role"] != "system" || !strings.HasPrefix(last["content"].(string), nagUnsaved) {
		t.Fatalf("no not-saved nudge: %v", last)
	}
}

// #512: "Turn off the office light bar" got control_node {mute} and "Got it, Jordan. The
// office light bars are now off." — a device-change claim with no successful control_device
// call this turn gets one nudge, and a failed result never passes as done.
func TestFollowThroughDeviceNotDone(t *testing.T) {
	def, err := pyjson.Loads(controlDeviceDefinition)
	if err != nil {
		t.Fatal(err)
	}
	conv := &conversation{tools: []prompts.Tool{def.(*pyjson.Object)}}
	utt := "Turn off the office light bar"
	msgs := []chatMsg{{Role: "user", Content: utt},
		{Role: "assistant", ToolCalls: []parse.ToolCall{{ID: "c1", Function: parse.FunctionCall{Name: "control_node", Arguments: `{"action": "mute"}`}}}},
		{Role: "tool", ToolCallID: "c1", Content: `{"success": true}`}}
	reply := "Got it, Jordan. The office light bars are now off."
	if nag := followThroughNag(conv, utt, reply, msgs); nag != deviceNotDoneRetry {
		t.Fatalf("nag %q", nag)
	}
	// A successful control_device call this turn makes the claim true: no nudge. The node's
	// result message carries no Name, so it is matched by tool_call_id.
	ran := append(slices.Clone(msgs),
		chatMsg{Role: "assistant", ToolCalls: []parse.ToolCall{{ID: "c2", Function: parse.FunctionCall{Name: "control_device", Arguments: `{"device_name": "office light bars", "action": "turn_off"}`}}}},
		chatMsg{Role: "tool", ToolCallID: "c2", Content: `{"success": true, "entity_id": "rgbic_tv_light_bars", "action": "turn_off", "error": null, "input_required": null}`})
	if nag := followThroughNag(conv, utt, reply, ran); nag != "" {
		t.Fatalf("after success: %q", nag)
	}
	// A failed control_device result is not done: the nudge still fires.
	failed := append(slices.Clone(msgs),
		chatMsg{Role: "assistant", ToolCalls: []parse.ToolCall{{ID: "c3", Function: parse.FunctionCall{Name: "control_device", Arguments: `{"device_name": "office light bars", "action": "turn_off"}`}}}},
		chatMsg{Role: "tool", ToolCallID: "c3", Content: `{"success": false, "entity_id": "rgbic_tv_light_bars", "action": "turn_off", "error": "Timed out waiting for node response", "input_required": null}`})
	if nag := followThroughNag(conv, utt, reply, failed); nag != deviceNotDoneRetry {
		t.Fatalf("after failure: %q", nag)
	}
	// An honest reply after the failure claims nothing.
	if nag := followThroughNag(conv, utt, "I couldn't reach the office light bars — nothing was turned off.", failed); nag != "" {
		t.Fatalf("honest failure reply: %q", nag)
	}
	// Spent once per run.
	nagged := append(slices.Clone(msgs), sysMsg(deviceNotDoneRetry))
	if nag := followThroughNag(conv, utt, reply, nagged); nag != "" {
		t.Fatalf("second nag %q", nag)
	}
	// A node's own control_device writes its own result: success is read leniently, as the
	// rest of the module reads node results (laxBool), so "true" is done.
	lax := append(slices.Clone(msgs),
		chatMsg{Role: "assistant", ToolCalls: []parse.ToolCall{{ID: "c4", Function: parse.FunctionCall{Name: "control_device", Arguments: `{"device_name": "office light bars", "action": "turn_off"}`}}}},
		chatMsg{Role: "tool", ToolCallID: "c4", Content: `{"success": "true", "entity_id": "rgbic_tv_light_bars"}`})
	if nag := followThroughNag(conv, utt, reply, lax); nag != "" {
		t.Fatalf("lax success: %q", nag)
	}
	// The node's own audio is control_node's job: no nudge.
	if nag := followThroughNag(conv, "turn down the volume", "Volume's down.", msgs); nag != "" {
		t.Fatalf("volume request: %q", nag)
	}
	// Nor are the node's own timers, alarms, reminders and playback devices: cancel_timer
	// answered the request and the reply is true, so the nudge must stay out of it —
	// control_device would answer "No device called 'timer' here" and the model would then
	// deny a change that did happen (review of #512).
	timer := []chatMsg{{Role: "user", Content: "turn off the timer"},
		{Role: "assistant", ToolCalls: []parse.ToolCall{{ID: "t1", Function: parse.FunctionCall{Name: "cancel_timer", Arguments: `{"label": "five minutes"}`}}}},
		{Role: "tool", ToolCallID: "t1", Content: `{"success": true}`}}
	for _, c := range []struct{ utt, reply string }{
		{"turn off the timer", "The timer is off."},
		{"turn off the alarm", "The alarm is off."},
		{"turn off the reminder", "That reminder is off."},
		{"turn off the radio", "The radio is off."},
	} {
		if nag := followThroughNag(conv, c.utt, c.reply, timer); nag != "" {
			t.Fatalf("%q -> %q: %q", c.utt, c.reply, nag)
		}
	}
	// Stacked articles: "all the lights" is the same #512 shape — control_node ran, the reply
	// says the lights are off, no control_device — so the nudge fires (review of #512).
	all := []chatMsg{{Role: "user", Content: "turn off all the lights"},
		{Role: "assistant", ToolCalls: []parse.ToolCall{{ID: "a1", Function: parse.FunctionCall{Name: "control_node", Arguments: `{"action": "mute"}`}}}},
		{Role: "tool", ToolCallID: "a1", Content: `{"success": true}`}}
	if nag := followThroughNag(conv, "turn off all the lights", "All the lights are off.", all); nag != deviceNotDoneRetry {
		t.Fatalf("all the lights: %q", nag)
	}
	// Without a device tool offered, nothing to nudge toward.
	if nag := followThroughNag(&conversation{}, utt, reply, msgs); nag != "" {
		t.Fatalf("no tool: %q", nag)
	}
}
