package winmatch

import (
	"errors"
	"testing"
)

func TestPickUnique(t *testing.T) {
	cands := []Candidate{
		{Apps: []string{"foot"}, Title: "claude — ~/jarvis", Recency: 0, Visible: true},
		{Apps: []string{"firefox"}, Title: "Docs — Mozilla Firefox", Recency: 4, Visible: true},
		{Apps: []string{"org.mozilla.firefox"}, Title: "Mail — Mozilla Firefox", Recency: 2, Visible: false},
		{Apps: []string{"Slack"}, Title: "general", Recency: 1, Visible: false},
		{Apps: []string{"Claude"}, Title: "Claude", Recency: 6, Visible: false},
		{Apps: []string{"kitty"}, Title: "notes", Recency: 3, Visible: true},
		{Apps: []string{"kitty"}, Title: "build", Recency: 5, Visible: true},
	}
	for _, c := range []struct {
		query   string
		focused int
		want    int
		code    string
	}{
		{"slack", -1, 3, ""},              // one match, even off screen: focusing brings it forward
		{"claude", -1, 4, ""},             // the app named Claude beats a terminal titled claude
		{"the firefox window", -1, 1, ""}, // two match: the only visible one
		{"kitty", 6, 6, ""},               // two visible: the focused one
		{"kitty", -1, -1, CodeAmbiguous},  // two visible, neither focused: ask
		{"terminal", -1, -1, CodeAmbiguous},
		{"notes", -1, 5, ""}, // title words
		{"spotify", -1, -1, CodeNotFound},
		{"this window", -1, -1, CodeNotFound}, // no name at all is never a match
	} {
		got, err := PickUnique(cands, c.query, c.focused)
		var we *Error
		switch {
		case c.code != "" && (!errors.As(err, &we) || we.Code != c.code):
			t.Errorf("%q: err %v, want %s", c.query, err, c.code)
		case c.code == "" && (err != nil || got != c.want):
			t.Errorf("%q: got %d, %v; want %d", c.query, got, err, c.want)
		}
	}
	_, err := PickUnique(cands, "kitty", -1)
	var we *Error
	if !errors.As(err, &we) || len(we.Candidates) != 2 || we.Candidates[0] != "Kitty — notes" {
		t.Fatalf("candidates %#v", we)
	}
}
