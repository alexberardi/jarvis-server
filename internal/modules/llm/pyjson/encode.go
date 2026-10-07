package pyjson

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// Dumps is json.dumps(v, ensure_ascii=ensureASCII) with the default separators ", " and
// ": ". It accepts the decoded value types plus common Go types (map[string]any is written
// with sorted keys, so prefer *Object where order matters).
func Dumps(v any, ensureASCII bool) string {
	var b strings.Builder
	dump(&b, v, ensureASCII)
	return b.String()
}

func dump(b *strings.Builder, v any, ascii bool) {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case string:
		writeString(b, x, ascii)
	case *big.Int:
		b.WriteString(x.String())
	case int:
		b.WriteString(strconv.Itoa(x))
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case float64:
		b.WriteString(FloatRepr(x, true))
	case float32:
		b.WriteString(FloatRepr(float64(x), true))
	case json.Number:
		b.WriteString(x.String())
	case json.RawMessage:
		if pv, err := Loads(string(x)); err == nil {
			dump(b, pv, ascii)
		} else {
			b.WriteString("null")
		}
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			dump(b, e, ascii)
		}
		b.WriteByte(']')
	case []string:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			writeString(b, e, ascii)
		}
		b.WriteByte(']')
	case *Object:
		b.WriteByte('{')
		for i, k := range x.keys {
			if i > 0 {
				b.WriteString(", ")
			}
			writeString(b, k, ascii)
			b.WriteString(": ")
			dump(b, x.m[k], ascii)
		}
		b.WriteByte('}')
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteString(", ")
			}
			writeString(b, k, ascii)
			b.WriteString(": ")
			dump(b, x[k], ascii)
		}
		b.WriteByte('}')
	default:
		// Anything else goes through encoding/json and back, so it keeps a JSON shape.
		raw, err := json.Marshal(x)
		if err != nil {
			b.WriteString("null")
			return
		}
		dump(b, json.RawMessage(raw), ascii)
	}
}

const hexDigits = "0123456789abcdef"

func writeU(b *strings.Builder, r rune) {
	b.WriteString(`\u`)
	b.WriteByte(hexDigits[r>>12&0xF])
	b.WriteByte(hexDigits[r>>8&0xF])
	b.WriteByte(hexDigits[r>>4&0xF])
	b.WriteByte(hexDigits[r&0xF])
}

// writeString is py_encode_basestring(_ascii).
func writeString(b *strings.Builder, s string, ascii bool) {
	b.WriteByte('"')
	for _, r := range decodeRunes(s) {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			switch {
			case r < 0x20:
				writeU(b, r)
			case ascii && r > 0x7e:
				if r >= 0x10000 {
					v := r - 0x10000
					writeU(b, 0xD800|(v>>10)&0x3FF)
					writeU(b, 0xDC00|v&0x3FF)
				} else {
					writeU(b, r)
				}
			default:
				writeRune(b, r)
			}
		}
	}
	b.WriteByte('"')
}

// FloatRepr is Python's float.__repr__ (repr style "r"): the shortest round-tripping digits,
// fixed notation for 1e-4 <= |x| < 1e16, else d.ddde±XX. jsonStyle spells the non-finite
// values as json.dumps does (Infinity, -Infinity, NaN) rather than inf/nan.
func FloatRepr(f float64, jsonStyle bool) string {
	switch {
	case math.IsNaN(f):
		if jsonStyle {
			return "NaN"
		}
		return "nan"
	case math.IsInf(f, 1):
		if jsonStyle {
			return "Infinity"
		}
		return "inf"
	case math.IsInf(f, -1):
		if jsonStyle {
			return "-Infinity"
		}
		return "-inf"
	}
	e := strconv.FormatFloat(f, 'e', -1, 64) // e.g. -1.2345e+17
	neg := strings.HasPrefix(e, "-")
	e = strings.TrimPrefix(e, "-")
	mant, expS, _ := strings.Cut(e, "e")
	exp, _ := strconv.Atoi(expS)
	digits := strings.Replace(mant, ".", "", 1)
	var out string
	if exp >= -4 && exp < 16 {
		decpt := exp + 1 // digits before the point
		switch {
		case decpt <= 0:
			out = "0." + strings.Repeat("0", -decpt) + digits
		case decpt >= len(digits):
			out = digits + strings.Repeat("0", decpt-len(digits)) + ".0"
		default:
			out = digits[:decpt] + "." + digits[decpt:]
		}
	} else {
		m := digits[:1]
		if len(digits) > 1 {
			m += "." + digits[1:]
		}
		sign := "+"
		if exp < 0 {
			sign = "-"
			exp = -exp
		}
		out = fmt.Sprintf("%se%s%02d", m, sign, exp)
	}
	if neg {
		return "-" + out
	}
	return out
}

// TypeName is type(v).__name__ for a decoded value.
func TypeName(v any) string {
	switch v.(type) {
	case nil:
		return "NoneType"
	case bool:
		return "bool"
	case string:
		return "str"
	case *big.Int, int, int64:
		return "int"
	case float64, float32:
		return "float"
	case []any:
		return "list"
	case *Object, map[string]any:
		return "dict"
	}
	return fmt.Sprintf("%T", v)
}

// Str is str(v) for a decoded value: strings as themselves, everything else as repr.
func Str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return Repr(v)
}

// Repr is repr(v) for a decoded value.
func Repr(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case bool:
		if x {
			return "True"
		}
		return "False"
	case string:
		return strRepr(x)
	case *big.Int:
		return x.String()
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return FloatRepr(x, false)
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = Repr(e)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case *Object:
		parts := make([]string, len(x.keys))
		for i, k := range x.keys {
			parts[i] = strRepr(k) + ": " + Repr(x.m[k])
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	return fmt.Sprint(v)
}

// strRepr is str.__repr__: single quotes unless the text has a ' and no ".
func strRepr(s string) string {
	quote := '\''
	if strings.ContainsRune(s, '\'') && !strings.ContainsRune(s, '"') {
		quote = '"'
	}
	var b strings.Builder
	b.WriteRune(quote)
	for _, r := range decodeRunes(s) {
		switch {
		case r == quote || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r < 0x7f:
			b.WriteRune(r)
		case r >= 0xD800 && r <= 0xDFFF:
			fmt.Fprintf(&b, `\u%04x`, r)
		case unicode.IsPrint(r):
			b.WriteRune(r)
		case r <= 0xff:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r <= 0xffff:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			fmt.Fprintf(&b, `\U%08x`, r)
		}
	}
	b.WriteRune(quote)
	return b.String()
}

// ToJSON is Compact as a json.RawMessage, so an object's key order survives encoding/json.
func ToJSON(v any) json.RawMessage {
	return json.RawMessage(Compact(v))
}

// Compact is json.dumps(v, separators=(",", ":"), ensure_ascii=False): valid JSON for
// encoding/json consumers (non-finite floats become null, which strict JSON requires).
func Compact(v any) string {
	var b strings.Builder
	compact(&b, v)
	return b.String()
}

func compact(b *strings.Builder, v any) {
	switch x := v.(type) {
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			b.WriteString("null")
			return
		}
		b.WriteString(FloatRepr(x, true))
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			compact(b, e)
		}
		b.WriteByte(']')
	case *Object:
		b.WriteByte('{')
		for i, k := range x.keys {
			if i > 0 {
				b.WriteByte(',')
			}
			writeString(b, toValidUTF8(k), false)
			b.WriteByte(':')
			compact(b, x.m[k])
		}
		b.WriteByte('}')
	case string:
		writeString(b, toValidUTF8(x), false)
	default:
		dump(b, v, false)
	}
}

// toValidUTF8 replaces WTF-8 lone surrogates with U+FFFD for strict consumers.
func toValidUTF8(s string) string {
	if !strings.Contains(s, "\xed") {
		return s
	}
	var b strings.Builder
	for _, r := range decodeRunes(s) {
		if r >= 0xD800 && r <= 0xDFFF {
			r = unicode.ReplacementChar
		}
		b.WriteRune(r)
	}
	return b.String()
}

// MarshalJSON writes the object compactly with its keys in order, so an *Object can sit
// inside values encoding/json writes.
func (o *Object) MarshalJSON() ([]byte, error) { return []byte(Compact(o)), nil }
