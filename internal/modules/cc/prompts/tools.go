package prompts

import (
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// Tool is one OpenAI-format tool definition ({"type":"function","function":{…}}, plus
// Jarvis extension keys) exactly as received from the node or the server-tool registry.
// Tools stay ordered raw objects end to end (docs/cc/03 §11): key order reaches the prompt
// bytes, so they are never round-tripped through map[string]any. Decode with pyjson.Loads.
type Tool = *pyjson.Object

// jarvisExtensionKeys are ToolBuilder._JARVIS_EXTENSION_KEYS.
var jarvisExtensionKeys = map[string]bool{
	"allow_direct_answer": true,
	"is_server_tool":      true,
	"command_name":        true,
	"keywords":            true,
	"examples":            true,
	"_refinable":          true,
}

// buildOptions are ToolBuilder.build's flags (descriptions are always kept and never
// truncated by the kept providers).
type buildOptions struct {
	paramDescriptions bool
	formatHints       bool
	excludeRefinable  bool
}

var nativeBuild = buildOptions{paramDescriptions: true, formatHints: true}

// buildTools is ToolBuilder.build: clean {"type":"function","function":{name, description,
// parameters}} definitions. Inputs are never mutated; stripped schemas are fresh copies.
func buildTools(tools []Tool, opt buildOptions) []Tool {
	out := make([]Tool, 0, len(tools))
	for _, tool := range tools {
		fv, _ := tool.Get("function")
		fn, _ := fv.(*pyjson.Object)
		if fn == nil || fn.Len() == 0 {
			continue
		}
		name, _ := fn.Get("name")
		if !truthy(name) {
			continue
		}
		clean := pyjson.NewObject()
		clean.Set("name", name)
		if d, _ := fn.Get("description"); truthy(d) {
			clean.Set("description", d)
		}
		if pv, _ := fn.Get("parameters"); truthy(pv) {
			if params, ok := pv.(*pyjson.Object); ok {
				if !opt.paramDescriptions {
					params = mapProperties(params, func(p *pyjson.Object) *pyjson.Object { return without(p, "description") })
				}
				if !opt.formatHints {
					params = mapProperties(params, stripFormat)
				}
				if opt.excludeRefinable {
					params = stripRefinable(params)
				}
				clean.Set("parameters", params)
			} else {
				clean.Set("parameters", pv)
			}
		}
		t := pyjson.NewObject()
		t.Set("type", "function")
		t.Set("function", clean)
		out = append(out, t)
	}
	return out
}

// mapProperties returns a shallow copy of params whose object-valued properties went through
// f (_strip_param_descriptions / _strip_format_keys). Falsy "properties" → plain copy.
func mapProperties(params *pyjson.Object, f func(*pyjson.Object) *pyjson.Object) *pyjson.Object {
	res := copyObject(params)
	pv, _ := params.Get("properties")
	props, ok := pv.(*pyjson.Object)
	if !ok || props.Len() == 0 {
		return res
	}
	clean := pyjson.NewObject()
	for _, k := range props.Keys() {
		v, _ := props.Get(k)
		if po, ok := v.(*pyjson.Object); ok {
			clean.Set(k, f(po))
		} else {
			clean.Set(k, v)
		}
	}
	res.Set("properties", clean)
	return res
}

// stripFormat drops "format" from a property and from its object-valued "items".
func stripFormat(p *pyjson.Object) *pyjson.Object {
	c := without(p, "format")
	if iv, ok := c.Get("items"); ok {
		if items, ok := iv.(*pyjson.Object); ok {
			c.Set("items", without(items, "format"))
		}
	}
	return c
}

// stripRefinable is _strip_refinable_params: properties marked truthy _refinable are removed,
// and from "required" too.
func stripRefinable(params *pyjson.Object) *pyjson.Object {
	res := copyObject(params)
	pv, _ := params.Get("properties")
	props, ok := pv.(*pyjson.Object)
	if !ok || props.Len() == 0 {
		return res
	}
	clean := pyjson.NewObject()
	removed := map[string]bool{}
	for _, k := range props.Keys() {
		v, _ := props.Get(k)
		if po, ok := v.(*pyjson.Object); ok {
			if r, _ := po.Get("_refinable"); truthy(r) {
				removed[k] = true
				continue
			}
		}
		clean.Set(k, v)
	}
	res.Set("properties", clean)
	if rv, has := res.Get("required"); has && len(removed) > 0 {
		var kept []any
		list, _ := rv.([]any)
		for _, r := range list {
			if s, ok := r.(string); ok && removed[s] {
				continue
			}
			kept = append(kept, r)
		}
		if kept == nil {
			kept = []any{}
		}
		res.Set("required", kept)
	}
	return res
}

// StripJarvisExtensions is ToolBuilder.strip_jarvis_extensions without the legacy aliasing
// bug (D8, 02.Q7): Jarvis-only top-level keys are dropped and _refinable markers removed
// from the property schemas of a copy; the cached input tools are never mutated, so text-path
// refinement still sees its markers.
func StripJarvisExtensions(tools []Tool) []Tool {
	out := make([]Tool, 0, len(tools))
	for _, tool := range tools {
		clean := pyjson.NewObject()
		for _, k := range tool.Keys() {
			if jarvisExtensionKeys[k] {
				continue
			}
			v, _ := tool.Get(k)
			clean.Set(k, v)
		}
		fv, _ := clean.Get("function")
		if fn, ok := fv.(*pyjson.Object); ok {
			pv, _ := fn.Get("parameters")
			if params, ok := pv.(*pyjson.Object); ok {
				propv, _ := params.Get("properties")
				if props, ok := propv.(*pyjson.Object); ok && props.Len() > 0 {
					newProps := pyjson.NewObject()
					for _, k := range props.Keys() {
						v, _ := props.Get(k)
						if po, ok := v.(*pyjson.Object); ok {
							v = without(po, "_refinable")
						}
						newProps.Set(k, v)
					}
					newParams := copyObject(params)
					newParams.Set("properties", newProps)
					newFn := copyObject(fn)
					newFn.Set("parameters", newParams)
					clean.Set("function", newFn)
				}
			}
		}
		out = append(out, clean)
	}
	return out
}

// toolsBlock is the Qwen <tools> block: one compact json.dumps per line (ensure_ascii, no
// HTML escaping, insertion order), or "<tools>\n</tools>" with no tools.
func toolsBlock(tools []Tool, opt buildOptions) string {
	clean := buildTools(tools, opt)
	if len(clean) == 0 {
		return "<tools>\n</tools>"
	}
	lines := make([]string, len(clean))
	for i, t := range clean {
		lines[i] = CompactASCII(t)
	}
	return "<tools>\n" + strings.Join(lines, "\n") + "\n</tools>"
}

// CompactASCII is json.dumps(v, separators=(",", ":")) with Python's defaults otherwise:
// ensure_ascii (\uXXXX, surrogate pairs above the BMP), no HTML escaping, insertion order.
func CompactASCII(v any) string {
	var b strings.Builder
	compactASCII(&b, v)
	return b.String()
}

func compactASCII(b *strings.Builder, v any) {
	switch x := v.(type) {
	case *pyjson.Object:
		b.WriteByte('{')
		for i, k := range x.Keys() {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(pyjson.Dumps(k, true))
			b.WriteByte(':')
			e, _ := x.Get(k)
			compactASCII(b, e)
		}
		b.WriteByte('}')
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			compactASCII(b, e)
		}
		b.WriteByte(']')
	default:
		b.WriteString(pyjson.Dumps(v, true))
	}
}

// copyObject is dict(o): a shallow copy keeping key order.
func copyObject(o *pyjson.Object) *pyjson.Object {
	c := pyjson.NewObject()
	for _, k := range o.Keys() {
		v, _ := o.Get(k)
		c.Set(k, v)
	}
	return c
}

// without is {k: v for k, v in o.items() if k != drop}.
func without(o *pyjson.Object, drop string) *pyjson.Object {
	c := pyjson.NewObject()
	for _, k := range o.Keys() {
		if k == drop {
			continue
		}
		v, _ := o.Get(k)
		c.Set(k, v)
	}
	return c
}

// truthy is Python truthiness for decoded JSON values.
func truthy(v any) bool {
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
	return pyjson.Repr(v) != "0"
}

// get returns o[k] (nil when o is nil or the key is absent).
func get(o *pyjson.Object, k string) any {
	if o == nil {
		return nil
	}
	v, _ := o.Get(k)
	return v
}

// getObj returns o[k] when it is an object.
func getObj(o *pyjson.Object, k string) *pyjson.Object {
	v, _ := get(o, k).(*pyjson.Object)
	return v
}

// pyStr is f"{v}" for a decoded JSON value.
func pyStr(v any) string {
	if v == nil {
		return "None"
	}
	return pyjson.Str(v)
}

// getStr is f"{o.get(k, def)}".
func getStr(o *pyjson.Object, k, def string) string {
	if o == nil {
		return def
	}
	v, ok := o.Get(k)
	if !ok {
		return def
	}
	return pyStr(v)
}
