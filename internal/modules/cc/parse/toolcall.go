package parse

import (
	"crypto/rand"
	"encoding/hex"
	"regexp"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// Finish reasons ToolCallParser reports.
const (
	FinishStop      = "stop"
	FinishToolCalls = "tool_calls"
)

// FunctionCall is a tool call's function: Arguments is always a JSON string (Python
// json.dumps bytes for parsed calls).
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolCall is the engine's internal tool-call shape: {"id","type":"function","function"} plus
// the optional model-written failure_message.
type ToolCall struct {
	ID             string       `json:"id"`
	Type           string       `json:"type"`
	Function       FunctionCall `json:"function"`
	FailureMessage string       `json:"failure_message,omitempty"`
}

// Result is ToolCallParser.parse_response's (finish_reason, tool_calls, assistant_message).
type Result struct {
	FinishReason string
	ToolCalls    []ToolCall
	// Message is the envelope's "message". Python passed any JSON value through; here a
	// missing key or null is "" and any other non-string is its Python str().
	Message string
}

// NewCallID returns "call_" + 12 hex chars, the shape of uuid4().hex[:12] ids.
func NewCallID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return "call_" + hex.EncodeToString(b[:])
}

// ParseToolCalls is ToolCallParser.parse_response: the model output (after the provider's
// ParseQwen3/ParseQwen25, or raw when that returned ok=false) → finish reason, tool calls and
// the assistant message.
//
//   - # comments outside strings are stripped per line, then the text is decoded;
//   - "tool_calls" (a non-empty list) wins, then a singular "tool_call", then — only when
//     neither key is present — the whole object as a bare call;
//   - a call is {"name","arguments"} or OpenAI's {"function":{"name","arguments"}}; string
//     arguments are decoded (undecodable → the call is dropped), null → {}, non-objects drop
//     the call; resolved_datetimes items are normalized (normalize_date_key) and nested
//     {"resolved_datetimes": …} objects unwrapped;
//   - invalid JSON retries on the first balanced {…} that decodes, then the first-{ to
//     last-} slice, else the whole text is the message;
//   - anything else that would raise in Python (a top-level array, string or number; a
//     non-object "function") makes the whole text the message.
func ParseToolCalls(output string) Result {
	cleaned := stripJSONComments(output)
	v, err := pyjson.Loads(cleaned)
	if err != nil {
		if _, isDecode := err.(*pyjson.DecodeError); isDecode {
			if extracted, ok := extractJSONFromText(output); ok {
				return ParseToolCalls(extracted)
			}
		}
		return Result{FinishReason: FinishStop, ToolCalls: []ToolCall{}, Message: output}
	}
	obj, isObj := v.(*pyjson.Object)
	if !isObj {
		// parsed.get → AttributeError → the generic except: whole output as the message.
		return Result{FinishReason: FinishStop, ToolCalls: []ToolCall{}, Message: output}
	}
	message := ""
	if m, ok := obj.Get("message"); ok {
		message = pyStrValue(m)
	}

	var calls []ToolCall
	raw, _ := obj.Get("tool_calls")
	if list, ok := raw.([]any); ok && len(list) > 0 {
		for _, item := range list {
			c, ok, crash := formatToolCall(item)
			if crash {
				return Result{FinishReason: FinishStop, ToolCalls: []ToolCall{}, Message: output}
			}
			if ok {
				calls = append(calls, c)
			}
		}
	}
	if len(calls) == 0 {
		if single, _ := obj.Get("tool_call"); pyTruthy(single) {
			c, ok, crash := formatToolCall(single)
			if crash {
				return Result{FinishReason: FinishStop, ToolCalls: []ToolCall{}, Message: output}
			}
			if ok {
				calls = append(calls, c)
			}
		}
	}
	_, hasCalls := obj.Get("tool_calls")
	_, hasCall := obj.Get("tool_call")
	if len(calls) == 0 && !hasCalls && !hasCall {
		c, ok, crash := formatToolCall(obj)
		if crash {
			return Result{FinishReason: FinishStop, ToolCalls: []ToolCall{}, Message: output}
		}
		if ok {
			calls = append(calls, c)
		}
	}
	if len(calls) > 0 {
		return Result{FinishReason: FinishToolCalls, ToolCalls: calls, Message: message}
	}
	return Result{FinishReason: FinishStop, ToolCalls: []ToolCall{}, Message: message}
}

// pyStrValue renders a decoded "message" value: strings as is, null as "", else str().
func pyStrValue(v any) string {
	if v == nil {
		return ""
	}
	return pyjson.Str(v)
}

// formatToolCall is ToolCallParser._format_tool_call. crash reports the cases where Python
// raised (a truthy non-object "function"), which abort the whole parse.
func formatToolCall(raw any) (ToolCall, bool, bool) {
	obj, ok := raw.(*pyjson.Object)
	if !ok {
		return ToolCall{}, false, false
	}
	name, _ := obj.Get("name")
	arguments, hasArgs := obj.Get("arguments")
	if !hasArgs {
		arguments = pyjson.NewObject()
	}
	if !pyTruthy(name) {
		if fn, has := obj.Get("function"); has {
			if !pyTruthy(fn) {
				fn = pyjson.NewObject()
			}
			fo, isObj := fn.(*pyjson.Object)
			if !isObj {
				return ToolCall{}, false, true // function.get → AttributeError
			}
			name, _ = fo.Get("name")
			if a, ok := fo.Get("arguments"); ok {
				arguments = a
			}
		}
	}
	if !pyTruthy(name) {
		return ToolCall{}, false, false
	}
	if s, isStr := arguments.(string); isStr {
		if decoded, err := pyjson.Loads(s); err == nil {
			arguments = decoded
		}
	}
	if arguments == nil {
		arguments = pyjson.NewObject()
	}
	args, isObj := arguments.(*pyjson.Object)
	if !isObj {
		return ToolCall{}, false, false
	}
	if dts, ok := args.Get("resolved_datetimes"); ok {
		if list, ok := dts.([]any); ok {
			unwrapped := []any{}
			for _, dt := range list {
				switch x := dt.(type) {
				case string:
					unwrapped = append(unwrapped, NormalizeDateKey(x))
				case *pyjson.Object:
					nested, has := x.Get("resolved_datetimes")
					if !has {
						unwrapped = append(unwrapped, dt)
						continue
					}
					switch n := nested.(type) {
					case []any:
						for _, nd := range n {
							if s, ok := nd.(string); ok {
								unwrapped = append(unwrapped, NormalizeDateKey(s))
							}
						}
					case string:
						unwrapped = append(unwrapped, NormalizeDateKey(n))
					}
				default:
					unwrapped = append(unwrapped, dt)
				}
			}
			args.Set("resolved_datetimes", unwrapped)
		}
	}
	call := ToolCall{
		ID:       NewCallID(),
		Type:     "function",
		Function: FunctionCall{Name: pyjson.Str(name), Arguments: pyjson.Dumps(args, true)},
	}
	if fm, ok := obj.Get("failure_message"); ok {
		if s, isStr := fm.(string); isStr && s != "" {
			call.FailureMessage = s
		}
	}
	return call, true, false
}

var pyWhitespaceRun = regexp.MustCompile(`[` + pySpaceClass + `]+`)

// NormalizeDateKey is normalize_date_key (date_resolution.py, identical in ToolCallParser):
// strip, lowercase, whitespace runs → "_", ":" → "_".
func NormalizeDateKey(raw string) string {
	text := PyLower(PyStrip(raw))
	text = pyWhitespaceRun.ReplaceAllLiteralString(text, "_")
	return strings.ReplaceAll(text, ":", "_")
}

// stripJSONComments is _strip_json_comments: per line, drop everything from a # outside a
// string (string state resets at each newline, as in Python).
func stripJSONComments(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		inString, escape := false, false
		for j := 0; j < len(line); j++ {
			c := line[j]
			switch {
			case escape:
				escape = false
			case c == '\\':
				escape = true
			case c == '"':
				inString = !inString
			case c == '#' && !inString:
				line = line[:j]
			}
			if j >= len(line) {
				break
			}
		}
		lines[i] = line
	}
	return strings.Join(lines, "\n")
}

// extractJSONFromText is _extract_json_from_text: the first balanced object that decodes,
// else the first-{ to last-} slice when it decodes.
func extractJSONFromText(text string) (string, bool) {
	for _, cand := range balancedJSONObjects(text) {
		cleaned := stripJSONComments(cand)
		if _, err := pyjson.Loads(cleaned); err == nil {
			return cleaned, true
		}
	}
	start, end := strings.IndexByte(text, '{'), strings.LastIndexByte(text, '}')
	if start != -1 && end != -1 && end > start {
		cleaned := stripJSONComments(text[start : end+1])
		if _, err := pyjson.Loads(cleaned); err == nil {
			return cleaned, true
		}
	}
	return "", false
}

// balancedJSONObjects is _extract_balanced_json_objects: a brace matcher that ignores braces
// inside strings.
func balancedJSONObjects(text string) []string {
	var objects []string
	inString, escape := false, false
	depth, start := 0, -1
	for i := 0; i < len(text); i++ {
		c := text[i]
		if escape {
			escape = false
			continue
		}
		if c == '\\' {
			escape = true
			continue
		}
		if c == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		switch c {
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			if depth > 0 {
				depth--
				if depth == 0 && start >= 0 {
					objects = append(objects, text[start:i+1])
					start = -1
				}
			}
		}
	}
	return objects
}
