package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// A Script is what the model says: rules tried in order against the conversation's last
// message, and the reply when none matches. Files live with the tests (e2e/llm/*.json); the
// format is in docs/decisions/2026-10-17-scripted-llm.md.
type Script struct {
	Name     string `json:"-"`
	Fallback string `json:"fallback,omitempty"`
	Rules    []Rule `json:"rules"`
}

// A Rule answers one kind of turn. Without AfterTool it applies when the last message is the
// user's and Match (a Go regexp, unanchored) matches its text. With AfterTool it applies only
// when the last message is the result of that tool, and Match, when set, must match the
// result's text: that is how a round trip ends with a reply instead of calling the tool again.
type Rule struct {
	Match     string     `json:"match,omitempty"`
	AfterTool string     `json:"after_tool,omitempty"`
	Reply     string     `json:"reply,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`

	re *regexp.Regexp
}

// ToolCall is one call the rule makes; Arguments is any JSON value (an object, normally).
type ToolCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// DefaultFallback is the reply when no rule matches and the script names none.
const DefaultFallback = "scripted: no rule matched"

// ParseScript reads and checks a script: every regexp compiles, every rule says something.
func ParseScript(name string, b []byte) (*Script, error) {
	var s Script
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	s.Name = name
	if s.Fallback == "" {
		s.Fallback = DefaultFallback
	}
	for i := range s.Rules {
		r := &s.Rules[i]
		if r.Match == "" && r.AfterTool == "" {
			return nil, fmt.Errorf("%s: rule %d: needs match or after_tool", name, i)
		}
		if r.Reply == "" && len(r.ToolCalls) == 0 {
			return nil, fmt.Errorf("%s: rule %d: needs reply or tool_calls", name, i)
		}
		for j, c := range r.ToolCalls {
			if c.Name == "" {
				return nil, fmt.Errorf("%s: rule %d: tool call %d has no name", name, i, j)
			}
			if len(c.Arguments) > 0 && !json.Valid(c.Arguments) {
				return nil, fmt.Errorf("%s: rule %d: tool call %d: arguments are not JSON", name, i, j)
			}
		}
		if r.Match != "" {
			re, err := regexp.Compile(r.Match)
			if err != nil {
				return nil, fmt.Errorf("%s: rule %d: %w", name, i, err)
			}
			r.re = re
		}
	}
	return &s, nil
}

// LoadScript reads a script file.
func LoadScript(path string) (*Script, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseScript(path, b)
}

// Turn is what a rule is matched against: the last message of the request.
type Turn struct {
	Role string // "user" or "tool" (anything else counts as user)
	Text string // the message's text
	Tool string // for a tool result, the name of the tool it answers ("" when unknown)
}

// Find returns the first rule for the turn, or nil.
func (s *Script) Find(t Turn) *Rule {
	for i := range s.Rules {
		r := &s.Rules[i]
		if t.Role == "tool" {
			if r.AfterTool == "" || r.AfterTool != t.Tool {
				continue
			}
		} else if r.AfterTool != "" {
			continue
		}
		if r.re != nil && !r.re.MatchString(t.Text) {
			continue
		}
		return r
	}
	return nil
}

// wireMessage is the part of an OpenAI chat message the script looks at.
type wireMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCallID string          `json:"tool_call_id"`
	Name       string          `json:"name"`
	ToolCalls  []struct {
		ID       string `json:"id"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	} `json:"tool_calls"`
}

// text is the message's text: a string, or the text parts of a content array.
func (m wireMessage) text() string {
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(m.Content, &parts) != nil {
		return ""
	}
	var out []string
	for _, p := range parts {
		if p.Type == "text" {
			out = append(out, p.Text)
		}
	}
	return strings.Join(out, "\n")
}

// lastTurn picks the message rules are matched against: the last message that is not a
// system one. cc adds system notes after a tool result ([TOOLS_FAILED], [TOOL_DEDUPE]) and
// context after the user's words, so those never hide the turn. A tool result is named by its
// tool_call_id through the assistant message that made the call. ok is false when there is no
// user or tool message at all (cc's prompt-cache warm-up: system messages and max_tokens 1).
func lastTurn(msgs []wireMessage) (t Turn, ok bool) {
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		switch m.Role {
		case "tool":
			t := Turn{Role: "tool", Text: m.text(), Tool: m.Name}
			for j := i - 1; j >= 0 && t.Tool == ""; j-- {
				for _, c := range msgs[j].ToolCalls {
					if c.ID == m.ToolCallID {
						t.Tool = c.Function.Name
					}
				}
			}
			return t, true
		case "user":
			return Turn{Role: "user", Text: m.text()}, true
		}
	}
	return Turn{}, false
}
