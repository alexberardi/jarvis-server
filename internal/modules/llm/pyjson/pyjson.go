// Package pyjson reproduces CPython's json module closely enough for byte-exact ports: the
// legacy llm-proxy repairs model output with json.loads/json.dumps, and the bytes it returns
// (separators ", " and ": ", float repr, ensure_ascii escaping) and the decode error it hits
// first (which picks the repair branch) are load-bearing (docs/llm/04 §3.2, §11).
//
// Values decode to: nil, bool, string, *big.Int (Python int), float64, []any and *Object (an
// insertion-ordered dict). Strings may hold lone surrogates, encoded WTF-8 style, because
// Python's json.loads accepts "\ud800".
//
// The decoder mirrors CPython 3.11's C scanner (_json.c), which is what the legacy service ran
// in prod: 3.13 added "Illegal trailing comma" errors, which would change the repair branch.
package pyjson

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Object is a Python dict decoded from JSON: keys keep their first-insertion position, and a
// later duplicate replaces the value in place (dict semantics).
type Object struct {
	keys []string
	m    map[string]any
}

// NewObject returns an empty object.
func NewObject() *Object { return &Object{m: map[string]any{}} }

// Keys returns the keys in insertion order.
func (o *Object) Keys() []string { return o.keys }

// Len is the number of keys.
func (o *Object) Len() int { return len(o.keys) }

// Get returns a key's value.
func (o *Object) Get(k string) (any, bool) {
	v, ok := o.m[k]
	return v, ok
}

// Set assigns a key, keeping its position when it already exists.
func (o *Object) Set(k string, v any) {
	if _, ok := o.m[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.m[k] = v
}

// DecodeError is json.JSONDecodeError. Pos is a code-point index, as in Python.
type DecodeError struct {
	Msg    string
	Pos    int
	Lineno int
	Colno  int
}

func (e *DecodeError) Error() string {
	return fmt.Sprintf("%s: line %d column %d (char %d)", e.Msg, e.Lineno, e.Colno, e.Pos)
}

// RecursionError stands in for Python's RecursionError on absurdly deep documents.
type RecursionError struct{}

func (RecursionError) Error() string {
	return "maximum recursion depth exceeded while decoding a JSON document"
}

const maxDepth = 900

// PairsHook builds an object from its key/value pairs, like json.loads(object_pairs_hook=...).
type PairsHook func(keys []string, vals []any) any

type decoder struct {
	s    []rune
	hook PairsHook
	// stop records a StopIteration (a missing value) and where it happened.
	depth int
}

type stopIteration struct{ pos int }

func (stopIteration) Error() string { return "stop" }

// Loads is json.loads(s).
func Loads(s string) (any, error) { return LoadsHook(s, nil) }

// LoadsHook is json.loads(s, object_pairs_hook=hook); a nil hook builds *Object.
func LoadsHook(s string, hook PairsHook) (any, error) {
	d := &decoder{s: decodeRunes(s), hook: hook}
	if len(d.s) > 0 && d.s[0] == 0xFEFF {
		return nil, d.errAt("Unexpected UTF-8 BOM (decode using utf-8-sig)", 0)
	}
	idx := d.skipWS(0)
	v, end, err := d.scanOnce(idx)
	if err != nil {
		if st, ok := err.(stopIteration); ok {
			return nil, d.errAt("Expecting value", st.pos)
		}
		return nil, err
	}
	end = d.skipWS(end)
	if end != len(d.s) {
		return nil, d.errAt("Extra data", end)
	}
	return v, nil
}

// decodeRunes is the code points of s; WTF-8 surrogates (from a previous decode) come back
// as surrogate code points so a re-parse sees what Python would.
func decodeRunes(s string) []rune {
	out := make([]rune, 0, len(s))
	for i := 0; i < len(s); {
		if s[i] == 0xED && i+2 < len(s) && s[i+1] >= 0xA0 && s[i+1] <= 0xBF && s[i+2] >= 0x80 && s[i+2] <= 0xBF {
			out = append(out, rune(s[i]&0x0F)<<12|rune(s[i+1]&0x3F)<<6|rune(s[i+2]&0x3F))
			i += 3
			continue
		}
		r, n := utf8.DecodeRuneInString(s[i:])
		out = append(out, r)
		i += n
	}
	return out
}

func (d *decoder) errAt(msg string, pos int) *DecodeError {
	lineno := 1
	last := -1
	for i := 0; i < pos && i < len(d.s); i++ {
		if d.s[i] == '\n' {
			lineno++
			last = i
		}
	}
	return &DecodeError{Msg: msg, Pos: pos, Lineno: lineno, Colno: pos - last}
}

func isWS(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '\r' }

func (d *decoder) skipWS(i int) int {
	for i < len(d.s) && isWS(d.s[i]) {
		i++
	}
	return i
}

func (d *decoder) has(i int, word string) bool {
	w := []rune(word)
	if i+len(w) > len(d.s) {
		return false
	}
	for k, r := range w {
		if d.s[i+k] != r {
			return false
		}
	}
	return true
}

func (d *decoder) scanOnce(idx int) (any, int, error) {
	n := len(d.s)
	if idx < 0 || idx >= n {
		return nil, 0, stopIteration{idx}
	}
	switch d.s[idx] {
	case '"':
		s, end, err := d.scanString(idx + 1)
		return s, end, err
	case '{':
		d.depth++
		defer func() { d.depth-- }()
		if d.depth > maxDepth {
			return nil, 0, RecursionError{}
		}
		return d.parseObject(idx + 1)
	case '[':
		d.depth++
		defer func() { d.depth-- }()
		if d.depth > maxDepth {
			return nil, 0, RecursionError{}
		}
		return d.parseArray(idx + 1)
	case 'n':
		if idx+3 < n && d.has(idx, "null") {
			return nil, idx + 4, nil
		}
	case 't':
		if idx+3 < n && d.has(idx, "true") {
			return true, idx + 4, nil
		}
	case 'f':
		if idx+4 < n && d.has(idx, "false") {
			return false, idx + 5, nil
		}
	case 'N':
		if idx+2 < n && d.has(idx, "NaN") {
			return math.NaN(), idx + 3, nil
		}
	case 'I':
		if idx+7 < n && d.has(idx, "Infinity") {
			return math.Inf(1), idx + 8, nil
		}
	case '-':
		if idx+8 < n && d.has(idx, "-Infinity") {
			return math.Inf(-1), idx + 9, nil
		}
	}
	return d.matchNumber(idx)
}

func isDigit(r rune) bool { return r >= '0' && r <= '9' }

func (d *decoder) matchNumber(start int) (any, int, error) {
	s := d.s
	endIdx := len(s) - 1
	idx := start
	if s[idx] == '-' {
		idx++
		if idx > endIdx {
			return nil, 0, stopIteration{start}
		}
	}
	switch {
	case s[idx] >= '1' && s[idx] <= '9':
		idx++
		for idx <= endIdx && isDigit(s[idx]) {
			idx++
		}
	case s[idx] == '0':
		idx++
	default:
		return nil, 0, stopIteration{start}
	}
	isFloat := false
	if idx < endIdx && s[idx] == '.' && isDigit(s[idx+1]) {
		isFloat = true
		idx += 2
		for idx <= endIdx && isDigit(s[idx]) {
			idx++
		}
	}
	if idx < endIdx && (s[idx] == 'e' || s[idx] == 'E') {
		eStart := idx
		idx++
		if idx < endIdx && (s[idx] == '-' || s[idx] == '+') {
			idx++
		}
		for idx <= endIdx && isDigit(s[idx]) {
			idx++
		}
		if isDigit(s[idx-1]) {
			isFloat = true
		} else {
			idx = eStart
		}
	}
	lit := string(s[start:idx])
	if isFloat {
		f, err := strconv.ParseFloat(lit, 64)
		if err != nil {
			// Out of range: Python's float() gives ±inf or 0.0, as ParseFloat's value does.
			if ne, ok := err.(*strconv.NumError); !ok || ne.Err != strconv.ErrRange {
				return nil, 0, err
			}
		}
		return f, idx, nil
	}
	b, ok := new(big.Int).SetString(lit, 10)
	if !ok {
		return nil, 0, stopIteration{start}
	}
	return b, idx, nil
}

func hexVal(r rune) (rune, bool) {
	switch {
	case r >= '0' && r <= '9':
		return r - '0', true
	case r >= 'a' && r <= 'f':
		return r - 'a' + 10, true
	case r >= 'A' && r <= 'F':
		return r - 'A' + 10, true
	}
	return 0, false
}

// scanString is scanstring_unicode with strict=True; end is the index after the opening quote.
func (d *decoder) scanString(end int) (string, int, error) {
	s := d.s
	n := len(s)
	begin := end - 1
	var b strings.Builder
	for {
		next := end
		var c rune
		for next < n {
			c = s[next]
			if c == '"' || c == '\\' {
				break
			}
			if c <= 0x1f {
				return "", 0, d.errAt("Invalid control character at", next)
			}
			next++
		}
		if next >= n {
			return "", 0, d.errAt("Unterminated string starting at", begin)
		}
		writeRunes(&b, s[end:next])
		next++
		if c == '"' {
			end = next
			break
		}
		if next == n {
			return "", 0, d.errAt("Unterminated string starting at", begin)
		}
		c = s[next]
		if c != 'u' {
			end = next + 1
			switch c {
			case '"', '\\', '/':
			case 'b':
				c = '\b'
			case 'f':
				c = '\f'
			case 'n':
				c = '\n'
			case 'r':
				c = '\r'
			case 't':
				c = '\t'
			default:
				return "", 0, d.errAt("Invalid \\escape", end-2)
			}
			writeRune(&b, c)
			continue
		}
		next++
		end = next + 4
		if end >= n {
			return "", 0, d.errAt("Invalid \\uXXXX escape", next-1)
		}
		c = 0
		for ; next < end; next++ {
			h, ok := hexVal(s[next])
			if !ok {
				return "", 0, d.errAt("Invalid \\uXXXX escape", end-5)
			}
			c = c<<4 | h
		}
		if c >= 0xD800 && c <= 0xDBFF && end+6 < n && s[end] == '\\' && s[end+1] == 'u' {
			var c2 rune
			e2 := end + 6
			for k := end + 2; k < e2; k++ {
				h, ok := hexVal(s[k])
				if !ok {
					return "", 0, d.errAt("Invalid \\uXXXX escape", e2-5)
				}
				c2 = c2<<4 | h
			}
			if c2 >= 0xDC00 && c2 <= 0xDFFF {
				c = 0x10000 + (c-0xD800)<<10 + (c2 - 0xDC00)
				end = e2
			}
		}
		writeRune(&b, c)
	}
	return b.String(), end, nil
}

// writeRune writes r as UTF-8, or as WTF-8 when it is a lone surrogate.
func writeRune(b *strings.Builder, r rune) {
	if r >= 0xD800 && r <= 0xDFFF {
		b.WriteByte(byte(0xE0 | r>>12))
		b.WriteByte(byte(0x80 | (r>>6)&0x3F))
		b.WriteByte(byte(0x80 | r&0x3F))
		return
	}
	b.WriteRune(r)
}

func writeRunes(b *strings.Builder, rs []rune) {
	for _, r := range rs {
		writeRune(b, r)
	}
}

func (d *decoder) parseObject(idx int) (any, int, error) {
	s := d.s
	endIdx := len(s) - 1
	var keys []string
	var vals []any
	idx = d.skipWS(idx)
	if idx > endIdx || s[idx] != '}' {
		for {
			if idx > endIdx || s[idx] != '"' {
				return nil, 0, d.errAt("Expecting property name enclosed in double quotes", idx)
			}
			key, next, err := d.scanString(idx + 1)
			if err != nil {
				return nil, 0, err
			}
			idx = d.skipWS(next)
			if idx > endIdx || s[idx] != ':' {
				return nil, 0, d.errAt("Expecting ':' delimiter", idx)
			}
			idx = d.skipWS(idx + 1)
			val, next, err := d.scanOnce(idx)
			if err != nil {
				return nil, 0, err
			}
			keys = append(keys, key)
			vals = append(vals, val)
			idx = d.skipWS(next)
			if idx <= endIdx && s[idx] == '}' {
				break
			}
			if idx > endIdx || s[idx] != ',' {
				return nil, 0, d.errAt("Expecting ',' delimiter", idx)
			}
			idx = d.skipWS(idx + 1)
		}
	}
	if d.hook != nil {
		return d.hook(keys, vals), idx + 1, nil
	}
	o := NewObject()
	for i, k := range keys {
		o.Set(k, vals[i])
	}
	return o, idx + 1, nil
}

func (d *decoder) parseArray(idx int) (any, int, error) {
	s := d.s
	endIdx := len(s) - 1
	out := []any{}
	idx = d.skipWS(idx)
	if idx > endIdx || s[idx] != ']' {
		for {
			val, next, err := d.scanOnce(idx)
			if err != nil {
				return nil, 0, err
			}
			out = append(out, val)
			idx = d.skipWS(next)
			if idx <= endIdx && s[idx] == ']' {
				break
			}
			if idx > endIdx || s[idx] != ',' {
				return nil, 0, d.errAt("Expecting ',' delimiter", idx)
			}
			idx = d.skipWS(idx + 1)
		}
	}
	return out, idx + 1, nil
}
