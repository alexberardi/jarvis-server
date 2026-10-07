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
