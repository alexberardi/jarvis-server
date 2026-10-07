package parse

import (
	"regexp"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

var (
	// qwen3_*: _THINK_BLOCK_RE, _THINK_UNCLOSED_RE (case-sensitive, unlike the engine's).
	thinkBlockRE    = regexp.MustCompile(`(?s)<think>.*?</think>[` + pySpaceClass + `]*`)
	thinkUnclosedRE = regexp.MustCompile(`(?s)<think>.*`)
	// qwen3_large_untrained._MESSAGE_WRAP_RE.
	messageWrapRE = regexp.MustCompile(`(?s)<message>[` + pySpaceClass + `]*(.*?)[` + pySpaceClass + `]*</message>`)
	// qwen25_medium_untrained._TOOL_CALL_TAG_RE.
	toolCallTagRE = regexp.MustCompile(`(?s)<tool_call>[` + pySpaceClass + `]*(.*?)[` + pySpaceClass + `]*</tool_call>`)
)

// arrayParams are always arrays: a string value is wrapped into a one-element list.
var arrayParams = []string{"resolved_datetimes"}

// StripThink removes closed <think>…</think> blocks (and the whitespace after each), then an
// unclosed <think> to end of text.
func StripThink(s string) string {
	s = thinkBlockRE.ReplaceAllLiteralString(s, "")
	return thinkUnclosedRE.ReplaceAllLiteralString(s, "")
}

// UnwrapMessage replaces every <message>…</message> with its trimmed inner text.
func UnwrapMessage(s string) string {
	return messageWrapRE.ReplaceAllString(s, "$1")
}

// SanitizeQwen3 is the Qwen3 sanitize_text: think blocks stripped, then (14B always; 8B/9B
// since D22) the <message> wrapper unwrapped, then str.strip().
func SanitizeQwen3(text string, unwrapMessage bool) string {
	s := StripThink(text)
	if unwrapMessage {
		s = UnwrapMessage(s)
	}
	return PyStrip(s)
}

// ParseQwen3 is a Qwen3 provider's parse_response: StripThink, the <message> unwrap when
// unwrapMessage (14B in legacy; 8B/9B never had it in parse_response and D22 only adds it to
// sanitize), then ParseQwen25. ok=false is Python's None (use the raw content as is).
func ParseQwen3(raw string, unwrapMessage bool) (string, bool) {
	s := StripThink(raw)
	if unwrapMessage {
		s = UnwrapMessage(s)
	}
	return ParseQwen25(s)
}

// ParseQwen25 is Qwen25MediumUntrained.parse_response, the text path's output contract:
//
//  1. every <tool_call>{json}</tool_call> is collected (bad JSON skipped); a string
//     resolved_datetimes argument is wrapped into a list;
//  2. a JSON object with "tool_calls" passes through (ok=false), except the hybrid envelope:
//     empty tool_calls with <tool_call> blocks inside "message", which are extracted;
//  3. a bare {"name","arguments"} object is wrapped;
//  4. plain text not starting with "{" becomes a message envelope.
//
// The result is json.dumps of {"message","tool_calls","error"} (Python separators,
// ensure_ascii). Divergence (D8): a <tool_call> body that is valid JSON but not an object
// raised AttributeError in Python; here it is skipped like bad JSON.
func ParseQwen25(raw string) (string, bool) {
	cleaned := PyStrip(raw)

	if calls := toolCallBlocks(cleaned); len(calls) > 0 {
		return envelope("", calls), true
	}

	if v, err := pyjson.Loads(cleaned); err == nil {
		if obj, isObj := v.(*pyjson.Object); isObj {
			if _, has := obj.Get("tool_calls"); has {
				existing, _ := obj.Get("tool_calls")
				if !pyTruthy(existing) {
					msg, _ := obj.Get("message")
					if text, isStr := msg.(string); isStr && strings.Contains(text, "<tool_call>") {
						if calls := toolCallBlocks(text); len(calls) > 0 {
							return envelope("", calls), true
						}
					}
				}
				return "", false // already Jarvis JSON
			}
			_, hasName := obj.Get("name")
			_, hasArgs := obj.Get("arguments")
			if hasName && hasArgs {
				return envelope("", []any{obj}), true
			}
		}
	}

	if cleaned != "" && !strings.HasPrefix(cleaned, "{") {
		return envelope(cleaned, []any{}), true
	}
	return "", false
}

// toolCallBlocks extracts and decodes every <tool_call> block in order.
func toolCallBlocks(s string) []any {
	var calls []any
	for _, m := range toolCallTagRE.FindAllStringSubmatch(s, -1) {
		v, err := pyjson.Loads(PyStrip(m[1]))
		if err != nil {
			continue
		}
		obj, ok := v.(*pyjson.Object)
		if !ok {
			continue
		}
		if args, ok := obj.Get("arguments"); ok {
			if ao, ok := args.(*pyjson.Object); ok {
				for _, k := range arrayParams {
					if sv, ok := ao.Get(k); ok {
						if str, ok := sv.(string); ok {
							ao.Set(k, []any{str})
						}
					}
				}
			}
		}
		calls = append(calls, obj)
	}
	return calls
}

func envelope(message string, calls []any) string {
	o := pyjson.NewObject()
	o.Set("message", message)
	o.Set("tool_calls", calls)
	o.Set("error", nil)
	return pyjson.Dumps(o, true)
}

// pyTruthy is Python truthiness for decoded JSON values.
func pyTruthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case []any:
		return len(x) > 0
	case *pyjson.Object:
		return x.Len() > 0
	case float64:
		return x != 0
	}
	if s := pyjson.Repr(v); s == "0" {
		return false
	}
	return true
}
