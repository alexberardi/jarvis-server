package dates

import (
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// IsDatetimeParam is is_datetime_param: format date-time, or an array of them.
func IsDatetimeParam(schema any) bool {
	o, ok := schema.(*pyjson.Object)
	if !ok {
		return false
	}
	if f, _ := o.Get("format"); f == "date-time" {
		return true
	}
	return IsDatetimeArray(schema)
}

// IsDatetimeArray is is_datetime_array: type array with items.format date-time.
func IsDatetimeArray(schema any) bool {
	o, ok := schema.(*pyjson.Object)
	if !ok {
		return false
	}
	if t, _ := o.Get("type"); t != "array" {
		return false
	}
	iv, _ := o.Get("items")
	items, ok := iv.(*pyjson.Object)
	if !ok {
		return false
	}
	f, _ := items.Get("format")
	return f == "date-time"
}

// SchemaProperties finds a tool's parameters.properties by name in tool definitions that are
// either OpenAI-wrapped ({"function":{…}}) or bare ({"name",…}); later definitions of a name
// win, as in the engine's tool_map. The engine looks names up in the turn's tools plus the
// full cached tool list so validation and injection agree (02 §7.5).
func SchemaProperties(tools []*pyjson.Object, name string) *pyjson.Object {
	var found *pyjson.Object
	for _, t := range tools {
		def := t
		if fv, ok := t.Get("function"); ok {
			fn, isObj := fv.(*pyjson.Object)
			if !isObj {
				continue
			}
			def = fn
		}
		if n, _ := def.Get("name"); n != name {
			continue
		}
		found = def
	}
	if found == nil {
		return nil
	}
	pv, _ := found.Get("parameters")
	params, _ := pv.(*pyjson.Object)
	if params == nil {
		return nil
	}
	propv, _ := params.Get("properties")
	props, _ := propv.(*pyjson.Object)
	return props
}

// InjectDates is the engine's _inject_date_keys for one tool call (corrected per D8/D40):
// every date-time parameter of the call's schema (props = parameters.properties) is filled
// or fixed in the JSON args, and the re-serialized args (json.dumps bytes) are returned with
// changed=true when anything moved.
//
//   - Empty parameter (absent, null, [] or ""): the turn's resolved date keys (turnKeys, the
//     extractor's output on the raw transcript); with none, today. An array gets all of them,
//     a scalar the first.
//   - Array parameter with values: ISO items are kept; the other strings are resolved together
//     as one key set, so ["tomorrow", "morning"] becomes one instant and a bucket key keeps
//     all its dates (legacy kept only the first). Non-strings are dropped.
//   - Scalar string that is not ISO: resolved as a key (first value).
//
// A value that does not resolve takes the turn's first resolved date, else today: there is no
// LLM fallback (D40 03.Q9).
func (c *Context) InjectDates(args string, props *pyjson.Object, turnKeys []string) (string, bool) {
	if props == nil {
		return args, false
	}
	v, err := pyjson.Loads(args)
	if err != nil {
		v = pyjson.NewObject()
	}
	obj, ok := v.(*pyjson.Object)
	if !ok {
		return args, false
	}
	turn, _ := c.Resolve(turnKeys)
	fallback := func() []string {
		if len(turn) > 0 {
			return turn
		}
		return []string{c.Today()}
	}

	mutated := false
	for _, name := range props.Keys() {
		schema, _ := props.Get(name)
		if !IsDatetimeParam(schema) {
			continue
		}
		isArray := IsDatetimeArray(schema)
		existing, _ := obj.Get(name)
		if isEmpty(existing) {
			dates := fallback()
			if isArray {
				obj.Set(name, strList(dates))
			} else {
				obj.Set(name, dates[0])
			}
			mutated = true
			continue
		}
		switch x := existing.(type) {
		case []any:
			if !isArray {
				continue
			}
			var kept, keys []string
			for _, item := range x {
				s, ok := item.(string)
				if !ok {
					continue
				}
				if IsISODatetime(s) {
					kept = append(kept, s)
				} else {
					keys = append(keys, s)
				}
			}
			if len(keys) > 0 {
				res, _ := c.Resolve(keys)
				if len(res) == 0 {
					res = fallback()
				}
				kept = append(kept, res...)
			}
			if len(kept) == 0 {
				kept = fallback()
			}
			if !sameStrings(x, kept) {
				obj.Set(name, strList(kept))
				mutated = true
			}
		case string:
			if isArray || IsISODatetime(x) {
				continue
			}
			res, _ := c.Resolve([]string{x})
			if len(res) == 0 {
				res = fallback()
			}
			obj.Set(name, res[0])
			mutated = true
		}
	}
	if !mutated {
		return args, false
	}
	return pyjson.Dumps(obj, true), true
}

// isEmpty is `existing in (None, [], "")`.
func isEmpty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == ""
	case []any:
		return len(x) == 0
	}
	return false
}

func strList(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func sameStrings(a []any, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if s, ok := a[i].(string); !ok || s != b[i] {
			return false
		}
	}
	return true
}
