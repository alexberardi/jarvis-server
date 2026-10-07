package timewindow

import (
	"strings"
	"testing"
)

// The golden cases are tests/test_time_window.py's: the ones the live model got wrong.

// envelope is the real envelope from the live calls.
const envelope = "Acceptable times: Tue 4-8pm; Wed 5-8pm; Thu 9am-8pm; Fri 9am-8pm; " +
	"Sat 9am-8pm; Sun 9am-8pm\n" +
	"Do not book: Mon 7am-5pm (Work); Tue 8am-4pm (Same day Epic training); " +
	"Wed 7am-5pm (Work)"

// verdict renders Available as "true", "false" or "none".
func verdict(r Result) string {
	if r.Available == nil {
		return "none"
	}
	if *r.Available {
		return "true"
	}
	return "false"
}

func TestCheckVerdicts(t *testing.T) {
	cases := []struct{ env, utterance, want string }{
		// The cases the model failed.
		{envelope, "How about Wednesday at 12?", "false"},
		{envelope, "Can you do Thursday at noon?", "true"},
		{envelope, "Is Tuesday at 3pm okay?", "false"},
		{envelope, "Does Friday at 10am work?", "true"},
		{envelope, "How about Wednesday at 6?", "true"},
		{envelope, "Could I come Thursday at 2pm?", "true"},
		{envelope, "Is Saturday at 8am possible?", "false"},
		{envelope, "How about Monday at 6pm?", "false"},
		// Bare-hour disambiguation.
		{"Acceptable times: Wed 5-8pm", "Wednesday at 6", "true"},
		{"Acceptable times: Thu 9am-8pm", "Thursday at 10", "true"},
		{"Acceptable times: Wed 5-8pm", "Wednesday at 12", "false"},
		{"Acceptable times: Wed 5-8pm", "Wednesday at 6am", "false"},
		// Do not book overrides acceptable.
		{"Acceptable times: Wed 9am-8pm\nDo not book: Wed 12-1pm (Lunch)", "Wednesday at 12:30pm", "false"},
		{"Acceptable times: Wed 9am-8pm\nDo not book: Wed 12-1pm (Lunch)", "Wednesday at 2pm", "true"},
		// Start inclusive, end exclusive.
		{"Acceptable times: Thu 9am-5pm", "Thursday at 9am", "true"},
		{"Acceptable times: Thu 9am-5pm", "Thursday at 5pm", "false"},
		{"Acceptable times: Thu 9am-5pm", "Thursday at 4pm", "true"},
		// Degradation: nothing to judge against.
		{"", "Thursday at noon", "none"},
		{"Acceptable times: (fill in your availability)", "Thursday at noon", "none"},
		// Fall-open hardening.
		{"Acceptable times: whenever works for you\nDo not book: Tue 8am-4pm", "Can you do Tuesday at noon?", "false"},
		{"Acceptable times: Tue Aug 5 4-8pm\nDo not book: Tue Aug 5 8am-4pm", "Tuesday at 1pm?", "false"},
		{"Acceptable times: Tue Aug 5 4-8pm\nDo not book: Tue Aug 5 8am-4pm", "Tuesday at 6pm?", "true"},
		{"Acceptable times: Tuesday 4 p.m. to 8 p.m.\nDo not book: Tuesday 8 a.m. to 4 p.m.", "Tuesday at 10am?", "false"},
		{"Acceptable times: Tuesday 4 p.m. to 8 p.m.\nDo not book: Tuesday 8 a.m. to 4 p.m.", "Tuesday at 5pm?", "true"},
		{"Acceptable times: Tue 4-8pm\nDo not book: Tue 8am-4pm", "How about Tuesday at 6pm?", "true"},
		{"Acceptable times: Tue 4-8pm\nDo not book: Tue 8am-4pm", "Tuesday at 6?", "true"},
		{"Acceptable times: Tuesday afternoons\nDo not book: Tuesday mornings", "Tuesday at 9am?", "none"},
	}
	for _, c := range cases {
		r := Check(c.env, c.utterance)
		if !r.TimeDetected {
			t.Errorf("%q: no time detected", c.utterance)
			continue
		}
		if got := verdict(r); got != c.want {
			t.Errorf("%q against %q: available=%s, want %s", c.utterance, c.env, got, c.want)
		}
	}
}

func TestNoTimeDetected(t *testing.T) {
	r := Check(envelope, "Okay, and the patient's name?")
	if r.TimeDetected || r.Available != nil || r.ProposedLabel != nil {
		t.Fatalf("got %+v", r)
	}
	if _, ok := ParseProposed("How about 3pm?"); ok {
		t.Error("a time with no day parsed")
	}
	if _, ok := ParseProposed("How about Thursday?"); ok {
		t.Error("a day with no time parsed")
	}
}

func TestLabels(t *testing.T) {
	for text, want := range map[string]string{
		"thursday at noon":  "Thursday at noon",
		"Friday at 10am":    "Friday at 10 am",
		"wed at 6":          "Wednesday at 6",
		"Tuesday at 2:30pm": "Tuesday at 2:30 pm",
	} {
		p, ok := ParseProposed(text)
		if !ok || p.Label != want {
			t.Errorf("%q: label %q (ok=%v), want %q", text, p.Label, ok, want)
		}
	}
}

func TestParseWindows(t *testing.T) {
	if w := ParseWindows("Acceptable times: Tue 4-8pm").Acceptable; len(w) != 1 || w[0] != (Interval{1, 16 * 60, 20 * 60}) {
		t.Errorf("inherit meridiem: %+v", w)
	}
	if w := ParseWindows("Acceptable times: Thu 9am-8pm").Acceptable; len(w) != 1 || w[0] != (Interval{3, 9 * 60, 20 * 60}) {
		t.Errorf("explicit am/pm: %+v", w)
	}
	if w := ParseWindows("Do not book: Wed 7am-5pm (Work)").Blocked; len(w) != 1 || w[0] != (Interval{2, 7 * 60, 17 * 60}) {
		t.Errorf("label strip: %+v", w)
	}
	if w := ParseWindows("Acceptable times: Wed nonsense; Thu 9am-8pm").Acceptable; len(w) != 1 || w[0].Day != 3 {
		t.Errorf("bad entry: %+v", w)
	}
}

func TestSummary(t *testing.T) {
	r := Check(envelope, "Thursday at noon")
	if r.AcceptableSummary == nil || !strings.HasPrefix(*r.AcceptableSummary, "Acceptable times: Tue 4-8pm") {
		t.Fatalf("summary %v", r.AcceptableSummary)
	}
}
