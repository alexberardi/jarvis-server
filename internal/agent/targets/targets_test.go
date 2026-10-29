package targets

import (
	"testing"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
)

func TestSpokenNumber(t *testing.T) {
	for in, want := range map[string]int{"7": 7, "7.": 7, "seven": 7, "Twenty three": 23, "twenty-three": 23,
		"one hundred and five": 105, "12!": 12} {
		if n, ok := SpokenNumber(in); !ok || n != want {
			t.Errorf("%q: %d %v, want %d", in, n, ok, want)
		}
	}
	for _, in := range []string{"", "0", "the first one", "seven dogs"} {
		if _, ok := SpokenNumber(in); ok {
			t.Errorf("%q read as a number", in)
		}
	}
}

// A model that leaves out the confidence, or writes it as a string, still picks.
func TestDecodePickAnswer(t *testing.T) {
	for raw, want := range map[string]struct {
		ref  string
		conf float64
		ok   bool
	}{
		`{"ref":"e3"}`:                                      {"e3", 1, true},
		`{"ref":"e3","confidence":"0.82"}`:                  {"e3", 0.82, true},
		"```json\n{\"ref\":\"e3\",\"confidence\":0.4}\n```": {"e3", 0.4, true},
		`I pick {"ref":"none","confidence":0.9}`:            {"none", 0.9, true},
		`e3`:                                                {"", 0, false},
		`{"ref":"e3","confidence":"high"}`:                  {"", 0, false},
	} {
		a, ok := decodePickAnswer(raw)
		if ok != want.ok || (ok && (a.Ref != want.ref || a.Confidence != want.conf)) {
			t.Errorf("%q: %+v %v, want %+v", raw, a, ok, want)
		}
	}
}

func snap(url string, els ...computeruse.SnapElement) computeruse.Snapshot {
	return computeruse.Snapshot{Gen: 1, URL: url, Elements: els}
}

func link(ref, name, target string) computeruse.SnapElement {
	return computeruse.SnapElement{Ref: ref, Role: "link", Name: name, Target: target}
}

// Resolve on a plain snapshot, as a desktop app's will be: exact, after a verb the start of a
// name and a sound-alike, and repeated names only when they are one destination.
func TestResolve(t *testing.T) {
	s := snap("https://www.youtube.com/results?search_query=hello",
		link("e1", "Adele - Hello (Official Music Video)", "https://www.youtube.com/watch?v=a"),
		link("e2", "Adele - Hello (Official Music Video)", "https://www.youtube.com/watch?v=a"),
		link("e3", "Lionel Richie - Hello", "https://www.youtube.com/watch?v=l"),
		computeruse.SnapElement{Ref: "e4", Role: "button", Name: "Reply"},
		computeruse.SnapElement{Ref: "e5", Role: "button", Name: "Reply"},
		computeruse.SnapElement{Ref: "e6", Role: "searchbox", Name: "Search"},
	)
	for _, c := range []struct {
		said  string
		loose bool
		ref   string
	}{
		{"the search box", false, "e6"},
		{"Adele Hello Official Music Video", false, "e1"}, // two links, one destination
		{"Adele Hello", false, ""},                        // a partial name needs a verb
		{"Adele Hello", true, "e1"},
		{"adil hello official music video", true, "e1"}, // misheard
		{"Reply", true, ""},                             // two different buttons: the pick's
		{"purple elephant", true, ""},
	} {
		ref, ok := Resolve(s, c.said, c.loose)
		if ref != c.ref || ok != (c.ref != "") {
			t.Errorf("Resolve(%q, loose %v) = %q %v, want %q", c.said, c.loose, ref, ok, c.ref)
		}
	}
}

func TestOrdinal(t *testing.T) {
	s := snap("https://www.youtube.com/results?search_query=hello",
		link("e1", "Home", "https://www.youtube.com/"),
		link("e2", "Adele", "https://www.youtube.com/watch?v=a"),
		link("e3", "Adele", "https://www.youtube.com/watch?v=a"),
		link("e4", "Lionel", "https://youtu.be/l"),
	)
	for said, want := range map[string]string{"the first video": "e2", "second result": "e4", "the third video": ""} {
		if ref, _ := Ordinal(s, said); ref != want {
			t.Errorf("%q: %q, want %q", said, ref, want)
		}
	}
	plain := s
	plain.URL = "https://news.test/"
	if ref, ok := Ordinal(plain, "the first result"); ok {
		t.Errorf("a result ordinal off a video results page: %q", ref)
	}
}
