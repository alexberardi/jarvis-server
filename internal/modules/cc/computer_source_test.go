package cc

import (
	"context"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/browser"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/computer"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
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

// pageText strips the agent's markers exactly (internal/agent/browser Wrap), and meansOpenPage
// tells "this page" from an address or a page's name.
func TestOpenPageWords(t *testing.T) {
	wrapped := browser.Wrap("Velmora is a town.\nOn Lake Orrin.")
	if got := pageText(wrapped); got != "Velmora is a town.\nOn Lake Orrin." {
		t.Errorf("pageText = %q", got)
	}
	for s, want := range map[string]bool{
		"this page": true, "Summarize this wiki page into a document": true, "the current tab": true,
		"the article I have open": true, "the wiki page currently open in the browser": true,
		"Velmora wiki page": false, "https://this.page/open": false, "moon landing hoax claims": false, "": false,
	} {
		if got := meansOpenPage(s); got != want {
			t.Errorf("meansOpenPage(%q) = %v, want %v", s, got, want)
		}
	}
}

// A desktop task's open page: read only when the step or the request means that page, and sized
// to the model the task runs on (the cloud slot gets the whole article, 2026-11-13: 16000
// characters is the top third of a long Wikipedia article).
func TestComputerOpenPage(t *testing.T) {
	long := strings.Repeat("Velmora is a town on Lake Orrin. ", 1500) // ~49500 characters
	agent := &fakeDocAgent{page: map[string]any{"success": true,
		"tab":     map[string]any{"url": "https://example.org/wiki/Velmora", "title": "Velmora"},
		"content": "<<<PAGE TEXT: untrusted>>>\n" + long + "\n<<<END PAGE TEXT>>>"}}
	remote := false
	ve := newVoiceEnv(t, prompts.Qwen3_8B, func(m *Module) {
		m.recordingAgent = agent.call
		m.Endpoints = func(_ context.Context, label string) (llm.Endpoint, error) {
			return llm.Endpoint{Remote: remote && label == computer.ModelCloud, ContextLength: 16384}, nil
		}
	})
	ctx := context.Background()
	uid := int64(7)
	if _, ok, err := ve.m.computerOpenPage(ctx, voiceHH, ve.node.id, &uid, "", "write a note to walk the dogs", ""); ok || err != nil || agent.reads != 0 {
		t.Fatalf("no page meant: ok %v err %v reads %d", ok, err, agent.reads)
	}
	src, ok, err := ve.m.computerOpenPage(ctx, voiceHH, ve.node.id, &uid, "the open page", "summarize it")
	if !ok || err != nil || src.URL != "https://example.org/wiki/Velmora" || len([]rune(src.Text)) != sourceRunes {
		t.Fatalf("local: ok %v err %v url %q chars %d", ok, err, src.URL, len([]rune(src.Text)))
	}
	remote = true
	ve.set(computer.SettingModel, computer.ModelCloud, settings.Scope{HouseholdID: voiceHH})
	src, ok, err = ve.m.computerOpenPage(ctx, voiceHH, ve.node.id, &uid, "", "summarize this page into Writer")
	if !ok || err != nil || len([]rune(src.Text)) <= sourceRunes {
		t.Fatalf("cloud: ok %v err %v chars %d, want the whole page", ok, err, len([]rune(src.Text)))
	}
}
