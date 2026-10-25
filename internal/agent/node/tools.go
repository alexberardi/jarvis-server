package node

import (
	"context"
	"encoding/json"
	"fmt"
)

// ToolDef is one client tool as CC wants it: the OpenAI-style client_tools entry and the
// available_commands entry (CommandDefinition: command_name, description, parameters required).
// Built-in Go tools render both from a Spec; a sidecar's Python commands arrive pre-rendered by
// jarvis-command-sdk.
type ToolDef struct {
	Name       string
	ClientTool map[string]any
	Command    map[string]any
}

// Request is the context CC sends with a tool_call.
type Request struct {
	ToolCallID   string
	UserID       *int64
	VoiceCommand string
}

// Toolset is a source of tools. Available is computed per call (capabilities change at
// runtime); Call returns the output object posted back to CC, which by convention carries
// "success" and, on failure, "error".
type Toolset interface {
	Available(ctx context.Context) []ToolDef
	Call(ctx context.Context, name string, args map[string]any, req Request) (output map[string]any, handled bool)
}

// Multi chains toolsets; the first that knows a name handles it.
type Multi []Toolset

// ChoiceAnswerer is a Toolset that put a numbered list on the island (an ambiguous window name)
// and can act on the user's answer itself: said is what was heard in the choice window; ok is
// false when it isn't an answer to the list (then the turn goes to command-center as usual).
type ChoiceAnswerer interface {
	AnswerChoice(ctx context.Context, said string) (out map[string]any, ok bool)
}

// AnswerChoice asks each Toolset that can answer a choice.
func (m Multi) AnswerChoice(ctx context.Context, said string) (map[string]any, bool) {
	for _, t := range m {
		if a, ok := t.(ChoiceAnswerer); ok {
			if out, ok := a.AnswerChoice(ctx, said); ok {
				return out, true
			}
		}
	}
	return nil, false
}

func (m Multi) Available(ctx context.Context) []ToolDef {
	var out []ToolDef
	seen := map[string]bool{}
	for _, t := range m {
		for _, d := range t.Available(ctx) {
			if !seen[d.Name] {
				seen[d.Name] = true
				out = append(out, d)
			}
		}
	}
	return out
}

func (m Multi) Call(ctx context.Context, name string, args map[string]any, req Request) (map[string]any, bool) {
	for _, t := range m {
		if out, ok := t.Call(ctx, name, args, req); ok {
			return out, true
		}
	}
	return nil, false
}

// --- rendering built-in tools ---

// Param is one tool parameter, in jarvis-command-sdk's vocabulary.
type Param struct {
	Name        string
	Type        string // "string", "int", "bool", ...
	Description string
	Required    bool
	Enum        []string
}

// Example is one prompt example.
type Example struct {
	VoiceCommand string
	Params       map[string]any
	Primary      bool
}

// Spec describes a built-in tool.
type Spec struct {
	Name        string
	Description string
	Params      []Param
	Keywords    []string
	Examples    []Example
	Risky       bool
	// RunWithoutConfirmation: may run in the background without a tap when no household
	// rule says otherwise (A7). Ignored by the server for risky tools.
	RunWithoutConfirmation bool
}

var jsonType = map[string]string{"string": "string", "int": "integer", "float": "number", "bool": "boolean"}

// Render builds the ToolDef the way jarvis-command-sdk's to_openai_tool_schema and
// get_command_schema do, so CC can't tell a Go tool from a Python one.
func (s Spec) Render() ToolDef {
	props := map[string]any{}
	required := []string{}
	params := []any{}
	for _, p := range s.Params {
		ps := map[string]any{"type": jsonType[p.Type]}
		if ps["type"] == "" {
			ps["type"] = "string"
		}
		if p.Description != "" {
			ps["description"] = p.Description
		}
		var enum any
		if len(p.Enum) > 0 {
			ps["enum"] = p.Enum
			enum = p.Enum
		}
		props[p.Name] = ps
		if p.Required {
			required = append(required, p.Name)
		}
		params = append(params, map[string]any{"name": p.Name, "type": p.Type, "description": p.Description,
			"required": p.Required, "default_value": nil, "enum_values": enum, "options_source": nil})
	}
	examples := []any{}
	for _, e := range s.Examples {
		examples = append(examples, map[string]any{"voice_command": e.VoiceCommand, "expected_parameters": e.Params, "is_primary": e.Primary})
	}
	keywords := s.Keywords
	if keywords == nil {
		keywords = []string{}
	}
	return ToolDef{
		Name: s.Name,
		ClientTool: map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": s.Name, "description": s.Description,
				"parameters": map[string]any{"type": "object", "properties": props, "required": required},
			},
			"allow_direct_answer": false, "is_risky": s.Risky, "run_without_confirmation": s.RunWithoutConfirmation,
			"keywords": keywords, "examples": examples,
		},
		Command: map[string]any{
			"command_name": s.Name, "description": s.Description, "allow_direct_answer": false,
			"is_risky": s.Risky, "run_without_confirmation": s.RunWithoutConfirmation,
			"examples": examples, "keywords": keywords, "parameters": params,
		},
	}
}

// DecodeArgs accepts CC's arguments as an object or a JSON-encoded string (mobile chat sends
// the model's raw argument string). Anything else is an empty map.
func DecodeArgs(v any) map[string]any {
	switch a := v.(type) {
	case map[string]any:
		return a
	case string:
		var m map[string]any
		if json.Unmarshal([]byte(a), &m) == nil && m != nil {
			return m
		}
	}
	return map[string]any{}
}

// Fail is the conventional failure output.
func Fail(format string, a ...any) map[string]any {
	return map[string]any{"success": false, "error": fmt.Sprintf(format, a...)}
}

// SignalsKey is where a Toolset puts node-local Signals in a call's output. They steer the
// node itself and are never sent to command-center: every consumer of a Call output (the
// MQTT dispatcher, the voice pipeline) takes them out with TakeSignals before posting it.
const SignalsKey = "_jarvis_node_signals"

// IslandKey and IslandOnlyKey mark a result whose outcome the node's island showed (a desktop
// command, DESKTOP-COMMANDS.md Q9): IslandKey is the text shown, IslandOnlyKey true tells the
// voice loop and command-center that nothing is to be spoken for it. Unlike the signals they go
// to command-center, which ends the turn without a formatting call.
const (
	IslandKey     = "island"
	IslandOnlyKey = "island_only"
	// ChoiceKey true: the island shows a numbered list to pick from; the node listens for the
	// answer (choice_listen_seconds) and command-center keeps the exchange open.
	ChoiceKey = "choice"
)

// Signals are the jarvis-command-sdk CommandResponse fields only the node acts on
// (N:utils/command_execution_service.py ToolExecutionResult). Go's built-in tools set none.
type Signals struct {
	// WaitForInput: the command expects the user to say more. A voice turn sends the results,
	// then speaks what comes back without running further tool calls, preferring the
	// command's own "message" (SDK default: true).
	WaitForInput bool
	// ClearHistory: the exchange is over; a follow-up window closes after this reply.
	ClearHistory bool
	// OnResponseComplete is the command's deferred step (a media command starting playback),
	// to run once the spoken reply and the follow-up window are over.
	OnResponseComplete func() error
}

// TakeSignals removes the Signals from out (if any) and returns them.
func TakeSignals(out map[string]any) Signals {
	s, _ := out[SignalsKey].(Signals)
	delete(out, SignalsKey)
	return s
}
