package textfilter

import (
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
)

// genericFillerResponses is _GENERIC_FILLER_RESPONSES.
var genericFillerResponses = map[string]bool{
	"task completed":      true,
	"task complete":       true,
	"task is complete":    true,
	"task done":           true,
	"task finished":       true,
	"completed":           true,
	"all tasks completed": true,
}

// FillerClarification replaces a terminal generic filler reply.
const FillerClarification = "Sorry, I didn't quite catch that — could you say it again?"

// RewriteTerminalFiller is ConversationHandler._rewrite_terminal_filler: a final prose reply
// that is generic completion filler ("Task completed.") becomes FillerClarification. Other
// stop reasons (not_for_me, tool_calls, validation_required, error, server_tool_complete)
// pass through.
func RewriteTerminalFiller(stopReason, message string) string {
	switch stopReason {
	case "not_for_me", "tool_calls", "validation_required", "error", "server_tool_complete":
		return message
	}
	norm := strings.TrimRight(parse.PyLower(parse.PyStrip(message)), " .!")
	if genericFillerResponses[norm] {
		return FillerClarification
	}
	return message
}

// IsTransientSystemBlockContent is _is_transient_system_block's content test for a
// role=system message: the per-turn blocks re-derived every turn (speaker, router hint,
// plain-text override, recently shown, ambient). Note the legacy "User Profile - If user
// asks" prefix no longer matches the speaker block's em-dash wording (docs/cc/03 §8.13); the Go
// port marks transient blocks structurally, so this is kept for reference and parity tests.
func IsTransientSystemBlockContent(content string) bool {
	return strings.HasPrefix(content, "You are speaking with ") ||
		strings.HasPrefix(content, "User Profile - If user asks") ||
		strings.HasPrefix(content, "Router hint:") ||
		strings.HasPrefix(content, "Respond naturally in plain text") ||
		strings.HasPrefix(content, prompts.RecentlyShownPrefix) ||
		strings.HasPrefix(content, prompts.AmbientContextPrefix)
}
