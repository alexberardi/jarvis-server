package cc

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// A desktop node's app names reach whisper's prompt (before the household's own sentence)
// and nothing else: not the LLM's messages.
func TestDesktopVocabularyBiasesWhisperOnly(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_8B)
	ve.set(settingSTTPrompt, "Hey Jarvis, turn on the kitchen lights.", settings.Scope{HouseholdID: voiceHH})
	opts := func() string {
		return ve.m.commandSTTOptions(context.Background(), voiceHH, ve.node.id, nil, nil).Prompt
	}
	if p := opts(); p != "Hey Jarvis, turn on the kitchen lights." {
		t.Fatalf("before: %q", p)
	}
	body := `{"conversation_id":"c1","node_context":{"timezone":"UTC","stt_vocabulary":"Omawrite, Neovim,\nSteam"},"client_tools":[]}`
	ve.do("POST", "/api/v0/conversation/start", body, ve.node.h()).want(200)
	if p := opts(); p != "Omawrite, Neovim, Steam. Hey Jarvis, turn on the kitchen lights." {
		t.Fatalf("with names: %q", p)
	}
	for _, r := range ve.eng.requests() {
		raw, _ := r["messages"].([]any)
		for _, m := range raw {
			if c, _ := m.(map[string]any)["content"].(string); strings.Contains(c, "Omawrite") {
				t.Fatal("app names reached the LLM")
			}
		}
	}
	// A later start without a list forgets it.
	ve.do("POST", "/api/v0/conversation/start", `{"conversation_id":"c2","node_context":{"timezone":"UTC"},"client_tools":[]}`, ve.node.h()).want(200)
	if p := opts(); strings.Contains(p, "Omawrite") {
		t.Fatalf("kept after a start without names: %q", p)
	}
}

func TestCleanVocabulary(t *testing.T) {
	long := strings.Repeat("Omawrite, ", 100)
	got := cleanVocabulary(long)
	if len(got) > maxSTTVocabulary || strings.HasSuffix(got, ",") || !strings.HasPrefix(got, "Omawrite, Omawrite") {
		t.Fatalf("%q", got)
	}
	if got := cleanVocabulary("A\x00B\tC‮D"); got != "A B C D" {
		t.Fatalf("%q", got)
	}
	if got := cleanVocabulary(strings.Repeat("é", 500)); len(got) > maxSTTVocabulary || !utf8.ValidString(got) {
		t.Fatalf("len %d", len(got))
	}
}
