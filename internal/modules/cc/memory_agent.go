package cc

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// Agent context (docs/cc/04 §3.3 C): household rows the node's background agents inject
// (weather, calendar, news, reminders) that match the utterance are appended to the user
// message, after the turn hints, so recency keeps them top of mind. Gated per household by
// model.advanced_context (default off) and memory.agent_context_enabled.

// agentContextEmbedTimeout keeps the per-turn embedding off the latency budget; on timeout
// the search falls back to keywords (04 §11 "Hot path").
const agentContextEmbedTimeout = 150 * time.Millisecond

const agentContextHeader = "You already know the following — weave relevant facts into your answer:"

// agentContextHint returns the "You already know…" block for the utterance, or "".
func (m *Module) agentContextHint(ctx context.Context, conv *conversation, utterance string) string {
	hh := conv.householdID
	if hh == "" {
		return ""
	}
	sc := settings.Scope{HouseholdID: hh}
	if !m.settings.Bool(ctx, settingAdvancedContext, sc) || !m.settings.Bool(ctx, settingAgentContextEnabled, sc) {
		return ""
	}
	limit := int(m.settings.Int(ctx, settingAgentContextMaxResults, sc))
	maxChars := int(m.settings.Int(ctx, settingAgentContextMaxChars, sc))
	threshold := m.settings.Float(ctx, settingAgentContextThreshold, sc)
	hits, _, err := m.searchMemories(ctx, memScope{HouseholdID: hh}, utterance, limit, threshold, agentContextEmbedTimeout)
	if err != nil {
		m.deps.Log.Warn("cc: agent context lookup failed (non-fatal)", "err", err)
		return ""
	}
	contents := make([]string, len(hits))
	for i, h := range hits {
		contents[i] = h.Content
	}
	return formatAgentContext(contents, maxChars)
}

// formatAgentContext is AgentContextService._format_results.
func formatAgentContext(contents []string, maxChars int) string {
	var lines []string
	total := utf8.RuneCountInString(agentContextHeader) + 1
	for _, c := range contents {
		line := "- " + c
		n := utf8.RuneCountInString(line)
		if total+n+1 > maxChars {
			break
		}
		lines = append(lines, line)
		total += n + 1
	}
	if len(lines) == 0 {
		return ""
	}
	return agentContextHeader + "\n" + strings.Join(lines, "\n")
}
