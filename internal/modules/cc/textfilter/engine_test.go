package textfilter

import "testing"

// ForceToolsGate composes the golden-tested shape and keyword detectors as the engine does.
func TestForceToolsGate(t *testing.T) {
	pool := []string{"medicine", "took my", "lights"}
	cases := []struct {
		utt     string
		replies []string
		pool    []string
		want    bool
	}{
		{"", nil, pool, true}, // no utterance: legacy always-retry
		{"What should I do with Miles today?", []string{"Maybe the park."}, pool, false},
		{"turn off the lights", []string{"Okay."}, pool, true},
		{"turn off the fan", []string{"Okay."}, pool, false}, // no keyword
		{"turn off the fan", nil, nil, true},                 // empty pool relaxes the keyword half
		{"Thank you", []string{"You're welcome!"}, pool, false},
		{"his medicine.", []string{"I'll check on Leo's meds for you."}, pool, true}, // reply arms it
		{"his stuff.", []string{"I'll check on that for you."}, pool, false},         // no keyword anywhere
		{"Leo took his medicine", []string{"Great!"}, pool, true},
	}
	for _, c := range cases {
		if got := ForceToolsGate(c.utt, c.replies, c.pool); got != c.want {
			t.Errorf("ForceToolsGate(%q, %q) = %v", c.utt, c.replies, got)
		}
	}
}

// Phrasings the legacy lists missed, found by the pipeline scenarios (tools/pipeline): a
// correction ("actually make it 15") and a memory request are commands, and "I'll adjust…",
// "I'll remember…" claim an action. Each let a prose reply skip its tool.
func TestShapesBeyondLegacy(t *testing.T) {
	for _, s := range []string{
		"actually make it 15",
		"make it 20 minutes",
		"change it to 10 minutes",
		"remember that my favorite color is green",
		"ok, move it to 5pm",
		"no wait, extend it by five minutes",
	} {
		if !IsActionCommandShaped(s) {
			t.Errorf("IsActionCommandShaped(%q) = false", s)
		}
	}
	for _, s := range []string{
		"Got it, I'll adjust the timer to 15 minutes for the pasta.",
		"Got it, I'll reset the timer to 15 minutes.",
		"Got it, Jordan. I'll remember that green is your favorite color.",
		"I'll remember that for you.",
		"I've changed it to 10 minutes.",
		"Your timer has been moved to 5pm.",
		"Changing it to 15 now.",
		"I've got that noted - green is your favorite color.",
		"Got it, that's saved.",
	} {
		if !ResponseClaimsAction(s) {
			t.Errorf("ResponseClaimsAction(%q) = false", s)
		}
	}
	for _, s := range []string{"I've got a question for you.", "Got it."} {
		if ResponseClaimsAction(s) {
			t.Errorf("ResponseClaimsAction(%q) = true", s)
		}
	}
	for _, s := range []string{"what can you make", "make sense?", "I can't remember", "Sure, anything else?"} {
		if IsActionCommandShaped(s) && ResponseClaimsAction(s) {
			t.Errorf("%q: command and claim", s)
		}
	}
	// Both together arm the guard when the keyword pool knows the subject.
	if !ForceToolsGate("actually make it 15", []string{"Got it, I'll adjust the timer to 15 minutes."}, []string{"timer"}) {
		t.Error("correction + claimed adjustment did not arm the guard")
	}
	// An elliptical claim ("timer adjusted") is no claim, but its words still name the subject.
	if !ForceToolsGate("actually make it 15", []string{"Got it, timer adjusted to 15 minutes for the pasta."}, []string{"timer"}) {
		t.Error("correction + elliptical reply did not arm the guard")
	}
}
