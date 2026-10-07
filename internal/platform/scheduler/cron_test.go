package scheduler

import (
	"testing"
	"time"
)

func TestCronNext(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	base := time.Date(2026, 10, 6, 20, 58, 30, 0, time.UTC) // a Tuesday
	cases := []struct {
		expr  string
		after time.Time
		want  time.Time
	}{
		{"* * * * *", base, time.Date(2026, 10, 6, 20, 59, 0, 0, time.UTC)},
		{"0 21 * * *", base, time.Date(2026, 10, 6, 21, 0, 0, 0, time.UTC)},
		{"*/15 * * * *", base, time.Date(2026, 10, 6, 21, 0, 0, 0, time.UTC)},
		{"30 7 * * 1-5", base, time.Date(2026, 10, 7, 7, 30, 0, 0, time.UTC)}, // weekday mornings
		{"0 9 * * 0", base, time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)},    // Sunday
		{"0 9 * * 7", base, time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)},    // 7 = Sunday too
		{"0 0 1 * *", base, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)},     // first of month
		{"0 8 1,15 * *", base, time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC)}, // list
		{"0 12 13 * 5", base, time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)},  // dom OR dow: Friday the 9th comes first
		{"0 0 29 2 *", base, time.Date(2028, 2, 29, 0, 0, 0, 0, time.UTC)},    // leap day
		{"0 21 * * *", base.In(ny), time.Date(2026, 10, 6, 21, 0, 0, 0, ny)},  // local time
		// DST spring-forward in New York (2027-03-14): 02:30 doesn't exist that day, so it
		// fires once at the first valid instant after it, 03:00 EDT.
		{"30 2 * * *", time.Date(2027, 3, 13, 3, 0, 0, 0, ny), time.Date(2027, 3, 14, 7, 0, 0, 0, time.UTC)},
		{"30 2 * * *", time.Date(2027, 3, 14, 7, 0, 0, 0, time.UTC).In(ny), time.Date(2027, 3, 15, 2, 30, 0, 0, ny)},
	}
	for _, c := range cases {
		cr, err := ParseCron(c.expr)
		if err != nil {
			t.Fatalf("%s: %v", c.expr, err)
		}
		got, ok := cr.Next(c.after)
		if !ok || !got.Equal(c.want) {
			t.Errorf("%q after %v: got %v (ok=%v), want %v", c.expr, c.after, got, ok, c.want)
		}
	}
}

func TestCronNeverMatches(t *testing.T) {
	c, err := ParseCron("0 0 31 2 *")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Next(time.Now()); ok {
		t.Fatal("Feb 31 matched")
	}
}

func TestCronParseErrors(t *testing.T) {
	for _, bad := range []string{"", "* * * *", "60 * * * *", "* 24 * * *", "* * 0 * *", "* * * 13 *",
		"* * * * 8", "*/0 * * * *", "5-1 * * * *", "a * * * *", "1-x * * * *"} {
		if _, err := ParseCron(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// TestCronDST pins the DST rules (docs/cc/08 §11): a skipped time fires once at the first
// valid instant after the gap; a repeated time fires once, on its first occurrence.
func TestCronDST(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	lon, _ := time.LoadLocation("Europe/London")
	utc := func(y int, mo time.Month, d, h, mi int) time.Time { return time.Date(y, mo, d, h, mi, 0, 0, time.UTC) }
	cases := []struct {
		name  string
		expr  string
		loc   *time.Location
		after time.Time   // UTC
		want  []time.Time // successive fires (UTC)
	}{
		{"NY spring gap daily", "30 2 * * *", ny, utc(2027, 3, 13, 12, 0),
			[]time.Time{utc(2027, 3, 14, 7, 0), utc(2027, 3, 15, 6, 30)}},
		{"NY spring every 15 min collapses the gap", "*/15 * * * *", ny, utc(2027, 3, 14, 6, 40), // 01:40 EST
			[]time.Time{utc(2027, 3, 14, 6, 45), utc(2027, 3, 14, 7, 0), utc(2027, 3, 14, 7, 15)}},
		{"NY fall repeat fires once", "30 1 * * *", ny, utc(2027, 11, 7, 4, 0), // 00:00 EDT
			[]time.Time{utc(2027, 11, 7, 5, 30), utc(2027, 11, 8, 6, 30)}},
		{"NY 8am weekdays across fall back", "0 8 * * 1-5", ny, utc(2027, 11, 5, 13, 0), // Fri 09:00 EDT
			[]time.Time{utc(2027, 11, 8, 13, 0), utc(2027, 11, 9, 13, 0)}},
		{"London spring gap", "30 1 * * *", lon, utc(2027, 3, 27, 12, 0), // 01:30 is skipped on the 28th
			[]time.Time{utc(2027, 3, 28, 1, 0), utc(2027, 3, 29, 0, 30)}},
		{"London fall repeat", "30 1 * * *", lon, utc(2027, 10, 30, 12, 0),
			[]time.Time{utc(2027, 10, 31, 0, 30), utc(2027, 11, 1, 1, 30)}},
	}
	for _, c := range cases {
		cr, err := ParseCron(c.expr)
		if err != nil {
			t.Fatal(err)
		}
		at := c.after.In(c.loc)
		for i, want := range c.want {
			got, ok := cr.Next(at)
			if !ok || !got.Equal(want) {
				t.Fatalf("%s: fire %d after %v: got %v (ok=%v), want %v", c.name, i, at, got.UTC(), ok, want)
			}
			at = got.Add(3 * time.Second) // the scheduler advances from the (slightly later) fire time
		}
	}
}
