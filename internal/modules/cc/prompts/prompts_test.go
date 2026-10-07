package prompts

import (
	"reflect"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

func TestServerToolGates(t *testing.T) {
	all := []string{"deep_research", "forget", "get_ha_entities", "identify_speaker", "list_scheduled_errands",
		"make_phone_call", "quick_search", "recall", "remember", "request_validation", "run_errand", "schedule_errand"}
	base := []string{"answer_question", "make_phone_call", "run_errand", "schedule_errand", "list_scheduled_errands"}
	cases := []struct {
		g          ToolGates
		text       []string
		nativeDrop []string
	}{
		{ToolGates{}, base, []string{"deep_research", "quick_search", "remember", "forget", "recall"}},
		{ToolGates{WebSearch: true}, append(append([]string{}, base...), "deep_research", "quick_search"), []string{"remember", "forget", "recall"}},
		// Memory without a known speaker offers nothing (D21).
		{ToolGates{MemoryEnabled: true, RecallEnabled: true}, base, []string{"deep_research", "quick_search", "remember", "forget", "recall"}},
		{ToolGates{SpeakerKnown: true, MemoryEnabled: true}, append(append([]string{}, base...), "remember", "forget"), []string{"deep_research", "quick_search", "recall"}},
		{ToolGates{WebSearch: true, SpeakerKnown: true, MemoryEnabled: true, RecallEnabled: true},
			append(append([]string{}, base...), "deep_research", "quick_search", "remember", "forget", "recall"), nil},
		// Recall needs memory too.
		{ToolGates{SpeakerKnown: true, RecallEnabled: true}, base, []string{"deep_research", "quick_search", "remember", "forget", "recall"}},
	}
	for i, c := range cases {
		if got := ServerToolNames(false, all, c.g); !reflect.DeepEqual(got, c.text) {
			t.Errorf("%d text = %v, want %v", i, got, c.text)
		}
		drop := map[string]bool{}
		for _, d := range c.nativeDrop {
			drop[d] = true
		}
		var want []string
		for _, n := range all {
			if !drop[n] {
				want = append(want, n)
			}
		}
		if got := ServerToolNames(true, all, c.g); !reflect.DeepEqual(got, want) {
			t.Errorf("%d native = %v, want %v", i, got, want)
		}
	}
}

func mustTools(t *testing.T, js string) []Tool {
	t.Helper()
	v, err := pyjson.Loads(js)
	if err != nil {
		t.Fatal(err)
	}
	return toTools(v)
}

// D8 (02.Q7): stripping never mutates the cached schemas, so text-path refinement still sees
// _refinable after the native transform ran.
func TestStripJarvisExtensionsDoesNotMutate(t *testing.T) {
	tools := mustTools(t, `[{"type":"function","keywords":["a"],"is_server_tool":true,"function":{"name":"play","parameters":{"type":"object","properties":{"speaker":{"type":"string","_refinable":true},"q":{"type":"string"}},"required":["q","speaker"]}}}]`)
	before := CompactASCII(tools[0])
	p, _ := Lookup(Qwen3_5_9B)
	got := NativeTools(p, tools)
	if CompactASCII(tools[0]) != before {
		t.Fatal("input tool mutated")
	}
	want := `{"type":"function","function":{"name":"play","parameters":{"type":"object","properties":{"speaker":{"type":"string"},"q":{"type":"string"}},"required":["q","speaker"]}}}`
	if CompactASCII(got[0]) != want {
		t.Errorf("native tool = %s", CompactASCII(got[0]))
	}
	// The text path removes refinable params (and from required) without touching the input.
	q8, _ := Lookup(Qwen3_8B)
	prompt := q8.BuildSystemPrompt(Context{Room: "k", VoiceMode: "brief"}, tools, nil)
	if !strings.Contains(prompt, `{"type":"function","function":{"name":"play","parameters":{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}}}`) {
		t.Errorf("text tools block wrong:\n%s", prompt)
	}
	if CompactASCII(tools[0]) != before {
		t.Fatal("input tool mutated by the text builder")
	}
}

func TestCompactASCII(t *testing.T) {
	v, _ := pyjson.Loads(`{"d":"Weather é <b> & 😀","n":[1,2.5,1e100,true,null],"k":{}}`)
	want := `{"d":"Weather \` + `u00e9 <b> & \` + `ud83d\` + `ude00","n":[1,2.5,1e+100,true,null],"k":{}}`
	if got := CompactASCII(v); got != want {
		t.Errorf("got %s", got)
	}
}

func TestWrapStashAndSwap(t *testing.T) {
	prompt, stash := Wrap("BASE\n\n\n", "", Characterization{Enabled: true, Text: "  likes tea  "})
	want := "BASE\n\n" + NotForMeInstruction + "\n\n" + ExchangeCompleteInstruction + "\n"
	if stash != want {
		t.Fatalf("stash = %q", stash)
	}
	if prompt != want+"\n"+CharacterizationSection("likes tea") {
		t.Errorf("prompt = %q", prompt)
	}
	if WithCharacterization(stash, "") != stash {
		t.Error("empty characterization must leave the base")
	}
	p2, _ := Wrap("BASE", "Be dry.", Characterization{})
	if !strings.HasSuffix(p2, "\n\n"+PersonalityReminder("Be dry.")+"\n") {
		t.Errorf("reminder missing: %q", p2[len(p2)-80:])
	}
}

func TestUserMessage(t *testing.T) {
	got := UserMessage("turn on the lights", []string{"[direction hint: x]", "", "[turn context: fresh wake]"}, "/no_think")
	want := "turn on the lights\n\n[direction hint: x]\n\n[turn context: fresh wake]\n/no_think"
	if got != want {
		t.Errorf("got %q", got)
	}
	if UserMessage("hi", nil, "") != "hi" {
		t.Error("bare utterance")
	}
}
