package cc

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/computer"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
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
	`\b(page|tab|article)\s+(\S+\s+){0,4}(open|opened)\b|\bopen\s+in\s+(the|my)\s+browser\b`)

// meansOpenPage is whether s names the page open in the browser rather than an address.
func meansOpenPage(s string) bool {
	return !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") && openPageRE.MatchString(s)
}

// toolReadPage is the agent's browser_read_page (internal/agent/browser, not imported: it brings
// the desktop's libraries), whose content is the page's text between a "<<<PAGE TEXT" line and
// a "<<<END PAGE TEXT>>>" line.
const toolReadPage = "browser_read_page"

// toolListTabs is the agent's browser_list_tabs, whose tabs say which one is current (the active
// tab of the window the user last focused).
const toolListTabs = "browser_list_tabs"

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
// The tab is named: without one the agent reads its task tab, where an earlier errand worked,
// not the page the user is looking at.
func (m *Module) openPageSource(ctx context.Context, nodeID string, uid int64, refs ...string) (computer.Source, error) {
	return m.openPageSourceOf(ctx, nodeID, uid, sourceRunes, refs...)
}

// computerOpenPage is errands.ComputerTasks.OpenPage: the open page when one of refs means it,
// sized to the model the task runs on (taskSourceRunes).
func (m *Module) computerOpenPage(ctx context.Context, hh, nodeID string, uid *int64, refs ...string) (computer.Source, bool, error) {
	if !slices.ContainsFunc(refs, meansOpenPage) {
		return computer.Source{}, false, nil
	}
	var id int64
	if uid != nil {
		id = *uid
	}
	src, err := m.openPageSourceOf(ctx, nodeID, id, m.taskSourceRunes(ctx, hh), refs...)
	return src, err == nil, err
}

// taskSourceRunes bounds the open page a computer task works from. It is in every round's
// prompt, so a local model gets sourceRunes; the cloud slot (computer.model = cloud, a remote
// endpoint) gets the whole article up to what the extension returns, as page reads do
// (readPageMaxChars): 16000 characters is the top third of a long Wikipedia article.
func (m *Module) taskSourceRunes(ctx context.Context, hh string) int {
	if m.settings.String(ctx, computer.SettingModel, settings.Scope{HouseholdID: hh}) == computer.ModelCloud {
		if ep, ok := m.endpoint(ctx, computer.ModelCloud); ok && ep.Remote {
			return readPageMaxChars
		}
	}
	return sourceRunes
}

// pageWordStop are request words that name no page ("summarize the wikipedia page into a document").
var pageWordStop = map[string]bool{"page": true, "pages": true, "article": true, "this": true, "that": true, "open": true,
	"opened": true, "current": true, "currently": true, "browser": true, "summary": true, "summarize": true,
	"summarise": true, "document": true, "into": true, "from": true, "with": true, "save": true, "saved": true,
	"write": true, "read": true, "whole": true, "start": true, "called": true, "named": true, "name": true,
	"libreoffice": true, "writer": true, "word": true, "note": true, "notes": true, "file": true, "text": true,
	"about": true, "what": true, "have": true, "website": true, "site": true, "main": true, "points": true,
	"words": true, "your": true, "each": true, "part": true, "paragraphs": true, "short": true, "cover": true,
	"covering": true, "create": true, "documents": true, "section": true, "sections": true, "intro": true,
	"first": true, "just": true, "every": true, "them": true, "should": true, "like": true}

var pageWordRE = regexp.MustCompile(`[\p{L}\p{N}]{4,}`)

// pickOpenTab is the tab "the page I have open" means, among the browser's tabs (browser_list_tabs).
// Never the Jarvis page itself (jarvis_page: the chat the user asks in, which is the tab in
// front when they type or approve there; 2026-11-13: Settings was summarized as the article).
// A page the request names ("the Wikipedia page": a word of refs in a tab's host or title)
// wins, then the current tab, then the only page open. More than one candidate and no way to
// tell is an error that lists them, never a guess.
func pickOpenTab(list []any, refs []string) (any, error) {
	type tab struct {
		id       any
		host, ti string
		current  bool
		active   bool
	}
	var tabs []tab
	for _, e := range list {
		tm, ok := e.(map[string]any)
		if !ok || tm["tab_id"] == nil || tm["jarvis_page"] == true {
			continue
		}
		u, _ := tm["url"].(string)
		// An address that isn't a web page's is out; one the browser didn't give stays (the
		// read checks what it got).
		host := ""
		if _, rest, ok := strings.Cut(u, "://"); ok {
			if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
				continue
			}
			host, _, _ = strings.Cut(rest, "/")
		}
		title, _ := tm["title"].(string)
		tabs = append(tabs, tab{id: tm["tab_id"], host: strings.ToLower(host), ti: strings.ToLower(title),
			current: tm["current"] == true, active: tm["active"] == true})
	}
	if len(tabs) == 0 {
		return nil, errors.New("there is no web page open in the browser")
	}
	prefer := func(c []tab) (any, bool) {
		for _, pred := range []func(tab) bool{func(t tab) bool { return t.current }, func(t tab) bool { return t.active }} {
			var hit []tab
			for _, t := range c {
				if pred(t) {
					hit = append(hit, t)
				}
			}
			if len(hit) == 1 {
				return hit[0].id, true
			}
		}
		if len(c) == 1 {
			return c[0].id, true
		}
		return nil, false
	}
	// A page the request names.
	best, score := []tab(nil), 0
	for _, t := range tabs {
		n := 0
		seen := map[string]bool{}
		for _, w := range pageWordRE.FindAllString(strings.ToLower(strings.Join(refs, " ")), -1) {
			if !pageWordStop[w] && !seen[w] && (strings.Contains(t.host, w) || strings.Contains(t.ti, w)) {
				seen[w] = true
				n++
			}
		}
		switch {
		case n > score:
			best, score = []tab{t}, n
		case n == score && n > 0:
			best = append(best, t)
		}
	}
	if score > 0 {
		if id, ok := prefer(best); ok {
			return id, nil
		}
	}
	if id, ok := prefer(tabs); ok {
		return id, nil
	}
	var names []string
	for _, t := range tabs[:min(len(tabs), 5)] {
		names = append(names, cmpOr(t.ti, t.host))
	}
	return nil, fmt.Errorf("I can't tell which page you mean: %d are open (%s). Bring it to the front, or say its name",
		len(tabs), strings.Join(names, "; "))
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// openPageSourceOf is openPageSource with at most limit characters of the page. refs are the
// request's words (the step's source, its goal), for a page it names.
func (m *Module) openPageSourceOf(ctx context.Context, nodeID string, uid int64, limit int, refs ...string) (computer.Source, error) {
	tabs := m.agentRecordingCall(ctx, nodeID, toolListTabs, map[string]any{}, uid)
	if !dispatchOK(tabs) {
		return computer.Source{}, fmt.Errorf("couldn't list the browser's tabs: %s", agentError(tabs))
	}
	list, _ := tabs["tabs"].([]any)
	current, err := pickOpenTab(list, refs)
	if err != nil {
		return computer.Source{}, err
	}
	out := m.agentRecordingCall(ctx, nodeID, toolReadPage, map[string]any{"tab_id": current, "max_chars": limit}, uid)
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
	return computer.Source{URL: url, Text: truncateRunes(text, limit)}, nil
}

// Page reads sized to the model that reads them (browser_read_page's max_chars, when the model
// gives none). The agent's own default (4000) suits a small local model; a cloud model, whose
// window the endpoint doesn't report, gets the whole article up to what the browser extension
// returns (50000). A local model with a known window gets about a fifth of it (4 characters a
// token). Found live 2026-11-13: Sonnet summarized only the top of a Wikipedia article.
const (
	readPageMinChars = 4000
	readPageMaxChars = 50000
)

// readPageCharsFor is the max_chars for a page read on ep, or 0 to leave the agent's default.
func readPageCharsFor(ep llm.Endpoint, ok bool) int {
	switch {
	case !ok:
		return 0
	case ep.Remote:
		return readPageMaxChars
	case ep.ContextLength > 0:
		return min(max(ep.ContextLength*4/5, readPageMinChars), readPageMaxChars)
	}
	return 0
}

// readPageChars is readPageCharsFor the slot this turn runs on.
func (m *Module) readPageChars(ctx context.Context) int {
	ep, ok := m.endpoint(ctx, m.conversationSlot(ctx))
	return readPageCharsFor(ep, ok)
}

// fillReadPageChars is call's arguments with max_chars n, when call is a browser_read_page that
// asks for less (or gives none) and n is known. The model's own smaller size cost a second read
// (2026-11-13: 30000, "the page was cut off", then 80000); a larger one is kept.
func fillReadPageChars(call parse.ToolCall, n int) (string, bool) {
	if call.Function.Name != toolReadPage || n <= 0 {
		return "", false
	}
	v, err := pyjson.Loads(call.Function.Arguments)
	o, isObj := v.(*pyjson.Object)
	if err != nil || !isObj {
		return "", false
	}
	if x, ok := o.Get("max_chars"); ok && x != nil {
		if have, isNum := readSize(x); !isNum || have >= n {
			return "", false
		}
	}
	o.Set("max_chars", n)
	return pyjson.Dumps(o, true), true
}

// PageSummaryHint is the turn's line when the user asks for a summary and the page reader is
// offered: the whole page, not its opening (found live 2026-11-13: a summary of a Wikipedia
// article's first paragraphs, "the intro gives a solid summary").
const PageSummaryHint = "[summary: read the whole page and cover all of it, start to end: the main points of each " +
	"part in your own words, never just the intro or first paragraphs. Spoken, a few sentences that still span the " +
	"whole page; typed into an app, short paragraphs with line breaks, under 2000 characters in all (one typing call); written to a document, a few short paragraphs.]"

// pageSummaryHint is PageSummaryHint for a summary request with the page reader, or "".
func pageSummaryHint(conv *conversation, utterance string) string {
	if !conv.hasTool(toolReadPage) || !asksForSummary(utterance) {
		return ""
	}
	return PageSummaryHint
}

// readSize is a decoded max_chars as an int: a number, or a numeric string (the agent takes
// both).
func readSize(x any) (int, bool) {
	switch v := x.(type) {
	case *big.Int:
		if v.IsInt64() {
			return int(v.Int64()), true
		}
	case float64:
		return int(v), true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		return n, err == nil
	}
	return 0, false
}
