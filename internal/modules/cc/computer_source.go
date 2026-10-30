package cc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/computer"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// sourceRunes bounds a computer task's source text: it is in every round's prompt.
const sourceRunes = 16000

// computerSource is the page a computer_task step works from (ELEMENT-LAYER-WORK.md C25: a
// desktop task can't read the browser). ref is its address, or words to search for when web
// search is on (the first result). The text is the page's main content when it marks one.
func (m *Module) computerSource(ctx context.Context, hh, ref string) (computer.Source, error) {
	urls, err := m.sourceURLs(ctx, hh, ref, 1)
	if err != nil {
		return computer.Source{}, err
	}
	return m.fetchSource(ctx, urls[0])
}

// sourceURLs is ref when it is an address, else the first n results of searching for it (web
// search on).
func (m *Module) sourceURLs(ctx context.Context, hh, ref string, n int) ([]string, error) {
	if strings.HasPrefix(ref, "https://") || strings.HasPrefix(ref, "http://") {
		return []string{ref}, nil
	}
	if !m.settings.Bool(ctx, settingWebSearch, settings.Scope{HouseholdID: hh}) {
		return nil, errors.New("web search is off, so it needs the page's address")
	}
	search := m.WebSearch
	if search == nil {
		search = &servertools.DuckDuckGo{}
	}
	results, err := search.Search(ctx, ref, n)
	if err != nil {
		return nil, fmt.Errorf("search failed: %w", err)
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("no page found for %q", ref)
	}
	var urls []string
	for _, r := range results[:min(n, len(results))] {
		urls = append(urls, r.URL)
	}
	return urls, nil
}

// fetchSource is the page at url as text: its main content, at most sourceRunes.
func (m *Module) fetchSource(ctx context.Context, url string) (computer.Source, error) {
	fetcher := m.WatchFetcher
	if fetcher == nil {
		fetcher = &servertools.Fetcher{}
	}
	status, body, err := fetcher.Get(ctx, url, http.Header{"User-Agent": {watchUserAgent}})
	if err != nil {
		return computer.Source{}, fmt.Errorf("fetch failed: %w", err)
	}
	if status >= 400 {
		return computer.Source{}, fmt.Errorf("HTTP %d", status)
	}
	text := servertools.ExtractText(mainContent(strings.ToValidUTF8(string(body), "�")))
	if strings.TrimSpace(text) == "" {
		return computer.Source{}, errors.New("the page has no readable text")
	}
	return computer.Source{URL: url, Text: truncateRunes(text, sourceRunes)}, nil
}

// mainContent is the page's <main> element when it has one (Wikipedia's article, without the
// menus before it), else the whole page.
func mainContent(page string) string {
	lower := strings.ToLower(page)
	start := strings.Index(lower, "<main")
	if start < 0 {
		return page
	}
	end := strings.LastIndex(lower, "</main>")
	if end < start {
		return page[start:]
	}
	return page[start:end]
}
