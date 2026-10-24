package cc

import (
	"strings"

	"github.com/alexberardi/jarvis-server/internal/agent/dictate"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	tf "github.com/alexberardi/jarvis-server/internal/modules/cc/textfilter"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// The confirmation gate (#574). A desktop node parks a submitting action behind the user's yes
// (dictate.Confirmer), and the model answers it with desktop_confirm. On the dev box the 9B
// called desktop_confirm {answer: "Yes"} straight after browser_fill parked, in the same turn,
// and the fill and submit ran without the user ever seeing the question; the only guard was the
// tool text. cc now enforces it: desktop_confirm reaches the node only after a request_validation
// was shown to the user since the park and the user answered it (a new turn, or the node's
// validation response), and its answer is the user's own words, whatever the model wrote. cc's
// own synthetic user messages (continuations, nags) never count.

// confirmGate is the parked action desktop_confirm may answer.
type confirmGate struct {
	id string
	// asked: a request_validation was shown to the user since the park (or since the user's
	// last answer); answer is the user's words after it, and heard says there are some.
	asked  bool
	heard  bool
	answer string
	// sent is the desktop_confirm call that carried the answer: its result closes the gate.
	sent string
}

// validationResponseID is the tool_call_id a node answers a request_validation question with
// (agent/voice ValidationRequest.answer; jarvisd's questions carry none).
const validationResponseID = "validation-response"

// confirmNotAnswered is desktop_confirm's result when the user hasn't answered.
const confirmNotAnswered = "Nothing was confirmed and nothing was done: the user hasn't answered yet. Ask them the " +
	"question with request_validation (options [\"Yes\", \"Cancel\"]) and wait for their reply; never answer it yourself."

// noteConfirmGate opens the gate for a parked action among results, and takes the user's answer
// from a node's validation response. Callers hold conv.mu.
func (m *Module) noteConfirmGate(conv *conversation, results []toolResult) {
	for _, tr := range results {
		o := outputObject(tr.Output)
		if g := conv.confirmGate; g != nil && g.sent != "" && tr.ToolCallID == g.sent {
			conv.confirmGate = nil // answered: the node keeps no question now
		}
		if tr.ToolCallID == validationResponseID && o != nil {
			if a, ok := o.Get("answer"); ok {
				conv.confirmGate.hear(pyjson.Str(a))
			}
			continue
		}
		if !tf.AwaitingConfirmation(o) {
			continue
		}
		id, _ := o.Get("confirmation_id")
		if ids, _ := id.(string); ids != "" {
			conv.confirmGate = &confirmGate{id: ids} // the node keeps only the newest
		}
	}
}

// hear records the user's words, if they answer a question that was shown.
func (g *confirmGate) hear(words string) {
	if g == nil || !g.asked {
		return
	}
	g.heard, g.answer = true, words
}

// shown notes that a request_validation question reached the user: only an answer after it counts.
func (g *confirmGate) shown() {
	if g == nil {
		return
	}
	g.asked, g.heard, g.answer = true, false, ""
}

// gateConfirm checks a desktop_confirm call: its arguments with the user's words as the answer,
// or the refusal to return instead of calling the node.
func (conv *conversation) gateConfirm(callID, args string) (string, *pyjson.Object) {
	g := conv.confirmGate
	o := argsObject(args)
	id, _ := o.Get("confirmation_id")
	ids, _ := id.(string)
	if g == nil || !g.heard || parse.PyStrip(ids) != g.id {
		return "", servertools.Obj("success", false, "done", false, "error", confirmNotAnswered)
	}
	o.Set("answer", strings.TrimSpace(g.answer))
	g.sent = callID
	return pyjson.Dumps(o, false), nil
}

// isConfirmCall reports a call that answers a parked action.
func isConfirmCall(conv *conversation, name string) bool {
	return name == dictate.ToolConfirm && !conv.serverNames[name]
}
