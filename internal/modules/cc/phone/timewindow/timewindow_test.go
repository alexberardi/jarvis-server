package timewindow

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// The real envelope from the live calls (session 804c0806 / b6c51d78).
const envelope = "Acceptable times: Tue 4-8pm; Wed 5-8pm; Thu 9am-8pm; Fri 9am-8pm; " +
	"Sat 9am-8pm; Sun 9am-8pm\n" +
	"Do not book: Mon 7am-5pm (Work); Tue 8am-4pm (Same day Epic training); " +
	"Wed 7am-5pm (Work)"

func avail(t *testing.T, env, utt string) *bool {
	t.Helper()
	return Check(env, utt).Available
}

func wantAvail(t *testing.T, env, utt string, want *bool) {
	t.Helper()
	got := avail(t, env, utt)
	if (got == nil) != (want == nil) || (got != nil && *got != *want) {
		t.Errorf("%q / %q: available = %v, want %v", env, utt, show(got), show(want))
	}
}

func show(b *bool) any {
	if b == nil {
		return nil
	}
	return *b
}

var (
	yes = func() *bool { b := true; return &b }()
	no  = func() *bool { b := false; return &b }()
)

// The cases the live model got wrong under /no_think (test_time_window.py).
func TestTheCasesTheModelFailed(t *testing.T) {
	for _, c := range []struct {
		utt  string
		want bool
	}{
		{"How about Wednesday at 12?", false},
		{"Can you do Thursday at noon?", true},
		{"Is Tuesday at 3pm okay?", false},
		{"Does Friday at 10am work?", true},
		{"How about Wednesday at 6?", true},
		{"Could I come Thursday at 2pm?", true},
		{"Is Saturday at 8am possible?", false},
		{"How about Monday at 6pm?", false},
	} {
		r := Check(envelope, c.utt)
		if !r.TimeDetected || r.Available == nil || *r.Available != c.want {
			t.Errorf("%q: got detected=%v available=%v, want %v", c.utt, r.TimeDetected, show(r.Available), c.want)
		}
	}
}

func TestBareHourDisambiguation(t *testing.T) {
	wantAvail(t, "Acceptable times: Wed 5-8pm", "Wednesday at 6", yes)
	wantAvail(t, "Acceptable times: Thu 9am-8pm", "Thursday at 10", yes)
	wantAvail(t, "Acceptable times: Wed 5-8pm", "Wednesday at 12", no)
	wantAvail(t, "Acceptable times: Wed 5-8pm", "Wednesday at 6am", no)
}

func TestDoNotBook(t *testing.T) {
	env := "Acceptable times: Wed 9am-8pm\nDo not book: Wed 12-1pm (Lunch)"
	wantAvail(t, env, "Wednesday at 12:30pm", no)
	wantAvail(t, env, "Wednesday at 2pm", yes)
}

func TestBoundaries(t *testing.T) {
	env := "Acceptable times: Thu 9am-5pm"
	wantAvail(t, env, "Thursday at 9am", yes)
	wantAvail(t, env, "Thursday at 5pm", no)
	wantAvail(t, env, "Thursday at 4pm", yes)
}

func TestParseProposed(t *testing.T) {
	if _, ok := ParseProposed("How about 3pm?"); ok {
		t.Error("a lone time without a day must not parse")
	}
	if _, ok := ParseProposed("How about Thursday?"); ok {
		t.Error("a day without a time must not parse")
	}
	for text, label := range map[string]string{
		"thursday at noon":  "noon",
		"Friday at 10am":    "10 am",
		"wed at 6":          "6",
		"Tuesday at 2:30pm": "2:30 pm",
	} {
		p, ok := ParseProposed(text)
		if !ok || !strings.Contains(p.Label, label) {
			t.Errorf("%q: label %q (ok=%v), want it to contain %q", text, p.Label, ok, label)
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
		t.Errorf("label stripped: %+v", w)
	}
	if w := ParseWindows("Acceptable times: Wed nonsense; Thu 9am-8pm").Acceptable; len(w) != 1 || w[0].Day != 3 {
		t.Errorf("bad entries drop: %+v", w)
	}
}

func TestDegradation(t *testing.T) {
	r := Check(envelope, "Okay, and the patient's name?")
	if r.TimeDetected || r.Available != nil {
		t.Errorf("no time: %+v", r)
	}
	r = Check("", "Thursday at noon")
	if !r.TimeDetected || r.Available != nil {
		t.Errorf("empty envelope: %+v", r)
	}
	wantAvail(t, "Acceptable times: (fill in your availability)", "Thursday at noon", nil)
}

func TestFallOpenHardening(t *testing.T) {
	wantAvail(t, "Acceptable times: whenever works for you\nDo not book: Tue 8am-4pm", "Can you do Tuesday at noon?", no)
	env := "Acceptable times: Tue Aug 5 4-8pm\nDo not book: Tue Aug 5 8am-4pm"
	wantAvail(t, env, "Tuesday at 1pm?", no)
	wantAvail(t, env, "Tuesday at 6pm?", yes)
	env = "Acceptable times: Tuesday 4 p.m. to 8 p.m.\nDo not book: Tuesday 8 a.m. to 4 p.m."
	wantAvail(t, env, "Tuesday at 10am?", no)
	wantAvail(t, env, "Tuesday at 5pm?", yes)
	env = "Acceptable times: Tue 4-8pm\nDo not book: Tue 8am-4pm"
	wantAvail(t, env, "How about Tuesday at 6pm?", yes)
	wantAvail(t, env, "Tuesday at 6?", yes)
	wantAvail(t, "Acceptable times: Tuesday afternoons\nDo not book: Tuesday mornings", "Tuesday at 9am?", nil)
}

// golden is check_time's output from the Python implementation (CC .venv, 2026-10-07),
// rendered as the check-time route's JSON.
const golden = `[{"env": "Acceptable times: Tue 4-8pm; Wed 5-8pm; Thu 9am-8pm; Fri 9am-8pm; Sat 9am-8pm; Sun 9am-8pm\nDo not book: Mon 7am-5pm (Work); Tue 8am-4pm (Same day Epic training); Wed 7am-5pm (Work)", "utt": "How about Wednesday at 12?", "want": {"time_detected": true, "available": false, "proposed_label": "Wednesday at 12", "acceptable_summary": "Acceptable times: Tue 4-8pm; Wed 5-8pm; Thu 9am-8pm; Fri 9am-8pm; Sat 9am-8pm; Sun 9am-8pm"}}, {"env": "Acceptable times: Tue 4-8pm; Wed 5-8pm; Thu 9am-8pm; Fri 9am-8pm; Sat 9am-8pm; Sun 9am-8pm\nDo not book: Mon 7am-5pm (Work); Tue 8am-4pm (Same day Epic training); Wed 7am-5pm (Work)", "utt": "Thursday at noon", "want": {"time_detected": true, "available": true, "proposed_label": "Thursday at noon", "acceptable_summary": "Acceptable times: Tue 4-8pm; Wed 5-8pm; Thu 9am-8pm; Fri 9am-8pm; Sat 9am-8pm; Sun 9am-8pm"}}, {"env": "Acceptable times: Tue 4-8pm; Wed 5-8pm; Thu 9am-8pm; Fri 9am-8pm; Sat 9am-8pm; Sun 9am-8pm\nDo not book: Mon 7am-5pm (Work); Tue 8am-4pm (Same day Epic training); Wed 7am-5pm (Work)", "utt": "Sunday at midnight", "want": {"time_detected": true, "available": false, "proposed_label": "Sunday at midnight", "acceptable_summary": "Acceptable times: Tue 4-8pm; Wed 5-8pm; Thu 9am-8pm; Fri 9am-8pm; Sat 9am-8pm; Sun 9am-8pm"}}, {"env": "Acceptable times: Tue 4-8pm; Wed 5-8pm; Thu 9am-8pm; Fri 9am-8pm; Sat 9am-8pm; Sun 9am-8pm\nDo not book: Mon 7am-5pm (Work); Tue 8am-4pm (Same day Epic training); Wed 7am-5pm (Work)", "utt": "monday 9:30 am", "want": {"time_detected": true, "available": false, "proposed_label": "Monday at 9:30 am", "acceptable_summary": "Acceptable times: Tue 4-8pm; Wed 5-8pm; Thu 9am-8pm; Fri 9am-8pm; Sat 9am-8pm; Sun 9am-8pm"}}, {"env": "Acceptable times: Tue 4-8pm; Wed 5-8pm; Thu 9am-8pm; Fri 9am-8pm; Sat 9am-8pm; Sun 9am-8pm\nDo not book: Mon 7am-5pm (Work); Tue 8am-4pm (Same day Epic training); Wed 7am-5pm (Work)", "utt": "Fri at 7:45pm works?", "want": {"time_detected": true, "available": true, "proposed_label": "Friday at 7:45 pm", "acceptable_summary": "Acceptable times: Tue 4-8pm; Wed 5-8pm; Thu 9am-8pm; Fri 9am-8pm; Sat 9am-8pm; Sun 9am-8pm"}}, {"env": "Acceptable times: Tue 4-8pm; Wed 5-8pm; Thu 9am-8pm; Fri 9am-8pm; Sat 9am-8pm; Sun 9am-8pm\nDo not book: Mon 7am-5pm (Work); Tue 8am-4pm (Same day Epic training); Wed 7am-5pm (Work)", "utt": "Saturday 8:00pm", "want": {"time_detected": true, "available": false, "proposed_label": "Saturday at 8 pm", "acceptable_summary": "Acceptable times: Tue 4-8pm; Wed 5-8pm; Thu 9am-8pm; Fri 9am-8pm; Sat 9am-8pm; Sun 9am-8pm"}}, {"env": "Acceptable times: Tue 4-8pm; Wed 5-8pm; Thu 9am-8pm; Fri 9am-8pm; Sat 9am-8pm; Sun 9am-8pm\nDo not book: Mon 7am-5pm (Work); Tue 8am-4pm (Same day Epic training); Wed 7am-5pm (Work)", "utt": "Saturday at 7:59 p.m.", "want": {"time_detected": true, "available": true, "proposed_label": "Saturday at 7:59 pm", "acceptable_summary": "Acceptable times: Tue 4-8pm; Wed 5-8pm; Thu 9am-8pm; Fri 9am-8pm; Sat 9am-8pm; Sun 9am-8pm"}}, {"env": "Acceptable times: Tue 4-8pm; Wed 5-8pm; Thu 9am-8pm; Fri 9am-8pm; Sat 9am-8pm; Sun 9am-8pm\nDo not book: Mon 7am-5pm (Work); Tue 8am-4pm (Same day Epic training); Wed 7am-5pm (Work)", "utt": "tues at 4", "want": {"time_detected": true, "available": true, "proposed_label": "Tuesday at 4", "acceptable_summary": "Acceptable times: Tue 4-8pm; Wed 5-8pm; Thu 9am-8pm; Fri 9am-8pm; Sat 9am-8pm; Sun 9am-8pm"}}, {"env": "Acceptable times: Tue 4-8pm; Wed 5-8pm; Thu 9am-8pm; Fri 9am-8pm; Sat 9am-8pm; Sun 9am-8pm\nDo not book: Mon 7am-5pm (Work); Tue 8am-4pm (Same day Epic training); Wed 7am-5pm (Work)", "utt": "WEDNESDAY AT 5PM", "want": {"time_detected": true, "available": true, "proposed_label": "Wednesday at 5 pm", "acceptable_summary": "Acceptable times: Tue 4-8pm; Wed 5-8pm; Thu 9am-8pm; Fri 9am-8pm; Sat 9am-8pm; Sun 9am-8pm"}}, {"env": "Acceptable times: Tue 4-8pm; Wed 5-8pm; Thu 9am-8pm; Fri 9am-8pm; Sat 9am-8pm; Sun 9am-8pm\nDo not book: Mon 7am-5pm (Work); Tue 8am-4pm (Same day Epic training); Wed 7am-5pm (Work)", "utt": "Thursday at 25", "want": {"time_detected": false, "available": null, "proposed_label": null, "acceptable_summary": null}}, {"env": "Acceptable times: Tue 4-8pm; Wed 5-8pm; Thu 9am-8pm; Fri 9am-8pm; Sat 9am-8pm; Sun 9am-8pm\nDo not book: Mon 7am-5pm (Work); Tue 8am-4pm (Same day Epic training); Wed 7am-5pm (Work)", "utt": "Thursday at 99 or 10am", "want": {"time_detected": true, "available": true, "proposed_label": "Thursday at 10 am", "acceptable_summary": "Acceptable times: Tue 4-8pm; Wed 5-8pm; Thu 9am-8pm; Fri 9am-8pm; Sat 9am-8pm; Sun 9am-8pm"}}, {"env": "Acceptable times: Tue 4-8pm; Wed 5-8pm; Thu 9am-8pm; Fri 9am-8pm; Sat 9am-8pm; Sun 9am-8pm\nDo not book: Mon 7am-5pm (Work); Tue 8am-4pm (Same day Epic training); Wed 7am-5pm (Work)", "utt": "Thursday 13:30", "want": {"time_detected": true, "available": true, "proposed_label": "Thursday at 13:30", "acceptable_summary": "Acceptable times: Tue 4-8pm; Wed 5-8pm; Thu 9am-8pm; Fri 9am-8pm; Sat 9am-8pm; Sun 9am-8pm"}}, {"env": "Acceptable times: Tue 4-8pm; Wed 5-8pm; Thu 9am-8pm; Fri 9am-8pm; Sat 9am-8pm; Sun 9am-8pm\nDo not book: Mon 7am-5pm (Work); Tue 8am-4pm (Same day Epic training); Wed 7am-5pm (Work)", "utt": "no time here", "want": {"time_detected": false, "available": null, "proposed_label": null, "acceptable_summary": null}}, {"env": "Acceptable times: Tue 4-8pm; Wed 5-8pm; Thu 9am-8pm; Fri 9am-8pm; Sat 9am-8pm; Sun 9am-8pm\nDo not book: Mon 7am-5pm (Work); Tue 8am-4pm (Same day Epic training); Wed 7am-5pm (Work)", "utt": "", "want": {"time_detected": false, "available": null, "proposed_label": null, "acceptable_summary": null}}, {"env": "Acceptable times: Tue 4-8pm; Wed 5-8pm; Thu 9am-8pm; Fri 9am-8pm; Sat 9am-8pm; Sun 9am-8pm\nDo not book: Mon 7am-5pm (Work); Tue 8am-4pm (Same day Epic training); Wed 7am-5pm (Work)", "utt": "   ", "want": {"time_detected": false, "available": null, "proposed_label": null, "acceptable_summary": null}}, {"env": "Acceptable times: Tue 4-8pm; Wed 5-8pm; Thu 9am-8pm; Fri 9am-8pm; Sat 9am-8pm; Sun 9am-8pm\nDo not book: Mon 7am-5pm (Work); Tue 8am-4pm (Same day Epic training); Wed 7am-5pm (Work)", "utt": "Friday, 123 Main St", "want": {"time_detected": true, "available": true, "proposed_label": "Friday at 12", "acceptable_summary": "Acceptable times: Tue 4-8pm; Wed 5-8pm; Thu 9am-8pm; Fri 9am-8pm; Sat 9am-8pm; Sun 9am-8pm"}}, {"env": "Acceptable times: Tue 4-8pm; Wed 5-8pm; Thu 9am-8pm; Fri 9am-8pm; Sat 9am-8pm; Sun 9am-8pm\nDo not book: Mon 7am-5pm (Work); Tue 8am-4pm (Same day Epic training); Wed 7am-5pm (Work)", "utt": "Sunday the 3rd at 2", "want": {"time_detected": true, "available": true, "proposed_label": "Sunday at 3", "acceptable_summary": "Acceptable times: Tue 4-8pm; Wed 5-8pm; Thu 9am-8pm; Fri 9am-8pm; Sat 9am-8pm; Sun 9am-8pm"}}, {"env": "Acceptable times: Tue 4-8pm; Wed 5-8pm; Thu 9am-8pm; Fri 9am-8pm; Sat 9am-8pm; Sun 9am-8pm\nDo not book: Mon 7am-5pm (Work); Tue 8am-4pm (Same day Epic training); Wed 7am-5pm (Work)", "utt": "thursdays at 0", "want": {"time_detected": true, "available": true, "proposed_label": "Thursday at 0", "acceptable_summary": "Acceptable times: Tue 4-8pm; Wed 5-8pm; Thu 9am-8pm; Fri 9am-8pm; Sat 9am-8pm; Sun 9am-8pm"}}, {"env": "Acceptable times: Mon 9-5", "utt": "Monday at 10", "want": {"time_detected": true, "available": true, "proposed_label": "Monday at 10", "acceptable_summary": "Acceptable times: Mon 9-5"}}, {"env": "Acceptable times: Mon 9-5", "utt": "Monday at 4pm", "want": {"time_detected": true, "available": true, "proposed_label": "Monday at 4 pm", "acceptable_summary": "Acceptable times: Mon 9-5"}}, {"env": "Acceptable times: Mon 11-1pm", "utt": "Monday at noon", "want": {"time_detected": true, "available": null, "proposed_label": "Monday at noon", "acceptable_summary": "Acceptable times: Mon 11-1pm"}}, {"env": "Acceptable times: Mon 9:30am-11:15am; Mon 2pm—4pm", "utt": "Monday at 3", "want": {"time_detected": true, "available": true, "proposed_label": "Monday at 3", "acceptable_summary": "Acceptable times: Mon 9:30am-11:15am; Mon 2pm—4pm"}}, {"env": "Acceptable times: Mon 9:30am-11:15am; Mon 2pm—4pm", "utt": "Monday at 11:15am", "want": {"time_detected": true, "available": false, "proposed_label": "Monday at 11:15 am", "acceptable_summary": "Acceptable times: Mon 9:30am-11:15am; Mon 2pm—4pm"}}, {"env": "acceptable times: tue 10am to 2pm\r\ndo not book: tue 12-1pm (lunch) (x)", "utt": "Tuesday at 12:30", "want": {"time_detected": true, "available": false, "proposed_label": "Tuesday at 12:30", "acceptable_summary": "acceptable times: tue 10am to 2pm"}}, {"env": "Do not book: Fri 8pm-9am", "utt": "Friday at 9pm", "want": {"time_detected": true, "available": null, "proposed_label": "Friday at 9 pm", "acceptable_summary": null}}, {"env": "Acceptable times: Fri 13-15", "utt": "Friday at 2pm", "want": {"time_detected": true, "available": null, "proposed_label": "Friday at 2 pm", "acceptable_summary": "Acceptable times: Fri 13-15"}}, {"env": "Acceptable times: Sat 9am-8pm\nDo not book: Sat 2-3pm", "utt": "Saturday at 2", "want": {"time_detected": true, "available": false, "proposed_label": "Saturday at 2", "acceptable_summary": "Acceptable times: Sat 9am-8pm"}}, {"env": "Acceptable times: (calendar unavailable — fill in your availability)", "utt": "Thu at 10am", "want": {"time_detected": true, "available": null, "proposed_label": "Thursday at 10 am", "acceptable_summary": "Acceptable times: (calendar unavailable — fill in your availability)"}}, {"env": "Acceptable times: Wedfoo 5-8pm", "utt": "Wed at 6", "want": {"time_detected": true, "available": true, "proposed_label": "Wednesday at 6", "acceptable_summary": "Acceptable times: Wedfoo 5-8pm"}}, {"env": "Acceptable times: Thu 12am-1am", "utt": "Thursday at 12:30 am", "want": {"time_detected": true, "available": true, "proposed_label": "Thursday at 12:30 am", "acceptable_summary": "Acceptable times: Thu 12am-1am"}}]`

func TestGoldenAgainstPython(t *testing.T) {
	var cases []struct {
		Env  string         `json:"env"`
		Utt  string         `json:"utt"`
		Want map[string]any `json:"want"`
	}
	if err := json.Unmarshal([]byte(golden), &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 25 {
		t.Fatalf("only %d golden cases", len(cases))
	}
	for _, c := range cases {
		b, _ := json.Marshal(Check(c.Env, c.Utt).JSON())
		var got map[string]any
		_ = json.Unmarshal(b, &got)
		if !reflect.DeepEqual(got, c.Want) {
			t.Errorf("%q / %q:\n got  %v\n want %v", c.Env, c.Utt, got, c.Want)
		}
	}
}

func TestJSONShape(t *testing.T) {
	got := Check("", "nothing").JSON()
	want := map[string]any{"time_detected": false, "available": nil, "proposed_label": nil, "acceptable_summary": nil}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v", got)
	}
}

func FuzzCheckNeverPanics(f *testing.F) {
	f.Add(envelope, "Wednesday at 12:99pm to 25")
	f.Add("Acceptable times: Mon 99-1am;;", "mon 0:00 a.m.")
	f.Fuzz(func(_ *testing.T, env, utt string) { _ = Check(env, utt) })
}
