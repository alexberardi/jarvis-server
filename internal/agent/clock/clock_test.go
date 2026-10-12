package clock

import (
	"context"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

func call(t *testing.T, args map[string]any) map[string]any {
	t.Helper()
	ny, _ := time.LoadLocation("America/New_York")
	tool := &Tool{Now: func() time.Time { return time.Date(2026, 10, 12, 4, 55, 0, 0, time.UTC) }, Local: ny}
	out, ok := tool.Call(context.Background(), ToolName, args, node.Request{})
	if !ok {
		t.Fatal("get_current_time not handled")
	}
	return out
}

// TestLocalTimeIsExact: the reason this tool exists. 00:55 is "12:55 AM", not the ambient
// block's quarter-hour "12:45".
func TestLocalTimeIsExact(t *testing.T) {
	for _, args := range []map[string]any{{}, {"location": "here"}, {"location": "local time"}, {"location": "  "}} {
		out := call(t, args)
		if out["message"] != "It's 12:55 AM." || out["current_date"] != "Monday, October 12" || out["location"] != "local" {
			t.Errorf("%v → %v", args, out)
		}
		if out["timezone"] != "America/New_York" {
			t.Errorf("timezone %v", out["timezone"])
		}
	}
}

func TestOtherZones(t *testing.T) {
	for loc, want := range map[string]string{
		"Asia/Tokyo": "It's 1:55 PM in Asia/Tokyo.",
		"tokyo":      "It's 1:55 PM in tokyo.",
		"London":     "It's 5:55 AM in London.",
		"UTC":        "It's 4:55 AM in UTC.",
	} {
		if out := call(t, map[string]any{"location": loc}); out["message"] != want {
			t.Errorf("%s → %v, want %q", loc, out["message"], want)
		}
	}
}

// TestUnknownLocationAnswersLocally: never an error for a location the model made up.
func TestUnknownLocationAnswersLocally(t *testing.T) {
	out := call(t, map[string]any{"location": "the kitchen"})
	if out["success"] != true || out["message"] != "It's 12:55 AM." || out["note"] == nil {
		t.Fatalf("%v", out)
	}
}

func TestOnlyItsOwnTool(t *testing.T) {
	if _, ok := (&Tool{}).Call(context.Background(), "get_weather", nil, node.Request{}); ok {
		t.Fatal("handled another tool")
	}
	defs := (&Tool{}).Available(context.Background())
	if len(defs) != 1 || defs[0].Name != ToolName {
		t.Fatalf("%v", defs)
	}
}

func callAt(t *testing.T, now time.Time, args map[string]any) map[string]any {
	t.Helper()
	ny, _ := time.LoadLocation("America/New_York")
	tool := &Tool{Now: func() time.Time { return now }, Local: ny}
	out, ok := tool.Call(context.Background(), ToolName, args, node.Request{})
	if !ok {
		t.Fatal("get_current_time not handled")
	}
	return out
}

// TestDateQuestions: jarvisd speaks message as is, so "what day is it?" must not answer the time.
func TestDateQuestions(t *testing.T) {
	mon := time.Date(2026, 10, 12, 4, 55, 0, 0, time.UTC)  // Monday 00:55 in New York
	nye := time.Date(2026, 12, 31, 20, 0, 0, 0, time.UTC)  // Thursday 15:00 in New York
	sat := time.Date(2026, 10, 17, 16, 0, 0, 0, time.UTC)  // Saturday noon in New York
	late := time.Date(2026, 10, 12, 20, 0, 0, 0, time.UTC) // Monday 16:00 NY, Tuesday 05:00 Tokyo
	cases := []struct {
		name string
		now  time.Time
		args map[string]any
		want string
		dow  string
		year int
		wknd bool
	}{
		{"default is the time", mon, map[string]any{}, "It's 12:55 AM.", "Monday", 2026, false},
		{"unknown query is the time", mon, map[string]any{"query": "fortnight"}, "It's 12:55 AM.", "Monday", 2026, false},
		{"date", mon, map[string]any{"query": "date"}, "It's Monday, October 12.", "Monday", 2026, false},
		{"weekday", mon, map[string]any{"query": "Weekday"}, "It's Monday.", "Monday", 2026, false},
		{"month", mon, map[string]any{"query": "month"}, "It's October.", "Monday", 2026, false},
		{"year", mon, map[string]any{"query": "year"}, "It's 2026.", "Monday", 2026, false},
		{"weekday, no", mon, map[string]any{"query": "weekend"}, "No, it's Monday.", "Monday", 2026, false},
		{"weekend, yes", sat, map[string]any{"query": "weekend"}, "Yes, it's the weekend.", "Saturday", 2026, true},
		{"tomorrow", mon, map[string]any{"query": "date", "days_offset": float64(1)}, "Tomorrow is Tuesday, October 13.", "Tuesday", 2026, false},
		{"tomorrow, default query", mon, map[string]any{"days_offset": float64(1)}, "Tomorrow is Tuesday, October 13.", "Tuesday", 2026, false},
		{"yesterday", mon, map[string]any{"query": "weekday", "days_offset": float64(-1)}, "Yesterday was Sunday.", "Sunday", 2026, true},
		{"in 3 days, string offset", mon, map[string]any{"query": "weekday", "days_offset": "3"}, "In 3 days it'll be Thursday.", "Thursday", 2026, false},
		{"3 days ago", mon, map[string]any{"query": "date", "days_offset": float64(-3)}, "3 days ago was Friday, October 9.", "Friday", 2026, false},
		{"tomorrow the weekend", mon, map[string]any{"query": "weekend", "days_offset": float64(5)}, "Yes, in 5 days it'll be Saturday.", "Saturday", 2026, true},
		{"tomorrow not the weekend", sat, map[string]any{"query": "weekend", "days_offset": float64(2)}, "No, in 2 days it'll be Monday.", "Monday", 2026, false},
		{"year rollover: date", nye, map[string]any{"query": "date", "days_offset": float64(1)}, "Tomorrow is Friday, January 1, 2027.", "Friday", 2027, false},
		{"year rollover: year", nye, map[string]any{"query": "year", "days_offset": float64(1)}, "Tomorrow is in 2027.", "Friday", 2027, false},
		{"year rollover: month", nye, map[string]any{"query": "month", "days_offset": float64(1)}, "Tomorrow is in January.", "Friday", 2027, false},
		{"offset clamped", mon, map[string]any{"query": "date", "days_offset": float64(100000)}, "In 366 days it'll be Wednesday, October 13, 2027.", "Wednesday", 2027, false},
		{"location: already Tuesday", late, map[string]any{"query": "weekday", "location": "Asia/Tokyo"}, "It's Tuesday in Asia/Tokyo.", "Tuesday", 2026, false},
		{"location + offset", late, map[string]any{"query": "date", "location": "Asia/Tokyo", "days_offset": float64(1)}, "Tomorrow is Wednesday, October 14 in Asia/Tokyo.", "Wednesday", 2026, false},
		{"local + offset at the same instant", late, map[string]any{"query": "date", "days_offset": float64(1)}, "Tomorrow is Tuesday, October 13.", "Tuesday", 2026, false},
	}
	for _, c := range cases {
		out := callAt(t, c.now, c.args)
		if out["message"] != c.want {
			t.Errorf("%s: message %q, want %q", c.name, out["message"], c.want)
		}
		if out["day_of_week"] != c.dow || out["year"] != c.year || out["is_weekend"] != c.wknd {
			t.Errorf("%s: day_of_week %v year %v is_weekend %v, want %s %d %v", c.name, out["day_of_week"], out["year"], out["is_weekend"], c.dow, c.year, c.wknd)
		}
		if out["current_time"] == nil || out["current_date"] == nil {
			t.Errorf("%s: lost the original fields: %v", c.name, out)
		}
	}
}

func TestDaysUntil(t *testing.T) {
	mon := time.Date(2026, 10, 12, 4, 55, 0, 0, time.UTC) // October 12 in New York (still the 12th in UTC too)
	nye := time.Date(2027, 1, 1, 3, 0, 0, 0, time.UTC)    // 22:00 December 31 in New York, already 2027 in UTC
	cases := []struct {
		name  string
		now   time.Time
		until string
		days  int
		want  string
	}{
		{"christmas", mon, "12-25", 74, "There are 74 days until December 25."},
		{"today", mon, "10-12", 0, "Today is October 12."},
		{"tomorrow", mon, "10-13", 1, "There's 1 day until October 13."},
		{"passed this year: next year's", mon, "01-01", 81, "There are 81 days until January 1."},
		{"with a year", mon, "2026-12-25", 74, "There are 74 days until December 25."},
		{"feb 29: the next leap year", mon, "02-29", 505, "There are 505 days until February 29."},
		{"local date, not UTC's", nye, "01-01", 1, "There's 1 day until January 1."},
	}
	for _, c := range cases {
		out := callAt(t, c.now, map[string]any{"until_date": c.until})
		if out["days_until"] != c.days || out["message"] != c.want {
			t.Errorf("%s: days_until %v message %q, want %d %q", c.name, out["days_until"], out["message"], c.days, c.want)
		}
	}
	for _, bad := range []string{"13-01", "02-30", "christmas", "12"} {
		out := callAt(t, mon, map[string]any{"until_date": bad})
		if _, ok := out["days_until"]; ok || out["note"] == nil || out["message"] != "It's 12:55 AM." {
			t.Errorf("%q: %v", bad, out)
		}
	}
}
