package cc

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/dictate"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
)

// confirmEnv is a desktop conversation whose browser_fill {submit: true} is parked as confirm-2.
func confirmEnv(t *testing.T) *voiceEnv {
	t.Helper()
	ve := newVoiceEnv(t, prompts.Qwen3_5_9B)
	ve.start("g1", confirmTools)
	ve.eng.push(nativeCall("call_f1", "browser_fill", `{"field": "Search", "text": "featured article", "submit": true}`))
	r := ve.turn("/api/v0/voice/command", "g1", "find today's featured article on wikipedia", nil).want(200).json()
	if r["stop_reason"] != "tool_calls" {
		t.Fatalf("fill: %v", r)
	}
	return ve
}

func (ve *voiceEnv) continueWith(id string, output map[string]any) map[string]any {
	ve.t.Helper()
	return ve.do("POST", "/api/v0/voice/command/continue", map[string]any{"conversation_id": "g1",
		"tool_results": []any{map[string]any{"tool_call_id": id, "output": output}}}, ve.node.h()).want(200).json()
}

func parkedOutput() map[string]any {
	return map[string]any{"success": false, "done": false, "status": "awaiting_confirmation", "confirmation_id": "confirm-2",
		"question": `Fill the Search field with "featured article" and submit?`, "expires_in_seconds": 60}
}

// confirmCallSent is the desktop_confirm call a response hands the node, or nil.
func confirmCallSent(t *testing.T, r map[string]any) map[string]any {
	t.Helper()
	calls, _ := r["tool_calls"].([]any)
	for _, c := range calls {
		cm := c.(map[string]any)
		fn, _ := cm["function"].(map[string]any)
		if fn["name"] == dictate.ToolConfirm {
			var args map[string]any
			if err := json.Unmarshal([]byte(fn["arguments"].(string)), &args); err != nil {
				t.Fatal(err)
			}
			return args
		}
	}
	return nil
}

func refusedInLastRequest(ve *voiceEnv) bool {
	msgs, _ := ve.eng.last()["messages"].([]any)
	for _, m := range msgs {
		mm := m.(map[string]any)
		if c, _ := mm["content"].(string); mm["role"] == "tool" && strings.Contains(c, "the user hasn't answered yet") {
			return true
		}
	}
	return false
}

// Devbox trace b1abfb62 (#574): browser_fill submit=true parked as confirm-2 and the 9B's very
// next call, in the same turn, was desktop_confirm {confirm-2, "Yes"}; the fill and submit ran
// with no question shown. cc refuses it without calling the node and the model is told to ask.
func TestSelfConfirmInTheSameTurnIsRefused(t *testing.T) {
	ve := confirmEnv(t)
	ve.eng.push(nativeCall("call_c1", dictate.ToolConfirm, `{"confirmation_id": "confirm-2", "answer": "Yes"}`))
	ve.eng.push(engineReply{content: `Should I fill the Search field with "featured article" and submit?`})
	r := ve.continueWith("call_f1", parkedOutput())
	if c := confirmCallSent(t, r); c != nil {
		t.Fatalf("self-confirmation sent to the node: %v", r)
	}
	if !refusedInLastRequest(ve) {
		t.Fatal("the model wasn't told the user hasn't answered")
	}
}

// The user's answer after the question was shown is what reaches the node, whatever the model
// wrote: a yes runs the action, a cancel doesn't (dictate.Confirmer classifies the words).
func TestConfirmCarriesTheUsersAnswer(t *testing.T) {
	asked := func(t *testing.T) *voiceEnv {
		ve := confirmEnv(t)
		ve.eng.push(nativeCall("call_v1", "request_validation",
			`{"question": "Fill the Search field and submit?", "parameter_name": "confirm", "options": ["Yes", "Cancel"]}`))
		if r := ve.continueWith("call_f1", parkedOutput()); r["stop_reason"] != "validation_required" {
			t.Fatalf("validation: %v", r)
		}
		return ve
	}
	for _, c := range []struct {
		name, words string
		answer      func(ve *voiceEnv) map[string]any
		want        dictate.Answer
	}{
		{name: "voice node yes", words: "yes go ahead", want: dictate.AnswerYes, answer: func(ve *voiceEnv) map[string]any {
			return ve.continueWith(validationResponseID, map[string]any{"answer": "yes go ahead", "parameter_name": "confirm",
				"question": "Fill the Search field and submit?", "options": []any{"Yes", "Cancel"}})
		}},
		{name: "chat cancel", words: "cancel", want: dictate.AnswerNo, answer: func(ve *voiceEnv) map[string]any {
			return ve.turn("/api/v0/voice/command", "g1", "cancel", nil).want(200).json()
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			ve := asked(t)
			ve.eng.push(nativeCall("call_c2", dictate.ToolConfirm, `{"confirmation_id": "confirm-2", "answer": "Yes"}`))
			sent := confirmCallSent(t, c.answer(ve))
			if sent == nil || sent["answer"] != c.words || sent["confirmation_id"] != "confirm-2" {
				t.Fatalf("sent %v, want the user's %q", sent, c.words)
			}
			if got := dictate.ClassifyAnswer(c.words); got != c.want {
				t.Fatalf("%q classifies as %v", c.words, got)
			}
		})
	}
}

// Only a user answer after a request_validation counts: a "yes" to a question asked in plain
// text (or never asked), cc's own continuation of a tool result, an answer before a re-ask and
// another question's id are all refused.
func TestConfirmNeedsAnAnswerToAShownQuestion(t *testing.T) {
	confirm := nativeCall("call_c3", dictate.ToolConfirm, `{"confirmation_id": "confirm-2", "answer": "Yes"}`)
	t.Run("plain-text question", func(t *testing.T) {
		ve := confirmEnv(t)
		ve.eng.say("Should I submit it?")
		ve.continueWith("call_f1", parkedOutput())
		ve.eng.push(confirm)
		ve.eng.say("I need you to confirm first.")
		if r := ve.turn("/api/v0/voice/command", "g1", "yes", nil).want(200).json(); confirmCallSent(t, r) != nil {
			t.Fatalf("sent without a shown question: %v", r)
		}
		if !refusedInLastRequest(ve) {
			t.Fatal("no refusal")
		}
		if u := lastUserOf(ve); !strings.Contains(u, "confirm-2") || !strings.Contains(u, "with request_validation") {
			t.Fatalf("the hint doesn't say to ask with request_validation: %q", u)
		}
	})
	t.Run("tool result continuation", func(t *testing.T) {
		ve := confirmEnv(t)
		ve.eng.push(nativeCall("call_v1", "request_validation", `{"question": "Submit?", "parameter_name": "confirm", "options": ["Yes", "Cancel"]}`))
		ve.continueWith("call_f1", parkedOutput())
		// The node's result for some other call is not the user's answer.
		ve.eng.push(confirm)
		ve.eng.say("Waiting for you.")
		if r := ve.continueWith("call_x", map[string]any{"success": true, "answer": "yes"}); confirmCallSent(t, r) != nil {
			t.Fatalf("sent on a tool result: %v", r)
		}
	})
	t.Run("asked again after the answer", func(t *testing.T) {
		ve := confirmEnv(t)
		ve.eng.push(nativeCall("call_v1", "request_validation", `{"question": "Submit?", "parameter_name": "confirm", "options": ["Yes", "Cancel"]}`))
		ve.continueWith("call_f1", parkedOutput())
		ve.eng.push(nativeCall("call_v2", "request_validation", `{"question": "Really submit?", "parameter_name": "confirm", "options": ["Yes", "Cancel"]}`))
		ve.turn("/api/v0/voice/command", "g1", "yes", nil).want(200)
		ve.eng.push(confirm)
		ve.eng.say("Waiting for you.")
		if r := ve.continueWith("call_x", map[string]any{"success": true}); confirmCallSent(t, r) != nil {
			t.Fatalf("sent on the answer to an earlier question: %v", r)
		}
	})
	t.Run("already answered", func(t *testing.T) {
		ve := confirmEnv(t)
		ve.eng.push(nativeCall("call_v1", "request_validation", `{"question": "Submit?", "parameter_name": "confirm", "options": ["Yes", "Cancel"]}`))
		ve.continueWith("call_f1", parkedOutput())
		ve.eng.push(confirm)
		if r := ve.turn("/api/v0/voice/command", "g1", "yes", nil).want(200).json(); confirmCallSent(t, r) == nil {
			t.Fatalf("not sent after the user's yes: %v", r)
		}
		// Its result closes the gate: a later yes to another question doesn't answer it again.
		ve.eng.push(nativeCall("call_v2", "request_validation", `{"question": "Anything else?", "parameter_name": "more", "options": ["Yes", "No"]}`))
		ve.continueWith("call_c3", map[string]any{"success": true, "filled": "Search"})
		ve.eng.push(confirm)
		ve.eng.say("Done.")
		// "yes please", not "yes": the same arguments again would be dropped as a duplicate call.
		if r := ve.turn("/api/v0/voice/command", "g1", "yes please", nil).want(200).json(); confirmCallSent(t, r) != nil {
			t.Fatalf("an answered question confirmed again: %v", r)
		}
	})
	t.Run("a later yes after a cancel", func(t *testing.T) {
		ve := confirmEnv(t)
		ve.eng.push(nativeCall("call_v1", "request_validation", `{"question": "Submit?", "parameter_name": "confirm", "options": ["Yes", "Cancel"]}`))
		ve.continueWith("call_f1", parkedOutput())
		// The user cancels and the model doesn't call desktop_confirm.
		ve.eng.say("OK, I won't submit it.")
		ve.turn("/api/v0/voice/command", "g1", "cancel", nil).want(200)
		// Their next "yes" is about something else; the parked action keeps the "cancel".
		ve.eng.push(confirm)
		sent := confirmCallSent(t, ve.turn("/api/v0/voice/command", "g1", "yes, and turn on the lights", nil).want(200).json())
		if sent != nil && sent["answer"] != "cancel" {
			t.Fatalf("a later yes answered the question: %v", sent)
		}
	})
	t.Run("another id", func(t *testing.T) {
		ve := confirmEnv(t)
		ve.eng.push(nativeCall("call_v1", "request_validation", `{"question": "Submit?", "parameter_name": "confirm", "options": ["Yes", "Cancel"]}`))
		ve.continueWith("call_f1", parkedOutput())
		ve.eng.push(nativeCall("call_c4", dictate.ToolConfirm, `{"confirmation_id": "confirm-1", "answer": "Yes"}`))
		ve.eng.say("Hm.")
		if r := ve.turn("/api/v0/voice/command", "g1", "yes", nil).want(200).json(); confirmCallSent(t, r) != nil {
			t.Fatalf("sent for another question: %v", r)
		}
	})
}
