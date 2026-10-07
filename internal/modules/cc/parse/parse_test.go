package parse

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

func loadFixture(t *testing.T, name string) any {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "fixtures", "golden", "prompts", name))
	if err != nil {
		t.Fatal(err)
	}
	v, err := pyjson.Loads(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func og(v any, k string) any {
	x, _ := v.(*pyjson.Object).Get(k)
	return x
}

var callIDRE = regexp.MustCompile(`^call_[0-9a-f]{12}$`)

// ToolCallParser.parse_response over _toolparse.json (ids masked in the fixture).
func TestToolCallParserGolden(t *testing.T) {
	rows := loadFixture(t, "_toolparse.json").([]any)
	for _, row := range rows {
		raw := og(row, "raw").(string)
		got := ParseToolCalls(raw)
		if got.FinishReason != og(row, "finish_reason").(string) {
			t.Errorf("%q: finish %q want %q", raw, got.FinishReason, og(row, "finish_reason"))
		}
		if want := pyStrValue(og(row, "message")); got.Message != want {
			t.Errorf("%q: message %q want %q", raw, got.Message, want)
		}
		want := og(row, "tool_calls").([]any)
		if len(got.ToolCalls) != len(want) {
			t.Errorf("%q: %d calls, want %d", raw, len(got.ToolCalls), len(want))
			continue
		}
		for i, w := range want {
			c := got.ToolCalls[i]
			if !callIDRE.MatchString(c.ID) || c.Type != "function" {
				t.Errorf("%q: id/type %q %q", raw, c.ID, c.Type)
			}
			fn := og(w, "function")
			if c.Function.Name != pyjson.Str(og(fn, "name")) || c.Function.Arguments != og(fn, "arguments").(string) {
				t.Errorf("%q call %d: %+v want %s", raw, i, c.Function, pyjson.Dumps(fn, true))
			}
			fm, _ := og(w, "failure_message").(string)
			if c.FailureMessage != fm {
				t.Errorf("%q call %d: failure_message %q want %q", raw, i, c.FailureMessage, fm)
			}
		}
	}
	t.Logf("%d ToolCallParser rows", len(rows))
}

func TestSentinels(t *testing.T) {
	for _, s := range []string{"<not_for_me/>", "<NOT-FOR-ME>", "< not for  me / >", "ok <not_for_me/>.", "<Not_For_Me/>"} {
		if !ContainsNotForMe(s) {
			t.Errorf("ContainsNotForMe(%q) = false", s)
		}
	}
	for _, s := range []string{"", "this is not for me", "<notforme/>", "<not_for_you/>"} {
		if ContainsNotForMe(s) {
			t.Errorf("ContainsNotForMe(%q) = true", s)
		}
	}
	for _, s := range []string{"<exchange_complete/>", "Done <ExchangeComplete>", "<exchange - complete />"} {
		if !ContainsExchangeComplete(s) {
			t.Errorf("ContainsExchangeComplete(%q) = false", s)
		}
	}
	if got := StripExchangeComplete("Timer set. <exchange_complete/> "); got != "Timer set." {
		t.Errorf("strip = %q", got)
	}
	if got := StripNotForMe(" <not_for_me/> "); got != "" {
		t.Errorf("strip = %q", got)
	}
	// Reasoning never counts: closed or unclosed think blocks, any case.
	if SentinelNotForMe("<think>maybe <not_for_me/>?</think>Sure, the lights are on.") {
		t.Error("sentinel inside think matched")
	}
	if SentinelNotForMe("Answer.<THINK>then <not_for_me/> truncated") {
		t.Error("sentinel inside unclosed think matched")
	}
	if !SentinelNotForMe("", "<think>x</think><not_for_me/>") {
		t.Error("sentinel outside think missed")
	}
	if !SentinelExchangeComplete("Bye! <exchange_complete/>") || SentinelExchangeComplete("<think><exchange_complete/></think>Hi") {
		t.Error("exchange_complete outside think")
	}
}

func TestNormalizeDateKeyAndStrings(t *testing.T) {
	cases := map[string]string{
		"Tomorrow": "tomorrow", " next week ": "next_week", "TOMORROW:MORNING": "tomorrow_morning",
		"in 2 hours": "in_2_hours", "this_weekend": "this_weekend", "a b\tc": "a_b_c", "İX": "i̇x",
	}
	for in, want := range cases {
		if got := NormalizeDateKey(in); got != want {
			t.Errorf("NormalizeDateKey(%q) = %q want %q", in, got, want)
		}
	}
	if PyStrip("\x1c a 　") != "a" {
		t.Error("PyStrip must strip \\x1c and U+3000")
	}
}

func TestParseQwenEdges(t *testing.T) {
	// Valid JSON that is not an object inside <tool_call> is skipped (Python raised).
	got, ok := ParseQwen25("<tool_call>[1]</tool_call>")
	if !ok || got != `{"message": "<tool_call>[1]</tool_call>", "tool_calls": [], "error": null}` {
		t.Errorf("non-object tool_call body: %q", got)
	}
	got, ok = ParseQwen25(`<tool_call>{"name":"a","arguments":{"resolved_datetimes":"today","x":"é"}}</tool_call>`)
	want := `{"message": "", "tool_calls": [{"name": "a", "arguments": {"resolved_datetimes": ["today"], "x": "\u00e9"}}], "error": null}`
	if !ok || got != want {
		t.Errorf("got %q", got)
	}
}
