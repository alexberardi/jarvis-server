package pyjson

import (
	"encoding/json"
	"math/big"
	"sort"
	"strings"
)

// DumpsIndent is json.dumps(v, indent=n, ensure_ascii=ensureASCII): one item per line with
// the separators "," and ": ", empty containers as [] and {}. Like Dumps, a map[string]any is
// written with sorted keys, *Object in its order, and anything else through encoding/json
// (a struct keeps its field order).
func DumpsIndent(v any, ensureASCII bool, indent int) string {
	var b strings.Builder
	dumpIndent(&b, v, ensureASCII, strings.Repeat(" ", indent), 0)
	return b.String()
}

func dumpIndent(b *strings.Builder, v any, ascii bool, unit string, level int) {
	nl := func(l int) {
		b.WriteByte('\n')
		b.WriteString(strings.Repeat(unit, l))
	}
	list := func(n int, item func(i int)) {
		if n == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteByte('[')
		for i := range n {
			if i > 0 {
				b.WriteByte(',')
			}
			nl(level + 1)
			item(i)
		}
		nl(level)
		b.WriteByte(']')
	}
	object := func(keys []string, get func(k string) any) {
		if len(keys) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			nl(level + 1)
			writeString(b, k, ascii)
			b.WriteString(": ")
			dumpIndent(b, get(k), ascii, unit, level+1)
		}
		nl(level)
		b.WriteByte('}')
	}
	switch x := v.(type) {
	case []any:
		list(len(x), func(i int) { dumpIndent(b, x[i], ascii, unit, level+1) })
	case []string:
		list(len(x), func(i int) { writeString(b, x[i], ascii) })
	case *Object:
		object(x.keys, func(k string) any { return x.m[k] })
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		object(keys, func(k string) any { return x[k] })
	case nil, bool, string, int, int64, float64, float32, json.Number, *big.Int:
		dump(b, x, ascii)
	case json.RawMessage:
		pv, err := Loads(string(x))
		if err != nil {
			b.WriteString("null")
			return
		}
		dumpIndent(b, pv, ascii, unit, level)
	default:
		raw, err := json.Marshal(x)
		if err != nil {
			b.WriteString("null")
			return
		}
		dumpIndent(b, json.RawMessage(raw), ascii, unit, level)
	}
}
