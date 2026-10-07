package errands

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/platform/db"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// --- fakes ---

// fakeLLM answers by prompt kind: plan, refine, replan, compose. A reply func can fail.
type fakeLLM struct {
	mu      sync.Mutex
	replies map[string][]string // kind → scripted contents (last repeats)
	errs    map[string]error
	last    map[string]string
	reqs    []llm.ChatRequest
}

func promptKind(p string) string {
	switch {
	case strings.HasPrefix(p, "You are Jarvis's errand planner revising"):
		return "refine"
	case strings.HasPrefix(p, "You are Jarvis's errand planner CONTINUING"):
		return "replan"
	case strings.HasPrefix(p, "You are Jarvis's errand planner."):
		return "plan"
	case strings.HasPrefix(p, "You are Jarvis giving the user the FINAL report"):
		return "compose"
	}
	return "?"
}

func (f *fakeLLM) say(kind string, contents ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.replies == nil {
		f.replies = map[string][]string{}
	}
	f.replies[kind] = append(f.replies[kind], contents...)
}

func (f *fakeLLM) fail(kind string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.errs == nil {
		f.errs = map[string]error{}
	}
	f.errs[kind] = err
}

func (f *fakeLLM) Chat(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, req)
	p := *req.Messages[0].Content.Text
	kind := promptKind(p)
	if err := f.errs[kind]; err != nil {
		return nil, err
	}
	list := f.replies[kind]
	if len(list) == 0 {
		if c, ok := f.last[kind]; ok {
			return &llm.ChatResponse{Content: c, FinishReason: "stop"}, nil
		}
		if kind == "compose" {
			return nil, errors.New("no compose scripted")
		}
		return &llm.ChatResponse{Content: ""}, nil
	}
	c := list[0]
	f.replies[kind] = list[1:]
	if f.last == nil {
		f.last = map[string]string{}
	}
	f.last[kind] = c
	if c == thoughtOut {
		return &llm.ChatResponse{Content: "", FinishReason: "length"}, nil
	}
	return &llm.ChatResponse{Content: c, FinishReason: "stop"}, nil
}

// thoughtOut scripts a reply where thinking used the whole max_tokens: no content, finish
// "length".
const thoughtOut = "\x00thought-out"

func (f *fakeLLM) requests(kind string) []llm.ChatRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []llm.ChatRequest
	for _, r := range f.reqs {
		if promptKind(*r.Messages[0].Content.Text) == kind {
			out = append(out, r)
		}
	}
	return out
}

type nodeCall struct {
	node, command string
	args          string
	user          *int64
	voice         string
}

type fakeNodes struct {
	mu       sync.Mutex
	calls    []nodeCall
	outputs  map[string]*pyjson.Object // command → output
	commands []*pyjson.Object
	report   bool
}

func (f *fakeNodes) RunTool(_ context.Context, node, cmd string, args *pyjson.Object, uid *int64, voice string, _ time.Duration) *pyjson.Object {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, nodeCall{node, cmd, pyjson.Dumps(args, true), uid, voice})
	if o, ok := f.outputs[cmd]; ok {
		return o
	}
	return servertools.Obj("success", true, "message", cmd+" ok")
}

func (f *fakeNodes) ReportCommands(context.Context, string, time.Duration) ([]*pyjson.Object, bool) {
	return f.commands, f.report
}

func (f *fakeNodes) ran() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		out = append(out, c.command)
	}
	return out
}

type fakeCards struct {
	mu      sync.Mutex
	n       int
	posted  []Card
	ids     []string
	updated map[string]Card
	gone    map[string]bool
}

func (f *fakeCards) Post(_ context.Context, c Card) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	id := "item-" + string(rune('0'+f.n))
	f.posted = append(f.posted, c)
	f.ids = append(f.ids, id)
	return id
}

func (f *fakeCards) Update(_ context.Context, id string, c Card) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.gone[id] {
		return false
	}
	if f.updated == nil {
		f.updated = map[string]Card{}
	}
	f.updated[id] = c
	return true
}

func (f *fakeCards) all() []Card {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Card(nil), f.posted...)
}

func (f *fakeCards) titles() []string {
	var out []string
	for _, c := range f.all() {
		out = append(out, c.Title)
	}
	return out
}

type fakeSettings map[string]bool

func (f fakeSettings) Int(context.Context, string, settings.Scope) int64 { return 0 }

func (f fakeSettings) Bool(_ context.Context, key string, _ settings.Scope) bool {
	if v, ok := f[key]; ok {
		return v
	}
	switch key {
	case SettingEnabled, settingMemoryEnabled, settingRecallEnabled:
		return true
	}
	return false
}

type fakePhone struct {
	mu       sync.Mutex
	enabled  bool
	placed   []ErrandCall
	fail     bool
	status   map[string]CallOutcome
	declined []string
}

func (f *fakePhone) Enabled(context.Context, string) bool { return f.enabled }

func (f *fakePhone) PlaceErrandCall(_ context.Context, c ErrandCall) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return "", errors.New("no number")
	}
	f.placed = append(f.placed, c)
	return "sess-" + c.Business, nil
}

func (f *fakePhone) CallStatus(_ context.Context, id string) (CallOutcome, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.status[id]
	return o, ok, nil
}

func (f *fakePhone) DeclineErrandCalls(_ context.Context, wf string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.declined = append(f.declined, wf)
	return true, nil
}

// fakeTool is a server tool with a canned result.
type fakeTool struct {
	name   string
	def    *pyjson.Object
	result any
	mu     sync.Mutex
	turns  []servertools.Turn
	args   []string
}

func (t *fakeTool) Name() string               { return t.name }
func (t *fakeTool) Definition() *pyjson.Object { return t.def }
func (t *fakeTool) Execute(_ context.Context, c servertools.Call, turn servertools.Turn) (any, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.turns = append(t.turns, turn)
	t.args = append(t.args, pyjson.Dumps(c.Args, true))
	return t.result, nil
}

func toolDef(name, desc string, props ...any) *pyjson.Object {
	return servertools.Obj("type", "function", "function", servertools.Obj("name", name, "description", desc,
		"parameters", servertools.Obj("type", "object", "properties", servertools.Obj(props...))))
}

// --- environment ---

type tenv struct {
	t      *testing.T
	s      *Service
	d      *db.DB
	q      *queue.Queue
	llm    *fakeLLM
	nodes  *fakeNodes
	cards  *fakeCards
	phone  *fakePhone
	tools  *servertools.Registry
	set    fakeSettings
	cancel context.CancelFunc
}

// ccMigrations reads the cc module's migrations from disk (the errands package has no embed of
// its own).
func ccMigrations(*testing.T) fs.FS { return os.DirFS(filepath.Join("..", "migrations")) }

const hh = "hh-1"

func newTenv(t *testing.T, start bool) *tenv {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "j.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := db.Migrate(ctx, d, "cc", ccMigrations(t)); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, d, queue.MigrationModule, queue.Migrations()); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	q := queue.New(d, log)
	q.PollInterval = 10 * time.Millisecond
	e := &tenv{t: t, d: d, q: q, llm: &fakeLLM{}, nodes: &fakeNodes{outputs: map[string]*pyjson.Object{}},
		cards: &fakeCards{}, phone: &fakePhone{status: map[string]CallOutcome{}}, tools: servertools.NewRegistry(),
		set: fakeSettings{}}
	e.s = &Service{DB: d, Log: log, LLM: e.llm, Tools: e.tools, Settings: e.set, Nodes: e.nodes, Cards: e.cards,
		Phone: e.phone, Timezone: func(context.Context, string) string { return "America/New_York" }}
	e.s.Register(q)
	if start {
		qctx, cancel := context.WithCancel(ctx)
		e.cancel = cancel
		t.Cleanup(cancel)
		q.Start(qctx)
	}
	return e
}

// waitFor polls cond until it holds (2 s).
func (e *tenv) waitFor(what string, cond func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	e.t.Fatalf("timed out waiting for %s", what)
}

// idle waits until no queued or running job is due.
func (e *tenv) idle() {
	e.t.Helper()
	e.waitFor("queue idle", func() bool {
		var n int
		_ = e.d.Read.QueryRow(`SELECT COUNT(*) FROM platform_jobs WHERE state IN ('queued','running') AND run_at <= ?`,
			time.Now().UnixMilli()).Scan(&n)
		return n == 0
	})
}

func (e *tenv) planState(id string) string {
	var s string
	_ = e.d.Read.QueryRow(`SELECT state FROM cc_errand_plans WHERE id = ?`, id).Scan(&s)
	return s
}

func (e *tenv) onlyPlan() *planRow {
	e.t.Helper()
	var id string
	if err := e.d.Read.QueryRow(`SELECT id FROM cc_errand_plans`).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	p, err := e.s.getPlan(context.Background(), id, hh)
	if err != nil || p == nil {
		e.t.Fatal(err)
	}
	return p
}

func (e *tenv) wf(id string) *workflowRow {
	e.t.Helper()
	w, err := e.s.loadWorkflow(context.Background(), id)
	if err != nil || w == nil {
		e.t.Fatalf("workflow %s: %v", id, err)
	}
	return w
}

func (e *tenv) wfState(id string) string {
	var s string
	_ = e.d.Read.QueryRow(`SELECT state FROM cc_workflows WHERE id = ?`, id).Scan(&s)
	return s
}

// planJSON is a planner reply.
func planJSON(summary string, steps ...string) string {
	return `{"summary": "` + summary + `", "steps": [` + strings.Join(steps, ", ") + `]}`
}

func step(cmd, label string, args string) string {
	if args == "" {
		args = "{}"
	}
	return `{"command": "` + cmd + `", "args": ` + args + `, "label": "` + label + `"}`
}

// draft plans goal through the queue and returns the drafted plan.
func (e *tenv) draft(goal string, uid *int64, menu []MenuEntry) *planRow {
	e.t.Helper()
	if err := e.s.DraftErrand(context.Background(), DraftRequest{HouseholdID: hh, NodeID: "node-1", UserID: uid,
		Goal: goal, Menu: menu}); err != nil {
		e.t.Fatal(err)
	}
	e.idle()
	return e.onlyPlan()
}

func (e *tenv) tap(cb string, data map[string]any) CallbackResult {
	e.t.Helper()
	return e.s.Callbacks()[cb](context.Background(), CallbackContext{HouseholdID: hh, UserID: 7, Data: data})
}

func ptr(v int64) *int64 { return &v }

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

var _ = sql.ErrNoRows
