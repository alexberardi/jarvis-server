package errands

import (
	"encoding/json"
	"unicode/utf8"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// Step is one plan step: {command, args, label, is_risky}. Args are passed through untyped,
// in the order the LLM produced them (§11 risks), and may carry a checkpoint's add_steps and
// reason.
type Step struct {
	Command string
	Args    *pyjson.Object
	Label   string
	IsRisky bool
}

// Object renders the step as the legacy dict (key order command, args, label, is_risky).
func (s Step) Object() *pyjson.Object {
	args := s.Args
	if args == nil {
		args = pyjson.NewObject()
	}
	o := pyjson.NewObject()
	o.Set("command", s.Command)
	o.Set("args", args)
	o.Set("label", s.Label)
	o.Set("is_risky", s.IsRisky)
	return o
}

// MarshalJSON keeps the legacy key order (card metadata, job payloads).
func (s Step) MarshalJSON() ([]byte, error) { return []byte(pyjson.Compact(s.Object())), nil }

// stepFromValue reads a decoded step dict. A non-dict is an empty step (skipped by the
// runner, as legacy skipped an empty command).
func stepFromValue(v any) Step {
	o, ok := v.(*pyjson.Object)
	if !ok {
		return Step{Args: pyjson.NewObject()}
	}
	st := Step{Command: objStr(o, "command"), Label: truthyStr(o, "label"), IsRisky: objTruthy(o, "is_risky")}
	if av, ok := o.Get("args"); ok {
		st.Args, _ = av.(*pyjson.Object)
	}
	if st.Args == nil {
		st.Args = pyjson.NewObject()
	}
	return st
}

func stepsToList(steps []Step) []any {
	out := make([]any, len(steps))
	for i, s := range steps {
		out[i] = s.Object()
	}
	return out
}

// stepsDumps is json.dumps(steps) (prompt bytes and the stored column).
func stepsDumps(steps []Step) string { return pyjson.Dumps(stepsToList(steps), true) }

// parseSteps reads the stored steps column.
func parseSteps(raw string) []Step {
	v, err := pyjson.Loads(raw)
	if err != nil {
		return nil
	}
	list, _ := v.([]any)
	out := make([]Step, 0, len(list))
	for _, e := range list {
		out = append(out, stepFromValue(e))
	}
	return out
}

// Result is one step outcome: {command, label, success, message, error, data, summary?,
// control?} (workflow_engine.py:150-175). "" stands for None.
type Result struct {
	Command string
	Label   string
	Success bool
	Message string
	Error   string
	Data    *pyjson.Object
	Summary string // a phone call's wrap-up, threaded into later call briefs
	Control bool   // a control-flow step (an approved request_replan): left out of the tally
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Object renders the result as the legacy dict.
func (r Result) Object() *pyjson.Object {
	o := pyjson.NewObject()
	o.Set("command", r.Command)
	o.Set("label", r.Label)
	o.Set("success", r.Success)
	o.Set("message", nullStr(r.Message))
	o.Set("error", nullStr(r.Error))
	if r.Data != nil {
		o.Set("data", r.Data)
	} else {
		o.Set("data", nil)
	}
	if r.Summary != "" {
		o.Set("summary", r.Summary)
	}
	if r.Control {
		o.Set("control", true)
	}
	return o
}

func resultsDumps(rs []Result) string {
	out := make([]any, len(rs))
	for i, r := range rs {
		out[i] = r.Object()
	}
	return pyjson.Dumps(out, true)
}

func parseResults(raw string) []Result {
	v, err := pyjson.Loads(raw)
	if err != nil {
		return nil
	}
	list, _ := v.([]any)
	out := make([]Result, 0, len(list))
	for _, e := range list {
		o, ok := e.(*pyjson.Object)
		if !ok {
			continue
		}
		r := Result{Command: objStr(o, "command"), Label: objStr(o, "label"), Success: objTruthy(o, "success"),
			Message: objStr(o, "message"), Error: objStr(o, "error"), Summary: objStr(o, "summary"),
			Control: objTruthy(o, "control")}
		if dv, ok := o.Get("data"); ok {
			r.Data, _ = dv.(*pyjson.Object)
		}
		out = append(out, r)
	}
	return out
}

// --- small helpers over decoded JSON ---

func objStr(o *pyjson.Object, k string) string {
	if o == nil {
		return ""
	}
	v, _ := o.Get(k)
	s, _ := v.(string)
	return s
}

func objObj(o *pyjson.Object, k string) *pyjson.Object {
	if o == nil {
		return nil
	}
	v, _ := o.Get(k)
	r, _ := v.(*pyjson.Object)
	return r
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
	case json.Number:
		return x.String() != "0"
	default:
		return pyjson.Str(v) != "0"
	}
}

func objTruthy(o *pyjson.Object, k string) bool {
	if o == nil {
		return false
	}
	v, _ := o.Get(k)
	return truthy(v)
}

// truthyStr is `o.get(k) or ""` rendered as a string (str() for a non-string truthy value).
func truthyStr(o *pyjson.Object, k string) string {
	if o == nil {
		return ""
	}
	v, _ := o.Get(k)
	if !truthy(v) {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return pyjson.Str(v)
}

// orStr is `o.get(a) or o.get(b) or ""`.
func orStr(o *pyjson.Object, keys ...string) any {
	if o == nil {
		return ""
	}
	for _, k := range keys {
		if v, _ := o.Get(k); truthy(v) {
			return v
		}
	}
	return ""
}

func isScalar(v any) bool {
	switch v.(type) {
	case string, bool, float64:
		return true
	}
	switch pyjson.TypeName(v) {
	case "int", "float", "str", "bool":
		return true
	}
	return false
}

func copyObj(o *pyjson.Object) *pyjson.Object {
	c := pyjson.NewObject()
	for _, k := range o.Keys() {
		v, _ := o.Get(k)
		c.Set(k, v)
	}
	return c
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func runeLen(s string) int { return utf8.RuneCountInString(s) }

// truncRunes is s[:n] on code points.
func truncRunes(s string, n int) string {
	if runeLen(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}
