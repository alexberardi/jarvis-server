package cc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/watches"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
	"github.com/alexberardi/jarvis-server/internal/platform/scheduler"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// Web watches (docs/autonomy/PLAN.md §2 "Sources", A9): each check fetches the watch's page or
// runs its search, reads the value with the background model, publishes a watch.web signal and
// hands the value to observeWatch. Nothing leaves the house unless web_scraping.allow_external
// is on.

// WatchFetcher is the GET a web watch reads its page with (the SSRF-guarded servertools.Fetcher).
type WatchFetcher interface {
	Get(ctx context.Context, url string, h http.Header) (int, []byte, error)
}

const (
	// watchUserAgent is quick_search's User-Agent (servertools.fetchUserAgent).
	watchUserAgent    = "Mozilla/5.0 (compatible; JarvisBot/1.0)"
	watchTextRunes    = 6000
	watchSearchCount  = 5
	watchSignalMaxTTL = 7 * 24 * time.Hour
)

var watchValueSchema = json.RawMessage(`{"type":"object","properties":{"found":{"type":"boolean"},"value":{"type":"string"}},"required":["found","value"]}`)

// runWatchCheck is a web watch's check job. The next check is armed before anything can fail.
func (m *Module) runWatchCheck(ctx context.Context, job queue.Job) ([]byte, error) {
	f, err := scheduler.DecodeFire(job.Payload)
	if err != nil {
		return nil, queue.Permanent(err)
	}
	var p struct {
		WatchID string `json:"watch_id"`
	}
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		return nil, queue.Permanent(err)
	}
	w, err := m.watchByID(ctx, p.WatchID)
	if err != nil || w == nil {
		return nil, err
	}
	now := m.now()
	if w.State != watches.StateActive || w.SourceKind != watches.SourceWeb || (w.ExpiresAt != nil && !now.Before(*w.ExpiresAt)) {
		return nil, nil
	}
	cadence := m.watchCadence(ctx, *w)
	m.armWatch(ctx, *w, now.Add(cadence))

	sc := settings.Scope{HouseholdID: w.HouseholdID}
	fail := func(msg string) ([]byte, error) {
		m.observeWatch(ctx, *w, watchObservation{Err: msg, At: now})
		return nil, nil
	}
	switch {
	case !m.watchesEnabled(ctx, w.HouseholdID):
		return fail("watches are turned off for this household")
	case !m.settings.Bool(ctx, settingWebScrapingExternal, sc):
		return fail("web fetches are off (web_scraping.allow_external)")
	case w.Source.Query != "" && !m.settings.Bool(ctx, settingWebSearch, sc):
		return fail("web search is off")
	}
	text, url, err := m.watchText(ctx, *w)
	if err != nil {
		return fail(err.Error())
	}
	value, err := m.extractWatchValue(ctx, *w, text)
	if err != nil {
		return fail(err.Error())
	}
	ttl := min(2*cadence, watchSignalMaxTTL)
	subject, summary := w.Title, w.Title+": "+value
	if _, err := m.saveSignal(ctx, newSignal{HouseholdID: w.HouseholdID, SourceKey: "watch:" + w.ID, Kind: "watch.web",
		Subject: &subject, Summary: &summary, Facts: map[string]any{"value": value, "previous": w.LastValue, "url": url},
		UserID: w.UserID, TTL: ttl}); err != nil {
		m.deps.Log.Warn("cc: watch signal not saved", "id", w.ID, "err", err)
	}
	m.observeWatch(ctx, *w, watchObservation{Value: value, SourceURL: url, At: now})
	return nil, nil
}

// watchByID loads a watch from its trigger's id alone; nil, nil when it is gone.
func (m *Module) watchByID(ctx context.Context, id string) (*watches.Watch, error) {
	var hh string
	err := m.deps.DB.Read.QueryRowContext(ctx, `SELECT household_id FROM cc_watches WHERE id = ?`, id).Scan(&hh)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return m.watches.Get(ctx, hh, id)
}

// watchCadence is the watch's check interval, at least watches.min_cadence_minutes.
func (m *Module) watchCadence(ctx context.Context, w watches.Watch) time.Duration {
	cadence := time.Duration(w.CadenceSeconds) * time.Second
	if m.settings != nil {
		floor := time.Duration(m.settings.Int(ctx, settingWatchesMinCadence, settings.Scope{HouseholdID: w.HouseholdID})) * time.Minute
		cadence = max(cadence, floor)
	}
	if cadence <= 0 {
		cadence = time.Hour
	}
	return cadence
}

// watchText is the text a check reads: the page's, or the search results' titles and snippets
// (one result per line). url is the page, or the first result's link.
func (m *Module) watchText(ctx context.Context, w watches.Watch) (text, url string, err error) {
	if w.Source.URL != "" {
		fetcher := m.WatchFetcher
		if fetcher == nil {
			fetcher = &servertools.Fetcher{}
		}
		status, body, err := fetcher.Get(ctx, w.Source.URL, http.Header{"User-Agent": {watchUserAgent}})
		if err != nil {
			return "", "", fmt.Errorf("fetch failed: %w", err)
		}
		if status >= 400 {
			return "", "", fmt.Errorf("fetch failed: HTTP %d", status)
		}
		return truncateRunes(servertools.ExtractText(string(body)), watchTextRunes), w.Source.URL, nil
	}
	search := m.WebSearch
	if search == nil {
		search = &servertools.DuckDuckGo{}
	}
	results, err := search.Search(ctx, w.Source.Query, watchSearchCount)
	if err != nil {
		return "", "", fmt.Errorf("search failed: %w", err)
	}
	if len(results) == 0 {
		return "", "", errors.New("the search found nothing")
	}
	lines := make([]string, len(results))
	for i, r := range results {
		lines[i] = r.Title + ": " + r.Snippet
	}
	return truncateRunes(strings.Join(lines, "\n"), watchTextRunes), results[0].URL, nil
}

func truncateRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// extractWatchValue asks the background model for the watch's value in text.
func (m *Module) extractWatchValue(ctx context.Context, w watches.Watch, text string) (string, error) {
	if m.LLM == nil {
		return "", errors.New("no model to read the page")
	}
	temp, maxTokens, budget := 0.0, 200, 0
	resp, err := m.LLM.Chat(ctx, llm.ChatRequest{
		Label: "background", Temperature: &temp, MaxTokens: &maxTokens, ReasoningBudget: &budget,
		ResponseFormat: &llm.ResponseFormat{Type: "json_object", JSONSchema: watchValueSchema},
		Messages: []llm.Message{
			{Role: "system", Content: llm.TextContent("You read one value from a web page for a household's watch. Answer only JSON.")},
			{Role: "user", Content: llm.TextContent("Read this from the page: " + w.Source.Extract + ".\n" +
				"Give the value exactly as the page shows it, in a few words (for a price, include the currency).\n" +
				"If the page doesn't show it, answer found=false.\n\nPage:\n" + text)},
		},
	})
	if err != nil {
		return "", fmt.Errorf("couldn't read the page: %w", err)
	}
	var out struct {
		Found bool   `json:"found"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal([]byte(resp.Content), &out); err != nil {
		return "", errors.New("couldn't read the page: the model's answer wasn't JSON")
	}
	if v := strings.TrimSpace(out.Value); out.Found && v != "" {
		return v, nil
	}
	return "", fmt.Errorf("couldn't find %s on the page", w.Source.Extract)
}

// reconcileWatchTriggers arms every active web watch that has no next check (an import, or a
// crash between a check and its re-arm): at its last check plus its cadence, else now.
func (m *Module) reconcileWatchTriggers(ctx context.Context) error {
	rows, err := m.deps.DB.Read.QueryContext(ctx, `SELECT id FROM cc_watches WHERE state = 'active' AND source_kind = 'web'`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		st, err := m.deps.Scheduler.Status(ctx, watchTrigger(id))
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil && !st.NextFireAt.IsZero() {
			continue
		}
		w, err := m.watchByID(ctx, id)
		if err != nil {
			return err
		}
		if w == nil {
			continue
		}
		at := m.now()
		if w.LastCheckedAt != nil {
			at = w.LastCheckedAt.Add(m.watchCadence(ctx, *w))
		}
		m.armWatch(ctx, *w, at)
	}
	return nil
}
