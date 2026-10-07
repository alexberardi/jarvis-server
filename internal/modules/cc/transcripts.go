package cc

import (
	"context"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// Learning from voice (D19, D50): every completed voice exchange writes one transcript row —
// on any of the four voice routes — when the conversation's speaker was confidently
// identified (so never with recognition off, the default) and the household has
// memory.enabled and memory.extraction_enabled on. Legacy logged only blocking /voice/command.
//
// An exchange that hands client tools to the node is written when its continue completes
// (user message = the original utterance, assistant = the final answer, tool_calls = what the
// node ran); if the node never continues, the next turn flushes it as it stood.

// pendingTranscript is an exchange waiting for its continue.
type pendingTranscript struct {
	userMessage string
	toolCalls   any // ordered tool call list
}

// noteTranscript records a finished turn.
func (m *Module) noteTranscript(ctx context.Context, conv *conversation, utterance string, res engineResult) {
	switch res.Stop {
	case stopNotForMe, stopError:
		return
	case stopToolCalls:
		conv.pendingTranscript = &pendingTranscript{userMessage: utterance, toolCalls: toolCallsJSON(res)}
		return
	}
	m.writeTranscript(ctx, conv, utterance, res.Message, nil)
}

// completeTranscript finishes the pending exchange with the continue's answer.
func (m *Module) completeTranscript(ctx context.Context, conv *conversation, res engineResult) {
	p := conv.pendingTranscript
	if p == nil {
		return
	}
	if res.Stop == stopToolCalls { // native continue chained more client tools
		return
	}
	conv.pendingTranscript = nil
	if res.Stop == stopNotForMe || res.Stop == stopError {
		res.Message = ""
	}
	m.writeTranscript(ctx, conv, p.userMessage, res.Message, p.toolCalls)
}

// flushPendingTranscript writes an exchange whose continue never came.
func (m *Module) flushPendingTranscript(ctx context.Context, conv *conversation) {
	if p := conv.pendingTranscript; p != nil {
		conv.pendingTranscript = nil
		m.writeTranscript(ctx, conv, p.userMessage, "", p.toolCalls)
	}
}

func toolCallsJSON(res engineResult) any {
	var list []any
	for _, c := range res.ToolCalls {
		list = append(list, toolCallObject(c.ID, c.Function.Name, c.Function.Arguments, c.FailureMessage))
	}
	return list
}

func toolCallObject(id, name, args, failure string) *pyjson.Object {
	var fm any
	if failure != "" {
		fm = failure
	}
	return servertools.Obj("id", id, "type", "function",
		"function", servertools.Obj("name", name, "arguments", args), "failure_message", fm)
}

// writeTranscript inserts the row. Failures are logged, never surfaced.
func (m *Module) writeTranscript(ctx context.Context, conv *conversation, user, assistant string, toolCalls any) {
	if conv.speakerID == 0 || conv.householdID == "" {
		return
	}
	sc := settings.Scope{HouseholdID: conv.householdID}
	if !m.settings.Bool(ctx, settingMemoryEnabled, sc) || !m.settings.Bool(ctx, settingExtractionEnabled, sc) {
		return
	}
	var tc, am any
	if l, ok := toolCalls.([]any); ok && len(l) > 0 {
		tc = pyjson.Dumps(l, true)
	}
	if assistant != "" {
		am = assistant
	}
	// A single small SQLite insert, done before the audio starts (the stream routes run the
	// turn first), on a context that outlives a client hang-up.
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, err := m.deps.DB.Write.ExecContext(wctx, `INSERT INTO cc_conversation_transcripts
		(user_id, household_id, conversation_id, user_message, assistant_message, tool_calls_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, conv.speakerID, conv.householdID, conv.id, user, am, tc, dbTime(m.now())); err != nil {
		m.deps.Log.Warn("cc: transcript write failed (non-fatal)", "conversation_id", conv.id, "err", err)
	}
}
