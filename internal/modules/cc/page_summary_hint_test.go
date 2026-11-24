package cc

import (
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
)

// A summary of the page covers the whole page, not its intro. Found live 2026-11-13: with all
// of a Wikipedia article read, Sonnet typed a summary of its first paragraphs ("The intro gives
// a solid summary").
func TestPageSummaryHint(t *testing.T) {
	readTool := `[{"type":"function","function":{"name":"browser_read_page","description":"Read the page.","parameters":{"type":"object","properties":{"max_chars":{"type":"integer"}}}}}]`
	for _, c := range []struct {
		name, tools, say string
		want             bool
	}{
		{"summarize into an app", readTool, "Summarize the Wikipedia article into the text edit window.", true},
		{"sum it up", readTool, "sum up this page for me", true},
		{"not a summary", readTool, "what does this page say about pricing", false},
		{"no page tool", weatherTool, "summarize this page", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			ce := newChatEnv(t, prompts.Qwen3_5_9B)
			ce.pub.tools = `{"client_tools": ` + c.tools + `, "available_commands": [], "installed_packages": []}`
			ce.eng.say("Done.")
			ce.chat("tok-7", map[string]any{"message": c.say})
			u := lastUserOf(ce.voiceEnv)
			if got := strings.Contains(u, PageSummaryHint); got != c.want {
				t.Fatalf("hint %v, want %v: %q", got, c.want, u)
			}
		})
	}
}
