package dates

import (
	"testing"
	"time"
)

// Friday, November 13, 2026, the day a computer task took "last month" for November.
func TestExplain(t *testing.T) {
	c := New(time.Date(2026, 11, 13, 21, 0, 0, 0, time.UTC), "UTC")
	for in, want := range map[string]string{
		"Do my budget for last month: download the statements": "last month = October 2026 (2026-10-01 to 2026-10-31)",
		"what did I spend Last  Month and this month":          "last month = October 2026 (2026-10-01 to 2026-10-31); this month = November 2026 (2026-11-01 to 2026-11-30)",
		"book it for tomorrow":                                 "tomorrow = Saturday, November 14, 2026 (2026-11-14)",
		"the day before yesterday, not yesterday":              "day before yesterday = Wednesday, November 11, 2026 (2026-11-11); yesterday = Thursday, November 12, 2026 (2026-11-12)",
		"download last year's statement":                       "last year = 2025 (2025-01-01 to 2025-12-31)",
		"anything from last week":                              "last week = 2026-11-01 to 2026-11-07",
		"summarize this page":                                  "",
		"remind me tonight at 9pm":                             "", // times of day aren't explained
		"the monthly report":                                   "",
	} {
		if got := c.Explain(in); got != want {
			t.Errorf("%q:\n got %q\nwant %q", in, got, want)
		}
	}
	// January: last month is last year's December.
	jan := New(time.Date(2027, 1, 5, 9, 0, 0, 0, time.UTC), "UTC")
	if got := jan.Explain("last month"); got != "last month = December 2026 (2026-12-01 to 2026-12-31)" {
		t.Errorf("january: %q", got)
	}
}
