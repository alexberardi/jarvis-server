package textfilter

import "testing"

func TestIsInterjectionShaped(t *testing.T) {
	for text, want := range map[string]bool{
		"Oh my gosh": true, "wow!": true, "Oh my god.": true, "ugh": true, "holy cow": true, "Huh?": true,
		"no": false, "what?": false, "wait": false, "oh, open firefox": false, "my god it's late": false, "": false,
		"oh my gosh oh my gosh wow": false,
	} {
		if got := IsInterjectionShaped(text); got != want {
			t.Errorf("IsInterjectionShaped(%q) = %v, want %v", text, got, want)
		}
	}
}
