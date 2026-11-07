package timewindow

import (
	"testing"
	"time"
)

func TestResolveDates(t *testing.T) {
	now := time.Date(2026, 11, 12, 14, 0, 0, 0, time.UTC) // Thursday
	for in, want := range map[string]string{
		"the day after tomorrow at 7":  "Saturday at 7",
		"November 13 at 7pm":           "Friday at 7pm",
		"Nov. 11th":                    "Thursday", // passed this year: 2027-11-11
		"the 13th":                     "Friday",
		"the 2nd":                      "Wednesday", // passed this month: December 2nd
		"Friday the 13th at 7":         "Friday at 7",
		"we may 7":                     "we may 7",
		"the 31st":                     "Thursday", // no Nov 31: December 31st
		"February 30":                  "February 30",
		"May 1, 2027":                  "Saturday",
		"Saturday, November 14, 2026!": "Saturday, !",
	} {
		if got := ResolveRelativeDays(in, now); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func FuzzCheckAllNeverPanics(f *testing.F) {
	f.Add("Acceptable times: Mon–Fri 5-8pm, weekends after 6pm", "the 31st at 7 or 12 noon, Sat")
	f.Add("Acceptable times: Fri 6pm or later; Sat", "730-555, $7.30, 7:99pm or 8ish")
	f.Fuzz(func(t *testing.T, env, utt string) {
		now := time.Date(2026, 1, 31, 23, 0, 0, 0, time.UTC)
		for _, r := range CheckAll(ResolveRelativeDays(env, now), ResolveRelativeDays(utt, now)) {
			if r.Available == nil || r.ProposedLabel == nil {
				t.Fatalf("incomplete verdict %+v", r)
			}
		}
	})
}
