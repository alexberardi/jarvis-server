// Package stepvalues is the inter-step value resolver for errand steps, ported from CC's
// app/services/step_value_resolver.py (docs/cc/08 §3.6; its caller is the errand executor,
// doc 09). D9 keeps errand autonomy, so it is ported ahead of its caller.
//
// An arg value may be a $-directive (a single-key object whose key starts with "$"); the
// resolver replaces it with a concrete value, or returns a skip reason meaning "this step no
// longer makes sense" (D45: the executor records a skipped step as a success).
//
//   - {"$leave_by": {"event_start": <iso>, "drive_from_step": <i>, "drive_field":
//     "duration_minutes", "buffer_minutes": 5}} → minutes from now until
//     event_start − drive − buffer; skipped when that is already past.
//   - {"$from_step": {"step": <i>, "field": <name>}} → the field from an earlier step's
//     data, else its top level.
package stepvalues

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Resolve resolves the $-directives in args against the earlier steps' results. A non-empty
// skip means skip the step; args is then returned untouched, and resolution stops at the
// first skip.
func Resolve(args map[string]any, prior []map[string]any, now time.Time) (map[string]any, string) {
	out := make(map[string]any, len(args))
	for k, v := range args {
		d, ok := directive(v)
		if !ok {
			out[k] = v
			continue
		}
		val, skip := resolveDirective(d, prior, now)
		if skip != "" {
			return args, skip
		}
		out[k] = val
	}
	return out, ""
}

type dir struct {
	key  string
	spec map[string]any
}

func directive(v any) (dir, bool) {
	m, ok := v.(map[string]any)
	if !ok || len(m) != 1 {
		return dir{}, false
	}
	for k, s := range m {
		if !strings.HasPrefix(k, "$") {
			return dir{}, false
		}
		spec, _ := s.(map[string]any)
		if spec == nil {
			spec = map[string]any{}
		}
		return dir{k, spec}, true
	}
	return dir{}, false
}

func resolveDirective(d dir, prior []map[string]any, now time.Time) (any, string) {
	switch d.key {
	case "$leave_by":
		return leaveBy(d.spec, prior, now)
	case "$from_step":
		return fromStep(d.spec, prior)
	}
	return nil, "unknown directive " + d.key
}

// pyInt is Python's int() on a JSON value: integers, truncated floats, numeric strings, bools.
func pyInt(v any) (int, bool) {
	switch x := v.(type) {
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return 0, false
		}
		return int(x), true
	case json.Number:
		if n, err := strconv.Atoi(x.String()); err == nil {
			return n, true
		}
		if f, err := x.Float64(); err == nil {
			return int(f), true
		}
	case int:
		return x, true
	case int64:
		return int(x), true
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(x)); err == nil {
			return n, true
		}
	}
	return 0, false
}

func intField(spec map[string]any, name string, def int) (int, bool) {
	v, ok := spec[name]
	if !ok {
		return def, true
	}
	return pyInt(v)
}

func stepResult(prior []map[string]any, idx int) (map[string]any, string) {
	if idx < 0 || idx >= len(prior) {
		return nil, fmt.Sprintf("no result for step %d", idx)
	}
	r := prior[idx]
	if r == nil {
		r = map[string]any{}
	}
	return r, ""
}

func dataOf(r map[string]any) map[string]any {
	d, _ := r["data"].(map[string]any)
	if d == nil {
		d = map[string]any{}
	}
	return d
}

func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case float64:
		return x != 0
	case string:
		return x != ""
	}
	return true
}

func driveMinutes(spec map[string]any, prior []map[string]any) (int, string) {
	idx, ok := intField(spec, "drive_from_step", 0)
	if !ok {
		return 0, "invalid drive_from_step"
	}
	field, _ := spec["drive_field"].(string)
	if field == "" {
		field = "duration_minutes"
	}
	r, skip := stepResult(prior, idx)
	if skip != "" {
		return 0, skip
	}
	if !truthy(r["success"]) {
		return 0, fmt.Sprintf("step %d (%v) did not succeed", idx, pyStr(r["command"]))
	}
	raw, ok := dataOf(r)[field]
	if !ok {
		raw = r[field]
	}
	n, ok := pyInt(raw)
	if !ok {
		return 0, fmt.Sprintf("step %d has no numeric '%s'", idx, field)
	}
	return n, ""
}

// pyStr renders a value the way an f-string would (None for nil).
func pyStr(v any) string {
	if v == nil {
		return "None"
	}
	return fmt.Sprint(v)
}

// parseTime is datetime.fromisoformat; a naive value is UTC.
func parseTime(v any) (time.Time, bool) {
	s, ok := v.(string)
	if !ok {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999Z07:00", "2006-01-02T15:04Z07:00"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	for _, layout := range []string{"2006-01-02T15:04:05.999999999", "2006-01-02T15:04", "2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func leaveBy(spec map[string]any, prior []map[string]any, now time.Time) (any, string) {
	drive, skip := driveMinutes(spec, prior)
	if skip != "" {
		return nil, skip
	}
	start, ok := parseTime(spec["event_start"])
	if !ok {
		return nil, "invalid event_start"
	}
	buffer, ok := intField(spec, "buffer_minutes", 5)
	if !ok {
		return nil, "invalid buffer_minutes"
	}
	leaveAt := start.Add(-time.Duration(drive+buffer) * time.Minute)
	rel := int(math.RoundToEven(leaveAt.Sub(now).Minutes())) // Python round(): half to even
	if rel <= 0 {
		return nil, fmt.Sprintf("departure time already passed (%d min)", rel)
	}
	return rel, ""
}

func fromStep(spec map[string]any, prior []map[string]any) (any, string) {
	idx, ok := intField(spec, "step", 0)
	if !ok {
		return nil, "invalid step"
	}
	field, _ := spec["field"].(string)
	r, skip := stepResult(prior, idx)
	if skip != "" {
		return nil, skip
	}
	if v, ok := dataOf(r)[field]; ok {
		return v, ""
	}
	if v, ok := r[field]; ok {
		return v, ""
	}
	return nil, fmt.Sprintf("step %d has no '%s'", idx, pyStr(spec["field"]))
}
