package servertools

import (
	"context"
	"log/slog"
	"math"
	"net/http"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// WebSearchGate is the execute-time re-check of the household's web_search.enabled setting
// (fail closed: any error must report false). The warmup offer gate is the primary one.
type WebSearchGate func(ctx context.Context, householdID string) bool

// quick_search limits (legacy _NUM_RESULTS, _MAX_CHARS_PER_PAGE; doc 04 §11 overall deadline).
const (
	quickResults      = 2
	quickMaxChars     = 4000
	quickOverallLimit = 12 * time.Second
)

// QuickSearch is the quick_search tool: a blocking DuckDuckGo search plus a scrape of the top
// two results, returned inline for the model to synthesize (docs/cc/04 §3.12).
type QuickSearch struct {
	Search  WebSearcher
	Fetch   *Fetcher
	Enabled WebSearchGate
	Log     *slog.Logger
	now     func() time.Time
}

// NewQuickSearch builds the tool. fetch may be nil (the default guarded fetcher).
func NewQuickSearch(search WebSearcher, fetch *Fetcher, enabled WebSearchGate, log *slog.Logger) *QuickSearch {
	return &QuickSearch{Search: search, Fetch: fetch, Enabled: enabled, Log: log}
}

func (t *QuickSearch) Name() string               { return "quick_search" }
func (t *QuickSearch) Definition() *pyjson.Object { return LegacyDefinition("quick_search") }

func (t *QuickSearch) log() *slog.Logger {
	if t.Log != nil {
		return t.Log
	}
	return slog.Default()
}

func (t *QuickSearch) clock() time.Time {
	if t.now != nil {
		return t.now()
	}
	return time.Now()
}

// webAllowed applies the fail-closed gate: no conversation or household means disabled.
func webAllowed(ctx context.Context, gate WebSearchGate, turn Turn) bool {
	if turn.ConversationID == "" || turn.HouseholdID == "" || gate == nil {
		return false
	}
	return gate(ctx, turn.HouseholdID)
}

func (t *QuickSearch) Execute(ctx context.Context, call Call, turn Turn) (any, error) {
	query := call.Str("query")
	if query == "" {
		return Obj("error", "missing_query", "message", "A search query is required"), nil
	}
	if !webAllowed(ctx, t.Enabled, turn) {
		t.log().Info("[web_search] BLOCKED — web_search disabled for household")
		return Obj("error", "web_search_disabled", "message", "Web search is disabled for this household."), nil
	}
	ctx, cancel := context.WithTimeout(ctx, quickOverallLimit)
	defer cancel()
	start := t.clock()

	results, err := t.Search.Search(ctx, query, quickResults)
	if err != nil {
		// Legacy _search_web logged and returned [] (→ no_results).
		t.log().Error("Quick search web search failed", "err", err)
		results = nil
	}
	if len(results) > quickResults {
		results = results[:quickResults]
	}
	if len(results) == 0 {
		t.log().Info("[web_search] NO RESULTS", "query", query)
		return Obj("error", "no_results", "message", "No search results found for: "+query), nil
	}
	sources := scrapeResults(ctx, t.Fetch, results, quickMaxChars)
	elapsed := t.clock().Sub(start).Seconds()
	var urls []string
	for _, s := range sources {
		v, _ := s.(*pyjson.Object).Get("url")
		urls = append(urls, v.(string))
	}
	t.log().Info("[web_search] EXECUTED", "query", query, "sources", len(sources), "seconds", elapsed, "urls", urls)
	return Obj("query", query, "sources", sources, "elapsed_seconds", round1(elapsed)), nil
}

// round1 is Python's round(x, 1) for display values.
func round1(x float64) float64 { return math.Round(x*10) / 10 }

// scrapeResults is _scrape_results: fetch each result page through the guarded fetcher and
// keep its extracted text (capped), falling back to the search snippet when the page fails,
// is blocked, or has no text.
func scrapeResults(ctx context.Context, f *Fetcher, results []SearchResult, maxChars int) []any {
	sources := []any{}
	hdr := http.Header{"User-Agent": {fetchUserAgent}}
	for _, r := range results {
		status, body, err := f.Get(ctx, r.URL, hdr)
		if err == nil && status == http.StatusOK {
			if content := ExtractText(toText(body)); content != "" {
				sources = append(sources, Obj("title", r.Title, "url", r.URL, "content", truncRunes(content, maxChars)))
				continue
			}
		}
		if r.Snippet != "" {
			sources = append(sources, Obj("title", r.Title, "url", r.URL, "content", r.Snippet))
		}
	}
	return sources
}
