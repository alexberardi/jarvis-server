package prompts

import (
	"strings"
	"testing"
)

// The <calendar> block leads with the exact local time and an explicit instruction to say it
// as is: a 9B model follows a stated rule better than an implied one.
func TestCalendarBlockClock(t *testing.T) {
	got := CalendarBlock("9:07 PM", "Today is Tuesday, October 13, 2026.")
	want := "<calendar>\n" +
		"The current local time is 9:07 PM. When the user asks what time it is, say exactly this " +
		"time: never round it, and never repeat a time from earlier in the conversation. For the time " +
		"in another city or time zone, call get_current_time when it is offered.\n" +
		"For any date or weekday the user asks about, read it from this table; never work a date out yourself:\n" +
		"Today is Tuesday, October 13, 2026.\n</calendar>"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if got := CalendarBlock("", "Today is Tuesday."); strings.Contains(got, "local time") {
		t.Fatalf("no clock, no time line: %q", got)
	}
	if CalendarBlock("9:07 PM", " ") != "" {
		t.Fatal("an empty table renders nothing")
	}
}
