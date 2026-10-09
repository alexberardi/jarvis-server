package cc

import (
	"context"
	"fmt"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// Context compaction (docs/cc/chat-images.md CI4/CI6). Every cc conversation (voice and chat
// share the cache) tracks its prompt size from the live call's usage. After a finished turn at
// or above cc.compaction.threshold of the live slot's context, a background job summarizes the
// older turns into one message; a turn that arrives at or above cc.compaction.hard_threshold
// with nothing compacted yet compacts first, synchronously.
//
// What survives: messages[0] byte for byte (the warmed system prompt, G1: the engine's prefix
// cache of it stays valid), one summary message right after it, and the last
// compactKeepTurns turns verbatim. Turns are split only at a user message that opens a new
// exchange, so a tool call never loses its result. Logged as counts only, never content.

const (
	settingCompactThreshold = "cc.compaction.threshold"
	settingCompactHard      = "cc.compaction.hard_threshold"

	convCompactJob = "cc.conv_compact"

	compactKeepTurns     = 2
	compactSummaryTokens = 400
	compactMsgRunes      = 1200  // per message in the summarizer's input
	compactInputRunes    = 24000 // the whole input (the newest part is kept)

	summaryPrefix = "Summary of the earlier conversation (older turns were compacted):\n"
)

const compactSystemPrompt = "You compress conversation history for a home voice assistant. Summarize the " +
	"conversation below in at most 150 words of plain prose, third person (\"The user asked …\"): facts and " +
	"preferences the user shared, what was asked and answered, actions taken with tools and their outcomes, " +
	"and anything still pending. Keep names, dates, times, numbers and ids exactly. No preamble."

func compactionDefinitions() []settings.Definition {
	return []settings.Definition{
		{Key: settingCompactThreshold, Category: "conversation", Type: settings.Float, Default: 0.75,
			Validate: floatRange(0.3, 0.95),
			Description: "Context compaction: after a turn whose prompt reached this fraction of the live model's " +
				"context, the background model summarizes the older turns into one message (the last two turns " +
				"stay verbatim). 0.3-0.95."},
		{Key: settingCompactHard, Category: "conversation", Type: settings.Float, Default: 0.90,
			Validate: floatRange(0.5, 0.98),
			Description: "Context compaction: a turn arriving at this fraction of the live model's context with " +
				"nothing compacted yet compacts first, before the turn (adds a summarizer call to that turn). " +
				"0.5-0.98; never below cc.compaction.threshold."},
	}
}

// floatRange validates a Float setting's bounds.
func floatRange(lo, hi float64) func(any) error {
	return func(v any) error {
		var f float64
		switch x := v.(type) {
		case float64:
			f = x
		case int64:
			f = float64(x)
		case int:
			f = float64(x)
		default:
			return fmt.Errorf("must be a number between %g and %g", lo, hi)
		}
		if f < lo || f > hi {
			return fmt.Errorf("must be between %g and %g", lo, hi)
		}
		return nil
	}
}

// compactThresholds are the async and sync fractions (hard never below async).
func (m *Module) compactThresholds(ctx context.Context) (float64, float64) {
	t := m.settings.Float(ctx, settingCompactThreshold, settings.Scope{})
	h := m.settings.Float(ctx, settingCompactHard, settings.Scope{})
	return t, max(t, h)
}

// liveContext is the live slot's per-request context, 0 when unknown.
func (m *Module) liveContext(ctx context.Context) int {
	ep, ok := m.endpoint(ctx, llm.LabelLive)
	if !ok {
		return 0
	}
	return ep.ContextLength
}

// noteUsage records a live call's prompt size. Callers hold conv.mu.
func (c *conversation) noteUsage(u llm.Usage) {
	if u.PromptTokens > 0 {
		c.promptTokens = u.PromptTokens
	}
}

// afterTurn runs when a turn has finished (not while client tool calls are outstanding): at
// the threshold it queues a compaction. Callers hold conv.mu.
func (m *Module) afterTurn(ctx context.Context, conv *conversation) {
	if conv.promptTokens <= 0 {
		return
	}
	n := m.liveContext(ctx)
	if n <= 0 {
		return
	}
	t, _ := m.compactThresholds(ctx)
	if float64(conv.promptTokens) < t*float64(n) {
		return
	}
	m.scheduleConvJob(convCompactJob, convJob{ConversationID: conv.id}, convCompactJob+":"+conv.id)
}

// compactBeforeTurn is the synchronous compaction at the hard threshold. Callers hold conv.mu.
func (m *Module) compactBeforeTurn(ctx context.Context, conv *conversation) {
	if conv.promptTokens <= 0 {
		return
	}
	n := m.liveContext(ctx)
	if n <= 0 {
		return
	}
	_, h := m.compactThresholds(ctx)
	if float64(conv.promptTokens) < h*float64(n) {
		return
	}
	end := traceFrom(ctx).measure("compaction", "cc", nil)
	plan, ok := planCompaction(conv.messages)
	if !ok {
		end(nil)
		return
	}
	summary, err := m.summarize(ctx, plan.older)
	end(err)
	if err != nil {
		m.deps.Log.Warn("cc: compaction failed", "conversation_id", conv.id, "mode", "sync", "err", err)
		return
	}
	m.applyCompaction(conv, plan, summary, "sync", n)
}

// compactJob is the async compaction: plan under the lock, summarize unlocked, then splice
// only when the summarized messages are still exactly what the history holds.
func (m *Module) compactJob(ctx context.Context, j convJob) {
	conv := m.convs.peek(j.ConversationID)
	if conv == nil || m.LLM == nil {
		return
	}
	n := m.liveContext(ctx)
	conv.mu.Lock()
	t, _ := m.compactThresholds(ctx)
	if n <= 0 || conv.promptTokens <= 0 || float64(conv.promptTokens) < t*float64(n) {
		conv.mu.Unlock()
		return // compacted meanwhile (sync), or no longer known to be large
	}
	plan, ok := planCompaction(conv.messages)
	conv.mu.Unlock()
	if !ok {
		return
	}
	summary, err := m.summarize(ctx, plan.older)
	if err != nil {
		m.deps.Log.Warn("cc: compaction failed", "conversation_id", conv.id, "mode", "async", "err", err)
		return
	}
	conv.mu.Lock()
	defer conv.mu.Unlock()
	if !plan.stillHolds(conv.messages) {
		m.deps.Log.Info("cc: compaction skipped: history changed", "conversation_id", conv.id)
		return
	}
	m.applyCompaction(conv, plan, summary, "async", n)
}

// compactionPlan is what one compaction replaces: messages[1:cut] (older, the summarizer's
// input, as they were when planned).
type compactionPlan struct {
	cut       int
	older     []chatMsg
	firstID   uint64
	turnsDone int
}

// stillHolds reports whether msgs still starts with the planned prefix, message by message
// (id, content, images): a turn or a description since the plan means a stale summary.
func (p compactionPlan) stillHolds(msgs []chatMsg) bool {
	if len(msgs) < p.cut || len(msgs) == 0 || msgs[0].id != p.firstID {
		return false
	}
	for i, o := range p.older {
		c := msgs[i+1]
		if c.id != o.id || c.Content != o.Content || len(c.images) != len(o.images) {
			return false
		}
	}
	return true
}

// turnStarts returns the index (into msgs) where each turn begins: a user message that doesn't
// continue an open exchange (as trimHistory splits), moved back over the transient blocks
// that precede it (they belong to that turn's request).
func turnStarts(msgs []chatMsg) []int {
	var starts []int
	for i := 1; i < len(msgs); i++ {
		if msgs[i].Role != "user" {
			continue
		}
		prev := -1
		for k := i - 1; k >= 1; k-- {
			if !msgs[k].transient {
				prev = k
				break
			}
		}
		if prev >= 1 && expectsContinuation(msgs[prev]) {
			continue
		}
		s := i
		for s-1 >= 1 && msgs[s-1].transient {
			s--
		}
		starts = append(starts, s)
	}
	return starts
}

// planCompaction picks what to summarize: everything between messages[0] and the last
// compactKeepTurns turns. ok=false when there is nothing older to summarize.
func planCompaction(msgs []chatMsg) (compactionPlan, bool) {
	if len(msgs) < 2 {
		return compactionPlan{}, false
	}
	starts := turnStarts(msgs)
	if len(starts) <= compactKeepTurns {
		return compactionPlan{}, false
	}
	cut := starts[len(starts)-compactKeepTurns]
	older := cloneMsgs(msgs[1:cut])
	content := 0
	for _, o := range older {
		if !o.transient && !o.summary {
			content++
		}
	}
	if content == 0 {
		return compactionPlan{}, false
	}
	return compactionPlan{cut: cut, older: older, firstID: msgs[0].id, turnsDone: len(starts) - compactKeepTurns}, true
}

// applyCompaction splices the summary in. Callers hold conv.mu.
func (m *Module) applyCompaction(conv *conversation, plan compactionPlan, summary string, mode string, ctxLen int) {
	before := len(conv.messages)
	out := make([]chatMsg, 0, len(conv.messages)-plan.cut+2)
	out = append(out, conv.messages[0])
	out = append(out, chatMsg{Role: "system", Content: summaryPrefix + summary, summary: true})
	out = append(out, conv.messages[plan.cut:]...)
	tokens := conv.promptTokens
	conv.commit(out)
	conv.rev++
	conv.promptTokens = 0 // unknown until the next live call
	m.deps.Log.Info("cc: conversation compacted", "conversation_id", conv.id, "mode", mode,
		"messages_before", before, "messages_after", len(conv.messages), "turns_summarized", plan.turnsDone,
		"prompt_tokens", tokens, "context", ctxLen)
}

// summarize asks the background slot (live when background can't) for the summary.
func (m *Module) summarize(ctx context.Context, older []chatMsg) (string, error) {
	transcript := renderForSummary(older)
	maxTok := compactSummaryTokens
	temp := 0.3
	zero := 0
	var lastErr error
	for _, label := range []string{llm.LabelBackground, llm.LabelLive} {
		resp, err := m.LLM.Chat(ctx, llm.ChatRequest{Label: label, MaxTokens: &maxTok, Temperature: &temp, ReasoningBudget: &zero,
			Messages: []llm.Message{
				{Role: "system", Content: llm.TextContent(compactSystemPrompt)},
				{Role: "user", Content: llm.TextContent(transcript)},
			}})
		if err != nil {
			lastErr = err
			continue
		}
		s := parse.PyStrip(thinkCaptureRE.ReplaceAllString(resp.Content, ""))
		if s == "" {
			lastErr = fmt.Errorf("empty summary from %s", label)
			continue
		}
		return s, nil
	}
	return "", lastErr
}

// renderForSummary is the summarizer's input: the older messages as a plain transcript
// (images as "[image]"; transient blocks and steering nags left out), newest part kept when
// long.
func renderForSummary(older []chatMsg) string {
	var lines []string
	for _, o := range older {
		if o.transient {
			continue
		}
		text := clip(o.Content, compactMsgRunes)
		switch {
		case o.summary:
			lines = append(lines, "Earlier summary: "+strings.TrimPrefix(o.Content, summaryPrefix))
		case o.Role == "user":
			img := ""
			if len(o.images) > 0 {
				img = strings.Repeat("[image] ", len(o.images))
			}
			lines = append(lines, "User: "+img+text)
		case o.Role == "assistant":
			s := "Assistant: " + text
			for _, tc := range o.ToolCalls {
				s += fmt.Sprintf("\n(Assistant called %s with %s)", tc.Function.Name, clip(tc.Function.Arguments, 300))
			}
			lines = append(lines, s)
		case o.Role == "tool":
			name := o.Name
			if name == "" {
				name = "tool"
			}
			lines = append(lines, fmt.Sprintf("Tool result (%s): %s", name, text))
		case o.Role == "system":
			if isNag(o, nagMustCall) || isNag(o, nagDedupe) || isNag(o, nagInvalid) || isNag(o, nagISO) ||
				strings.Contains(o.Content, nagDoubleCheck) {
				continue
			}
			lines = append(lines, "Note: "+text)
		}
	}
	out := strings.Join(lines, "\n")
	if r := []rune(out); len(r) > compactInputRunes {
		out = "…" + string(r[len(r)-compactInputRunes:])
	}
	return out
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
