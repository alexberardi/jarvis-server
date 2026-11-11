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
	url := ref
	if !strings.HasPrefix(ref, "https://") && !strings.HasPrefix(ref, "http://") {
		if !m.settings.Bool(ctx, settingWebSearch, settings.Scope{HouseholdID: hh}) {
			return computer.Source{}, errors.New("web search is off, so it needs the page's address")
		}
		search := m.WebSearch
		if search == nil {
			search = &servertools.DuckDuckGo{}
		}
		results, err := search.Search(ctx, ref, 1)
		if err != nil {
			return computer.Source{}, fmt.Errorf("search failed: %w", err)
		}
		if len(results) == 0 {
			return computer.Source{}, fmt.Errorf("no page found for %q", ref)
		}
		url = results[0].URL
	}
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
