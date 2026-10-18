package cc

import (
	"context"
	"errors"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	tf "github.com/alexberardi/jarvis-server/internal/modules/cc/textfilter"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// Continuing a conversation with the node's client tool results (docs/cc/01 §3.4, 02 §3.4).
// D23: the text path is one formatting call; the native path re-enters the loop. Except (#56):
// while "open X and type Y" still owes the typing, the text path re-enters the loop too, so the
// follow-through nudge can fire (typingPending).

// toolResult is one {tool_call_id, output} the node posts; Output is the decoded JSON value
// (ordered), or a string.
type toolResult struct {
	ToolCallID string
	Output     any
	// images were attached to the output (tool_images.go): lifted out on arrival, shown to the
	// live model with this result only.
	images []chatImage
}

// outputString is the role=tool content: strings as is, anything else json.dumps.
func outputString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return pyjson.Dumps(v, true)
}

// outputObject decodes a result to an object when possible (strings are parsed as JSON).
func outputObject(v any) *pyjson.Object {
	if s, ok := v.(string); ok {
		d, err := pyjson.Loads(s)
		if err != nil {
			return nil
		}
		v = d
	}
	o, _ := v.(*pyjson.Object)
	return o
}

// formatTextMode is _format_tool_result_text_mode: the text path's single formatting call.
func (m *Module) formatTextMode(ctx context.Context, conv *conversation, msgs []chatMsg, results []toolResult) (res engineResult, out []chatMsg) {
	end := traceFrom(ctx).measure("final_response_generation", "llm_proxy", nil)
	defer func() {
		var err error
		if res.Stop == stopError {
			err = errors.New(res.Err)
		}
		end(err)
	}()
	outputs := resultOutputs(results)
	if content, all := tf.FastPathMessage(outputs); all {
		msgs = withoutRole(msgs, "tool")
		if content != "" {
			msgs = append(msgs, chatMsg{Role: "assistant", Content: content})
		}
		return engineResult{Stop: stopComplete, Message: content}, msgs
	}
	tctx := tf.ToolResultsContext(outputs)
	question := ""
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" && parse.PyStrip(msgs[i].Content) != "" {
			question = parse.PyStrip(msgs[i].Content)
			break
		}
	}
	msgs = withoutRole(m.visionGuard(ctx, msgs), "tool")
	msgs = append(msgs, chatMsg{Role: "user", Content: tf.TextModeFormatPrompt(question, tctx, tf.IsKnowledgeDelegation(tctx)),
		images: resultImages(results)})

	maxTok := 256
	temp := 0.7
	resp, err := m.LLM.Chat(ctx, llm.ChatRequest{Label: llm.LabelLive, Messages: toLLM(append(cloneMsgs(msgs), failedNote(results)...)),
		MaxTokens: &maxTok, Temperature: &temp, ReasoningBudget: m.thinkingBudget(ctx, conv.householdID)})
	raw := ""
	if err != nil {
		// Legacy raised here; the turn becomes an error (the route's error shape).
		m.deps.Log.Error("cc: formatting call failed", "conversation_id", conv.id, "err", err)
		return engineResult{Stop: stopError, Err: llmErrText(err)}, msgs
	}
	conv.noteUsage(resp.Usage)
	raw = resp.Content
	content, reasoning := tf.FinishFormattedReply(raw, prompts.SanitizeText, outputs)
	if content != "" {
		msgs = append(msgs, chatMsg{Role: "assistant", Content: content})
	}
	return engineResult{Stop: stopComplete, Message: content, Reasoning: reasoning}, msgs
}

// toolsFailedNote is added (transient, after the results) when every tool call of a round
// failed. Small models have answered "Done, I've typed that" over a success:false result; this
// says in so many words that nothing happened. A round with any success gets no note: the
// model has something real to report and must say which part failed itself.
const toolsFailedNote = "[TOOLS_FAILED] Every tool call just now failed (success: false), so nothing was done. " +
	"Do not say it worked or that anything was done. Tell the user it failed, using each result's error, " +
	"and ask what they want to do."

// allToolsFailed reports whether every result is an object with success: false.
func allToolsFailed(results []toolResult) bool {
	if len(results) == 0 {
		return false
	}
	for _, r := range results {
		o := outputObject(r.Output)
		if o == nil {
			return false
		}
		v, _ := o.Get("success")
		if ok, isBool := v.(bool); !isBool || ok {
			return false
		}
	}
	return true
}

// failedNote is the note to append for these results, or nil.
func failedNote(results []toolResult) []chatMsg {
	if allToolsFailed(results) {
		return []chatMsg{transientSys(toolsFailedNote)}
	}
	return nil
}

// toolResultsMeta is a trace's view of the results: per result its tool (from the call in the
// history), success and error_code. Never the output itself: it can hold what the user
// dictated or what was on their screen.
func toolResultsMeta(msgs []chatMsg, results []toolResult) []any {
	names := map[string]string{}
	for _, m := range msgs {
		for _, c := range m.ToolCalls {
			names[c.ID] = c.Function.Name
		}
	}
	out := make([]any, 0, len(results))
	for _, r := range results {
		e := map[string]any{"tool": names[r.ToolCallID]}
		addResultMeta(e, r.Output)
		out = append(out, e)
	}
	return out
}

// addResultMeta copies a result's success and error_code (when present) into a span's metadata.
func addResultMeta(meta map[string]any, output any) {
	o := outputObject(output)
	if o == nil {
		return
	}
	if v, ok := o.Get("success"); ok {
		if b, isBool := v.(bool); isBool {
			meta["success"] = b
		}
	}
	if v, ok := o.Get("error_code"); ok {
		if c, isStr := v.(string); isStr && c != "" {
			meta["error_code"] = c
		}
	}
}

// continueMeta is the continue span's metadata: the results' summary, when the conversation
// is known and the caller doesn't give up waiting for it.
func (m *Module) continueMeta(ctx context.Context, convID string, results []toolResult) map[string]any {
	conv := m.convs.get(convID)
	if conv == nil || len(results) == 0 {
		return nil
	}
	if lockTurn(ctx, conv) != nil {
		return nil
	}
	defer conv.mu.Unlock()
	return map[string]any{"tool_results": toolResultsMeta(conv.messages, results)}
}

func resultOutputs(results []toolResult) []any {
	out := make([]any, len(results))
	for i, r := range results {
		out[i] = r.Output
	}
	return out
}

// continueBlocking is continue_conversation_with_tool_results.
func (m *Module) continueBlocking(ctx context.Context, convID string, results []toolResult) (engineResult, *conversation, error) {
	conv := m.convs.get(convID)
	if conv == nil {
		return engineResult{}, nil, errPrecondition
	}
	if err := lockTurn(ctx, conv); err != nil {
		return engineResult{}, nil, err
	}
	defer conv.mu.Unlock()
	stashReferenced(conv, results)
	results = m.prepareToolImages(ctx, results)
	msgs := cloneMsgs(conv.messages)
	for _, tr := range results {
		msgs = append(msgs, toolMsg(tr))
	}
	var res engineResult
	if native := conv.mode.Native(); native || typingPending(conv, msgs) {
		msgs = append(msgs, failedNote(results)...)
		utterance := lastUserContent(msgs)
		maxIter := 10
		if !native {
			maxIter = 3
		}
		res, msgs = m.runEngine(ctx, engineInput{conv: conv, msgs: msgs, maxIter: maxIter, utterance: utterance,
			dateKeys: conv.dateKeys, turn: m.toolTurn(conv, utterance)})
	} else {
		res, msgs = m.formatTextMode(ctx, conv, msgs, results)
	}
	if res.Stop != stopError {
		conv.commit(msgs)
		if res.Stop != stopToolCalls {
			m.afterTurn(ctx, conv)
		}
	}
	res = applyExchangeComplete(res)
	m.completeTranscript(ctx, conv, res)
	return res, conv, nil
}

// typingPending reports an "open X and type Y" turn whose typing hasn't been called yet (the
// unfinished-request nudge's condition, followthrough.go): its continue needs the tool loop on
// either path, not one formatting call.
func typingPending(conv *conversation, msgs []chatMsg) bool {
	return conv.hasTool(desktopTypeTool) && !calledThisTurn(msgs, desktopTypeTool) &&
		tf.TypeAfterOpenWords(lastUserContent(msgs)) != ""
}

// continuePlan is the prepared continue-stream: either text to speak directly (fast path) or
// an LLM stream to run, plus the history to commit afterwards.
type continuePlan struct {
	conv    *conversation
	spoken  string    // fast path: speak this, no LLM call
	llmMsgs []chatMsg // the LLM copy (with the plain-text override on the text path)
	commit  []chatMsg // the history the stream's answer is appended to
	results []toolResult
	// base and rev are the history and its background-edit count when planned: a description
	// or compaction during the stream is rebased into the commit, not overwritten.
	base  []chatMsg
	rev   uint64
	usage llm.Usage // the stream's usage (compaction's prompt size)
}

// planContinueStream is the first half of stream_continue_with_tool_results; nil means
// "fall back to the blocking continue" (202), also when the caller gives up waiting for
// another turn of the conversation.
func (m *Module) planContinueStream(ctx context.Context, convID string, results []toolResult) *continuePlan {
	conv := m.convs.get(convID)
	if conv == nil {
		return nil
	}
	if lockTurn(ctx, conv) != nil {
		return nil
	}
	defer conv.mu.Unlock()
	if len(conv.messages) == 0 || typingPending(conv, conv.messages) {
		return nil // the typing is still owed: the blocking continue runs the loop
	}
	stashReferenced(conv, results)
	results = m.prepareToolImages(ctx, results)
	msgs := m.visionGuard(ctx, cloneMsgs(conv.messages))
	native := conv.mode.Native()
	plan := &continuePlan{conv: conv, results: results, base: cloneMsgs(conv.messages), rev: conv.rev}

	{
		{
			if spoken := tf.ContinueStreamFastSpoken(resultOutputs(results)); spoken != "" {
				var committed []chatMsg
				if native {
					committed = cloneMsgs(msgs)
					for _, tr := range results {
						committed = append(committed, toolMsg(tr))
					}
				} else {
					committed = withoutRole(msgs, "tool")
				}
				committed = append(committed, chatMsg{Role: "assistant", Content: spoken})
				conv.commit(committed)
				plan.spoken = spoken
				return plan
			}
		}
	}
	if native {
		for _, tr := range results {
			msgs = append(msgs, toolMsg(tr))
		}
		plan.llmMsgs = append(cloneMsgs(msgs), failedNote(results)...)
	} else {
		tctx := tf.ToolResultsContext(resultOutputs(results))
		msgs = withoutRole(msgs, "tool")
		msgs = append(msgs, chatMsg{Role: "user", Content: tf.ContinueToolResultsMessage(tctx, tf.IsKnowledgeDelegation(tctx)),
			images: resultImages(results)})
		plan.llmMsgs = append(append(cloneMsgs(msgs), sysMsg(continueStreamOverride)), failedNote(results)...)
	}
	plan.commit = msgs
	return plan
}

// commitContinueStream appends the streamed answer (or the substituted fallback) and commits.
func (m *Module) commitContinueStream(ctx context.Context, plan *continuePlan, answer string) {
	conv := plan.conv
	conv.mu.Lock()
	defer conv.mu.Unlock()
	if plan.spoken != "" {
		m.completeTranscript(ctx, conv, engineResult{Stop: stopComplete, Message: plan.spoken})
		m.afterTurn(ctx, conv)
		return
	}
	conv.noteUsage(plan.usage)
	if answer != "" {
		commit := append(cloneMsgs(plan.commit), chatMsg{Role: "assistant", Content: answer})
		if conv.rev != plan.rev {
			commit = rebaseCommit(plan.base, conv.messages, commit)
		}
		conv.commit(commit)
	}
	m.completeTranscript(ctx, conv, engineResult{Stop: stopComplete, Message: answer})
	m.afterTurn(ctx, conv)
}
