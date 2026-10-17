package cc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/watches"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
	"github.com/alexberardi/jarvis-server/internal/platform/scheduler"
)

// fakeWatchFetcher answers every GET with one status and body and records the requests.
type fakeWatchFetcher struct {
	mu     sync.Mutex
	status int
	body   string
	urls   []string
	agents []string
}

func (f *fakeWatchFetcher) Get(_ context.Context, url string, h http.Header) (int, []byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.urls = append(f.urls, url)
	f.agents = append(f.agents, h.Get("User-Agent"))
	return f.status, []byte(f.body), nil
}

func (f *fakeWatchFetcher) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.urls)
}

// fakeWatchSearcher returns fixed results and records the queries.
type fakeWatchSearcher struct {
	mu      sync.Mutex
	results []servertools.SearchResult
	queries []string
}

func (s *fakeWatchSearcher) Search(_ context.Context, q string, _ int) ([]servertools.SearchResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queries = append(s.queries, q)
	return s.results, nil
}

func (s *fakeWatchSearcher) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.queries)
}

// fakeWatchLLM answers Chat with reply and records the requests.
type fakeWatchLLM struct {
	mu    sync.Mutex
	reply string
	reqs  []llm.ChatRequest
}

func (f *fakeWatchLLM) Chat(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, req)
	return &llm.ChatResponse{Content: f.reply}, nil
}

func (f *fakeWatchLLM) Stream(context.Context, llm.ChatRequest) (<-chan llm.Frame, error) {
	return nil, errors.New("no stream")
}

func (f *fakeWatchLLM) Embed(context.Context, []string) (llm.Embeddings, error) {
	return llm.Embeddings{}, errors.New("no embeddings")
}

// userPrompt is the last request's user message.
func (f *fakeWatchLLM) userPrompt(t *testing.T) string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reqs) == 0 {
		t.Fatal("no LLM call")
	}
	msgs := f.reqs[len(f.reqs)-1].Messages
	return *msgs[len(msgs)-1].Content.Text
}

type webWatchEnv struct {
	*sigEnv
	fetch  *fakeWatchFetcher
	search *fakeWatchSearcher
	model  *fakeWatchLLM
	sched  *scheduler.Scheduler
}

// newWebWatchEnv has watches, external fetches and web search on, and a scheduler for the triggers.
func newWebWatchEnv(t *testing.T) *webWatchEnv {
	t.Helper()
	we := &webWatchEnv{fetch: &fakeWatchFetcher{status: 200, body: "<html><body><p>Kettle $99</p></body></html>"},
		search: &fakeWatchSearcher{}, model: &fakeWatchLLM{reply: `{"found":true,"value":"$99"}`}}
	we.sigEnv = newSigEnv(t, func(m *Module) {
		m.WatchFetcher, m.WebSearch, m.LLM = we.fetch, we.search, we.model
	})
	we.set(settingHouseholdTimezone, "America/New_York", sigHH)
	we.set(settingWatchesEnabled, true, sigHH)
	we.set(settingWebScrapingExternal, true, sigHH)
	we.set(settingWebSearch, true, sigHH)
	we.sched = scheduler.New(we.d, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	we.m.deps.Scheduler = we.sched
	return we
}

func (we *webWatchEnv) create(src watches.Source, cadence int) watches.Watch {
	we.t.Helper()
	uid := int64(7)
	w, err := we.m.watches.Create(context.Background(), watches.Watch{HouseholdID: sigHH, UserID: &uid, Title: "Kettle price",
		SourceKind: watches.SourceWeb, Source: src, Condition: priceBelow100, FirePolicy: watches.FireOnce, CadenceSeconds: cadence})
	if err != nil {
		we.t.Fatal(err)
	}
	return w
}

func (we *webWatchEnv) createURL() watches.Watch {
	return we.create(watches.Source{URL: watchURLItem, Extract: "the price"}, 0)
}

// check runs the watch's check job as the scheduler would.
func (we *webWatchEnv) check(id string) {
	we.t.Helper()
	inner, _ := json.Marshal(map[string]string{"watch_id": id})
	p, _ := json.Marshal(scheduler.Fire{Trigger: watchTrigger(id), ScheduledAt: we.clock(), Payload: inner})
	if _, err := we.m.runWatchCheck(context.Background(), queue.Job{Type: watchCheckJob, Payload: p}); err != nil {
		we.t.Fatal(err)
	}
}

// nextCheck is when the watch's trigger fires next (zero when there is none).
func (we *webWatchEnv) nextCheck(id string) time.Time {
	we.t.Helper()
	st, err := we.sched.Status(context.Background(), watchTrigger(id))
	if err != nil {
		return time.Time{}
	}
	return st.NextFireAt
}

func (we *webWatchEnv) wantError(id, want string) {
	we.t.Helper()
	if got := we.watch(sigHH, id); got.LastError != want || got.LastCheckedAt == nil {
		we.t.Fatalf("last_error %q, want %q", got.LastError, want)
	}
	if n := len(we.alerts()); n != 0 {
		we.t.Fatalf("%d alerts", n)
	}
}

func TestWebWatchURLFires(t *testing.T) {
	we := newWebWatchEnv(t)
	w := we.createURL()
	we.check(w.ID)

	if we.fetch.calls() != 1 || we.fetch.urls[0] != watchURLItem || we.fetch.agents[0] != "Mozilla/5.0 (compatible; JarvisBot/1.0)" {
		t.Fatal(we.fetch.urls, we.fetch.agents)
	}
	req := we.model.reqs[0]
	if req.Label != "background" || req.Temperature == nil || *req.Temperature != 0 || req.MaxTokens == nil || *req.MaxTokens != 200 ||
		req.ReasoningBudget == nil || *req.ReasoningBudget != 0 || req.ResponseFormat == nil || req.ResponseFormat.Type != "json_object" ||
		!strings.Contains(string(req.ResponseFormat.JSONSchema), `"found"`) || len(req.Messages) != 2 || req.Messages[0].Role != "system" {
		t.Fatalf("%+v", req)
	}
	if p := we.model.userPrompt(t); !strings.HasPrefix(p, "Read this from the page: the price.\n") || !strings.HasSuffix(p, "\n\nPage:\nKettle $99") {
		t.Fatalf("%q", p)
	}
	alerts := we.alerts()
	if len(alerts) != 1 || alerts[0]["value"] != "$99" || alerts[0]["source_url"] != watchURLItem {
		t.Fatal(alerts)
	}
	if got := we.watch(sigHH, w.ID); got.LastValue != "$99" || got.LastError != "" {
		t.Fatal(got)
	}

	var kind, subject, summary, facts, expires string
	var uid int64
	if err := we.d.Read.QueryRow(`SELECT kind, subject, summary, facts, user_id, expires_at FROM cc_signals
		WHERE household_id = ? AND source_key = ?`, sigHH, "watch:"+w.ID).Scan(&kind, &subject, &summary, &facts, &uid, &expires); err != nil {
		t.Fatal(err)
	}
	var f map[string]any
	_ = json.Unmarshal([]byte(facts), &f)
	if kind != "watch.web" || subject != "Kettle price" || summary != "Kettle price: $99" || uid != 7 ||
		f["value"] != "$99" || f["previous"] != "" || f["url"] != watchURLItem || expires != dbTime(we.clock().Add(2*time.Hour)) {
		t.Fatal(kind, subject, summary, facts, uid, expires)
	}
	// The watch fired once and is done, so its next check is dropped.
	if at := we.nextCheck(w.ID); !at.IsZero() {
		t.Fatal("still armed", at)
	}
}

func TestWebWatchArmsNextCheck(t *testing.T) {
	we := newWebWatchEnv(t)
	we.model.reply = `{"found":true,"value":"$129"}`
	w := we.createURL()
	we.check(w.ID)
	if at := we.nextCheck(w.ID); !at.Equal(we.clock().Add(time.Hour)) {
		t.Fatal(at)
	}
	// A cadence under watches.min_cadence_minutes (30) waits the minimum.
	short := we.create(watches.Source{URL: watchURLItem, Extract: "the price"}, 600)
	we.check(short.ID)
	if at := we.nextCheck(short.ID); !at.Equal(we.clock().Add(30 * time.Minute)) {
		t.Fatal(at)
	}
}

func TestWebWatchExternalOff(t *testing.T) {
	we := newWebWatchEnv(t)
	we.set(settingWebScrapingExternal, false, sigHH)
	w := we.createURL()
	we.check(w.ID)
	if we.fetch.calls() != 0 {
		t.Fatal("fetched")
	}
	we.wantError(w.ID, "web fetches are off (web_scraping.allow_external)")
	if at := we.nextCheck(w.ID); !at.Equal(we.clock().Add(time.Hour)) {
		t.Fatal("not re-armed", at)
	}
}

func TestWebWatchWatchesOff(t *testing.T) {
	we := newWebWatchEnv(t)
	we.set(settingWatchesEnabled, false, sigHH)
	w := we.createURL()
	we.check(w.ID)
	if we.fetch.calls() != 0 {
		t.Fatal("fetched")
	}
	we.wantError(w.ID, "watches are turned off for this household")
}

func TestWebWatchQuery(t *testing.T) {
	we := newWebWatchEnv(t)
	we.search.results = []servertools.SearchResult{
		{Title: "Kettle deals", URL: "https://shop.example.com/k", Snippet: "Now $99"},
		{Title: "Kettle review", URL: "https://review.example.com/k", Snippet: "Was $129"},
	}
	w := we.create(watches.Source{Query: "kettle price", Extract: "the price"}, 0)

	we.set(settingWebSearch, false, sigHH)
	we.check(w.ID)
	if we.search.calls() != 0 {
		t.Fatal("searched")
	}
	we.wantError(w.ID, "web search is off")

	we.set(settingWebSearch, true, sigHH)
	we.check(w.ID)
	if we.search.calls() != 1 || we.search.queries[0] != "kettle price" || we.fetch.calls() != 0 {
		t.Fatal(we.search.queries, we.fetch.calls())
	}
	if p := we.model.userPrompt(t); !strings.HasSuffix(p, "\n\nPage:\nKettle deals: Now $99\nKettle review: Was $129") {
		t.Fatalf("%q", p)
	}
	alerts := we.alerts()
	if len(alerts) != 1 || alerts[0]["source_url"] != "https://shop.example.com/k" {
		t.Fatal(alerts)
	}
}

func TestWebWatchNotFound(t *testing.T) {
	we := newWebWatchEnv(t)
	we.model.reply = `{"found":false,"value":""}`
	w := we.createURL()
	we.check(w.ID)
	we.wantError(w.ID, "couldn't find the price on the page")
	var n int
	we.d.Read.QueryRow(`SELECT COUNT(*) FROM cc_signals WHERE source_key = ?`, "watch:"+w.ID).Scan(&n)
	if n != 0 {
		t.Fatal("signal saved")
	}
}

func TestWebWatchHTTPError(t *testing.T) {
	we := newWebWatchEnv(t)
	we.fetch.status = 404
	w := we.createURL()
	we.check(w.ID)
	we.wantError(w.ID, "fetch failed: HTTP 404")
	if len(we.model.reqs) != 0 {
		t.Fatal("asked the model")
	}
}

func TestWebWatchTruncatesText(t *testing.T) {
	we := newWebWatchEnv(t)
	we.fetch.body = "<p>" + strings.Repeat("é", 7000) + "</p>"
	w := we.createURL()
	we.check(w.ID)
	p := we.model.userPrompt(t)
	page := p[strings.Index(p, "\n\nPage:\n")+len("\n\nPage:\n"):]
	if page != strings.Repeat("é", 6000) {
		t.Fatalf("%d runes", len([]rune(page)))
	}
}

func TestWebWatchSkipsPausedAndExpired(t *testing.T) {
	we := newWebWatchEnv(t)
	ctx := context.Background()
	paused := we.createURL()
	if _, err := we.m.watches.SetState(ctx, sigHH, paused.ID, watches.StatePaused); err != nil {
		t.Fatal(err)
	}
	we.check(paused.ID)
	expired := we.createURL()
	we.advance(watches.DefaultTTL + time.Hour)
	we.check(expired.ID)
	we.check("wt_missing")
	if we.fetch.calls() != 0 {
		t.Fatal("fetched")
	}
	for _, id := range []string{paused.ID, expired.ID} {
		if at := we.nextCheck(id); !at.IsZero() {
			t.Fatal("armed", id, at)
		}
	}
}

func TestWebWatchReconcile(t *testing.T) {
	we := newWebWatchEnv(t)
	ctx := context.Background()
	fresh := we.createURL()
	checked := we.createURL()
	if err := we.m.watches.RecordCheck(ctx, checked.ID, "$129", ""); err != nil {
		t.Fatal(err)
	}
	paused := we.createURL()
	we.m.watches.SetState(ctx, sigHH, paused.ID, watches.StatePaused)
	armed := we.createURL()
	we.m.armWatch(ctx, armed, we.clock().Add(5*time.Minute))

	if err := we.m.reconcileWatchTriggers(ctx); err != nil {
		t.Fatal(err)
	}
	if at := we.nextCheck(fresh.ID); !at.Equal(we.clock()) {
		t.Fatal("fresh", at)
	}
	if at := we.nextCheck(checked.ID); !at.Equal(we.clock().Add(time.Hour)) {
		t.Fatal("checked", at)
	}
	if at := we.nextCheck(paused.ID); !at.IsZero() {
		t.Fatal("paused", at)
	}
	if at := we.nextCheck(armed.ID); !at.Equal(we.clock().Add(5 * time.Minute)) {
		t.Fatal("armed", at)
	}
}
