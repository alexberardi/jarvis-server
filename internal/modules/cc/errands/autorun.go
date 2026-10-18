package errands

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/policy"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// Errand autonomy: the plan-start checkpoint (autonomy PLAN §1, checkpoint 1; legacy
// autorun_gate.py) and the inter-step value resolver (step_value_resolver.py). The household's
// rules, resolved by the policy (A7), are the trust boundary, not an allowlist: a plan autoruns
// only when every step resolves to allow. Risky steps and steps with a business counterparty
// never resolve to allow from a tool's declaration alone (policy.Decide). Nothing produces
// $-directives yet; the runner still resolves them on every step, as legacy did.

const maxAutorunSteps = 3

// IsAutorunEligible is is_autorun_eligible: whether a fresh plan may run without a tap, given
// each step's verdict (in step order). Fail-closed: a missing verdict asks.
func IsAutorunEligible(steps []Step, verdicts []policy.Verdict) (bool, string) {
	if len(steps) == 0 {
		return false, "empty plan"
	}
	if len(steps) > maxAutorunSteps {
		return false, fmt.Sprintf("too many steps for autorun (%d > %d)", len(steps), maxAutorunSteps)
	}
	for i, st := range steps {
		effect := policy.Ask
		if i < len(verdicts) {
			effect = verdicts[i].Effect
		}
		if effect != policy.Allow {
			return false, fmt.Sprintf("step '%s' resolves to %s", strings.TrimSpace(st.Command), effect)
		}
	}
	return true, "eligible"
}

// decideSteps resolves each step against the household's rules. A control step (replan) is not
// a capability and is allowed without asking the policy.
func (s *Service) decideSteps(ctx context.Context, hh string, steps []Step, source string) []policy.Verdict {
	out := make([]policy.Verdict, len(steps))
	for i, st := range steps {
		cmd := strings.TrimSpace(st.Command)
		switch {
		case controlCommands[cmd]:
			out[i] = policy.Verdict{Effect: policy.Allow, Reason: "control step"}
		case s.Policy == nil:
			out[i] = policy.Verdict{Effect: policy.Ask, Reason: "no rules; asks"}
		default:
			out[i] = s.Policy.Decide(ctx, hh, policy.Action{Tool: cmd, Args: st.Args, IsRisky: st.IsRisky,
				RunWithoutConfirmation: st.RunWithoutConfirmation, Source: source})
		}
	}
	return out
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
