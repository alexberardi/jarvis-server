// Package jsonmode is response_format json_object handling, ported byte-exact from the legacy
// llm-proxy (services/chat_runner.py, docs/llm/04 §3.2): the system instruction, the repair
// cascade, the minimal schema validator, and the correction turn for the one retry.
//
// The repair cascade is steered by CPython's json error messages and returns
// json.dumps-formatted text, so it runs on pyjson (CPython 3.11 semantics). Golden fixtures:
// fixtures/golden/llm/json_*.json (G7).
package jsonmode

import (
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// SystemMessage is JSON_SYSTEM_MESSAGE, injected when response_format is json_object.
const SystemMessage = `You must respond with valid JSON only. Do not include any text before or after the JSON.
Do not wrap the JSON in markdown code blocks.
Output compact JSON without unnecessary whitespace.
Ensure all strings are properly escaped and all brackets are balanced.`

// AugmentSystem is inject_json_system_message's rewrite of one system message, given the
// message's text parts. Each part is followed by "\n"; the instruction is appended unless the
// text already says "JSON" and "valid json", in which case the text is kept, trailing "\n"
// and all (04 §8.6: ported as is, it feeds the KV prefix cache).
func AugmentSystem(parts []string) string {
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p)
		b.WriteString("\n")
	}
	existing := b.String()
	if !strings.Contains(strings.ToUpper(existing), "JSON") || !strings.Contains(strings.ToLower(existing), "valid json") {
		return PyStrip(existing) + "\n\n" + SystemMessage
	}
	return existing
}

// --- Python string helpers ---

func isPySpace(r rune) bool {
	switch {
	case r >= '\t' && r <= '\r', r >= 0x1c && r <= 0x20, r == 0x85, r == 0xa0, r == 0x1680,
		r >= 0x2000 && r <= 0x200a, r == 0x2028, r == 0x2029, r == 0x202f, r == 0x205f, r == 0x3000:
		return true
	}
	return false
}

// PyStrip is str.strip().
func PyStrip(s string) string { return strings.TrimFunc(s, isPySpace) }

// PySlice is s[:n] in code points.
func PySlice(s string, n int) string {
	i := 0
	for k := range s {
		if i == n {
			return s[:k]
		}
		i++
	}
	return s
}

// --- repair cascade ---

// dupHook is repair_duplicate_keys' object_pairs_hook: a repeated key collects its values into
// a list (appending to the first value when that is already a list).
func dupHook(keys []string, vals []any) any {
	o := pyjson.NewObject()
	for i, k := range keys {
		if cur, ok := o.Get(k); ok {
			l, isList := cur.([]any)
			if !isList {
				l = []any{cur}
			}
			o.Set(k, append(l, vals[i]))
			continue
		}
		o.Set(k, vals[i])
	}
	return o
}

// RepairDuplicateKeys is repair_duplicate_keys; ok is false where Python returns None.
func RepairDuplicateKeys(content string) (string, bool) {
	v, err := pyjson.LoadsHook(content, dupHook)
	if err != nil {
		return "", false
	}
	return pyjson.Dumps(v, false), true
}

// RepairUnescapedQuotes is repair_unescaped_quotes: it escapes every quote and re-reads the
// whole text as one JSON string (its leading re.sub is an identity).
func RepairUnescapedQuotes(content string) (string, bool) {
	r := strings.ReplaceAll(content, `\"`, `"`)
	r = strings.ReplaceAll(r, `"`, `\"`)
	r = `"` + r + `"`
	r = strings.ReplaceAll(r, `"\"`, `"`)
	r = strings.ReplaceAll(r, `\""`, `"`)
	v, err := pyjson.Loads("[" + r + "]")
	if err != nil {
		return "", false
	}
	l, ok := v.([]any)
	if !ok || len(l) == 0 {
		return "", false
	}
	return pyjson.Dumps(l[0], true), true
}

// ExtractJSON is extract_json_from_text: finditer over \{[\s\S]*\}|\[[\s\S]*\] (each match
// runs to the last closer), returning the first match that parses.
func ExtractJSON(content string) (string, bool) {
	for p := 0; p < len(content); {
		var closer byte
		switch content[p] {
		case '{':
			closer = '}'
		case '[':
			closer = ']'
		default:
			p++
			continue
		}
		j := strings.LastIndexByte(content[p+1:], closer)
		if j < 0 {
			p++
			continue
		}
		end := p + 1 + j + 1
		cand := content[p:end]
		if _, err := pyjson.Loads(cand); err == nil {
			return cand, true
		}
		p = end
	}
	return "", false
}

const pySpaceClass = `[\t\n\x0b\f\r\x1c-\x20\x{85}\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}]`

var (
	trailingCommaBeforeCloser = regexp.MustCompile(`,` + pySpaceClass + `*([\}\]])`)
	trailingCommaAtEnd        = regexp.MustCompile(`,` + pySpaceClass + `*\z`)
)

// RepairTruncated is repair_truncated_json: cut at the last closer if that parses, else drop
// trailing commas and append the missing closers (all braces, then all brackets).
func RepairTruncated(content string) (string, bool) {
	last := max(strings.LastIndexByte(content, '}'), strings.LastIndexByte(content, ']'))
	if last != -1 {
		if t := content[:last+1]; parses(t) {
			return t, true
		}
	}
	r := trailingCommaBeforeCloser.ReplaceAllString(content, "$1")
	r = trailingCommaAtEnd.ReplaceAllString(r, "")
	braces := strings.Count(r, "{") - strings.Count(r, "}")
	brackets := strings.Count(r, "[") - strings.Count(r, "]")
	r += strings.Repeat("}", max(0, braces)) + strings.Repeat("]", max(0, brackets))
	if !parses(r) {
		return "", false
	}
	return r, true
}

func parses(s string) bool {
	_, err := pyjson.Loads(s)
	return err == nil
}

// IsTruncated is is_json_truncated.
func IsTruncated(content string) bool {
	s := PyStrip(content)
	if s == "" {
		return false
	}
	if !strings.HasPrefix(s, "{") && !strings.HasPrefix(s, "[") {
		return false
	}
	if strings.HasPrefix(s, "{") && !strings.HasSuffix(s, "}") {
		return true
	}
	if strings.HasPrefix(s, "[") && !strings.HasSuffix(s, "]") {
		return true
	}
	if strings.HasSuffix(s, ":") {
		return true
	}
	return strings.Count(s, "{")-strings.Count(s, "}") > 0 || strings.Count(s, "[")-strings.Count(s, "]") > 0
}

// Parse is parse_json_response: the repaired content and whether it is valid JSON. Invalid
// input comes back unchanged.
func Parse(content string) (string, bool) {
	if _, err := pyjson.Loads(content); err == nil {
		if rd, ok := RepairDuplicateKeys(content); ok && rd != "" && rd != content {
			return rd, true
		}
		return content, true
	} else if de, isDecode := err.(*pyjson.DecodeError); isDecode {
		msg := de.Error()
		if strings.Contains(msg, "Expecting") && (strings.Contains(msg, "delimiter") || strings.Contains(msg, "property name")) {
			if r, ok := RepairUnescapedQuotes(content); ok && r != "" && parses(r) {
				return r, true
			}
		}
		if strings.Contains(msg, "Unterminated string") {
			if r, ok := RepairTruncated(content); ok && r != "" && parses(r) {
				return r, true
			}
		}
	}
	if rd, ok := RepairDuplicateKeys(content); ok && rd != "" && parses(rd) {
		return rd, true
	}
	if ex, ok := ExtractJSON(content); ok && ex != "" {
		final := ex
		if rd, ok := RepairDuplicateKeys(ex); ok && rd != "" {
			final = rd
		}
		if _, err := pyjson.Loads(final); err == nil {
			return final, true
		} else {
			if rq, ok := RepairUnescapedQuotes(ex); ok && rq != "" {
				f := rq
				if rd, ok := RepairDuplicateKeys(rq); ok && rd != "" {
					f = rd
				}
				if parses(f) {
					return f, true
				}
			}
			if strings.Contains(err.Error(), "Unterminated string") {
				if rt, ok := RepairTruncated(ex); ok && rt != "" {
					f := rt
					if rd, ok := RepairDuplicateKeys(rt); ok && rd != "" {
						f = rd
					}
					if parses(f) {
						return f, true
					}
				}
			}
		}
	}
	if rq, ok := RepairUnescapedQuotes(content); ok && rq != "" {
		f := rq
		if rd, ok := RepairDuplicateKeys(rq); ok && rd != "" {
			f = rd
		}
		if parses(f) {
			return f, true
		}
	}
	if rt, ok := RepairTruncated(content); ok && rt != "" && parses(rt) {
		return rt, true
	}
	return content, false
}

// --- schema ---

// PyError is a Python exception the legacy validator raised on a malformed schema
// (AttributeError, TypeError); Error() is "<Type>: <message>" and Msg is str(e).
type PyError struct{ Type, Msg string }

func (e *PyError) Error() string { return e.Type + ": " + e.Msg }

func attrErr(v any, attr string) *PyError {
	return &PyError{"AttributeError", fmt.Sprintf("'%s' object has no attribute '%s'", pyjson.TypeName(v), attr)}
}

// truthy is Python's bool(v) for decoded JSON values.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case *big.Int:
		return x.Sign() != 0
	case float64:
		return x != 0
	case []any:
		return len(x) > 0
	case *pyjson.Object:
		return x.Len() > 0
	}
	return true
}

// get is schema.get(key) with Python's AttributeError for a non-dict.
func get(schema any, key string) (any, error) {
	o, ok := schema.(*pyjson.Object)
	if !ok {
		return nil, attrErr(schema, "get")
	}
	v, _ := o.Get(key)
	return v, nil
}

func matchesType(v any, t any) bool {
	switch t {
	case "null":
		return v == nil
	case "string":
		_, ok := v.(string)
		return ok
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "integer":
		_, ok := v.(*big.Int)
		return ok
	case "number":
		switch v.(type) {
		case *big.Int, float64:
			return true
		}
		return false
	case "object":
		_, ok := v.(*pyjson.Object)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	}
	return true
}

// iterate is `for x in v` over a decoded value.
func iterate(v any) ([]any, error) {
	switch x := v.(type) {
	case []any:
		return x, nil
	case string:
		var out []any
		for _, r := range x {
			out = append(out, string(r))
		}
		return out, nil
	case *pyjson.Object:
		out := make([]any, len(x.Keys()))
		for i, k := range x.Keys() {
			out[i] = k
		}
		return out, nil
	}
	return nil, &PyError{"TypeError", fmt.Sprintf("'%s' object is not iterable", pyjson.TypeName(v))}
}

// contains is `key in value` for a dict.
func contains(o *pyjson.Object, key any) (bool, error) {
	switch k := key.(type) {
	case string:
		_, ok := o.Get(k)
		return ok, nil
	case []any, *pyjson.Object:
		return false, &PyError{"TypeError", fmt.Sprintf("unhashable type: '%s'", pyjson.TypeName(k))}
	}
	return false, nil
}

// Validate is validate_json_schema: "" when value satisfies the subset (type incl. type lists,
// required, properties, items), else the first error, e.g. "$.foo is required". A malformed
// schema gives the exception Python raised.
func Validate(value, schema any) (string, error) { return validate(value, schema, "$") }

func validate(value, schema any, path string) (string, error) {
	if !truthy(schema) {
		return "", nil
	}
	st, err := get(schema, "type")
	if err != nil {
		return "", err
	}
	if st != nil {
		ok := false
		if list, isList := st.([]any); isList {
			for _, t := range list {
				if matchesType(value, t) {
					ok = true
					break
				}
			}
		} else {
			ok = matchesType(value, st)
		}
		if !ok {
			return fmt.Sprintf("%s expected type %s, got %s", path, pyjson.Str(st), pyjson.TypeName(value)), nil
		}
	}
	_, isObj := value.(*pyjson.Object)
	if st == "object" || (st == nil && isObj) {
		obj, ok := value.(*pyjson.Object)
		if !ok {
			return path + " expected object", nil
		}
		req, _ := get(schema, "required")
		if truthy(req) {
			keys, err := iterate(req)
			if err != nil {
				return "", err
			}
			for _, k := range keys {
				in, err := contains(obj, k)
				if err != nil {
					return "", err
				}
				if !in {
					return fmt.Sprintf("%s.%s is required", path, pyjson.Str(k)), nil
				}
			}
		}
		props, _ := get(schema, "properties")
		if truthy(props) {
			po, ok := props.(*pyjson.Object)
			if !ok {
				return "", attrErr(props, "items")
			}
			for _, k := range po.Keys() {
				if v, in := obj.Get(k); in {
					ps, _ := po.Get(k)
					if e, err := validate(v, ps, path+"."+k); err != nil || e != "" {
						return e, err
					}
				}
			}
		}
	}
	_, isList := value.([]any)
	if st == "array" || (st == nil && isList) {
		list, ok := value.([]any)
		if !ok {
			return path + " expected array", nil
		}
		items, _ := get(schema, "items")
		if truthy(items) {
			for i, it := range list {
				if e, err := validate(it, items, fmt.Sprintf("%s[%d]", path, i)); err != nil || e != "" {
					return e, err
				}
			}
		}
	}
	return "", nil
}

// Summarize is summarize_json_schema, the schema hint in the correction turn.
func Summarize(schema any) (string, error) {
	if !truthy(schema) {
		return "", nil
	}
	props, err := get(schema, "properties")
	if err != nil {
		return "", err
	}
	req, _ := get(schema, "required")
	var propTypes []string
	if truthy(props) {
		po, ok := props.(*pyjson.Object)
		if !ok {
			return "", attrErr(props, "items")
		}
		for _, name := range po.Keys() {
			ps, _ := po.Get(name)
			o, ok := ps.(*pyjson.Object)
			if !ok {
				return "", attrErr(ps, "get")
			}
			t, has := o.Get("type")
			if !has {
				t = "any"
			}
			propTypes = append(propTypes, name+":"+pyjson.Str(t))
		}
	}
	summary := "Required keys: (none)"
	if truthy(req) {
		items, err := iterate(req)
		if err != nil {
			return "", &PyError{"TypeError", "can only join an iterable"}
		}
		parts := make([]string, len(items))
		for i, it := range items {
			s, ok := it.(string)
			if !ok {
				return "", &PyError{"TypeError", fmt.Sprintf("sequence item %d: expected str instance, %s found", i, pyjson.TypeName(it))}
			}
			parts[i] = s
		}
		summary = "Required keys: " + strings.Join(parts, ", ")
	}
	if len(propTypes) > 0 {
		summary += "\nAllowed keys: " + strings.Join(propTypes, ", ")
	}
	return summary, nil
}

// CorrectionTurn is the user message fix_json_with_retry appends.
func CorrectionTurn(invalid string, schema any) (string, error) {
	note := ""
	if IsTruncated(invalid) {
		note = " The response was truncated. Please provide a COMPLETE response."
	}
	hint, err := Summarize(schema)
	if err != nil {
		return "", err
	}
	block := ""
	if hint != "" {
		block = "\n\nSchema constraints:\n" + hint
	}
	return "The previous response was not valid JSON or did not match the required schema." + note + "\n\n" +
		"Please provide ONLY valid JSON with no explanation text. Ensure:\n" +
		"- All brackets and braces are properly closed\n" +
		"- All strings are properly quoted and escaped  \n" +
		"- The response is complete\n" +
		block + "\n\n" +
		"Previous (truncated/invalid) response preview:\n" +
		PySlice(invalid, 800) + "\n\n" +
		"Return the corrected, complete JSON:", nil
}

// RetryTemperature is the retry's max(0.3, t - 0.2).
func RetryTemperature(t float64) float64 { return max(0.3, t-0.2) }

// RetryMaxTokens is the retry's token budget; orig 0 means the request set none (4096).
func RetryMaxTokens(orig int, invalid string) int {
	est := utf8.RuneCountInString(invalid) / 3
	if orig == 0 {
		orig = 4096
	}
	buffer := max(2000, est/2)
	n := max(int(float64(orig)*1.5), est+buffer, 8192)
	return min(n, 16384)
}
