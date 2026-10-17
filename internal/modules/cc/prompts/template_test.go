package prompts

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	cases := []struct {
		text     string
		line     int
		variable string
		msg      string // "" means valid
	}{
		{"", 0, "", "the prompt is empty"},
		{"  \n", 0, "", "the prompt is empty"},
		{"a {{room}} b", 0, "", ""},
		{"a {{ room }} b", 0, "", ""},
		{"a { b } c", 0, "", ""},
		{"x\n{{nope}}", 2, "nope", "unknown variable {{nope}}"},
		{"{{Room}}", 1, "Room", "unknown variable {{Room}}"},
		{"a {{room", 1, "", "unclosed {{"},
		{"a {{room\n}}", 1, "", "unclosed {{"},
		{strings.Repeat("a", MaxPromptBytes+1), 0, "", "the prompt is longer than 32 KB"},
		{"\xff", 0, "", "the prompt is not valid UTF-8"},
		{"{{trim}}x", 0, "", ""},
	}
	for i, c := range cases {
		err := Validate(c.text)
		if c.msg == "" {
			if err != nil {
				t.Errorf("%d: Validate(%q) = %v, want nil", i, c.text, err)
			}
			continue
		}
		var te *TemplateError
		if !errors.As(err, &te) {
			t.Errorf("%d: Validate(%q) = %v, want a TemplateError", i, c.text, err)
			continue
		}
		if te.Line != c.line || te.Variable != c.variable || te.Msg != c.msg {
			t.Errorf("%d: Validate(%q) = %+v, want line %d variable %q msg %q", i, c.text, *te, c.line, c.variable, c.msg)
		}
	}
}

func TestTemplateErrorString(t *testing.T) {
	if got := (&TemplateError{Line: 3, Msg: "unclosed {{"}).Error(); got != "line 3: unclosed {{" {
		t.Errorf("got %q", got)
	}
	if got := (&TemplateError{Msg: "the prompt is empty"}).Error(); got != "the prompt is empty" {
		t.Errorf("got %q", got)
	}
}

func TestRender(t *testing.T) {
	cases := []struct {
		text   string
		values map[string]string
		want   string
	}{
		{"room={{room}}", map[string]string{"room": "kitchen"}, "room=kitchen"},
		{"A{{persona}}B", map[string]string{"persona": ""}, "AB"},
		{"A{{persona}}B", map[string]string{"persona": "P"}, "A\n\nPB"},
		{"A\n\n{{trim}}B", nil, "AB"},
		{"A{{agents}}\n{{trim}}\n\nZ", map[string]string{"agents": ""}, "A\n\nZ"},
		{"A{{agents}}\n{{trim}}\n\nZ", map[string]string{"agents": "G"}, "A\n\nG\n\nZ"},
		// Inserted values are never scanned again.
		{"A{{persona}}", map[string]string{"persona": "{{room}}", "room": "kitchen"}, "A\n\n{{room}}"},
		{"x{{nope}}y", nil, "xy"},
		{"x {{room", map[string]string{"room": "kitchen"}, "x {{room"},
		{"a {{room\n{{room}}", map[string]string{"room": "kitchen"}, "a {{room\nkitchen"},
		{"a { b", nil, "a { b"},
	}
	for i, c := range cases {
		if got := Render(c.text, c.values); got != c.want {
			t.Errorf("%d: Render(%q) = %q, want %q", i, c.text, got, c.want)
		}
	}
}

func TestVariablesUniqueAndDocumented(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	doc, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "docs", "cc", "prompt-contract.md"))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, v := range Variables {
		if seen[v.Name] {
			t.Errorf("%s: duplicate name", v.Name)
		}
		seen[v.Name] = true
		switch v.Kind {
		case KindInline, KindSection, KindDirective:
		default:
			t.Errorf("%s: kind %q", v.Name, v.Kind)
		}
		if !strings.Contains(string(doc), "`"+v.Name+"`") {
			t.Errorf("%s: not in docs/cc/prompt-contract.md", v.Name)
		}
	}
}
