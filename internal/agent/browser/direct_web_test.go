package browser

import (
	"context"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// Tonight's transcripts: each went to the model, which searched on the server or opened a window.
func TestWebRequest(t *testing.T) {
	for said, want := range map[string]string{
		"Search for dogs on Google.":                      "https://www.google.com/search?q=dogs",
		"Search for dogs on Google and Firefox.":          "",
		"In Firefox, search for dogs on Google.":          "https://www.google.com/search?q=dogs",
		"in Firefox, search for dogs.":                    "https://www.google.com/search?q=dogs",
		"Search for dogs in my Google page.":              "https://www.google.com/search?q=dogs",
		"Open a new tab and search for golden retrievers": "https://www.google.com/search?q=golden+retrievers",
		"Search for cheap flights in a new tab":           "https://www.google.com/search?q=cheap+flights",
		"google best pizza near me":                       "https://www.google.com/search?q=best+pizza+near+me",
		"Open a new tab.":                                 "https://www.google.com/",
		"Jarvis, open a new tab in Firefox":               "https://www.google.com/",
		"go to youtube.com":                               "https://youtube.com",
		"Open news.ycombinator.com in a new tab.":         "https://news.ycombinator.com",
		"search for my notes about the trip":              "",
		"what's the weather":                              "",
		"open Firefox":                                    "",
	} {
		if got := webRequest(said); got != want {
			t.Errorf("%q: %q, want %q", said, got, want)
		}
	}
}

func TestDirectWebOpensANewTab(t *testing.T) {
	h, path := startHub(t)
	ts := NewTools(h)
	f := connectExt(t, h, path, "Firefox", true, func(m Message) Message {
		return ok(map[string]any{"tab": map[string]any{"tab_id": 7.0, "url": m.Args["url"]}})
	})
	out, ok := ts.Direct(context.Background(), "Search for dogs on Google.")
	if !ok || out["success"] != true || out[node.IslandOnlyKey] != true {
		t.Fatalf("%v %v", out, ok)
	}
	if a := lastArgs(t, f, "open"); a["url"] != "https://www.google.com/search?q=dogs" || a["background"] != nil {
		t.Fatalf("open %v", a)
	}
	if ok, err := ts.OpenLink(context.Background(), "https://example.com"); !ok || err != nil {
		t.Fatalf("OpenLink %v %v", ok, err)
	}
	if ok, _ := ts.OpenLink(context.Background(), "mailto:a@b.c"); ok {
		t.Fatal("mailto went to the browser")
	}
}
