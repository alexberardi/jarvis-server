package prompts

import (
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
)

func TestModeNative(t *testing.T) {
	if !(Mode{ToolCalling: ToolsNative}).Native() || (Mode{ToolCalling: ToolsText}).Native() || (Mode{}).Native() {
		t.Fatal("Native() is true only for native tool calling")
	}
}

func TestModeUserMessageSuffix(t *testing.T) {
	for _, c := range []struct {
		suffix, include bool
		want            string
	}{
		{false, false, ""}, {false, true, ""}, {true, false, "/no_think"}, {true, true, "/think"},
	} {
		if got := (Mode{ThinkingSuffix: c.suffix}).UserMessageSuffix(c.include); got != c.want {
			t.Errorf("suffix=%v include=%v: %q, want %q", c.suffix, c.include, got, c.want)
		}
	}
}

func TestModeResponseFormat(t *testing.T) {
	if (Mode{ToolCalling: ToolsNative}).ResponseFormat() != nil {
		t.Fatal("native: response_format must be nil")
	}
	if got := CompactASCII((Mode{ToolCalling: ToolsText}).ResponseFormat()); got != `{"type":"text"}` {
		t.Fatalf("text: %s", got)
	}
}

func TestModeParseAndSanitize(t *testing.T) {
	got, ok := ParseResponse(`<think>hm</think><tool_call>{"name":"get_time","arguments":{}}</tool_call>`)
	if !ok || got == "" {
		t.Fatalf("tool call: %q %v", got, ok)
	}
	raw := `<message><tool_call>{"name":"get_time","arguments":{}}</tool_call></message>`
	want, wantOK := parse.ParseQwen3(raw, true)
	if got, ok := ParseResponse(raw); got != want || ok != wantOK || !ok {
		t.Fatalf("message unwrap: %q %v, want %q %v", got, ok, want, wantOK)
	}
	if got := SanitizeText("<think>x</think> <message> Hello </message> "); got != "Hello" {
		t.Fatalf("sanitize: %q", got)
	}
}

func TestModeNativeToolsMatchesProviders(t *testing.T) {
	tools := mustTools(t, `[{"type":"function","keywords":["a"],"function":{"name":"play","description":"d","parameters":{"type":"object","properties":{"q":{"type":"string","description":"x","format":"date","_refinable":true}}}}}]`)
	want, _ := Lookup(Qwen3_5_9B)
	if got, exp := CompactASCII(NativeTools(tools)[0]), CompactASCII(want.BuildTools(StripJarvisExtensions(tools))[0]); got != exp {
		t.Fatalf("native tools %s, want %s", got, exp)
	}
}

func TestThinkDelimiters(t *testing.T) {
	if ThinkOpen != "<think>" || ThinkClose != "</think>" {
		t.Fatal(ThinkOpen, ThinkClose)
	}
}
