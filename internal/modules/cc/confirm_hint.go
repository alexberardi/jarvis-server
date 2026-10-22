package cc

import (
	"fmt"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/dictate"
	tf "github.com/alexberardi/jarvis-server/internal/modules/cc/textfilter"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// The pending-question hint (#420). A desktop node parks a click, a submitting fill or an Enter
// behind the user's yes (dictate.Confirmer): its result is status awaiting_confirmation with a
// confirmation_id. When the model asks in plain text instead of request_validation, the user's
// "yes" arrives as a new turn, and on the MBP the model then ran web_search and said it had
// filled the field. The next turn after a parked action gets one line naming the question, the
// id and the tool that answers it; later turns don't.

// pendingConfirm is a parked action's question, from its tool result.
type pendingConfirm struct {
	id, question string
	deadline     time.Time
}

// notePendingConfirm keeps the last awaiting_confirmation among results for the next turn;
// results without one (desktop_confirm's own answer) clear it. Callers hold conv.mu.
func (m *Module) notePendingConfirm(conv *conversation, results []toolResult) {
	conv.pendingConfirm = nil
	for _, tr := range results {
		o := outputObject(tr.Output)
		if !tf.AwaitingConfirmation(o) {
			continue
		}
		id, _ := o.Get("confirmation_id")
		q, _ := o.Get("question")
		ids, _ := id.(string)
		if ids == "" {
			continue
		}
		// The agent's timeout is fixed (dictate.DefaultConfirmTimeout); a later answer finds nothing.
		conv.pendingConfirm = &pendingConfirm{id: ids, question: pyjson.Str(q), deadline: m.now().Add(dictate.DefaultConfirmTimeout)}
	}
}

// pendingConfirmHint is the hint for the turn after a parked action, or "". It is used once.
func (m *Module) pendingConfirmHint(conv *conversation) string {
	p := conv.pendingConfirm
	conv.pendingConfirm = nil
	if p == nil || !conv.hasTool(dictate.ToolConfirm) || m.now().After(p.deadline) {
		return ""
	}
	return fmt.Sprintf("[waiting for the user's answer: %q (confirmation_id %s). Nothing has been done yet. If this "+
		"answers it, call %s with that confirmation_id and the user's words; never say it was done unless that "+
		"call's result shows it.]", p.question, p.id, dictate.ToolConfirm)
}
