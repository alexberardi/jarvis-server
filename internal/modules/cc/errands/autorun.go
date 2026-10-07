package errands

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// Errand autonomy, ported dormant (D9, 09.Q5): the plan-start blast gate (autorun_gate.py)
// and the inter-step value resolver (step_value_resolver.py). Nothing produces $-directives
// or autoruns a plan yet; the runner still resolves directives on every step, as legacy did.

// autorunAllowlist is the trust boundary: read-only or node-local reversible writes only.
var autorunAllowlist = map[string]bool{"get_drive_time": true, "reminder": true}

const maxAutorunSteps = 3

// IsAutorunEligible is is_autorun_eligible: whether a fresh plan may run without a tap.
// Fail-closed.
func IsAutorunEligible(steps []Step) (bool, string) {
	if len(steps) == 0 {
		return false, "empty plan"
	}
	if len(steps) > maxAutorunSteps {
		return false, fmt.Sprintf("too many steps for autorun (%d > %d)", len(steps), maxAutorunSteps)
	}
	for _, st := range steps {
		cmd := strings.TrimSpace(st.Command)
		if !autorunAllowlist[cmd] {
			return false, fmt.Sprintf("command '%s' is not autorun-allowlisted", cmd)
		}
		if st.IsRisky {
			return false, fmt.Sprintf("step '%s' is marked risky", cmd)
		}
		if objTruthy(st.Args, "business") {
			return false, fmt.Sprintf("step '%s' targets an outbound counterparty", cmd)
		}
	}
	return true, "eligible"
}

func isDirective(v any) (string, *pyjson.Object, bool) {
	o, ok := v.(*pyjson.Object)
	if !ok || o.Len() != 1 || !strings.HasPrefix(o.Keys()[0], "$") {
		return "", nil, false
	}
	k := o.Keys()[0]
	sv, _ := o.Get(k)
	spec, _ := sv.(*pyjson.Object)
	if spec == nil {
		spec = pyjson.NewObject()
	}
	return k, spec, true
}

// pyInt is int(v) for a decoded JSON value (ok=false where Python raised).
func pyInt(v any) (int64, bool) {
	switch x := v.(type) {
	case *big.Int:
		if x.IsInt64() {
			return x.Int64(), true
		}
		return 0, false
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return 0, false
		}
		return int64(x), true
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		return n, err == nil
	}
	return 0, false
}

func specInt(spec *pyjson.Object, k string, def int64) (int64, bool) {
	v, ok := spec.Get(k)
	if !ok {
		return def, true
	}
	return pyInt(v)
}

func specStr(spec *pyjson.Object, k, def string) string {
	v, ok := spec.Get(k)
	if !ok {
		return def
	}
	s, _ := v.(string)
	return s
}

func resultField(r Result, field string) (any, bool) {
	if r.Data != nil {
		if v, ok := r.Data.Get(field); ok {
			return v, true
		}
	}
	o := r.Object()
	return o.Get(field)
}

func driveMinutes(spec *pyjson.Object, prior []Result) (int64, string) {
	idx, _ := specInt(spec, "drive_from_step", 0)
	field := specStr(spec, "drive_field", "duration_minutes")
	if idx < 0 || idx >= int64(len(prior)) {
		return 0, fmt.Sprintf("no result for step %d", idx)
	}
	r := prior[idx]
	if !r.Success {
		return 0, fmt.Sprintf("step %d (%s) did not succeed", idx, r.Command)
	}
	raw, _ := resultField(r, field)
	n, ok := pyInt(raw)
	if !ok {
		return 0, fmt.Sprintf("step %d has no numeric '%s'", idx, field)
	}
	return n, ""
}

func parseISO(raw string) (time.Time, bool) {
	for _, f := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.Parse(f, raw); err == nil {
			return t, true // a naive timestamp is treated as UTC
		}
	}
	return time.Time{}, false
}

func leaveBy(spec *pyjson.Object, prior []Result, now time.Time) (any, string) {
	drive, skip := driveMinutes(spec, prior)
	if skip != "" {
		return nil, skip
	}
	raw, _ := spec.Get("event_start")
	s, _ := raw.(string)
	start, ok := parseISO(s)
	if !ok {
		return nil, "invalid event_start"
	}
	buf, _ := specInt(spec, "buffer_minutes", 5)
	leaveAt := start.Add(-time.Duration(drive+buf) * time.Minute)
	rel := int64(math.RoundToEven(leaveAt.Sub(now).Seconds() / 60))
	if rel <= 0 {
		return nil, fmt.Sprintf("departure time already passed (%d min)", rel)
	}
	return big.NewInt(rel), ""
}

func fromStep(spec *pyjson.Object, prior []Result) (any, string) {
	idx, _ := specInt(spec, "step", 0)
	fv, _ := spec.Get("field")
	field, _ := fv.(string)
	if idx < 0 || idx >= int64(len(prior)) {
		return nil, fmt.Sprintf("no result for step %d", idx)
	}
	if v, ok := resultField(prior[idx], field); ok {
		return v, ""
	}
	return nil, fmt.Sprintf("step %d has no '%s'", idx, field)
}

// ResolveStepArgs is resolve_step_args: replace $-directives with concrete values, or return
// a skip reason (the step no longer makes sense; the runner records it as a skipped success).
func ResolveStepArgs(args *pyjson.Object, prior []Result, now time.Time) (*pyjson.Object, string) {
	if args == nil {
		return pyjson.NewObject(), ""
	}
	out := pyjson.NewObject()
	for _, k := range args.Keys() {
		v, _ := args.Get(k)
		key, spec, ok := isDirective(v)
		if !ok {
			out.Set(k, v)
			continue
		}
		var val any
		var skip string
		switch key {
		case "$leave_by":
			val, skip = leaveBy(spec, prior, now)
		case "$from_step":
			val, skip = fromStep(spec, prior)
		default:
			skip = "unknown directive " + key
		}
		if skip != "" {
			return args, skip
		}
		out.Set(k, val)
	}
	return out, ""
}
