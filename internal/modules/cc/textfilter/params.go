package textfilter

import (
	"math/big"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/dates"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// core/param_validation.py and the engine's _build_param_maps: client tool-call arguments
// checked against the node's declared parameter types and enum values. The error strings are
// prompt bytes (they go into the [INVALID_PARAM_RETRY] nag).

// NormalizeParamType is normalize_param_type: the lowercased base type and whether it is an
// array ("array<t>", "array[t]", "t[]"). ok=false when the base type is empty (no check).
func NormalizeParamType(paramType string) (base string, isArray, ok bool) {
	if paramType == "" {
		return "", false, false
	}
	raw := parse.PyLower(parse.PyStrip(paramType))
	switch {
	case strings.HasPrefix(raw, "array<") && strings.HasSuffix(raw, ">"):
		base, isArray = parse.PyStrip(raw[len("array<"):len(raw)-1]), true
	case strings.HasPrefix(raw, "array[") && strings.HasSuffix(raw, "]"):
		base, isArray = parse.PyStrip(raw[len("array["):len(raw)-1]), true
	case strings.HasSuffix(raw, "[]"):
		base, isArray = parse.PyStrip(raw[:len(raw)-2]), true
	default:
		base = raw
	}
	return base, isArray, base != ""
}

var isoDateRE = mustPy(`^\d{4}-\d{2}-\d{2}$`)

// IsISODate is is_iso_date (re.match ^\d{4}-\d{2}-\d{2}$, Unicode digits, trailing \n ok).
func IsISODate(value string) bool { return isoDateRE.search(value) }

func isPyInt(v any) bool { _, ok := v.(*big.Int); return ok }

// ValidateScalar is validate_scalar over a pyjson-decoded value. Unknown types pass.
func ValidateScalar(value any, baseType string) bool {
	switch baseType {
	case "string":
		_, ok := value.(string)
		return ok
	case "int", "integer":
		return isPyInt(value)
	case "float", "number", "double":
		_, f := value.(float64)
		return f || isPyInt(value)
	case "bool", "boolean":
		_, ok := value.(bool)
		return ok
	case "date":
		s, ok := value.(string)
		return ok && IsISODate(s)
	case "datetime":
		s, ok := value.(string)
		return ok && dates.IsISODatetime(s)
	}
	return true
}

// ValidateValue is validate_value: scalar, or a list whose every item validates.
func ValidateValue(value any, baseType string, isArray bool) bool {
	if isArray {
		list, ok := value.([]any)
		if !ok {
			return false
		}
		for _, it := range list {
			if !ValidateScalar(it, baseType) {
				return false
			}
		}
		return true
	}
	return ValidateScalar(value, baseType)
}

// ParamSpec is one declared parameter of a command.
type ParamSpec struct {
	Name string
	Type string // "" = no type check
	// Enum is enum_values when it is a non-empty list (nil otherwise).
	Enum []any
}

// ParamMaps are _build_param_maps' type and enum maps, keeping Python dict order: per tool,
// parameters in first-declared order (a later duplicate updates the value in place).
type ParamMaps struct {
	tools []string
	types map[string]*orderedSpec
	enums map[string]*orderedSpec
}

type orderedSpec struct {
	names []string
	vals  map[string]any // string type, or []any enum
}

func (o *orderedSpec) set(k string, v any) {
	if _, ok := o.vals[k]; !ok {
		o.names = append(o.names, k)
	}
	o.vals[k] = v
}

// BuildParamMaps is _build_param_maps over the cached available_commands (a pyjson-decoded
// list of CommandDefinition dicts).
func BuildParamMaps(availableCommands []any) *ParamMaps {
	pm := &ParamMaps{types: map[string]*orderedSpec{}, enums: map[string]*orderedSpec{}}
	get := func(m map[string]*orderedSpec, k string) *orderedSpec {
		if o, ok := m[k]; ok {
			return o
		}
		o := &orderedSpec{vals: map[string]any{}}
		m[k] = o
		return o
	}
	for _, c := range availableCommands {
		cmd, _ := c.(*pyjson.Object)
		if cmd == nil {
			continue
		}
		name, _ := objGet(cmd, "command_name").(string)
		if name == "" {
			continue
		}
		params, _ := objGet(cmd, "parameters").([]any)
		for _, p := range params {
			po, _ := p.(*pyjson.Object)
			if po == nil {
				continue
			}
			pname, _ := objGet(po, "name").(string)
			ptype, _ := objGet(po, "type").(string)
			if pname != "" && ptype != "" {
				get(pm.types, name).set(pname, ptype)
			}
			if ev, ok := objGet(po, "enum_values").([]any); pname != "" && ok && len(ev) > 0 {
				get(pm.enums, name).set(pname, ev)
			}
		}
	}
	return pm
}

// ToolCallArgs is the part of a tool call validation reads: the function name and the raw
// arguments JSON string.
type ToolCallArgs struct {
	Name      string
	Arguments string
}

// FindInvalidParams is find_invalid_params: "{tool}.{param} expected {type}" and
// "{tool}.{param} must be one of: a, b" per offending argument, in call order.
func FindInvalidParams(calls []ToolCallArgs, pm *ParamMaps) []string {
	invalid := []string{}
	if pm == nil || (len(pm.types) == 0 && len(pm.enums) == 0) {
		return invalid
	}
	for _, call := range calls {
		if call.Name == "" {
			continue
		}
		args := pyjson.NewObject()
		if v, err := pyjson.Loads(call.Arguments); err == nil {
			o, ok := v.(*pyjson.Object)
			if !ok {
				continue
			}
			args = o
		}
		if ts, ok := pm.types[call.Name]; ok {
			for _, pname := range ts.names {
				val, present := args.Get(pname)
				if !present {
					continue
				}
				ptype := ts.vals[pname].(string)
				base, isArray, ok := NormalizeParamType(ptype)
				if !ok {
					continue
				}
				if !ValidateValue(val, base, isArray) {
					invalid = append(invalid, call.Name+"."+pname+" expected "+ptype)
				}
			}
		}
		if es, ok := pm.enums[call.Name]; ok {
			for _, pname := range es.names {
				val, _ := args.Get(pname)
				if val == nil {
					continue
				}
				allowed := es.vals[pname].([]any)
				sv := pyjson.Str(val)
				found := false
				parts := make([]string, len(allowed))
				for i, a := range allowed {
					parts[i] = pyjson.Str(a)
					if s, ok := a.(string); ok && s == sv {
						found = true
					}
				}
				if !found {
					invalid = append(invalid, call.Name+"."+pname+" must be one of: "+strings.Join(parts, ", "))
				}
			}
		}
	}
	return invalid
}

func objGet(o *pyjson.Object, k string) any {
	if o == nil {
		return nil
	}
	v, _ := o.Get(k)
	return v
}
