package servertools

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/modules/notifications"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
)

// Deep research (docs/cc/04 §3.11). Changed by D40 (04.Q9) / D31: the whole search → scrape
// → summarize chain is one durable queue job with a 10-minute deadline; results and failures
// are pushed to the speaker (the household when unknown) through the in-process notify
// service; <think> is stripped from the stored body (D8).

// DeepResearchJob is the queue job type that runs one research request.
const DeepResearchJob = "cc.deep_research"

// DeepResearchDeadline bounds a research run, measured from the tool call.
const DeepResearchDeadline = 10 * time.Minute

// Research limits (legacy _RESULT_COUNTS and batch_fetch(max_concurrent=3, max_chars=6000)).
var researchResults = map[string]int{"quick": 3, "thorough": 6}

const (
	researchMaxChars    = 6000
	researchConcurrency = 3
	researchSource      = "jarvis-command-center"
	researchCategory    = "deep_research"
)

const summarizeSystemPrompt = `You are a research analyst. Synthesize the following web sources into a comprehensive, well-organized summary.

Guidelines:
- Use markdown formatting with headers for major themes
- Cite sources inline using [Source Title](URL) format
- Note areas of agreement and disagreement between sources
- Highlight key findings, recommendations, and caveats
- Keep the summary focused and actionable
- If sources are insufficient, acknowledge limitations`

// JobEnqueuer enqueues a durable job (*queue.Queue).
type JobEnqueuer interface {
	Enqueue(ctx context.Context, jobType string, payload []byte, o queue.Options) (int64, error)
}

// LLM is the in-process LLM service (*llm.Service).
type LLM interface {
	Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error)
}

// Notifier is the in-process notifications service (*notifications.Module).
type Notifier interface {
	CreateInboxItem(ctx context.Context, tx *sql.Tx, in notifications.NewInboxItem) (notifications.InboxItem, error)
	Notify(ctx context.Context, tx *sql.Tx, source string, n notifications.Notification) (notifications.Delivery, error)
}

// researchRequest is the job payload.
type researchRequest struct {
	Query       string `json:"query"`
	Depth       string `json:"depth"`
	HouseholdID string `json:"household_id"`
	SpeakerID   *int64 `json:"speaker_user_id"`
	EnqueuedAt  int64  `json:"enqueued_at_ms"`
}

// DeepResearch is the deep_research tool: it validates, re-checks the web-search gate and
// enqueues the research job, answering immediately.
type DeepResearch struct {
	Jobs    JobEnqueuer
	Enabled WebSearchGate
	Log     *slog.Logger
	now     func() time.Time
}

// NewDeepResearch builds the tool.
func NewDeepResearch(jobs JobEnqueuer, enabled WebSearchGate, log *slog.Logger) *DeepResearch {
	return &DeepResearch{Jobs: jobs, Enabled: enabled, Log: log}
}

func (t *DeepResearch) Name() string               { return "deep_research" }
func (t *DeepResearch) Definition() *pyjson.Object { return LegacyDefinition("deep_research") }

func (t *DeepResearch) Execute(ctx context.Context, call Call, turn Turn) (any, error) {
	query := call.Str("query")
	depth := call.Str("depth")
	if query == "" {
		return Obj("error", "missing_query", "message", "A research query is required"), nil
	}
	if turn.ConversationID == "" {
		return Obj("error", "no_conversation", "message", "No conversation context available"), nil
	}
	if turn.HouseholdID == "" {
		return Obj("error", "no_household", "message", "No household context available"), nil
	}
	if !webAllowed(ctx, t.Enabled, turn) {
		logOr(t.Log).Info("deep_research blocked — web_search disabled for household", "household", turn.HouseholdID)
		return Obj("error", "web_search_disabled", "message", "Web search is disabled for this household."), nil
	}
	if depth != "quick" && depth != "thorough" {
		depth = "quick"
	}
	now := time.Now
	if t.now != nil {
		now = t.now
	}
	req := researchRequest{Query: query, Depth: depth, HouseholdID: turn.HouseholdID, EnqueuedAt: now().UnixMilli()}
	if turn.Speaker.Known() {
		id := turn.Speaker.UserID
		req.SpeakerID = &id
	}
	payload, _ := json.Marshal(req)
	if t.Jobs == nil {
		return Obj("error", "task_failed", "message", "Could not start research: no job queue"), nil
	}
	if _, err := t.Jobs.Enqueue(ctx, DeepResearchJob, payload, queue.Options{MaxAttempts: 1}); err != nil {
		logOr(t.Log).Error("Failed to start research task", "err", err)
		return Obj("error", "task_failed", "message", "Could not start research: "+err.Error()), nil
	}
	logOr(t.Log).Info("Started deep research", "query", query, "depth", depth, "household", turn.HouseholdID)
	return Obj("status", "accepted",
		"message", "Research started on: "+query+". I'll send you a notification when the results are ready."), nil
}

func logOr(l *slog.Logger) *slog.Logger {
	if l != nil {
		return l
	}
	return slog.Default()
}

// ResearchRunner runs DeepResearchJob jobs.
type ResearchRunner struct {
	Search WebSearcher
	Fetch  *Fetcher
	LLM    LLM
	Notify Notifier
	Log    *slog.Logger
	now    func() time.Time
}

// NewResearchRunner builds the job runner. fetch may be nil (the default guarded fetcher).
func NewResearchRunner(search WebSearcher, fetch *Fetcher, model LLM, notify Notifier, log *slog.Logger) *ResearchRunner {
	return &ResearchRunner{Search: search, Fetch: fetch, LLM: model, Notify: notify, Log: log}
}

// Register registers the job handler on q: one attempt (a failure push is the outcome, not a
// retry), and a lease past the deadline.
func (r *ResearchRunner) Register(q *queue.Queue) {
	q.Register(DeepResearchJob, queue.Handler{Run: r.Run, MaxAttempts: 1, Lease: DeepResearchDeadline + time.Minute})
}

func (r *ResearchRunner) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

var errResearchTimeout = errors.New("research timed out")

// Run is the queue handler. It never returns an error for a research failure: the failure is
// delivered as a push, and the job is done.
func (r *ResearchRunner) Run(ctx context.Context, job queue.Job) ([]byte, error) {
	var req researchRequest
	if err := json.Unmarshal(job.Payload, &req); err != nil {
		return nil, queue.Permanent(err)
	}
	start := r.clock()
	deadline := start.Add(DeepResearchDeadline)
	if req.EnqueuedAt > 0 {
		deadline = time.UnixMilli(req.EnqueuedAt).Add(DeepResearchDeadline)
	}
	var err error
	if !deadline.After(start) {
		err = errResearchTimeout
	} else {
		rctx, cancel := context.WithDeadline(ctx, deadline)
		err = r.research(rctx, req, start)
		if err != nil && errors.Is(rctx.Err(), context.DeadlineExceeded) {
			err = errResearchTimeout
		}
		cancel()
	}
	if err != nil {
		if ctx.Err() != nil && !errors.Is(err, errResearchTimeout) {
			return nil, ctx.Err() // jarvisd is shutting down; nothing to report
		}
		logOr(r.Log).Error("Deep research failed", "query", req.Query, "err", err)
		r.push(context.WithoutCancel(ctx), req, "Research Failed",
			"Research on \""+req.Query+"\" failed: "+err.Error(), map[string]any{"type": "deep_research_failed"})
	}
	return nil, nil
}

type scrapedPage struct {
	url, title, text string
	ok               bool
}

var thinkBlockRE = regexp.MustCompile(`<think>[\s\S]*?</think>`)

func (r *ResearchRunner) research(ctx context.Context, req researchRequest, start time.Time) error {
	n := researchResults[req.Depth]
	if n == 0 {
		n = 3
	}
	results, err := r.Search.Search(ctx, req.Query, n)
	if err != nil {
		logOr(r.Log).Error("Web search failed", "err", err)
		results = nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if len(results) > n {
		results = results[:n]
	}
	if len(results) == 0 {
		return errors.New("No search results found")
	}
	pages := r.scrape(ctx, results)
	var ok []scrapedPage
	for _, p := range pages {
		if p.ok {
			ok = append(ok, p)
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if len(ok) == 0 {
		return errors.New("Could not scrape any of the search results")
	}

	texts := make([]string, len(ok))
	for i, p := range ok {
		title := p.title
		if title == "" {
			title = "Source " + strconv.Itoa(i+1)
		}
		texts[i] = fmt.Sprintf("## Source %d: %s\nURL: %s\n\n%s", i+1, title, p.url, p.text)
	}
	user := "Research query: " + req.Query + "\n\n" + strings.Join(texts, "---") + "\n\n" +
		fmt.Sprintf("Please synthesize these %d sources into a comprehensive research summary.", len(ok))
	temp := 0.3
	resp, err := r.LLM.Chat(ctx, llm.ChatRequest{
		Label:       llm.LabelBackground,
		Temperature: &temp,
		Messages: []llm.Message{
			{Role: "system", Content: llm.TextContent(summarizeSystemPrompt)},
			{Role: "user", Content: llm.TextContent(user)},
		},
	})
	if err != nil {
		return err
	}
	summary := strings.TrimSpace(thinkBlockRE.ReplaceAllString(resp.Content, ""))
	if summary == "" {
		r.push(context.WithoutCancel(ctx), req, "Research Failed",
			"Research on \""+req.Query+"\" produced an empty summary", map[string]any{"type": "deep_research_failed"})
		return nil
	}
	preview := summary
	if runeLen(summary) > 200 {
		cut := truncRunes(summary, 200)
		if i := strings.LastIndex(cut, " "); i >= 0 {
			cut = cut[:i]
		}
		preview = cut + "..."
	}
	sources := make([]any, len(results))
	for i, s := range results {
		sources[i] = map[string]any{"title": s.Title, "url": s.URL}
	}
	item, err := r.Notify.CreateInboxItem(context.WithoutCancel(ctx), nil, notifications.NewInboxItem{
		HouseholdID: req.HouseholdID, UserID: req.SpeakerID,
		Title: "Research: " + req.Query, Summary: preview, Body: summary,
		Category: researchCategory, SourceService: researchSource,
		Metadata: map[string]any{
			"query": req.Query, "depth": req.Depth, "sources": sources,
			"pages_scraped": len(ok), "pages_attempted": len(results),
			"elapsed_seconds": round1(r.clock().Sub(start).Seconds()),
		},
	})
	if err != nil {
		return err
	}
	r.push(context.WithoutCancel(ctx), req, "Research Complete", "Results ready: "+req.Query,
		map[string]any{"type": "deep_research", "inbox_item_id": item.ID})
	logOr(r.Log).Info("Research delivered", "query", req.Query, "inbox_item", item.ID)
	return nil
}

// scrape fetches the result pages, three at a time, keeping the readable text.
func (r *ResearchRunner) scrape(ctx context.Context, results []SearchResult) []scrapedPage {
	out := make([]scrapedPage, len(results))
	sem := make(chan struct{}, researchConcurrency)
	var wg sync.WaitGroup
	hdr := http.Header{"User-Agent": {fetchUserAgent}}
	for i, res := range results {
		wg.Add(1)
		go func(i int, u string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				out[i] = scrapedPage{url: u}
				return
			}
			defer func() { <-sem }()
			p := scrapedPage{url: u}
			status, body, err := r.Fetch.Get(ctx, u, hdr)
			if err == nil && status == http.StatusOK {
				page := toText(body)
				if text := ExtractText(page); text != "" {
					p.text = truncRunes(text, researchMaxChars)
					p.title = pageTitle(page)
					p.ok = true
				}
			}
			out[i] = p
		}(i, res.URL)
	}
	wg.Wait()
	return out
}

// push notifies the speaker, or the household when the speaker is unknown (D40 04.Q9).
func (r *ResearchRunner) push(ctx context.Context, req researchRequest, title, body string, data map[string]any) {
	n := notifications.Notification{
		TargetType: "household", TargetID: req.HouseholdID,
		Title: title, Body: body, Data: data, Priority: "default", Category: researchCategory,
	}
	if req.SpeakerID != nil {
		n.TargetType, n.TargetID = "user", strconv.FormatInt(*req.SpeakerID, 10)
	}
	if _, err := r.Notify.Notify(ctx, nil, researchSource, n); err != nil {
		logOr(r.Log).Warn("Research push failed", "err", err)
	}
}
