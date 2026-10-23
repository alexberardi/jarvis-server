package cc

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
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

// Web app plan §3.6: a web chat from the computer whose agent is the chosen node carries
// client {surface: web, same_device: true}; the turn then says the page is in front and to name
// the app. Only then, and only with the typing tool; mobile (no client) is unaffected.
func TestSameDeviceHint(t *testing.T) {
	typeTool := `[{"type":"function","function":{"name":"desktop_type_text","description":"Type text.","parameters":{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}}}]`
	for _, c := range []struct {
		name   string
		tools  string
		client any
		want   bool
	}{
		{"web, same device", typeTool, map[string]any{"surface": "web", "same_device": true}, true},
		{"web, another device", typeTool, map[string]any{"surface": "web", "same_device": false}, false},
		{"no client (mobile)", typeTool, nil, false},
		{"same device but not web", typeTool, map[string]any{"surface": "mobile", "same_device": true}, false},
		{"same device, no typing tool", weatherTool, map[string]any{"surface": "web", "same_device": true}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			ce := newChatEnv(t, prompts.Qwen3_5_9B)
			ce.pub.tools = `{"client_tools": ` + c.tools + `, "available_commands": [], "installed_packages": []}` // the node's own report
			ce.eng.say("Done.")
			body := map[string]any{"message": "type hello"}
			if c.client != nil {
				body["client"] = c.client
			}
			ce.chat("tok-7", body)
			u := lastUserOf(ce.voiceEnv)
			if got := strings.Contains(u, SameDeviceHint); got != c.want {
				t.Fatalf("hint %v, want %v: %q", got, c.want, u)
			}
		})
	}
	ce := newChatEnv(t, prompts.Qwen3_5_9B)
	ce.do("POST", "/api/v0/mobile/chat", map[string]any{"node_id": ce.node.id, "household_id": voiceHH, "message": "hi",
		"client": "web"}, bearer("tok-7")).want(400)
}

func TestTypedAppNamed(t *testing.T) {
	for _, c := range []struct{ utt, text, want, cleaned string }{
		{"type into omawrite that I need to walk the dogs", "I need to walk the dogs", "omawrite", ""},
		{"type buy more coffee filters into omawrite", "buy more coffee filters", "omawrite", ""},
		{"write running ten minutes late in slack", "running ten minutes late", "slack", ""},
		{"Type hello into the terminal.", "hello", "the terminal", ""},
		{"type hi into google chrome please", "hi", "google chrome", ""},
		{"type hi into visual studio code", "hi", "", ""}, // three words after "into"
		{"type into Visual Studio, fix the build", "fix the build", "Visual Studio", ""},
		{"type I'm going to the store", "I'm going to the store", "", ""}, // "to" is not an app
		{"type hello world", "hello world", "", ""},
		{"type I live in Boston", "I live in Boston", "", ""}, // the place is part of the text
		{"type hello in all caps", "HELLO", "", ""},
		{"write hola in spanish", "hola", "", ""},
		{"type it in here", "it", "", ""},
		{"open slack and type hi in it", "hi", "", ""},
		{"I left my keys in the car", "", "", ""}, // no typing verb before "in"
		// The 9B echoing a trailing "into <app>" inside text (#519): the tail comes off the
		// text and the app fills.
		{"type buy more coffee filters into omawrite", "buy more coffee filters into omawrite", "omawrite", "buy more coffee filters"},
		{"type buy more coffee filters into omawrite", "Buy more coffee filters into omawrite.", "omawrite", "Buy more coffee filters"},
		{"Type hello into the terminal.", "hello into the terminal", "the terminal", "hello"},
		{"type omawrite needs an update into omawrite", "omawrite needs an update into omawrite", "omawrite", "omawrite needs an update"},
		{"type buy more coffee filters into omawrite", "omawrite: buy more coffee filters", "", ""}, // not a trailing echo
		{"type into omawrite", "into omawrite", "", ""},                                             // nothing would be left to type
		{"write running ten minutes late in slack", "running ten minutes late in slack", "", ""},    // bare "in" stays text ("I live in Boston")
	} {
		gotApp, gotText := typedAppNamed(c.utt, c.text)
		wantText := c.text
		if c.cleaned != "" {
			wantText = c.cleaned
		}
		if gotApp != c.want || gotText != wantText {
			t.Errorf("%q (text %q) -> %q, %q, want %q, %q", c.utt, c.text, gotApp, gotText, c.want, wantText)
		}
	}
}

func TestFillTypedApp(t *testing.T) {
	utt := "type into omawrite that I need to walk the dogs"
	call := func(name, args string) parse.ToolCall {
		return parse.ToolCall{Function: parse.FunctionCall{Name: name, Arguments: args}}
	}
	for _, c := range []struct {
		utt  string // "" = the default above
		call parse.ToolCall
		want string // "" = unchanged
	}{
		{"", call(desktopTypeTool, `{"text": "I need to walk the dogs"}`), `{"text": "I need to walk the dogs", "app": "omawrite"}`},
		{"", call(desktopTypeTool, `{"text": "I need to walk the dogs", "app": ""}`), `{"text": "I need to walk the dogs", "app": "omawrite"}`},
		{"", call(desktopTypeTool, `{"text": "I need to walk the dogs", "app": "quillpad"}`), ""}, // the model's own app stands
		{"", call("desktop_press_key", `{"key": "enter"}`), ""},
		{"", call(desktopTypeTool, `not json`), ""},
		// The echoed tail (#519): app fills and the echo comes off the text.
		{"type buy more coffee filters into omawrite",
			call(desktopTypeTool, `{"text": "buy more coffee filters into omawrite"}`),
			`{"text": "buy more coffee filters", "app": "omawrite"}`},
	} {
		cu := c.utt
		if cu == "" {
			cu = utt
		}
		got, ok := fillTypedApp(cu, c.call)
		if ok != (c.want != "") || (ok && got != c.want) {
			t.Errorf("%s %s -> %q %v, want %q", c.call.Function.Name, c.call.Function.Arguments, got, ok, c.want)
		}
	}
}

// "type into omawrite that I need to walk the dogs": the 9B called desktop_type_text without
// app 2 times in 3 (#230), and the text went to the window in front. The node gets the app.
func TestTypingKeepsNamedApp(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_5_9B)
	ve.start("m5", desktopTools)
	ve.eng.push(nativeCall("call_t1", "desktop_type_text", `{"text": "I need to walk the dogs"}`))
	r := ve.turn("/api/v0/voice/command/stream", "m5", "type into omawrite that I need to walk the dogs", nil).want(202).json()
	calls, _ := r["tool_calls"].([]any)
	if r["stop_reason"] != "tool_calls" || len(calls) != 1 {
		t.Fatalf("%v", r)
	}
	if s := jsonOf(calls[0]); !strings.Contains(s, `\"app\": \"omawrite\"`) {
		t.Fatalf("call %s", s)
	}
}

// On the continue path the fill reads the voice command, not the composed user message (its
// hints and /no_think suffix follow the app name).
func TestTypingKeepsNamedAppOnContinue(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_5_9B)
	ve.start("m6", desktopTools)
	ve.eng.push(nativeCall("call_f1", "desktop_list_windows", `{}`))
	ve.turn("/api/v0/voice/command/stream", "m6", "type buy more coffee filters into omawrite", nil).want(202)
	ve.eng.push(nativeCall("call_t1", "desktop_type_text", `{"text": "buy more coffee filters"}`))
	b := ve.do("POST", "/api/v0/voice/command/continue", map[string]any{"conversation_id": "m6",
		"tool_results": []any{map[string]any{"tool_call_id": "call_f1", "output": map[string]any{"success": true}}}},
		ve.node.h()).want(200).json()
	calls, _ := b["tool_calls"].([]any)
	if b["stop_reason"] != "tool_calls" || len(calls) != 1 {
		t.Fatalf("continue %v", b)
	}
	var args map[string]any
	fn, _ := calls[0].(map[string]any)["function"].(map[string]any)
	if s, _ := fn["arguments"].(string); json.Unmarshal([]byte(s), &args) != nil || args["app"] != "omawrite" {
		t.Fatalf("call %s", jsonOf(calls[0]))
	}
}
