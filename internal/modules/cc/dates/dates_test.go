package dates

import (
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// The corrected spec (D8, 03.Q1), as worked examples. New York unless stated.
func TestCorrectedSpec(t *testing.T) {
	tue := "2026-10-06T14:00:00Z" // Tuesday 10:00 EDT
	cases := []struct {
		now, zone string
		keys      []string
		want      []string
		unres     []string
	}{
		// §8.2's examples: local-time modifiers, one combined instant, no stray midnight.
		{tue, "America/New_York", []string{"morning", "next_tuesday"}, []string{"2026-10-13T11:00:00Z"}, nil},
		{tue, "America/New_York", []string{"at_3pm"}, []string{"2026-10-06T19:00:00Z"}, nil},
		{tue, "America/New_York", []string{"at_7_30pm", "tomorrow"}, []string{"2026-10-07T23:30:00Z"}, nil},
		{tue, "America/New_York", []string{"tonight", "at_9pm"}, []string{"2026-10-07T01:00:00Z"}, nil},
		{tue, "America/New_York", []string{"Tomorrow", "Morning", "evening"}, []string{"2026-10-07T11:00:00Z", "2026-10-07T22:00:00Z"}, nil},
		{tue, "America/New_York", []string{"this_weekend", "morning"}, []string{"2026-10-10T11:00:00Z", "2026-10-11T11:00:00Z"}, nil},
		{tue, "America/New_York", []string{"midnight", "tomorrow"}, []string{"2026-10-07T04:00:00Z"}, nil},
		{tue, "America/New_York", []string{"tomorrow", "at_dinner"}, []string{"2026-10-07T22:00:00Z"}, nil},
		{tue, "America/New_York", []string{"evening"}, []string{"2026-10-06T22:00:00Z"}, nil},
		// §8.1: next_<day> is next week's named day (legacy: next_monday was a Sunday).
		{tue, "America/New_York", []string{"next_monday"}, []string{"2026-10-12T04:00:00Z"}, nil},
		{tue, "America/New_York", []string{"next_sunday"}, []string{"2026-10-11T04:00:00Z"}, nil},
		{tue, "America/New_York", []string{"last_friday"}, []string{"2026-10-02T04:00:00Z"}, nil},
		{tue, "America/New_York", []string{"this_friday"}, []string{"2026-10-09T04:00:00Z"}, nil},
		// Relative offsets.
		{tue, "America/New_York", []string{"in_90_minutes"}, []string{"2026-10-06T15:30:00Z"}, nil},
		{tue, "America/New_York", []string{"in_1_hours_30_minutes"}, []string{"2026-10-06T15:30:00Z"}, nil},
		{tue, "America/New_York", []string{"In 2 Hours"}, []string{"2026-10-06T16:00:00Z"}, nil},
		{tue, "America/New_York", []string{"in_2_hours", "morning"}, []string{"2026-10-06T16:00:00Z"}, nil},
		// in_N_days keeps the wall time across the fall-back change (12:00 EDT → 12:00 EST).
		{"2026-10-31T16:00:00Z", "America/New_York", []string{"in_2_days"}, []string{"2026-11-02T17:00:00Z"}, nil},
		// §8.4 DST-correct midnights on both transition days.
		{"2027-03-14T06:30:00Z", "America/New_York", []string{"today", "tomorrow"}, []string{"2027-03-14T05:00:00Z", "2027-03-15T04:00:00Z"}, nil},
		{"2027-11-07T05:30:00Z", "America/New_York", []string{"today", "tomorrow"}, []string{"2027-11-07T04:00:00Z", "2027-11-08T05:00:00Z"}, nil},
		// The spring-forward gap: 02:30 does not exist, it is 03:30 EDT (zoneinfo fold=0).
		{"2027-03-14T06:30:00Z", "America/New_York", []string{"at_2_30am"}, []string{"2027-03-14T07:30:00Z"}, nil},
		// §8.6: calendar years, even on Dec 31 of a leap year.
		{"2028-12-31T12:00:00Z", "UTC", []string{"next_year"}, []string{"2029-01-01T00:00:00Z", "2029-12-31T00:00:00Z"}, nil},
		{"2028-12-31T12:00:00Z", "UTC", []string{"last_year"}, []string{"2027-01-01T00:00:00Z", "2027-12-31T00:00:00Z"}, nil},
		// §8.5: minute offsets survive.
		{tue, "UTC+05:30", []string{"today"}, []string{"2026-10-05T18:30:00Z"}, nil},
		{tue, "UTC-03:30", []string{"today"}, []string{"2026-10-06T03:30:00Z"}, nil},
		// Gap keys (03.Q9) and unknown keys.
		{tue, "UTC", []string{"after_dinner"}, []string{"2026-10-06T19:00:00Z"}, nil},
		{tue, "UTC", []string{"during_breakfast", "tomorrow"}, []string{"2026-10-07T08:00:00Z"}, nil},
		{tue, "UTC", []string{"someday"}, nil, []string{"someday"}},
		{tue, "UTC", []string{"at_home"}, nil, []string{"at_home"}},
		{tue, "Not/AZone", []string{"today"}, []string{"2026-10-06T00:00:00Z"}, nil},
	}
	for _, c := range cases {
		now, _ := time.Parse(time.RFC3339, c.now)
		got, unres := New(now, c.zone).Resolve(c.keys)
		if strings.Join(got, ",") != strings.Join(c.want, ",") || strings.Join(unres, ",") != strings.Join(c.unres, ",") {
			t.Errorf("%s %s %v = %v %v, want %v %v", c.now, c.zone, c.keys, got, unres, c.want, c.unres)
		}
	}
}

func TestParseTime(t *testing.T) {
	cases := map[string][3]int{ // h, m, ok
		"9am": {9, 0, 1}, "3pm": {15, 0, 1}, "9_30am": {9, 30, 1}, "7:30pm": {19, 30, 1}, "7.30pm": {19, 30, 1},
		"730pm": {19, 30, 1}, "1230pm": {12, 30, 1}, "1930": {19, 30, 1}, "12am": {0, 0, 1}, "12pm": {12, 0, 1},
		"0": {0, 0, 1}, "noon": {0, 0, 0}, "25": {0, 0, 0}, "730": {7, 30, 1}, "13pm": {0, 0, 0}, "": {0, 0, 0},
	}
	for in, w := range cases {
		h, m, ok := ParseTime(in)
		if h != w[0] || m != w[1] || ok != (w[2] == 1) {
			t.Errorf("ParseTime(%q) = %d %d %v", in, h, m, ok)
		}
	}
}

// 03.Q11: /generate/date-context never sends null for the node's strict DateContext fields.
func TestDateContextTimezoneShape(t *testing.T) {
	summer, _ := time.Parse(time.RFC3339, "2026-07-01T12:00:00Z")
	cases := map[string][3]any{
		"":                 {"UTC", "UTC", false},
		"Not/AZone":        {"UTC", "UTC", false},
		"Local":            {"UTC", "UTC", false},
		"UTC+05:30":        {"UTC+05:30", "UTC+05:30", false},
		"UTC-5":            {"Etc/GMT+5", "Etc/GMT+5", false},
		"America/New_York": {"America/New_York", "America/New_York", true},
	}
	for zone, w := range cases {
		o := New(summer, zone).Object()
		tz, _ := o.Get("timezone")
		got := [3]any{og(tz, "user_timezone"), og(tz, "current_timezone"), og(tz, "is_dst")}
		if got != w {
			t.Errorf("%q timezone = %v, want %v", zone, got, w)
		}
	}
	// The wire shape round-trips as JSON with the legacy top-level keys in order.
	keys := New(summer, "UTC").Object().Keys()
	want := "current relative_dates weekend weeks months years weekdays timezone time_expressions"
	if strings.Join(keys, " ") != want {
		t.Errorf("keys %v", keys)
	}
}

func TestInjectDates(t *testing.T) {
	now, _ := time.Parse(time.RFC3339, "2026-10-06T14:00:00Z") // Tue 10:00 EDT
	c := New(now, "America/New_York")
	props := mustObj(t, `{"resolved_datetimes":{"type":"array","items":{"type":"string","format":"date-time"}},"when":{"type":"string","format":"date-time"},"city":{"type":"string"}}`)
	cases := []struct {
		args string
		turn []string
		want string
	}{
		// Empty params take the turn's keys (scalar: the first).
		{`{"city": "Paris"}`, []string{"tomorrow"}, `{"city": "Paris", "resolved_datetimes": ["2026-10-07T04:00:00Z"], "when": "2026-10-07T04:00:00Z"}`},
		// No keys at all: today.
		{`{"resolved_datetimes": [], "when": ""}`, nil, `{"resolved_datetimes": ["2026-10-06T04:00:00Z"], "when": "2026-10-06T04:00:00Z"}`},
		// §8.3: a bucket key keeps all its dates (legacy: Saturday only).
		{`{"resolved_datetimes": ["this_weekend"], "when": "2026-10-06T20:00:00Z"}`, nil, `{"resolved_datetimes": ["2026-10-10T04:00:00Z", "2026-10-11T04:00:00Z"], "when": "2026-10-06T20:00:00Z"}`},
		// Keys in the args resolve together: one combined instant.
		{`{"resolved_datetimes": ["tomorrow", "morning"], "when": "tomorrow"}`, nil, `{"resolved_datetimes": ["2026-10-07T11:00:00Z"], "when": "2026-10-07T04:00:00Z"}`},
		// An unknown key takes the turn's first date, else today (no LLM fallback, D40).
		{`{"resolved_datetimes": ["whenever"], "when": "whenever"}`, []string{"next_week"}, `{"resolved_datetimes": ["2026-10-11T04:00:00Z", "2026-10-12T04:00:00Z", "2026-10-13T04:00:00Z", "2026-10-14T04:00:00Z", "2026-10-15T04:00:00Z", "2026-10-16T04:00:00Z", "2026-10-17T04:00:00Z"], "when": "2026-10-11T04:00:00Z"}`},
		{`{"resolved_datetimes": ["whenever"], "when": "whenever"}`, nil, `{"resolved_datetimes": ["2026-10-06T04:00:00Z"], "when": "2026-10-06T04:00:00Z"}`},
		// Undecodable args are treated as {}.
		{`{nope`, nil, `{"resolved_datetimes": ["2026-10-06T04:00:00Z"], "when": "2026-10-06T04:00:00Z"}`},
	}
	for _, cs := range cases {
		got, changed := c.InjectDates(cs.args, props, cs.turn)
		if !changed || got != cs.want {
			t.Errorf("InjectDates(%s, %v)\n got %s %v\nwant %s", cs.args, cs.turn, got, changed, cs.want)
		}
	}
	// Already ISO everywhere: untouched, byte for byte.
	in := `{"resolved_datetimes":["2026-10-07T04:00:00Z"],"when":"2026-10-07T04:00:00+00:00"}`
	if got, changed := c.InjectDates(in, props, []string{"today"}); changed || got != in {
		t.Errorf("ISO args changed: %s", got)
	}
	if got, changed := c.InjectDates(`[1]`, props, nil); changed || got != `[1]` {
		t.Error("non-object args must pass through")
	}
}

func TestSchemaProperties(t *testing.T) {
	v, _ := pyjson.Loads(`[{"type":"function","function":{"name":"a","parameters":{"properties":{"x":{}}}}},{"name":"b","parameters":{"properties":{"y":{}}}},{"type":"function","function":{"name":"a","parameters":{"properties":{"z":{}}}}}]`)
	var tools []*pyjson.Object
	for _, x := range v.([]any) {
		tools = append(tools, x.(*pyjson.Object))
	}
	if p := SchemaProperties(tools, "a"); p == nil || p.Keys()[0] != "z" {
		t.Error("later definition must win")
	}
	if p := SchemaProperties(tools, "b"); p == nil || p.Keys()[0] != "y" {
		t.Error("bare definition")
	}
	if SchemaProperties(tools, "c") != nil {
		t.Error("unknown tool")
	}
}

func mustObj(t *testing.T, s string) *pyjson.Object {
	t.Helper()
	v, err := pyjson.Loads(s)
	if err != nil {
		t.Fatal(err)
	}
	return v.(*pyjson.Object)
}
