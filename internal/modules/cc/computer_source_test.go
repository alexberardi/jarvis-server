package cc

import (
	"context"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
)

// A computer task's source: an address is fetched as is; words are searched (with web search
// on) and the first result fetched; the text is the page's <main>, without the menus before it.
func TestComputerSource(t *testing.T) {
	fetch := &fakeWatchFetcher{status: 200, body: `<html><body><nav>Main menu Donate Log in</nav>` +
		`<main id="content"><h1>Apollo 11</h1><p>Apollo 11 was the first crewed Moon landing.</p></main><footer>Privacy</footer></body></html>`}
	search := &fakeWatchSearcher{results: []servertools.SearchResult{{Title: "Apollo 11", URL: "https://en.wikipedia.org/wiki/Apollo_11"}}}
	e := newSigEnv(t, func(m *Module) { m.WatchFetcher, m.WebSearch = fetch, search })
	ctx := context.Background()

	src, err := e.m.computerSource(ctx, sigHH, "https://en.wikipedia.org/wiki/Apollo_11")
	if err != nil || src.URL != "https://en.wikipedia.org/wiki/Apollo_11" || !strings.Contains(src.Text, "first crewed Moon landing") ||
		strings.Contains(src.Text, "Donate") || strings.Contains(src.Text, "Privacy") {
		t.Fatalf("by address: %+v %v", src, err)
	}
	if search.calls() != 0 {
		t.Error("an address was searched")
	}

	if _, err := e.m.computerSource(ctx, sigHH, "Apollo 11 Wikipedia"); err == nil || !strings.Contains(err.Error(), "web search is off") {
		t.Errorf("words with web search off: %v", err)
	}
	e.set(settingWebSearch, true, sigHH)
	src, err = e.m.computerSource(ctx, sigHH, "Apollo 11 Wikipedia")
	if err != nil || src.URL != "https://en.wikipedia.org/wiki/Apollo_11" || search.calls() != 1 {
		t.Fatalf("by search: %+v %v", src, err)
	}

	fetch.status = 404
	if _, err := e.m.computerSource(ctx, sigHH, "https://example.com/gone"); err == nil || err.Error() != "HTTP 404" {
		t.Errorf("404: %v", err)
	}
	fetch.status, fetch.body = 200, `<html><body><main><script>x()</script></main></body></html>`
	if _, err := e.m.computerSource(ctx, sigHH, "https://example.com/empty"); err == nil {
		t.Error("a page with no text was accepted")
	}
	fetch.body = "<p>" + strings.Repeat("word ", 10000) + "</p>"
	if src, _ := e.m.computerSource(ctx, sigHH, "https://example.com/long"); len([]rune(src.Text)) > sourceRunes {
		t.Errorf("text is %d runes, want at most %d", len([]rune(src.Text)), sourceRunes)
	}
}
