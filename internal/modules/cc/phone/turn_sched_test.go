package phone

import (
	"context"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone/live"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
)

// The brief's time window vetoes a time the business proposes, until the caller has answered a
// question with a time of their own mid-call: theirs is newer than the plan.
func TestCallerTimeAnswerLiftsTheVeto(t *testing.T) {
	rt := &callRuntime{brief: live.Brief{Goal: "book a table", Constraints: "Acceptable times: Fri 6-7pm"}}
	p := (&Service{}).newTurnPipeline(rt, nil)
	heard := "We can do Friday at 9pm instead."
	if p.schedulingNote(heard) == "" {
		t.Fatal("no verdict on a proposed time outside the window")
	}
	p.timesFromCaller = true
	if note := p.schedulingNote(heard); note != "" {
		t.Fatalf("still vetoed after the caller's answer: %q", note)
	}
}

// The outcome is judged against what the caller approved mid-call, not only the plan.
func TestAssessmentHearsTheCallersDecisions(t *testing.T) {
	f := &fakeLLM{assess: `{"summary": "Booked 7:30.", "goal_achieved": true}`}
	rt := &callRuntime{brief: live.Brief{Goal: "book Friday 7pm", InitiatorName: "Alex"}}
	p := (&Service{LLM: f}).newTurnPipeline(rt, nil)
	p.records = append(p.records, turnRecord{})
	p.answers = []string{`asked "7 is full; 6:15 or 7:30?", answered "7:30"`}
	if _, ok := p.s.assess(context.Background(), p, "book Friday 7pm"); ok == nil || !*ok {
		t.Fatalf("achieved = %v", ok)
	}
	msgs := f.assessed[0].Messages
	if got := *msgs[len(msgs)-1].Content.Text; !strings.Contains(got, `answered "7:30"`) || !strings.Contains(got, "Alex approved") {
		t.Fatalf("instruction: %q", got)
	}
}

// "7 is booked; we have 6:15 or 7:30" against a brief for Friday at 7: the alternatives are
// flagged and the caller is to be asked, instead of the refused 7 being called open.
func TestOfferedAlternativesOutsideTheBriefAreFlagged(t *testing.T) {
	rt := &callRuntime{brief: live.Brief{Goal: "book a table", InitiatorName: "Alex",
		Constraints: "Acceptable times: Friday at 7pm"}}
	p := (&Service{}).newTurnPipeline(rt, nil)
	note := p.schedulingNote("Sorry, Friday at 7:00 PM is fully booked. We do have 6:15 PM or 7:30 PM available for two.")
	// 7:30 is within the grace of a 7pm slot: take it, no question to the caller.
	// 7 is refused (not an offer), 6:15 is outside, 7:30 is within the grace: book it, no question.
	for _, want := range []string{"They can't do Friday at 7 pm.", "Friday at 6:15 pm is outside", "Friday at 7:30 pm is within",
		"book it with them now; don't ask Alex first"} {
		if !strings.Contains(note, want) {
			t.Fatalf("note lacks %q: %q", want, note)
		}
	}
	if strings.Contains(note, "ESCALATE: …") {
		t.Fatalf("note: %q", note)
	}
	if strings.Contains(note, "OPEN on the caller's calendar. Treat it as available") {
		t.Fatalf("called a refused slot open: %q", note)
	}
}

func TestSpokenOffersWithNoDayAreFlagged(t *testing.T) {
	// Qwen3.5-9B's host, verbatim: no weekday, times spelled out.
	rt := &callRuntime{brief: live.Brief{Goal: "book a table", InitiatorName: "Alex",
		Constraints: "Acceptable times: Friday at 7 PM."}}
	p := (&Service{}).newTurnPipeline(rt, nil)
	note := p.schedulingNote("Hi Jordan, can I put you down for a table for two? We're fully booked for seven, but we have seats at six fifteen or seven thirty.")
	if !strings.Contains(note, "Friday at 6:15 is outside") || !strings.Contains(note, "Friday at 7:30 is within") {
		t.Fatalf("note: %q", note)
	}
}

func TestSpokenClock(t *testing.T) {
	for in, want := range map[string]string{
		"six fifteen or seven-thirty": "6:15 or 7:30",
		"seven o'clock":               "7 o'clock",
		"seven forty five":            "7:45",
		"eight pm":                    "8 pm",
		"a table for two":             "a table for two",
	} {
		if got := spokenClock(in); got != want {
			t.Errorf("spokenClock(%q) = %q, want %q", in, got, want)
		}
	}
	if !mightBeTime("7:30 works, book that") || !mightBeTime("six fifteen is fine") || mightBeTime("a table for two") {
		t.Fatal("mightBeTime")
	}
}

func TestCopiedPromptExampleIsNotAsked(t *testing.T) {
	// Qwen3.5-9B on a pizza booking, after hearing "Um...".
	evs := dropCopiedExample([]any{live.Escalate{Question: "what is the patient's date of birth?"}, live.Hangup{}}, "Um...")
	if len(evs) != 1 {
		t.Fatalf("events: %v", evs)
	}
	if evs := dropCopiedExample([]any{live.Escalate{Question: "What is the patient's date of birth?"}}, "And the date of birth?"); len(evs) != 1 {
		t.Fatal("dropped a real question the business asked")
	}
	if spokenClock("it doesn't look like it's available anymore I could do 715") != "it doesn't look like it's available anymore I could do 7:15" ||
		spokenClock("we're at 1200 Main Street") != "we're at 1200 Main Street" {
		t.Fatal("run-together clock")
	}
	if holdRE.MatchString("Of course, take your time.") || !holdRE.MatchString("Let me check, one moment.") {
		t.Fatal("holdRE")
	}
	if !mightBeTime("I could do 7.30. Does that work?") || mightBeTime("a large pie is $12.50") {
		t.Fatal("dotted clock")
	}
}

func TestOnlyOffersOutsideTheWindowMeansCallBack(t *testing.T) {
	rt := &callRuntime{brief: live.Brief{Goal: "book a table", InitiatorName: "Alex",
		Constraints: "Acceptable times: Friday at 7 PM."}}
	p := (&Service{}).newTurnPipeline(rt, nil)
	note := p.schedulingNote("We're full until 9. I could do 9:15?")
	if !strings.Contains(note, "check with Alex and call back") || !strings.Contains(note, "do not use [ESCALATE]") {
		t.Fatalf("note: %q", note)
	}
	p.schedulingNote("Really, 9:15 is all I have.")
	if len(p.outcomeFacts) != 1 || p.outcomeFacts[0] != "offered Friday at 9:15, outside your acceptable times" {
		t.Fatalf("facts: %v", p.outcomeFacts)
	}
	// 7:15 for a 7pm table is fine.
	if note := p.schedulingNote("I can do 715 if that works."); !strings.Contains(note, "within Alex's acceptable times: if they're offering it, accept it") {
		t.Fatalf("7:15 note: %q", note)
	}
}

func TestDedupeFacts(t *testing.T) {
	got := dedupeFacts([]string{"offered Tuesday at 1, outside your acceptable times", "booked Wednesday at 3pm",
		"booked cleaning for Jordan Avery on Wednesday at 3pm", "booked Wednesday at 3pm"})
	if len(got) != 2 || got[1] != "booked cleaning for Jordan Avery on Wednesday at 3pm" {
		t.Fatalf("facts: %q", got)
	}
}

func TestARepeatedOfferIsNotJudgedAgain(t *testing.T) {
	rt := &callRuntime{brief: live.Brief{Goal: "book a table", Constraints: "Acceptable times: Friday at 7pm"}}
	p := (&Service{}).newTurnPipeline(rt, nil)
	if p.schedulingNote("Fully booked at 7. I can do 6:15 PM or 7:30 PM on Friday.") == "" {
		t.Fatal("no note on the offers")
	}
	// Gemma 4 stalled 20 s when the host's "Yes, 7:30 PM. Can I get a name?" re-noted 7:30.
	if note := p.schedulingNote("Yes, we can do 7:30 PM on Friday. Can I get a name?"); note != "" {
		t.Fatalf("re-noted an offer: %q", note)
	}
	if p.schedulingNote("Or Friday at 9:15 PM?") == "" {
		t.Fatal("a new offer got no note")
	}
}

func TestNoThinkOnlyForModelsThatTakeIt(t *testing.T) {
	msgs := []chatMsg{{"system", "You are Jarvis.\n\n/no_think"}, {"assistant", "Hi."}, {"user", "Papa V's? /no_think"}, {"system", "/no_think"}}
	text := func(ms []llm.Message) string {
		var b strings.Builder
		for _, m := range ms {
			b.WriteString(m.Role + ":" + *m.Content.Text + "|")
		}
		return b.String()
	}
	if got := text((&callRuntime{suffix: true}).wire(msgs)); strings.Count(got, "/no_think") != 3 {
		t.Fatalf("Qwen lost its directive: %s", got)
	}
	// Gemma 4 read "/no_think" as text on every turn of a real call.
	if got := text((&callRuntime{}).wire(msgs)); got != "system:You are Jarvis.|assistant:Hi.|user:Papa V's?|" {
		t.Fatalf("Gemma got: %s", got)
	}
}

func TestCallRuntimeTakesTheResolvedThinkingSwitch(t *testing.T) {
	s := &Service{}
	if !s.thinkingSuffix(t.Context()) {
		t.Fatal("unwired should keep the legacy directive")
	}
	s.ThinkingSuffix = func(context.Context) bool { return false }
	if s.thinkingSuffix(t.Context()) {
		t.Fatal("the hook was ignored")
	}
}

func TestARefusedTimeIsNotAnOffer(t *testing.T) {
	rt := &callRuntime{brief: live.Brief{Goal: "book a table", InitiatorName: "Jordan", Constraints: "Acceptable times: Friday at 7pm"}}
	for heard, want := range map[string][]string{
		// Whisper's text of the voice-mode baseline call where Gemma 4 kept asking for 7.
		"I'm sorry, we are fully booked at 7.00 this Friday.  I can offer you 6.15 PM or 7.30 PM for two.": {
			"They can't do Friday at 7", "Friday at 7:30"},
		"Friday at 7 isn't available, but we have 7:30.":                     {"They can't do Friday at 7", "Friday at 7:30"},
		"We can't do 7 on Friday. Nothing until 9:15.":                       {"They can't do Friday at 7", "call back"},
		"Sure, Friday at 7 works. What's the name?":                          {"within Jordan's acceptable times: if they're offering it, accept it"},
		"We're closed Friday at 7 for a private party; 6:45 is open though.": {"They can't do Friday at 7", "Friday at 6:45 is within"},
	} {
		p := (&Service{}).newTurnPipeline(rt, nil)
		note := p.schedulingNote(heard)
		for _, w := range want {
			if !strings.Contains(note, w) {
				t.Errorf("%q\nnote lacks %q:\n%s", heard, w, note)
			}
		}
	}
}

func TestAnAppointmentDateIsNotTheDateOfBirth(t *testing.T) {
	dob := []live.RestrictedField{{Key: "date_of_birth", Label: "Date of birth", Value: "November 12, 1978"}}
	for s, want := range map[string]bool{
		// The real call: every line naming the new appointment was replaced by a refusal.
		"I'd like to move it to Thursday, November 12, around 4:30.": false,
		"Her date of birth is November 12, 1978.":                    true,
		"She was born November 12.":                                  true,
		"That's November 12 1978.":                                   true,
		"The birthday on file is the 12th of November.":              true,
	} {
		if got := len(restrictedIn(s, dob)) > 0; got != want {
			t.Errorf("%q: flagged %v, want %v", s, got, want)
		}
	}
	member := []live.RestrictedField{{Key: "insurance_member_id", Label: "Insurance member ID", Value: "BSN9920311"}}
	if len(restrictedIn("The member ID is BSN9920311.", member)) == 0 {
		t.Fatal("a non-date field stopped being guarded")
	}
}
