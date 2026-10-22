package cc

import (
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	tf "github.com/alexberardi/jarvis-server/internal/modules/cc/textfilter"
)

const confirmTools = `[
 {"type": "function", "function": {"name": "browser_fill", "description": "Fill a field on the page.",
  "parameters": {"type": "object", "properties": {"field": {"type": "string"}, "text": {"type": "string"}, "submit": {"type": "boolean"}}, "required": ["text"]}},
  "keywords": ["fill", "search", "browser", "google"]},
 {"type": "function", "function": {"name": "desktop_confirm", "description": "Answer a pending question.",
  "parameters": {"type": "object", "properties": {"confirmation_id": {"type": "string"}, "answer": {"type": "string"}}, "required": ["confirmation_id", "answer"]}},
  "keywords": ["yes", "confirm"]}
]`

// MBP rc18 (#420): browser_fill submit=true was parked behind the user's yes; on the follow-up
// the model ran web_search and said it had filled the field. The next turn after a parked
// action now says which question is waiting and how to answer it, once.
func TestPendingConfirmHintOnTheNextTurn(t *testing.T) {
	for name, model := range map[string]string{"native": prompts.Qwen3_5_9B, "text": prompts.Qwen3_14B} {
		t.Run(name, func(t *testing.T) {
			ve := newVoiceEnv(t, model)
			ve.start("p1", confirmTools)
			fill := `{"field": "search", "text": "best dog grooming", "submit": true}`
			if name == "native" {
				ve.eng.push(nativeCall("call_f1", "browser_fill", fill))
			} else {
				ve.eng.say(`<tool_call>{"name": "browser_fill", "arguments": ` + fill + `}</tool_call>`)
			}
			r := ve.turn("/api/v0/voice/command", "p1", "search google for best dog grooming", nil).want(200).json()
			calls, _ := r["tool_calls"].([]any)
			if r["stop_reason"] != "tool_calls" || len(calls) != 1 {
				t.Fatalf("%v", r)
			}
			id := calls[0].(map[string]any)["id"]

			ve.eng.say(`Fill the search field with "best dog grooming" and submit?`)
			ve.do("POST", "/api/v0/voice/command/continue", map[string]any{"conversation_id": "p1",
				"tool_results": []any{map[string]any{"tool_call_id": id, "output": map[string]any{
					"success": false, "done": false, "status": "awaiting_confirmation", "confirmation_id": "confirm-7",
					"question": `Fill the search field with "best dog grooming" and submit?`, "expires_in_seconds": 60}}}},
				ve.node.h()).want(200)

			ve.eng.say("OK.")
			ve.turn("/api/v0/voice/command", "p1", "yes go ahead", nil).want(200)
			u := lastUserOf(ve)
			if !strings.Contains(u, "confirm-7") || !strings.Contains(u, "desktop_confirm") ||
				!strings.Contains(u, "Nothing has been done yet") {
				t.Fatalf("no pending-question hint: %q", u)
			}

			ve.eng.say("It's sunny.")
			ve.turn("/api/v0/voice/command", "p1", "what's the weather", nil).want(200)
			if u := lastUserOf(ve); strings.Contains(u, "confirm-7") {
				t.Fatalf("hint repeated on a later turn: %q", u)
			}

			// Answered within the turn (desktop_confirm's result came back): nothing is waiting.
			conv := ve.m.convs.get("p1")
			ve.m.notePendingConfirm(conv, []toolResult{{Output: `{"status": "awaiting_confirmation", "confirmation_id": "confirm-8", "question": "Click OK?"}`}})
			ve.m.notePendingConfirm(conv, []toolResult{{Output: `{"success": true, "clicked": "ok"}`}})
			if h := ve.m.pendingConfirmHint(conv); h != "" {
				t.Fatalf("hint after the answer: %q", h)
			}
		})
	}
}

// A parked action says success: false (it isn't done), but it is a question, not a failure:
// the model isn't told "every tool failed", a reply asking it isn't nudged as a false claim,
// and an empty reply falls back to the question rather than "The command failed."
func TestAwaitingConfirmationIsNotAFailure(t *testing.T) {
	parked := `{"success": false, "done": false, "status": "awaiting_confirmation", "confirmation_id": "confirm-1", "question": "Click OK?"}`
	if allToolsFailed([]toolResult{{Output: parked}}) {
		t.Error("allToolsFailed")
	}
	if toolFailedThisTurn([]chatMsg{{Role: "user", Content: "click ok"}, {Role: "tool", Content: parked}}) {
		t.Error("toolFailedThisTurn")
	}
	if got := tf.ToolResultFallback([]any{parked}); got != "Click OK?" {
		t.Errorf("fallback %q", got)
	}
}
