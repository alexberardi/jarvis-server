package phone

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/timewindow"
)

// The call's time understanding against speech as whisper writes it and briefs as a planning
// model drafts them. The offer matrix is generated: every offer phrasing × lead-in × day
// phrasing × sentence shape × envelope, each with the verdict an oracle computes from what the
// phrase means (not from the parser). The hand tables cover distractors, mixed offers and the
// drafted envelopes. Decisions behind the expectations: docs/decisions/2026-11-06-call-time-phrases.md.

// phraseNow is Thursday 2026-11-12, 2 pm: "tomorrow" and "the 13th" are Friday.
var phraseNow = time.Date(2026, 11, 12, 14, 0, 0, 0, time.UTC)

const (
	mon = iota
	tue
	wed
	thu
	fri
	sat
	sun
	noDay      = -1 // no day said: the envelope's only day, if it has one
	unplacable = -2 // a day that isn't one weekday ("this weekend"): no verdict
)

var dayName = [...]string{"Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"}

func hm(h, m int) int { return h*60 + m }

type envCase struct {
	name, details string
	acc, blk      []timewindow.Interval
}

func iv(day, start, end int) timewindow.Interval {
	return timewindow.Interval{Day: day, Start: start, End: end}
}

func slot(day, m int) timewindow.Interval {
	return iv(day, m-timewindow.Grace, m+timewindow.Grace+1)
}

// matrixEnvs are the envelopes every generated offer is judged against.
var matrixEnvs = []envCase{
	{"slot", "Please book a table for two at Papa V's. If asked: party of two. Acceptable times: Tomorrow at 7 p.m.",
		[]timewindow.Interval{slot(fri, hm(19, 0))}, nil},
	{"ranges", "Acceptable times: Fri 5-8pm; Sat 6-9pm",
		[]timewindow.Interval{iv(fri, hm(17, 0), hm(20, 0)), iv(sat, hm(18, 0), hm(21, 0))}, nil},
	{"blocked", "Acceptable times: Fri 5-9pm\nDo not book: Fri 6-7pm (soccer)",
		[]timewindow.Interval{iv(fri, hm(17, 0), hm(21, 0))}, []timewindow.Interval{iv(fri, hm(18, 0), hm(19, 0))}},
}

// timeKind is what a phrasing needs around it to be a time.
type timeKind int

const (
	clockShaped timeKind = iota // a time on its own: "7:30", "7pm", "quarter to eight", "noon"
	withPrep                    // carries its own time word: "around 7", "after 8"
	runTogether                 // "730", "7 30": a time after an offer word, else a room or a count
	bareHour                    // "8", "eight": a time only after "at", "how about", "can/could do"
)

type timePhrase struct {
	say      string
	readings []int // what the speaker can mean, in minutes; a bare 1-12 has both
	clock    string
	kind     timeKind
}

func both(h, m int) []int { return []int{hm(h, m), hm(h+12, m)} }

var timePhrases = []timePhrase{
	{"7:30", both(7, 30), "7:30", clockShaped},
	{"7.30", both(7, 30), "7:30", clockShaped},
	{"seven thirty", both(7, 30), "7:30", clockShaped},
	{"seven-thirty", both(7, 30), "7:30", clockShaped},
	{"half seven", both(7, 30), "7:30", clockShaped},
	{"half past seven", both(7, 30), "7:30", clockShaped},
	{"half past 7", both(7, 30), "7:30", clockShaped},
	{"quarter past seven", both(7, 15), "7:15", clockShaped},
	{"quarter after 7", both(7, 15), "7:15", clockShaped},
	{"quarter to eight", both(7, 45), "7:45", clockShaped},
	{"a quarter till 8", both(7, 45), "7:45", clockShaped},
	{"ten to eight", both(7, 50), "7:50", clockShaped},
	{"six fifteen", both(6, 15), "6:15", clockShaped},
	{"seven forty-five", both(7, 45), "7:45", clockShaped},
	{"7 o'clock", both(7, 0), "7", clockShaped},
	{"seven o'clock", both(7, 0), "7", clockShaped},
	{"7pm", []int{hm(19, 0)}, "7 pm", clockShaped},
	{"7 p.m.", []int{hm(19, 0)}, "7 pm", clockShaped},
	{"7 PM", []int{hm(19, 0)}, "7 pm", clockShaped},
	{"seven pm", []int{hm(19, 0)}, "7 pm", clockShaped},
	{"7:30pm", []int{hm(19, 30)}, "7:30 pm", clockShaped},
	{"7.30 p.m.", []int{hm(19, 30)}, "7:30 pm", clockShaped},
	{"730pm", []int{hm(19, 30)}, "7:30 pm", clockShaped},
	{"19:30", []int{hm(19, 30)}, "19:30", clockShaped},
	{"noon", []int{hm(12, 0)}, "12 pm", clockShaped},
	{"12 noon", []int{hm(12, 0)}, "12 pm", clockShaped},
	{"midnight", []int{0}, "12 am", clockShaped},
	{"eight ish", both(8, 0), "8", clockShaped},
	{"eight-ish", both(8, 0), "8", clockShaped},
	{"7ish", both(7, 0), "7", clockShaped},
	{"around 7", both(7, 0), "7", withPrep},
	{"around seven", both(7, 0), "7", withPrep},
	{"about 7:30", both(7, 30), "7:30", withPrep},
	{"after 8", both(8, 0), "8", withPrep},
	{"before 6", both(6, 0), "6", withPrep},
	{"730", both(7, 30), "7:30", runTogether},
	{"7 30", both(7, 30), "7:30", runTogether},
	{"715", both(7, 15), "7:15", runTogether},
	{"8", both(8, 0), "8", bareHour},
	{"eight", both(8, 0), "8", bareHour},
	{"7", both(7, 0), "7", bareHour},
}

// leads are what a host says before the time, and which kinds each makes a time. "for eight"
// is a party; "we've got eight" / "I have eight" are a count as often as a time, so a bare hour
// after them is not judged (the model still hears it).
var leads = []struct {
	say       string
	run, bare bool
}{
	{"at", true, true},
	{"how about", true, true},
	{"can do", true, true},
	{"could do", true, true},
	{"we've got", true, false},
	{"I have", true, false},
	{"for", false, false},
	{"", false, false},
}

var dayPhrases = []struct {
	say string
	day int
}{
	{"on Friday", fri}, {"Friday", fri}, {"Fri", fri}, {"on Fri.", fri}, {"this Friday", fri}, {"next Friday", fri},
	{"Friday the 13th", fri}, {"on the 13th", fri}, {"November 13th", fri}, {"tomorrow", fri}, {"tomorrow night", fri},
	{"tonight", thu}, {"today", thu}, {"Thursday", thu}, {"Thurs", thu}, {"Tues", tue},
	{"on Saturday", sat}, {"Sat", sat}, {"the day after tomorrow", sat}, {"on Sunday", sun},
	{"this weekend", unplacable}, {"", noDay},
}

func isTime(kind timeKind, lead struct {
	say       string
	run, bare bool
}) bool {
	switch kind {
	case runTogether:
		return lead.run
	case bareHour:
		return lead.bare
	}
	return true
}

// oracle is the verdict for an intended time: a Do-not-book reading vetoes; else any reading
// inside an Acceptable window is open.
func oracle(env envCase, day int, readings []int) bool {
	for _, m := range readings {
		for _, b := range env.blk {
			if b.Day == day && b.Start <= m && m < b.End {
				return false
			}
		}
	}
	for _, m := range readings {
		for _, a := range env.acc {
			if a.Day == day && a.Start <= m && m < a.End {
				return true
			}
		}
	}
	return false
}

func soleEnvDay(env envCase) int {
	d := env.acc[0].Day
	for _, a := range env.acc {
		if a.Day != d {
			return unplacable
		}
	}
	return d
}

func sentence(parts ...string) string {
	var keep []string
	for _, p := range parts {
		if p != "" {
			keep = append(keep, p)
		}
	}
	return strings.Join(keep, " ")
}

func verdictMap(vs []timewindow.Result) map[string]bool {
	out := map[string]bool{}
	for _, v := range vs {
		out[*v.ProposedLabel] = *v.Available
	}
	return out
}

func envelopeOf(details string) string {
	return timewindow.ResolveRelativeDays(ExtractConstraintEnvelope(details), phraseNow)
}

func TestOfferPhraseMatrix(t *testing.T) {
	t.Logf("%d generated offer cases", len(matrixEnvs)*len(timePhrases)*len(leads)*len(dayPhrases)*2)
	for _, env := range matrixEnvs {
		t.Run(env.name, func(t *testing.T) {
			t.Parallel() // ~35 s under -race on one core
			envText := envelopeOf(env.details)
			for _, tp := range timePhrases {
				for _, lead := range leads {
					for _, dp := range dayPhrases {
						offer := sentence(lead.say, tp.say)
						for shape, heard := range []string{
							sentence(offer, dp.say) + "?",                             // "how about 7:30 on Friday?"
							strings.TrimPrefix(sentence(dp.say+",", offer)+".", ", "), // "Friday, how about 7:30."
						} {
							timeSaid := isTime(tp.kind, lead)
							want := map[string]bool{}
							day := dp.day
							if day == noDay {
								day = soleEnvDay(env)
							}
							if timeSaid && day >= 0 {
								want[dayName[day]+" at "+tp.clock] = oracle(env, day, tp.readings)
							}
							// The gate is open for every time, and (live.MightProposeTime) for any weekday.
							wantGate := timeSaid || dp.day >= 0
							gated, vs := judgeOffer(envText, heard, phraseNow)
							if got := verdictMap(vs); gated != wantGate || !reflect.DeepEqual(got, want) {
								t.Errorf("[%s/%d] %q: gated=%v verdicts=%v, want gated=%v %v", env.name, shape, heard, gated, got, wantGate, want)
							}
						}
					}
				}
			}
		})
	}
}

// Text that must not be read as a time, and offers that mix several times and days.
func TestOfferPhraseTable(t *testing.T) {
	slotEnv, rangesEnv := envelopeOf(matrixEnvs[0].details), envelopeOf(matrixEnvs[1].details)
	cases := []struct {
		env, heard string
		gated      bool
		want       map[string]bool
	}{
		// Distractors.
		{slotEnv, "Is that a table for 2?", false, nil},
		{slotEnv, "Party of 4, got it.", false, nil},
		{slotEnv, "A large pie is $12.50.", false, nil},
		{slotEnv, "We're at 1200 Main Street.", true, nil}, // live gate: "at 12"
		{slotEnv, "We're at 12 Main Street.", true, nil},
		{slotEnv, "We're in suite 715.", false, nil},
		{slotEnv, "Room 730 is ready.", false, nil},
		{slotEnv, "Call us back at 732-555-0142.", true, nil},
		{slotEnv, "Your member ID is DX-77120.", false, nil},
		{slotEnv, "That's 2 people?", false, nil},
		{slotEnv, "So that's 8 people at the table.", false, nil},
		{slotEnv, "We've got eight.", false, nil},
		{slotEnv, "A table for eight?", false, nil},
		{slotEnv, "I had a question about one of the specials.", false, nil},
		{slotEnv, "We've been here since 2026.", false, nil},
		{slotEnv, "The 14th?", true, nil}, // resolves to Saturday; no time
		{slotEnv, "Friday, table for 2.", true, nil},
		{slotEnv, "Friday the 13th works, for 2 people.", true, nil},
		{slotEnv, "We're full until 9.", false, nil},
		{slotEnv, "Same time Saturday?", true, nil}, // no clock said: the model carries it
		{slotEnv, "Eight.", false, nil},             // decided: a lone hour word is no time
		// Days that aren't days.
		{slotEnv, "My friend and I could do 7:30.", true, map[string]bool{"Friday at 7:30": true}},
		{rangesEnv, "My friend and I could do 7:30.", true, nil},
		{slotEnv, "The sun's out, so 7:30?", true, map[string]bool{"Friday at 7:30": true}},
		{slotEnv, "This weekend at 7:30?", true, nil},
		// Several times, several days.
		{slotEnv, "Sorry, Friday at 7 is booked. We have 6:15 or 7:30.", true,
			map[string]bool{"Friday at 7": true, "Friday at 6:15": false, "Friday at 7:30": true}},
		{slotEnv, "We can do 6:15 or 7:30, and 7:30 is the better table.", true,
			map[string]bool{"Friday at 6:15": false, "Friday at 7:30": true}},
		{slotEnv, "Can we do 7 or 8?", true, map[string]bool{"Friday at 7": true, "Friday at 8": false}},
		{rangesEnv, "Saturday at 7 or Sunday at 8?", true, map[string]bool{"Saturday at 7": true, "Sunday at 8": false}},
		{rangesEnv, "Friday is full; 7:30 on Saturday?", true, map[string]bool{"Saturday at 7:30": true}},
		{rangesEnv, "On Friday we have 4:45, or 7:30 on Saturday.", true, map[string]bool{"Friday at 4:45": false, "Saturday at 7:30": true}},
		{slotEnv, "Table for 2 on Friday at 7:30?", true, map[string]bool{"Friday at 7:30": true}},
		{slotEnv, "We're full until 9. I could do 9:15?", true, map[string]bool{"Friday at 9:15": false}},
		{slotEnv, "Tomorrow at 7 p.m. it is.", true, map[string]bool{"Friday at 7 pm": true}},
		{slotEnv, "Tonight at 7 p.m.?", true, map[string]bool{"Thursday at 7 pm": false}},
		{slotEnv, "it doesn't look like it's available anymore I could do 715", true, map[string]bool{"Friday at 7:15": true}},
		{slotEnv, "Hi Jordan, can I put you down for a table for two? We're fully booked for seven, but we have seats at six fifteen or seven thirty.", true,
			map[string]bool{"Friday at 6:15": false, "Friday at 7:30": true}},
		// Nothing to judge against.
		{"", "Friday at 7:30?", true, nil},
		{envelopeOf("Acceptable times: (fill in your availability before calling)"), "Friday at 7:30?", true, nil},
	}
	for _, c := range cases {
		gated, vs := judgeOffer(c.env, c.heard, phraseNow)
		got := verdictMap(vs)
		want := c.want
		if want == nil {
			want = map[string]bool{}
		}
		if gated != c.gated || !reflect.DeepEqual(got, want) {
			t.Errorf("%q against %q: gated=%v verdicts=%v, want gated=%v %v", c.heard, c.env, gated, got, c.gated, want)
		}
	}
}

// The drafted brief's Acceptable / Do-not-book lines, as the planning model writes them.
func TestDraftedEnvelopes(t *testing.T) {
	all := func(days []int, s, e int) []timewindow.Interval {
		var out []timewindow.Interval
		for _, d := range days {
			out = append(out, iv(d, s, e))
		}
		return out
	}
	cases := []envCase{
		{"at", "Acceptable times: Friday at 7pm", []timewindow.Interval{slot(fri, hm(19, 0))}, nil},
		{"no at", "Acceptable times: Friday 7 PM.", []timewindow.Interval{slot(fri, hm(19, 0))}, nil},
		{"tomorrow", "Acceptable times: Tomorrow at 7 p.m.", []timewindow.Interval{slot(fri, hm(19, 0))}, nil},
		{"tonight", "Acceptable times: tonight at 7pm", []timewindow.Interval{slot(thu, hm(19, 0))}, nil},
		{"day after", "Acceptable times: the day after tomorrow at 7pm", []timewindow.Interval{slot(sat, hm(19, 0))}, nil},
		{"date", "Acceptable times: November 13 at 7pm", []timewindow.Interval{slot(fri, hm(19, 0))}, nil},
		{"weekday and date", "Acceptable times: Friday, November 13, 2026 at 7pm", []timewindow.Interval{slot(fri, hm(19, 0))}, nil},
		{"weekday the date", "Acceptable times: Friday the 13th at 7pm", []timewindow.Interval{slot(fri, hm(19, 0))}, nil},
		{"party in line", "Acceptable times: Friday at 7pm for 2 people", []timewindow.Interval{slot(fri, hm(19, 0))}, nil},
		{"two slots", "Acceptable times: Friday at 7pm or 8pm", []timewindow.Interval{slot(fri, hm(19, 0)), slot(fri, hm(20, 0))}, nil},
		{"ranges", "Acceptable times: Fri 5-8pm; Sat 6-9pm",
			[]timewindow.Interval{iv(fri, hm(17, 0), hm(20, 0)), iv(sat, hm(18, 0), hm(21, 0))}, nil},
		{"comma list", "Acceptable times: Fri 5-8pm, Sat 6-9pm",
			[]timewindow.Interval{iv(fri, hm(17, 0), hm(20, 0)), iv(sat, hm(18, 0), hm(21, 0))}, nil},
		{"or days", "Acceptable times: Friday or Saturday at 7pm", []timewindow.Interval{slot(fri, hm(19, 0)), slot(sat, hm(19, 0))}, nil},
		{"day list", "Acceptable times: Fri, Sat 6-9pm", all([]int{fri, sat}, hm(18, 0), hm(21, 0)), nil},
		{"day range", "Acceptable times: Mon–Fri 5-8pm", all([]int{mon, tue, wed, thu, fri}, hm(17, 0), hm(20, 0)), nil},
		{"weekend", "Acceptable times: this weekend 6-9pm", all([]int{sat, sun}, hm(18, 0), hm(21, 0)), nil},
		{"weekends after", "Acceptable times: weekends after 6pm", all([]int{sat, sun}, hm(18, 0), 24*60), nil},
		{"after", "Acceptable times: Friday after 6pm", []timewindow.Interval{iv(fri, hm(18, 0), 24*60)}, nil},
		{"or later", "Acceptable times: Friday 6pm or later", []timewindow.Interval{iv(fri, hm(18, 0), 24*60)}, nil},
		{"before", "Acceptable times: Friday before 2pm", []timewindow.Interval{iv(fri, 0, hm(14, 0))}, nil},
		{"mid-line", "Please call Papa V's Pizza to reserve a table for two people on Friday at 7 PM. If asked: Confirm the party size is two. Acceptable times: Friday at 7 PM.\n- Callback number: 732-555-0142",
			[]timewindow.Interval{slot(fri, hm(19, 0))}, nil},
		{"markdown", "**Acceptable times:** Friday at 7pm\n- **Do not book:** Friday 8-9pm",
			[]timewindow.Interval{slot(fri, hm(19, 0))}, []timewindow.Interval{iv(fri, hm(20, 0), hm(21, 0))}},
		{"two lines", "Acceptable times: Friday at 7pm\nAcceptable times: Saturday at 7pm",
			[]timewindow.Interval{slot(fri, hm(19, 0)), slot(sat, hm(19, 0))}, nil},
		{"do not book", "Acceptable times: Fri 5-9pm\nDo not book: Fri 6-7pm (soccer); tomorrow 8:30pm",
			[]timewindow.Interval{iv(fri, hm(17, 0), hm(21, 0))}, []timewindow.Interval{iv(fri, hm(18, 0), hm(19, 0)), slot(fri, hm(20, 30))}},
		{"placeholder", "Book a cleaning.\nAcceptable times: (fill in your availability before calling)", nil, nil},
		{"calendar placeholder", "Acceptable times: (calendar shows no free windows — edit me)", nil, nil},
		{"typed beside placeholder", "Acceptable times: Friday at 7pm (fill in your availability before calling)",
			[]timewindow.Interval{slot(fri, hm(19, 0))}, nil},
	}
	key := func(ivs []timewindow.Interval) []string {
		var out []string
		for _, i := range ivs {
			out = append(out, fmt.Sprintf("%s %d-%d", dayName[i.Day], i.Start, i.End))
		}
		sort.Strings(out)
		return out
	}
	for _, c := range cases {
		ws := timewindow.ParseWindows(envelopeOf(c.details))
		if !reflect.DeepEqual(key(ws.Acceptable), key(c.acc)) || !reflect.DeepEqual(key(ws.Blocked), key(c.blk)) {
			t.Errorf("%s: %q → %q\n acceptable %v, want %v\n blocked %v, want %v", c.name, c.details, envelopeOf(c.details),
				key(ws.Acceptable), key(c.acc), key(ws.Blocked), key(c.blk))
		}
	}
	// Two Acceptable lines are one summary in the note.
	r := timewindow.CheckAll(envelopeOf("Acceptable times: Friday at 7pm\nAcceptable times: Saturday at 7pm."), "Friday at 9pm?")
	if len(r) != 1 || *r[0].AcceptableSummary != "Acceptable times: Friday at 7pm; Saturday at 7pm." {
		t.Fatalf("summary: %+v", r)
	}
}

// The gate never drops a time CheckAll would judge, whatever the text.
func FuzzGateCoversVerdicts(f *testing.F) {
	for _, tp := range timePhrases {
		f.Add(matrixEnvs[0].details, "how about "+tp.say+" on Friday?")
	}
	f.Add("Acceptable times: Fri 5-8pm", "the 31st at 7, November 31st, 12 noon, 1200 Main St, $7.30")
	f.Fuzz(func(t *testing.T, details, heard string) {
		env := envelopeOf(details)
		gated, vs := judgeOffer(env, heard, phraseNow)
		if !gated && len(timewindow.CheckAll(env, spokenClock(timewindow.ResolveRelativeDays(heard, phraseNow)))) > 0 {
			t.Fatalf("gate dropped a verdict: %q", heard)
		}
		for _, v := range vs {
			if v.Available == nil || v.ProposedLabel == nil || *v.ProposedLabel == "" {
				t.Fatalf("incomplete verdict %+v for %q", v, heard)
			}
		}
	})
}
