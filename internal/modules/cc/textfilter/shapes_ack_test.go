package textfilter

import "testing"

func TestIsAcknowledgementShaped(t *testing.T) {
	for text, want := range map[string]bool{
		"thanks": true, "Thanks!": true, "ok thanks": true, "OK, thank you so much.": true, "got it": true,
		"gotcha": true, "cool": true, "no thanks": true, "nope": true, "never mind": true, "that’s all, thanks": true,
		"hey jarvis, thanks": true, "bye": true,
		// Not closers: these can accept an offer, or ask for something.
		"ok": false, "yes": false, "sure": false, "great": false, "perfect": false, "do it": false,
		"thanks, now add milk": false, "check it again": false, "thanks?": false, "": false,
		"no, add eggs to the shopping list": false,
	} {
		if got := IsAcknowledgementShaped(text); got != want {
			t.Errorf("IsAcknowledgementShaped(%q) = %v, want %v", text, got, want)
		}
	}
}
