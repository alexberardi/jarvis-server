package cc

import (
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
)

// A page read is sized to the model that will read it: a cloud model gets the whole article (up
// to what the extension returns), a local one a share of its window, an unknown one the agent's
// default. Found live 2026-11-13: Sonnet summarized only the top of a Wikipedia article (the
// agent's 4000-character default) and said so.
func TestReadPageCharsFor(t *testing.T) {
	cases := []struct {
		name string
		ep   llm.Endpoint
		ok   bool
		want int
	}{
		{"no endpoint", llm.Endpoint{}, false, 0},
		{"cloud", llm.Endpoint{Remote: true}, true, readPageMaxChars},
		{"local 16k", llm.Endpoint{ContextLength: 16384}, true, 16384 * 4 / 5},
		{"local small", llm.Endpoint{ContextLength: 2048}, true, readPageMinChars},
		{"local huge", llm.Endpoint{ContextLength: 262144}, true, readPageMaxChars},
		{"local unknown", llm.Endpoint{}, true, 0},
	}
	for _, c := range cases {
		if got := readPageCharsFor(c.ep, c.ok); got != c.want {
			t.Errorf("%s: %d, want %d", c.name, got, c.want)
		}
	}
}

func TestFillReadPageChars(t *testing.T) {
	call := func(name, args string) parse.ToolCall {
		var c parse.ToolCall
		c.Function.Name, c.Function.Arguments = name, args
		return c
	}
	out, ok := fillReadPageChars(call(toolReadPage, `{}`), 50000)
	if !ok || out != `{"max_chars": 50000}` {
		t.Fatalf("not filled: %v %s", ok, out)
	}
	out, ok = fillReadPageChars(call(toolReadPage, `{"tab_id":"chrome:7"}`), 50000)
	if !ok || out != `{"tab_id": "chrome:7", "max_chars": 50000}` {
		t.Fatalf("tab kept? %v %s", ok, out)
	}
	// A smaller size of the model's own is raised: 30000 cost a second read.
	for _, args := range []string{`{"max_chars": 30000}`, `{"max_chars": "30000"}`, `{"max_chars": 1200.0}`} {
		if out, ok := fillReadPageChars(call(toolReadPage, args), 50000); !ok || out != `{"max_chars": 50000}` {
			t.Errorf("%s: %v %s", args, ok, out)
		}
	}
	for _, c := range []struct {
		name string
		call parse.ToolCall
		n    int
	}{
		{"the model's larger size", call(toolReadPage, `{"max_chars": 80000}`), 50000},
		{"the same size", call(toolReadPage, `{"max_chars": 50000}`), 50000},
		{"another tool", call("desktop_type_text", `{"text":"x"}`), 50000},
		{"no size known", call(toolReadPage, `{}`), 0},
		{"not an object", call(toolReadPage, `nope`), 50000},
	} {
		if _, ok := fillReadPageChars(c.call, c.n); ok {
			t.Errorf("%s: changed", c.name)
		}
	}
}
