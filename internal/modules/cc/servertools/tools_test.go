package servertools

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/modules/notifications"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
)

func TestDefinitionsMatchFixture(t *testing.T) {
	r := NewRegistry()
	r.Register(NewQuickSearch(nil, nil, nil, nil))
	r.Register(NewDeepResearch(nil, nil, nil))
	r.Register(NewHAEntities())
	for _, n := range r.Names() {
		tool, _ := r.Get(n)
		if got := pyjson.Dumps(tool.Definition(), true); got != legacyDefinitions[n] {
			t.Errorf("%s definition differs:\n got %s\nwant %s", n, got, legacyDefinitions[n])
		}
	}
	if got := r.Names(); strings.Join(got, ",") != "deep_research,get_ha_entities,quick_search" {
		t.Errorf("names %v", got)
	}
}

func dumps(v any) string { return pyjson.Dumps(v, true) }

func args(kv ...any) *pyjson.Object { return Obj(kv...) }

type fakeSearch struct {
	res   []SearchResult
	err   error
	calls int
	max   int
}

func (f *fakeSearch) Search(_ context.Context, _ string, max int) ([]SearchResult, error) {
	f.calls++
	f.max = max
	return f.res, f.err
}

func gate(on bool) WebSearchGate { return func(context.Context, string) bool { return on } }

var turn = Turn{ConversationID: "c1", HouseholdID: "hh1", NodeID: "n1"}

func pagesServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/a":
			w.Write([]byte("<html><title>Page A</title><body><p>Alpha &amp; " + strings.Repeat("x", 5000) + "</p></body></html>"))
		case "/b":
			w.Write([]byte("<html><title>Page B</title><body>Bravo text</body></html>"))
		case "/empty":
			w.Write([]byte("<script>only()</script>"))
		case "/private":
			http.Redirect(w, r, "http://192.168.1.1/", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestQuickSearch(t *testing.T) {
	srv := pagesServer()
	defer srv.Close()
	ctx := context.Background()
	s := &fakeSearch{res: []SearchResult{
		{Title: "A", URL: srv.URL + "/a", Snippet: "snipA"},
		{Title: "P", URL: srv.URL + "/private", Snippet: "snipP"},
		{Title: "extra", URL: srv.URL + "/b"},
	}}
	tool := NewQuickSearch(s, loopbackOK(), gate(true), nil)

	got, _ := tool.Execute(ctx, Call{Name: "quick_search", Args: args()}, turn)
	if dumps(got) != `{"error": "missing_query", "message": "A search query is required"}` {
		t.Fatal(dumps(got))
	}
	off := NewQuickSearch(s, loopbackOK(), gate(false), nil)
	got, _ = off.Execute(ctx, Call{Args: args("query", "mars")}, turn)
	if dumps(got) != `{"error": "web_search_disabled", "message": "Web search is disabled for this household."}` || s.calls != 0 {
		t.Fatal("gate:", dumps(got))
	}
	// No household → fail closed even with the gate on.
	got, _ = tool.Execute(ctx, Call{Args: args("query", "mars")}, Turn{ConversationID: "c"})
	if !strings.Contains(dumps(got), "web_search_disabled") {
		t.Fatal(dumps(got))
	}

	got, _ = tool.Execute(ctx, Call{Args: args("query", "mars")}, turn)
	o := got.(*pyjson.Object)
	if strings.Join(o.Keys(), ",") != "query,sources,elapsed_seconds" || s.max != 2 {
		t.Fatalf("keys %v max %d", o.Keys(), s.max)
	}
	src, _ := o.Get("sources")
	list := src.([]any)
	if len(list) != 2 {
		t.Fatalf("sources %s", dumps(src))
	}
	a := list[0].(*pyjson.Object)
	content, _ := a.Get("content")
	if len([]rune(content.(string))) != 4000 || !strings.HasPrefix(content.(string), "Page A Alpha & xxx") {
		t.Fatalf("content %q", content.(string)[:40])
	}
	// The private redirect falls back to the snippet.
	if dumps(list[1]) != `{"title": "P", "url": "`+srv.URL+`/private", "content": "snipP"}` {
		t.Fatal(dumps(list[1]))
	}

	none := NewQuickSearch(&fakeSearch{err: errors.New("ddg down")}, nil, gate(true), nil)
	got, _ = none.Execute(ctx, Call{Args: args("query", "mars")}, turn)
	if dumps(got) != `{"error": "no_results", "message": "No search results found for: mars"}` {
		t.Fatal(dumps(got))
	}
}

func TestHAEntities(t *testing.T) {
	agents, err := pyjson.Loads(`{"home_assistant": {
		"floors": {"Downstairs": ["Kitchen", "Living Room"], "Upstairs": ["Bedroom"]},
		"light_controls": {"Kitchen Lights": {"entity_id": "light.kitchen_group", "state": "on", "area": "Kitchen"}},
		"device_controls": {
			"light": [
				{"entity_id": "light.kitchen_group", "name": "Kitchen Group", "state": "on", "area": "Kitchen"},
				{"entity_id": "light.lamp", "name": "Lamp", "state": "off", "area": "Living Room"},
				{"entity_id": "light.dead", "name": "Dead", "state": "unavailable", "area": "Kitchen"},
				{"entity_id": "light.bed", "name": "Bed", "state": "off", "area": "Bedroom"},
				{"entity_id": "light.loose", "name": "Loose"}
			],
			"lock": [{"entity_id": "lock.front", "name": "Front Door", "state": "locked", "area": ""}]
		}}}`)
	if err != nil {
		t.Fatal(err)
	}
	tr := turn
	tr.Agents = agents.(*pyjson.Object)
	tool := NewHAEntities()
	run := func(kv ...any) string {
		res, err := tool.Execute(context.Background(), Call{Args: args(kv...)}, tr)
		if err != nil {
			return "ERR " + err.Error()
		}
		return dumps(res)
	}
	cases := []struct {
		args []any
		want string
	}{
		{[]any{"domain", "light"}, `{"domain": "light", "count": 4, "devices": ["- Kitchen \u2014 Kitchen Lights: light.kitchen_group (currently on)", "- Living Room \u2014 Lamp: light.lamp (currently off)", "- Bedroom \u2014 Bed: light.bed (currently off)", "- Loose: light.loose (currently unknown)"]}`},
		{[]any{"domain", "light", "area", "living room"}, `{"domain": "light", "count": 1, "devices": ["- Living Room \u2014 Lamp: light.lamp (currently off)"]}`},
		{[]any{"domain", "light", "floor", "downstairs"}, `{"domain": "light", "count": 2, "devices": ["- Kitchen \u2014 Kitchen Lights: light.kitchen_group (currently on)", "- Living Room \u2014 Lamp: light.lamp (currently off)"]}`},
		{[]any{"domain", "light", "floor", "Upstairs", "area", "Kitchen"}, `{"domain": "light", "count": 0, "devices": []}`},
		{[]any{"domain", "light", "floor", "Attic"}, `{"error": "floor_not_found", "message": "Floor 'Attic' not found. Available floors: Downstairs, Upstairs"}`},
		{[]any{"domain", "lock"}, `{"domain": "lock", "count": 1, "devices": ["- Front Door: lock.front (currently locked)"]}`},
		{[]any{"domain", "fan"}, `{"domain": "fan", "count": 0, "devices": []}`},
		{[]any{}, "ERR GetHAEntitiesTool.execute() missing 1 required positional argument: 'domain'"},
	}
	for _, c := range cases {
		if got := run(c.args...); got != c.want {
			t.Errorf("%v:\n got %s\nwant %s", c.args, got, c.want)
		}
	}
	tr.Agents = nil
	if got := run("domain", "light"); got != `{"error": "no_ha_data", "message": "This node has no Home Assistant data"}` {
		t.Error(got)
	}
	tr.ConversationID = ""
	if got := run("domain", "light"); !strings.Contains(got, "missing_conversation_id") {
		t.Error(got)
	}
	// device_agent fallback, and home_assistant present-but-empty wins over it.
	ag, _ := pyjson.Loads(`{"device_agent": {"device_controls": {"switch": [{"entity_id": "switch.a", "name": "A", "state": "on"}]}}}`)
	tr = turn
	tr.Agents = ag.(*pyjson.Object)
	if got := run("domain", "switch"); got != `{"domain": "switch", "count": 1, "devices": ["- A: switch.a (currently on)"]}` {
		t.Error(got)
	}
	ag, _ = pyjson.Loads(`{"home_assistant": {}, "device_agent": {"device_controls": {}}}`)
	tr.Agents = ag.(*pyjson.Object)
	if got := run("domain", "switch"); !strings.Contains(got, "no_ha_data") {
		t.Error(got)
	}
}

// --- deep research ---

type fakeQueue struct {
	jobType string
	payload []byte
	err     error
}

func (q *fakeQueue) Enqueue(_ context.Context, jt string, p []byte, _ queue.Options) (int64, error) {
	q.jobType, q.payload = jt, p
	return 1, q.err
}

type fakeLLM struct {
	content string
	err     error
	block   bool
	req     llm.ChatRequest
}

func (f *fakeLLM) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	f.req = req
	if f.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if f.err != nil {
		return nil, f.err
	}
	return &llm.ChatResponse{Content: f.content}, nil
}

type fakeNotify struct {
	mu     sync.Mutex
	inbox  []notifications.NewInboxItem
	pushes []notifications.Notification
}

func (f *fakeNotify) CreateInboxItem(_ context.Context, _ *sql.Tx, in notifications.NewInboxItem) (notifications.InboxItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inbox = append(f.inbox, in)
	return notifications.InboxItem{ID: "inbox-1"}, nil
}

func (f *fakeNotify) Notify(_ context.Context, _ *sql.Tx, _ string, n notifications.Notification) (notifications.Delivery, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pushes = append(f.pushes, n)
	return notifications.Delivery{}, nil
}

func TestDeepResearchTool(t *testing.T) {
	ctx := context.Background()
	q := &fakeQueue{}
	tool := NewDeepResearch(q, gate(true), nil)
	tr := turn
	tr.Speaker = Speaker{UserID: 7, Name: "Alex"}
	got, _ := tool.Execute(ctx, Call{Args: args("query", "keyboards", "depth", "bogus")}, tr)
	if dumps(got) != `{"status": "accepted", "message": "Research started on: keyboards. I'll send you a notification when the results are ready."}` {
		t.Fatal(dumps(got))
	}
	var req researchRequest
	_ = json.Unmarshal(q.payload, &req)
	if q.jobType != DeepResearchJob || req.Depth != "quick" || req.SpeakerID == nil || *req.SpeakerID != 7 || req.HouseholdID != "hh1" {
		t.Fatalf("job %s %+v", q.jobType, req)
	}
	for _, c := range []struct {
		tool *DeepResearch
		call *pyjson.Object
		turn Turn
		want string
	}{
		{tool, args(), tr, "missing_query"},
		{tool, args("query", "x"), Turn{}, "no_conversation"},
		{tool, args("query", "x"), Turn{ConversationID: "c"}, "no_household"},
		{NewDeepResearch(q, gate(false), nil), args("query", "x"), tr, `{"error": "web_search_disabled", "message": "Web search is disabled for this household."}`},
		{NewDeepResearch(&fakeQueue{err: errors.New("db locked")}, gate(true), nil), args("query", "x"), tr, `{"error": "task_failed", "message": "Could not start research: db locked"}`},
	} {
		got, _ := c.tool.Execute(ctx, Call{Args: c.call}, c.turn)
		if s := dumps(got); !strings.Contains(s, c.want) {
			t.Errorf("want %s, got %s", c.want, s)
		}
	}
}

func job(t *testing.T, req researchRequest) queue.Job {
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return queue.Job{Type: DeepResearchJob, Payload: b}
}

func TestResearchRunnerHappyPath(t *testing.T) {
	srv := pagesServer()
	defer srv.Close()
	s := &fakeSearch{res: []SearchResult{
		{Title: "A", URL: srv.URL + "/a"}, {Title: "B", URL: srv.URL + "/b"},
		{Title: "E", URL: srv.URL + "/empty"}, {Title: "P", URL: srv.URL + "/private"},
	}}
	model := &fakeLLM{content: "<think>hmm</think>\n## Summary\n" + strings.Repeat("word ", 60)}
	n := &fakeNotify{}
	r := NewResearchRunner(s, loopbackOK(), model, n, nil)
	uid := int64(7)
	if _, err := r.Run(context.Background(), job(t, researchRequest{Query: "keyboards", Depth: "thorough", HouseholdID: "hh1", SpeakerID: &uid, EnqueuedAt: time.Now().UnixMilli()})); err != nil {
		t.Fatal(err)
	}
	if s.max != 6 || model.req.Label != llm.LabelBackground || *model.req.Temperature != 0.3 {
		t.Fatalf("search max %d label %s", s.max, model.req.Label)
	}
	user := *model.req.Messages[1].Content.Text
	if !strings.HasPrefix(user, "Research query: keyboards\n\n## Source 1: Page A\nURL: "+srv.URL+"/a\n\nPage A Alpha") ||
		!strings.Contains(user, "---## Source 2: Page B\nURL: "+srv.URL+"/b\n\nPage B Bravo text") ||
		!strings.HasSuffix(user, "\n\nPlease synthesize these 2 sources into a comprehensive research summary.") {
		t.Fatalf("user content: %.300q … %q", user, user[len(user)-120:])
	}
	if *model.req.Messages[0].Content.Text != summarizeSystemPrompt {
		t.Fatal("system prompt")
	}
	if len(n.inbox) != 1 || len(n.pushes) != 1 {
		t.Fatalf("inbox %d pushes %d", len(n.inbox), len(n.pushes))
	}
	it := n.inbox[0]
	if it.Title != "Research: keyboards" || it.Category != "deep_research" || *it.UserID != 7 ||
		strings.Contains(it.Body, "<think>") || !strings.HasSuffix(it.Summary, "...") || len(it.Summary) > 203 ||
		it.Metadata["pages_scraped"] != 2 || it.Metadata["pages_attempted"] != 4 {
		t.Fatalf("inbox %+v", it)
	}
	p := n.pushes[0]
	if p.TargetType != "user" || p.TargetID != "7" || p.Title != "Research Complete" || p.Body != "Results ready: keyboards" ||
		p.Data["inbox_item_id"] != "inbox-1" || p.Data["type"] != "deep_research" || p.Category != "deep_research" {
		t.Fatalf("push %+v", p)
	}
}

func TestResearchRunnerFailures(t *testing.T) {
	srv := pagesServer()
	defer srv.Close()
	now := time.Now()
	cases := []struct {
		name    string
		search  *fakeSearch
		model   *fakeLLM
		req     researchRequest
		wantMsg string
	}{
		{"no results", &fakeSearch{}, &fakeLLM{}, researchRequest{EnqueuedAt: now.UnixMilli()}, `Research on "q" failed: No search results found`},
		{"nothing scraped", &fakeSearch{res: []SearchResult{{URL: srv.URL + "/private"}}}, &fakeLLM{}, researchRequest{EnqueuedAt: now.UnixMilli()}, `Research on "q" failed: Could not scrape any of the search results`},
		{"empty summary", &fakeSearch{res: []SearchResult{{URL: srv.URL + "/b"}}}, &fakeLLM{content: "<think>x</think>  "}, researchRequest{EnqueuedAt: now.UnixMilli()}, `Research on "q" produced an empty summary`},
		{"llm error", &fakeSearch{res: []SearchResult{{URL: srv.URL + "/b"}}}, &fakeLLM{err: errors.New("model offline")}, researchRequest{EnqueuedAt: now.UnixMilli()}, `Research on "q" failed: model offline`},
		{"stale job", &fakeSearch{}, &fakeLLM{}, researchRequest{EnqueuedAt: now.Add(-11 * time.Minute).UnixMilli()}, `Research on "q" failed: research timed out`},
		{"deadline mid-run", &fakeSearch{res: []SearchResult{{URL: srv.URL + "/b"}}}, &fakeLLM{block: true},
			researchRequest{EnqueuedAt: now.Add(-DeepResearchDeadline + 300*time.Millisecond).UnixMilli()}, `Research on "q" failed: research timed out`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n := &fakeNotify{}
			r := NewResearchRunner(c.search, loopbackOK(), c.model, n, nil)
			c.req.Query, c.req.HouseholdID, c.req.Depth = "q", "hh1", "quick"
			if _, err := r.Run(context.Background(), job(t, c.req)); err != nil {
				t.Fatal(err)
			}
			if len(n.inbox) != 0 || len(n.pushes) != 1 {
				t.Fatalf("inbox %d pushes %d", len(n.inbox), len(n.pushes))
			}
			p := n.pushes[0]
			if p.Body != c.wantMsg || p.Title != "Research Failed" || p.TargetType != "household" || p.TargetID != "hh1" ||
				p.Data["type"] != "deep_research_failed" {
				t.Fatalf("push %+v", p)
			}
		})
	}
}
