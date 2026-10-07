package stepvalues

import (
	"reflect"
	"testing"
	"time"
)

// Golden cases from tests/test_step_value_resolver.py.

var now = time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)

func drive(minutes any, success bool) map[string]any {
	r := map[string]any{"command": "get_drive_time", "success": success, "label": "drive", "data": nil}
	if success {
		r["data"] = map[string]any{"duration_minutes": minutes}
	}
	return r
}

func leave(spec map[string]any) map[string]any { return map[string]any{"$leave_by": spec} }

func TestResolve(t *testing.T) {
	cases := []struct {
		name     string
		args     map[string]any
		prior    []map[string]any
		want     map[string]any // nil: expect a skip
		wantSkip string         // "" with want == nil: any skip
	}{
		{"leave_by computes relative minutes", // event 13:00, drive 18, buffer 5 → leave 12:37
			map[string]any{"action": "set", "text": "Leave for Dentist", "relative_minutes": leave(map[string]any{
				"event_start": "2026-08-13T13:00:00+00:00", "drive_from_step": float64(0), "buffer_minutes": float64(5)})},
			[]map[string]any{drive(float64(18), true)},
			map[string]any{"action": "set", "text": "Leave for Dentist", "relative_minutes": 37}, ""},
		{"departure already passed",
			map[string]any{"relative_minutes": leave(map[string]any{
				"event_start": "2026-08-13T12:10:00+00:00", "drive_from_step": float64(0), "buffer_minutes": float64(5)})},
			[]map[string]any{drive(float64(30), true)}, nil, "departure time already passed (-25 min)"},
		{"from_step substitutes a prior field",
			map[string]any{"minutes": map[string]any{"$from_step": map[string]any{"step": float64(0), "field": "duration_minutes"}}},
			[]map[string]any{drive(float64(22), true)}, map[string]any{"minutes": float64(22)}, ""},
		{"failed drive result skips",
			map[string]any{"relative_minutes": leave(map[string]any{"event_start": "2026-08-13T13:00:00+00:00", "drive_from_step": float64(0)})},
			[]map[string]any{drive(float64(0), false)}, nil, "step 0 (get_drive_time) did not succeed"},
		{"missing step skips",
			map[string]any{"relative_minutes": leave(map[string]any{"event_start": "2026-08-13T13:00:00+00:00", "drive_from_step": float64(2)})},
			nil, nil, "no result for step 2"},
		{"plain args pass through",
			map[string]any{"action": "set", "text": "hi", "relative_minutes": float64(20)}, nil,
			map[string]any{"action": "set", "text": "hi", "relative_minutes": float64(20)}, ""},
		{"naive event_start is UTC",
			map[string]any{"relative_minutes": leave(map[string]any{"event_start": "2026-08-13T13:00:00", "drive_from_step": float64(0), "buffer_minutes": float64(0)})},
			[]map[string]any{drive(float64(20), true)}, map[string]any{"relative_minutes": 40}, ""},
		{"numeric string drive time",
			map[string]any{"m": leave(map[string]any{"event_start": "2026-08-13T13:00:00Z", "drive_from_step": float64(0), "buffer_minutes": float64(0)})},
			[]map[string]any{drive("20", true)}, map[string]any{"m": 40}, ""},
		{"non-numeric drive time skips",
			map[string]any{"m": leave(map[string]any{"event_start": "2026-08-13T13:00:00Z"})},
			[]map[string]any{drive("soon", true)}, nil, "step 0 has no numeric 'duration_minutes'"},
		{"unknown directive skips",
			map[string]any{"m": map[string]any{"$nope": map[string]any{}}}, nil, nil, "unknown directive $nope"},
		{"a two-key dict is not a directive",
			map[string]any{"m": map[string]any{"$a": 1, "$b": 2}}, nil, map[string]any{"m": map[string]any{"$a": 1, "$b": 2}}, ""},
		{"from_step missing field",
			map[string]any{"m": map[string]any{"$from_step": map[string]any{"step": float64(0), "field": "eta"}}},
			[]map[string]any{drive(float64(1), true)}, nil, "step 0 has no 'eta'"},
		{"from_step falls back to the top level",
			map[string]any{"m": map[string]any{"$from_step": map[string]any{"step": float64(0), "field": "label"}}},
			[]map[string]any{drive(float64(1), true)}, map[string]any{"m": "drive"}, ""},
	}
	for _, c := range cases {
		got, skip := Resolve(c.args, c.prior, now)
		if c.want == nil {
			if skip == "" || (c.wantSkip != "" && skip != c.wantSkip) {
				t.Errorf("%s: skip %q, want %q", c.name, skip, c.wantSkip)
			}
			if !reflect.DeepEqual(got, c.args) {
				t.Errorf("%s: a skip must return the args untouched, got %v", c.name, got)
			}
			continue
		}
		if skip != "" || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v (skip %q), want %v", c.name, got, skip, c.want)
		}
	}
}

func TestRoundHalfToEven(t *testing.T) {
	// 12:00 → leave at 12:02:30 is 2.5 min: Python's round() gives 2.
	args := map[string]any{"m": leave(map[string]any{"event_start": "2026-08-13T12:07:30Z", "buffer_minutes": float64(0)})}
	got, skip := Resolve(args, []map[string]any{drive(float64(5), true)}, now)
	if skip != "" || got["m"] != 2 {
		t.Fatalf("got %v %q", got, skip)
	}
}
