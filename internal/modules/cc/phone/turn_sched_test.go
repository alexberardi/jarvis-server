package phone

import (
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
