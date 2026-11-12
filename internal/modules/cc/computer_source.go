package cc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
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

// openPageRE is words that mean the page open in the user's browser ("this page", "the article
// I have open", "the page open in my browser"), not an address or words to search for.
var openPageRE = regexp.MustCompile(`(?i)\b(this|current|open|opened|active)\s+(\S+\s+)?(web\s*)?(page|tab|article)\b|` +
	`\b(page|tab|article)\s+(\S+\s+){0,2}(open|opened)\b|\bopen\s+in\s+(the|my)\s+browser\b`)

// meansOpenPage is whether s names the page open in the browser rather than an address.
func meansOpenPage(s string) bool {
	return !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") && openPageRE.MatchString(s)
}

// toolReadPage is the agent's browser_read_page (internal/agent/browser, not imported: it brings
// the desktop's libraries), whose content is the page's text between a "<<<PAGE TEXT" line and
// a "<<<END PAGE TEXT>>>" line.
const toolReadPage = "browser_read_page"

// pageText is browser_read_page's content without its markers.
func pageText(content string) string {
	lines := strings.Split(strings.TrimSpace(content), "\n")
	if len(lines) > 0 && strings.HasPrefix(lines[0], "<<<PAGE TEXT") {
		lines = lines[1:]
	}
	if n := len(lines); n > 0 && strings.HasPrefix(lines[n-1], "<<<END PAGE TEXT") {
		lines = lines[:n-1]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// openPageSource is the page open in the browser on the user's computer, as its extension reads
// it (browser_read_page on the current tab): the page the user sees, logins and home network
// included, which the server's own fetch can't reach.
func (m *Module) openPageSource(ctx context.Context, nodeID string, uid int64) (computer.Source, error) {
	out := m.agentRecordingCall(ctx, nodeID, toolReadPage, map[string]any{"max_chars": sourceRunes}, uid)
	if !dispatchOK(out) {
		return computer.Source{}, fmt.Errorf("couldn't read the page open in the browser: %s", agentError(out))
	}
	tab, _ := out["tab"].(map[string]any)
	url, _ := tab["url"].(string)
	content, _ := out["content"].(string)
	text := pageText(content)
	if (!strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://")) || text == "" {
		return computer.Source{}, errors.New("there is no web page open in the browser")
	}
	return computer.Source{URL: url, Text: truncateRunes(text, sourceRunes)}, nil
}
