package textfilter

import "testing"

func TestClaimsScreenAction(t *testing.T) {
	yes := []string{
		"Already scrolled down a bit. Want me to go further or check out any specific dog info?",
		"Already clicked on Wikipedia. Want to see what it says or check out another result?",
		"Got it - scrolled down a bit. Need anything else?",
		"I've opened the second link (\"Petfinder\") from your Firefox search tab.",
		"Done — I typed \"ls -a\" in the terminal.",
		"Got it — enter pressed in the terminal.",
		"Switched you to the Discord tab.",
	}
	no := []string{
		"",
		"I'll open the message app and type that in for you.",
		"I couldn't click that — there's no such button.",
		"Want me to press enter to go there?",
		"Nothing was typed: Google Messages isn't open.",
		"It's 9:46 PM.",
		"Should I scroll down?",
		"I'm not able to open that.",
	}
	for _, s := range yes {
		if !ClaimsScreenAction(s) {
			t.Errorf("missed claim: %q", s)
		}
	}
	for _, s := range no {
		if ClaimsScreenAction(s) {
			t.Errorf("false claim: %q", s)
		}
	}
}
