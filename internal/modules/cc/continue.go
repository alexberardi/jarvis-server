package cc

import (
	"context"
	"errors"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
	tf "github.com/alexberardi/jarvis-server/internal/modules/cc/textfilter"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// Continuing a conversation with the node's client tool results (docs/cc/01 §3.4, 02 §3.4).
// D23: the text path is one formatting call; the native path re-enters the loop.

// toolResult is one {tool_call_id, output} the node posts; Output is the decoded JSON value
// (ordered), or a string.
type toolResult struct {
	ToolCallID string
	Output     any
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
	msgs = append(msgs, chatMsg{Role: "user", Content: tf.TextModeFormatPrompt(question, tctx, tf.IsKnowledgeDelegation(tctx))})

	maxTok := 256
	temp := 0.7
	resp, err := m.LLM.Chat(ctx, llm.ChatRequest{Label: llm.LabelLive, Messages: toLLM(msgs), MaxTokens: &maxTok,
		Temperature: &temp, ReasoningBudget: m.thinkingBudget(ctx, conv.householdID)})
	raw := ""
	if err != nil {
		// Legacy raised here; the turn becomes an error (the route's error shape).
		m.deps.Log.Error("cc: formatting call failed", "conversation_id", conv.id, "err", err)
		return engineResult{Stop: stopError, Err: err.Error()}, msgs
	}
	conv.noteUsage(resp.Usage)
	raw = resp.Content
	content, reasoning := tf.FinishFormattedReply(raw, conv.provider.SanitizeText, outputs)
	if content != "" {
		msgs = append(msgs, chatMsg{Role: "assistant", Content: content})
	}
	return engineResult{Stop: stopComplete, Message: content, Reasoning: reasoning}, msgs
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
	conv.mu.Lock()
	defer conv.mu.Unlock()
	stashReferenced(conv, results)
	msgs := cloneMsgs(conv.messages)
	for _, tr := range results {
		msgs = append(msgs, chatMsg{Role: "tool", ToolCallID: tr.ToolCallID, Content: outputString(tr.Output)})
	}
	var res engineResult
	if conv.provider.SupportsNativeTools() {
		utterance := lastUserContent(msgs)
		res, msgs = m.runEngine(ctx, engineInput{conv: conv, msgs: msgs, maxIter: 10, utterance: utterance,
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
// "fall back to the blocking continue" (202).
func (m *Module) planContinueStream(ctx context.Context, convID string, results []toolResult) *continuePlan {
	conv := m.convs.get(convID)
	if conv == nil {
		return nil
	}
	conv.mu.Lock()
	defer conv.mu.Unlock()
	stashReferenced(conv, results)
	if len(conv.messages) == 0 {
		return nil
	}
	msgs := m.visionGuard(ctx, cloneMsgs(conv.messages))
	native := conv.provider.SupportsNativeTools()
	plan := &continuePlan{conv: conv, results: results, base: cloneMsgs(conv.messages), rev: conv.rev}

	{
		{
			if spoken := tf.ContinueStreamFastSpoken(resultOutputs(results)); spoken != "" {
				var committed []chatMsg
				if native {
					committed = cloneMsgs(msgs)
					for _, tr := range results {
						committed = append(committed, chatMsg{Role: "tool", ToolCallID: tr.ToolCallID, Content: outputString(tr.Output)})
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
			msgs = append(msgs, chatMsg{Role: "tool", ToolCallID: tr.ToolCallID, Content: outputString(tr.Output)})
		}
		plan.llmMsgs = cloneMsgs(msgs)
	} else {
		tctx := tf.ToolResultsContext(resultOutputs(results))
		msgs = withoutRole(msgs, "tool")
		msgs = append(msgs, chatMsg{Role: "user", Content: tf.ContinueToolResultsMessage(tctx, tf.IsKnowledgeDelegation(tctx))})
		plan.llmMsgs = append(cloneMsgs(msgs), sysMsg(continueStreamOverride))
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
