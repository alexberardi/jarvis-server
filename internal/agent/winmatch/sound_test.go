package winmatch

import "testing"

func TestSoundKey(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"omawrite", "anarat"},
		{"Omerite", "anarat"},
		{"oh my right", "anarat"},
		{"Neovim", "nafan"},
		{"NeoVim", "nafan"},
		{"neo vim", "nafan"},
		{"oven", "afan"},
		{"", ""},
	} {
		if got := SoundKey(tc.in); got != tc.want {
			t.Errorf("SoundKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSoundsLike(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		like bool
		near bool
	}{
		{"oh my right", "omawrite", true, true},
		{"Omerite", "omawrite", true, true},
		{"neo vim", "neovim", true, true},
		// "the oven" (the article is dropped before matching) is one sound away from neovim:
		// a suggestion, never a match.
		{"oven", "neovim", false, true},
		{"steam", "stream", false, true},
		{"food", "foot", false, false}, // too short to be anything
		{"firefox", "alacritty", false, false},
		{"slack", "lutris", false, false},
	} {
		if got := SoundsLike(tc.a, tc.b); got != tc.like {
			t.Errorf("SoundsLike(%q, %q) = %v", tc.a, tc.b, got)
		}
		if got := SoundsNear(tc.a, tc.b); got != tc.near {
			t.Errorf("SoundsNear(%q, %q) = %v", tc.a, tc.b, got)
		}
	}
}

func TestSimilarityRanksTheIntendedAppFirst(t *testing.T) {
	names := []string{"Omawrite", "Neovim", "Steam", "Firefox", "Alacritty", "Obsidian", "OBS Studio"}
	for heard, want := range map[string]string{"Omerite": "Omawrite", "oh my right": "Omawrite", "oven": "Neovim", "fire fox": "Firefox"} {
		best, bestS := "", -1.0
		for _, n := range names {
			if s := Similarity(heard, n); s > bestS {
				best, bestS = n, s
			}
		}
		if best != want {
			t.Errorf("%q: best %q (%.2f), want %q", heard, best, bestS, want)
		}
	}
	if Similarity("steam", "Steam") != 1 {
		t.Error("exact is 1")
	}
}

func TestPickUniqueBySound(t *testing.T) {
	cands := []Candidate{
		{Apps: []string{"Alacritty"}, Title: "~", Recency: 0, Visible: true},
		{Apps: []string{"omawrite"}, Title: "notes.md", Recency: 1},
	}
	m, err := PickUniqueMatch(cands, "oh my right", 0)
	if err != nil || m.Index != 1 || !m.Fuzzy {
		t.Fatalf("%+v %v", m, err)
	}
	if _, err := PickUniqueMatch(cands, "the oven", 0); err == nil {
		t.Fatal("a sound-near name picked a window")
	}
}
