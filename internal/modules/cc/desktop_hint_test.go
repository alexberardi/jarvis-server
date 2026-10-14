package cc

import (
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
)

func TestSpokenAppName(t *testing.T) {
	names := strings.Split("Alacritty, Firefox, Omawrite, Neovim, Steam, HEY, X, Google Maps", ",")
	for utt, want := range map[string]string{
		"NeoVim, can you open it?":         "NeoVim",
		"Neo Vim, can you open it?":        "Neo Vim",
		"Can you open Steam?":              "Steam",
		"Open up the omawrite app please":  "omawrite",
		"Launch Google Maps.":              "Google Maps",
		"Hey, can you open it?":            "", // too short to be anything but a word
		"Mom, can you open the door?":      "",
		"Can you open the oven?":           "",
		"I think Firefox is slow today":    "", // a mention, not a request or an address
		"Steam is down, isn't it?":         "",
		"what's the weather like tomorrow": "",
	} {
		if got := spokenAppName(utt, names); got != want {
			t.Errorf("%q -> %q, want %q", utt, got, want)
		}
	}
}

// The real failure: a follow-up "NeoVim, can you open it?" after "Can you open the oven?" was
// dropped as not for me. On a desktop node that sent its app names, the user message now says
// NeoVim is an app; on any other node, or without names, nothing is added.
func TestDesktopAppHintOnTheTurn(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_8B)
	tools := `[{"type":"function","function":{"name":"desktop_open","description":"Open an app.","parameters":{"type":"object","properties":{"target":{"type":"string"}},"required":["target"]}}}]`
	ve.do("POST", "/api/v0/conversation/start", `{"conversation_id":"c1","node_context":{"timezone":"UTC","stt_vocabulary":"Alacritty, Neovim, Steam"},"client_tools":`+tools+`}`, ve.node.h()).want(200)
	ve.eng.say("Opening Neovim.")
	ve.turn("/api/v0/voice/command", "c1", "NeoVim, can you open it?", map[string]any{"turn_source": "wake", "wake_confidence": 0.9}).want(200)
	if u := lastUserOf(ve); !strings.Contains(u, `[desktop: "NeoVim" is an app on this computer, not a person;`) {
		t.Fatalf("no hint: %q", u)
	}
	if strings.Contains(lastUserOf(ve), "Alacritty") {
		t.Fatal("the catalogue reached the LLM")
	}

	ve.do("POST", "/api/v0/conversation/start", `{"conversation_id":"c2","node_context":{"timezone":"UTC"},"client_tools":`+tools+`}`, ve.node.h()).want(200)
	ve.eng.say("Opening Neovim.")
	ve.turn("/api/v0/voice/command", "c2", "NeoVim, can you open it?", map[string]any{"turn_source": "wake", "wake_confidence": 0.9}).want(200)
	if u := lastUserOf(ve); strings.Contains(u, "[desktop:") {
		t.Fatalf("hint without names: %q", u)
	}
}

func lastUserOf(ve *voiceEnv) string {
	r := ve.eng.last()
	msgs, _ := r["messages"].([]any)
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i].(map[string]any)
		if m["role"] == "user" {
			c, _ := m["content"].(string)
			return c
		}
	}
	return ""
}
