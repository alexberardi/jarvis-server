// Prompt text templates: the variable registry, Validate and Render. The contract (the
// variables, the {{name}} syntax, the trim directive, the size limit) is docs/cc/prompt-contract.md.
package prompts

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
)

// Variable kinds: an inline value is written as is, a section is preceded by a blank line and
// vanishes when empty, a directive acts on the rendered text.
const (
	KindInline    = "inline"
	KindSection   = "section"
	KindDirective = "directive"
)

// Variable is one {{name}} a prompt may use.
type Variable struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Description string `json:"description"`
}

// Variables is the registry, in the order the contract lists them.
var Variables = []Variable{
	{"room", KindInline, "The node's room (unknown when unset)."},
	{"voice_mode", KindInline, "The node's voice style (brief when unset)."},
	{"persona", KindSection, "The household persona, fenced in <personality>; empty when there is none."},
	{"persona_reminder", KindSection, "The end-of-prompt restatement of the persona's voice; empty when there is none."},
	{"tools", KindSection, "Text tool calling: the tool schemas and the <tool_call> format. Empty with native tool calling."},
	{"tools_compact", KindSection, "As tools, without parameter descriptions."},
	{"date_keys", KindSection, "The DT_KEYS date vocabulary and how to use it."},
	{"tool_guidance", KindSection, "Hints the offered tools add to the prompt."},
	{"direct_answers", KindSection, "Which commands must call a tool and which may be answered directly."},
	{"agents", KindSection, "The node's smart-home devices by room, or a summary over 20 devices."},
	{"rooms", KindSection, "The household's room hierarchy."},
	{"trim", KindDirective, "Removes the whitespace right before it."},
}

// MaxPromptBytes caps a prompt's text.
const MaxPromptBytes = 32 << 10

// TemplateError is the first problem Validate found; Line is 1-based, 0 for the whole text.
type TemplateError struct {
	Line     int
	Variable string
	Msg      string
}

func (e *TemplateError) Error() string {
	if e.Line > 0 {
		return "line " + strconv.Itoa(e.Line) + ": " + e.Msg
	}
	return e.Msg
}

var varNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// variableKind returns the registered kind of name, or "" when it is not a variable.
func variableKind(name string) string {
	if !varNameRe.MatchString(name) {
		return ""
	}
	for _, v := range Variables {
		if v.Name == name {
			return v.Kind
		}
	}
	return ""
}

// nextToken finds the next {{ at or after i. It returns the {{ index (-1 when none), the index
// just past the closing }} and the trimmed name; end is -1 when the {{ is unclosed on its line.
func nextToken(text string, i int) (start, end int, name string) {
	o := strings.Index(text[i:], "{{")
	if o < 0 {
		return -1, -1, ""
	}
	start = i + o
	rest := text[start+2:]
	c := strings.Index(rest, "}}")
	if c < 0 || strings.Contains(rest[:c], "\n") {
		return start, -1, ""
	}
	return start, start + 2 + c + 2, strings.TrimSpace(rest[:c])
}

// Validate reports the first problem in a prompt's text, or nil.
func Validate(text string) error {
	if !utf8.ValidString(text) {
		return &TemplateError{Msg: "the prompt is not valid UTF-8"}
	}
	if len(text) > MaxPromptBytes {
		return &TemplateError{Msg: "the prompt is longer than 32 KB"}
	}
	if strings.TrimSpace(text) == "" {
		return &TemplateError{Msg: "the prompt is empty"}
	}
	for i := 0; ; {
		start, end, name := nextToken(text, i)
		if start < 0 {
			return nil
		}
		line := strings.Count(text[:start], "\n") + 1
		if end < 0 {
			return &TemplateError{Line: line, Msg: "unclosed {{"}
		}
		if variableKind(name) == "" {
			return &TemplateError{Line: line, Variable: name, Msg: "unknown variable {{" + name + "}}"}
		}
		i = end
	}
}

// Render fills a prompt's variables from values. It never fails: an unknown variable renders
// as nothing, an unclosed {{ is copied as is, and inserted values are never scanned again.
func Render(text string, values map[string]string) string {
	var b strings.Builder
	for i := 0; i < len(text); {
		start, end, name := nextToken(text, i)
		if start < 0 {
			b.WriteString(text[i:])
			break
		}
		b.WriteString(text[i:start])
		if end < 0 {
			b.WriteString("{{")
			i = start + 2
			continue
		}
		switch variableKind(name) {
		case KindDirective:
			if name == "trim" {
				s := parse.PyRStrip(b.String())
				b.Reset()
				b.WriteString(s)
			}
		case KindInline:
			b.WriteString(values[name])
		case KindSection:
			if v := values[name]; v != "" {
				b.WriteString("\n\n" + v)
			}
		}
		i = end
	}
	return b.String()
}
