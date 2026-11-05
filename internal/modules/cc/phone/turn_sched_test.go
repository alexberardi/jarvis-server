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
