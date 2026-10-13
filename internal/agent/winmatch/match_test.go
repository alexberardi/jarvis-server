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

func TestNear(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"omerite", "omawrite", true}, // the real misheard name: a substitution and an insertion
		{"slak", "slack", true},
		{"fierfox", "firefox", true}, // a swap of neighbours is one edit
		{"firefx", "firefox", true},
		{"food", "foot", false},    // short names are too easy to hit
		{"kitten", "kitty", false}, // two edits in six letters is too far
		{"chrome", "chromium", false},
		{"spotify", "slack", false},
		{"omawrite", "omawrite", true},
	} {
		if got := Near(c.a, c.b); got != c.want {
			t.Errorf("Near(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
	if d := distance([]rune("omerite"), []rune("omawrite")); d != 2 {
		t.Errorf("distance %d", d)
	}
}

func TestFuzzyAppNames(t *testing.T) {
	cands := []Candidate{
		{Apps: []string{"Alacritty", "Alacritty"}, Title: "~", Recency: 0, Visible: true},
		{Apps: []string{"org.mozilla.firefox"}, Title: "Docs — Mozilla Firefox", Recency: 1, Visible: true},
		{Apps: []string{"omawrite", "omawrite"}, Title: "notes.md", Recency: 2, Visible: false},
		{Apps: []string{"Slack"}, Title: "general", Recency: 3, Visible: true},
		{Apps: []string{"foot"}, Title: "build", Recency: 4, Visible: true},
	}
	for _, c := range []struct {
		query string
		want  int
		fuzzy bool
		code  string
	}{
		{"Omerite", 2, true, ""},
		{"the omerite window", 2, true, ""},
		{"fire fox", 1, false, ""}, // compaction already matches it exactly
		{"fierfox", 1, true, ""},   // the reverse-DNS id's last part
		{"slak", 3, true, ""},
		{"Slack", 3, false, ""},
		{"food", -1, false, CodeNotFound}, // too short to guess
		{"spotify", -1, false, CodeNotFound},
	} {
		m, err := PickUniqueMatch(cands, c.query, -1)
		var we *Error
		switch {
		case c.code != "" && (!errors.As(err, &we) || we.Code != c.code):
			t.Errorf("%q: err %v, want %s", c.query, err, c.code)
		case c.code == "" && (err != nil || m.Index != c.want || m.Fuzzy != c.fuzzy):
			t.Errorf("%q: got %+v, %v; want %d fuzzy=%v", c.query, m, err, c.want, c.fuzzy)
		}
	}
	// Pick (read-the-screen) uses the same tier, and a fuzzy hit that isn't visible is
	// window_not_visible like any other.
	if i, err := Pick(cands, "slak"); err != nil || i != 3 {
		t.Errorf("Pick slak: %d, %v", i, err)
	}
	var we *Error
	if _, err := Pick(cands, "omerite"); !errors.As(err, &we) || we.Code != CodeNotVisible {
		t.Errorf("Pick omerite: %v", err)
	}
	// Not found names the open apps (all of them when acting on a window).
	_, err := PickUnique(cands, "spotify", -1)
	if !errors.As(err, &we) || we.Query != "spotify" || len(we.Apps) != 5 || we.Apps[2] != "Omawrite" {
		t.Errorf("not found: %#v", we)
	}
}

func TestFuzzyNeverPicksBetweenApps(t *testing.T) {
	cands := []Candidate{
		{Apps: []string{"slack"}, Title: "general", Recency: 0, Visible: true},
		{Apps: []string{"slark"}, Title: "x", Recency: 1, Visible: true},
		{Apps: []string{"slack"}, Title: "random", Recency: 2, Visible: false},
	}
	for _, pick := range []func() error{
		func() error { _, err := PickUnique(cands, "slak", -1); return err },
		func() error { _, err := Pick(cands, "slak"); return err },
	} {
		var we *Error
		if err := pick(); !errors.As(err, &we) || we.Code != CodeAmbiguous || len(we.Candidates) != 2 {
			t.Errorf("two near apps: %#v", err)
		}
	}
	// Two windows of the one near app: the usual rules (the only visible one).
	if m, err := PickUniqueMatch(cands[:1:1], "slak", -1); err != nil || m.Index != 0 || !m.Fuzzy {
		t.Errorf("one app: %+v %v", m, err)
	}
	two := []Candidate{cands[0], cands[2]}
	if m, err := PickUniqueMatch(two, "slak", -1); err != nil || m.Index != 0 || !m.Fuzzy {
		t.Errorf("one app, two windows: %+v %v", m, err)
	}
}
