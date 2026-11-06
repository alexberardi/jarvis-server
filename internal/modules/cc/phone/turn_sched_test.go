package phone

import (
	"context"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone/live"
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
	if !strings.Contains(note, "Friday at 6:15 pm is outside") || !strings.Contains(note, "Acceptable: Friday at 7 pm, Friday at 7:30 pm") ||
		strings.Contains(note, "ESCALATE: …") {
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
	if !strings.Contains(note, "Friday at 6:15 is outside") || !strings.Contains(note, "Acceptable: Friday at 7:30") {
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
	if note := p.schedulingNote("I can do 715 if that works."); !strings.Contains(note, "OPEN on the caller's calendar") {
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
