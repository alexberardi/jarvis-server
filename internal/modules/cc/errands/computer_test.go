package errands

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/computer"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/policy"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// Computer tasks in errands (plan P3 piece 11): the real computer.Loop with a fake executor on
// the computer, the harness's scripted model and fake policy.

// fakeComputer is the agent's computer_observe / computer_act / computer_stop.
type fakeComputer struct {
	mu       sync.Mutex
	observes []computer.Observe
	url      string
	els      []computer.Element
	batches  []computer.Batch
	stops    []string
	// onAct, when set, runs before each batch is answered (after the batches so far are counted).
	onAct func(n int)
	// downloads are in Downloads once a batch ran.
	downloads []computer.FileEntry
	// pausedAt is the batch (1-based) the user takes over the computer in: it answers paused.
	pausedAt int
}

func (f *fakeComputer) Observe(_ context.Context, o computer.Observe) (computer.Observation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.observes = append(f.observes, o)
	return computer.Observation{URL: f.url, Title: "Shop", Elements: f.els}, nil
}

func (f *fakeComputer) Act(_ context.Context, b computer.Batch) (computer.ActResult, error) {
	f.mu.Lock()
	f.batches = append(f.batches, b)
	n, hook := len(f.batches), f.onAct
	f.mu.Unlock()
	if hook != nil {
		hook(n)
	}
	if n == f.pausedAt {
		return computer.ActResult{Paused: true, Results: []computer.StepResult{{I: 0, Code: "paused", Error: "paused"}}}, nil
	}
	var res computer.ActResult
	for i := range b.Actions {
		res.Results = append(res.Results, computer.StepResult{I: i, OK: true})
	}
	return res, nil
}

func (f *fakeComputer) Files(_ context.Context, a computer.Files) (computer.FileInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if a.Op == "list" && len(f.batches) > 0 {
		return computer.FileInfo{Entries: f.downloads}, nil
	}
	return computer.FileInfo{}, nil
}

func (f *fakeComputer) Stop(_ context.Context, runID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops = append(f.stops, runID)
}

func (f *fakeComputer) acted() []computer.Batch {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]computer.Batch(nil), f.batches...)
}

func (f *fakeComputer) stopped() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.stops...)
}

// fakeApprovals records the digests it minted tokens for.
type fakeApprovals struct {
	mu      sync.Mutex
	digests []string
}

func (f *fakeApprovals) Mint(_ context.Context, _, _, _, digest string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.digests = append(f.digests, digest)
	return "tok-" + digest[:8], nil
}

func (f *fakeApprovals) minted() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.digests...)
}

// withInts adds integer settings to the harness's booleans.
type withInts struct {
	fakeSettings
	ints map[string]int64
}

func (s withInts) Int(_ context.Context, key string, _ settings.Scope) int64 { return s.ints[key] }

type cenv struct {
	*tenv
	comp *fakeComputer
	appr *fakeApprovals
	runs *computer.Store
}

const shopSite = "shop.example"

// newComputerEnv is the harness with computer tasks on, one computer ("comp-1") with computer
// control on, and a checkout page on it.
func newComputerEnv(t *testing.T) *cenv {
	e := newTenv(t, true)
	c := &cenv{tenv: e, appr: &fakeApprovals{}, runs: &computer.Store{DB: e.d},
		comp: &fakeComputer{url: "https://" + shopSite + "/checkout", els: []computer.Element{
			{Ref: "e1", Role: "button", Name: "Place your order"},
			{Ref: "e2", Role: "link", Name: "Cart"},
			{Ref: "e3", Role: "button", Name: "Add to cart"},
			{Ref: "e4", Role: "link", Name: "Home"},
			{Ref: "e5", Role: "textbox", Name: "Search"},
		}}}
	e.s.Computer = &ComputerTasks{
		Loop:    &computer.Loop{LLM: e.llm, Exec: c.comp, Policy: e.policy, Audit: e.act, Store: c.runs, Approvals: c.appr},
		Targets: func(context.Context, string) ([]string, string) { return []string{"comp-1"}, "" },
	}
	// The household allows the shop; paying asks (the defaults).
	e.policy.set(policy.ToolComputerTask+"|"+policy.TargetSite+shopSite, policy.Verdict{Effect: policy.Allow, Reason: "computer task default (site:)"})
	e.policy.set(policy.ToolComputerTask+"|"+policy.TargetAction+computeruse.CatPay, policy.Verdict{Effect: policy.Ask, Reason: "computer task default (action:pay)"})
	return c
}

func computerStep(goal string) Step {
	args := pyjson.NewObject()
	args.Set("goal", goal)
	args.Set("target", "browser")
	args.Set("site", shopSite)
	return Step{Command: policy.ToolComputerTask, Args: args, Label: "Order the mug"}
}

// launchRun inserts a run of kind and queues its first drive, as launch does.
func (c *cenv) launchRun(id, kind string, steps ...Step) {
	c.t.Helper()
	now := dbTime(time.Now())
	if _, err := c.d.Write.Exec(`INSERT INTO cc_workflows (id, kind, household_id, user_id, node_id, goal, title, steps, cursor,
		results_json, revision, state, waiting_on, inbox_item_id, created_at, updated_at)
		VALUES (?, ?, ?, 7, 'node-1', 'order a blue mug', 'Order a mug', ?, 0, '[]', 1, 'waiting', NULL, NULL, ?, ?)`,
		id, kind, hh, stepsDumps(steps), now, now); err != nil {
		c.t.Fatal(err)
	}
	if err := c.s.enqueueResume(context.Background(), resumeJob{WorkflowID: id, Step: -1, Kind: signalStart}); err != nil {
		c.t.Fatal(err)
	}
}

const (
	checkoutReply = `{"checkout": {"product": "Blue mug", "price": "$12.99", "currency": "USD", "seller": "Shop Example", "ship_to": "12 Elm St", "card": "4242"}, ` +
		`"actions": [{"kind": "press", "ref": "e1", "intent": "Place the order"}]}`
	doneReply = `{"done": true, "summary": "Ordered the blue mug.", "verify": {"url_contains": "/checkout"}}`
)

func (c *cenv) cardTitled(prefix string) (Card, bool) {
	for _, cd := range c.cards.all() {
		if strings.HasPrefix(cd.Title, prefix) {
			return cd, true
		}
	}
	return Card{}, false
}

func elementLabels(md map[string]any) []string {
	var out []string
	els, _ := md["interactive_elements"].([]any)
	for _, el := range els {
		if m, ok := el.(map[string]any); ok {
			out = append(out, m["label"].(string))
		}
	}
	return out
}

func TestPlannerOffersComputerTask(t *testing.T) {
	c := newComputerEnv(t)
	ctx := context.Background()
	node := []*pyjson.Object{
		mustObj(t, `{"command_name": "get_weather", "description": "Weather"}`),
		mustObj(t, `{"command_name": "computer_act", "description": "Act on the computer."}`),
		mustObj(t, `{"command_name": "computer_observe", "description": "Look at the computer."}`),
		mustObj(t, `{"command_name": "browser_open", "description": "Open a page in the browser."}`),
		mustObj(t, `{"command_name": "browser_fill", "description": "Fill in a field on the page."}`),
		mustObj(t, `{"command_name": "desktop_open", "description": "Open an app or a web address."}`),
	}
	got := c.s.FullMenu(ctx, node, hh, ptr(7))
	var names []string
	var entry MenuEntry
	for _, m := range got {
		names = append(names, m.Command)
		if m.Command == policy.ToolComputerTask {
			entry = m
		}
	}
	if strings.Join(names, ",") != "get_weather,computer_task" {
		t.Fatalf("menu = %v", names)
	}
	if !strings.HasPrefix(entry.Description, "Do a multi-step task on the user's computer") {
		t.Fatalf("entry = %+v", entry)
	}
	for _, arg := range []string{"goal", "target", "site", "app", "success"} {
		if _, ok := entry.Args.Get(arg); !ok {
			t.Fatalf("args %s missing: %s", arg, pyjson.Dumps(entry.Args, true))
		}
	}

	// Without a computer with computer control on, it isn't offered.
	// Without a computer with computer control on, it isn't offered, and the browser's own
	// commands are (a website task through computer_task never mixes with them).
	c.s.Computer.Targets = func(context.Context, string) ([]string, string) { return nil, "Computer control is off." }
	names = nil
	for _, m := range c.s.FullMenu(ctx, node, hh, ptr(7)) {
		if m.Command == policy.ToolComputerTask || computeruse.IsInternalTool(m.Command) {
			t.Fatalf("offered %s without a computer", m.Command)
		}
		names = append(names, m.Command)
	}
	if strings.Join(names, ",") != "get_weather,browser_open,browser_fill,desktop_open" {
		t.Fatalf("menu without a computer = %v", names)
	}
	c.s.Computer = nil
	for _, m := range c.s.FullMenu(ctx, node, hh, ptr(7)) {
		if m.Command == policy.ToolComputerTask {
			t.Fatal("offered without computer tasks")
		}
	}
}

func TestComputerStepDone(t *testing.T) {
	c := newComputerEnv(t)
	c.llm.say("computer", `{"thought": "search", "actions": [{"kind": "set_value", "ref": "e5", "text": "blue mug", "intent": "Search for a blue mug"}]}`, doneReply)
	c.launchRun("wf_c", "errand", computerStep("find a blue mug"))
	c.idle()
	w := c.wf("wf_c")
	if w.State != "done" || len(w.Results) != 1 || !w.Results[0].Success || w.Results[0].Message != `Done, checked: the address contains "/checkout". Ordered the blue mug.` {
		t.Fatalf("run = %s %+v", w.State, w.Results)
	}
	if b := c.comp.acted(); len(b) != 1 || b[0].Target.Site != shopSite || b[0].Actions[0].Intent != "Search for a blue mug" {
		t.Fatalf("batches = %+v", b)
	}
	run, err := c.runs.ForStep(context.Background(), "wf_c", 0)
	if err != nil || run.State != computer.RunDone || run.NodeID != "comp-1" || run.Goal != "find a blue mug" {
		t.Fatalf("computer run = %+v %v", run, err)
	}
	if _, ok := c.cardTitled("✅ Errand done: "); !ok {
		t.Fatalf("cards = %v", c.cards.titles())
	}
}

// A done whose check never holds fails the step with the reason, after five more rounds (C4).
func TestComputerStepCheckFails(t *testing.T) {
	c := newComputerEnv(t)
	wrong := `{"done": true, "summary": "Ordered the blue mug.", "verify": {"url_contains": "/thanks"}}`
	for range 6 {
		c.llm.say("computer", wrong)
	}
	c.launchRun("wf_c", "errand", computerStep("find a blue mug"))
	c.idle()
	w := c.wf("wf_c")
	if w.State != "failed" || len(w.Results) != 1 || w.Results[0].Success ||
		!strings.HasPrefix(w.Results[0].Error, `I couldn't confirm the address contains "/thanks": the address is https://`+shopSite+"/checkout") {
		t.Fatalf("run = %s %+v", w.State, w.Results)
	}
}

func TestComputerStepNoComputerFails(t *testing.T) {
	c := newComputerEnv(t)
	c.s.Computer.Targets = func(context.Context, string) ([]string, string) { return nil, "Computer control is off." }
	c.launchRun("wf_c", "errand", computerStep("find a blue mug"))
	c.idle()
	w := c.wf("wf_c")
	if w.State != "failed" || len(w.Results) != 1 || w.Results[0].Error != "Computer control is off." {
		t.Fatalf("run = %s %+v", w.State, w.Results)
	}
}

// parkOnCheckout runs a computer step to its checkout confirmation and returns the card.
func (c *cenv) parkOnCheckout(kind string) Card {
	c.t.Helper()
	c.llm.say("computer", checkoutReply, doneReply)
	c.launchRun("wf_c", kind, computerStep("order a blue mug"))
	c.idle()
	if st := c.wfState("wf_c"); st != "waiting" {
		c.t.Fatalf("state = %s, cards %v", st, c.cards.titles())
	}
	card, ok := c.cardTitled(stepTitlePrefix)
	if !ok {
		c.t.Fatalf("no confirmation card: %v", c.cards.titles())
	}
	return card
}

func TestCheckoutCardThenApprove(t *testing.T) {
	c := newComputerEnv(t)
	card := c.parkOnCheckout("errand")
	if len(c.comp.acted()) != 0 {
		t.Fatal("the order ran before the OK")
	}
	if up, _ := c.cards.update("item-1"); up.Metadata["steps"].([]any)[0].(map[string]any)["result"] != "Waiting for you in "+shopSite {
		t.Fatalf("progress card while waiting = %#v", up.Metadata["steps"])
	}
	if !card.Push {
		t.Fatal("the confirmation isn't pushed")
	}
	for _, line := range []string{"Product: Blue mug", "Price: $12.99", "Seller: Shop Example", "Ship to: 12 Elm St", "Card: •••• 4242"} {
		if !strings.Contains(card.Body, line) {
			t.Fatalf("body lacks %q:\n%s", line, card.Body)
		}
	}
	co, _ := card.Metadata["checkout"].(map[string]any)
	if co["product"] != "Blue mug" || co["price"] != "$12.99" || co["seller"] != "Shop Example" || co["ship_to"] != "12 Elm St" || co["card"] != "4242" {
		t.Fatalf("metadata.checkout = %#v", card.Metadata["checkout"])
	}
	if got := strings.Join(elementLabels(card.Metadata), ","); got != "Approve,Cancel" {
		t.Fatalf("buttons = %s", got)
	}
	run, err := c.runs.ForStep(context.Background(), "wf_c", 0)
	if err != nil || run.Pending == nil {
		t.Fatalf("run = %+v %v", run, err)
	}
	var pending []string
	for _, h := range run.Pending.Actions {
		if h.Digest != "" {
			pending = append(pending, h.Digest)
		}
	}

	r := c.tap(CallbackApproveStep, map[string]any{"workflow_id": "wf_c", "step": 0, "revision": card.Metadata["revision"]})
	if !r.Success {
		t.Fatalf("approve = %+v", r)
	}
	c.idle()
	if got := c.appr.minted(); !slices.Equal(got, pending) || len(got) != 1 {
		t.Fatalf("minted %v, pending %v", got, pending)
	}
	b := c.comp.acted()
	if len(b) != 1 || b[0].Actions[0].Intent != "Place the order" || b[0].Actions[0].Approval == "" {
		t.Fatalf("batches = %+v", b)
	}
	if w := c.wf("wf_c"); w.State != "done" || w.Results[0].Message != `Done, checked: the address contains "/checkout". Ordered the blue mug.` {
		t.Fatalf("run = %s %+v", w.State, w.Results)
	}
	if ev, ok := orderEvent(c); !ok || ev.Reason != "approved by the user" {
		t.Fatalf("audit = %+v", ev)
	}
	// The progress card lists what ran, once each (not the held action as well).
	up, _ := c.cards.update("item-1")
	st := up.Metadata["steps"].([]any)[0].(map[string]any)
	if prg, _ := st["progress"].([]string); !slices.Equal(prg, []string{"Place the order"}) || up.Metadata["state"] != "done" {
		t.Fatalf("progress card = %#v", up.Metadata)
	}
}

func TestCheckoutCancelNothingRuns(t *testing.T) {
	c := newComputerEnv(t)
	card := c.parkOnCheckout("errand")
	els, _ := card.Metadata["interactive_elements"].([]any)
	cancel := els[1].(map[string]any)
	r := c.tap(cancel["callback"].(string), cancel["data"].(map[string]any))
	if !r.Success {
		t.Fatalf("cancel = %+v", r)
	}
	c.idle()
	if st := c.wfState("wf_c"); st != "cancelled" {
		t.Fatalf("state = %s", st)
	}
	if up, _ := c.cards.update("item-1"); up.Metadata["state"] != "stopped" || len(elementLabels(up.Metadata)) != 0 {
		t.Fatalf("progress card = %#v", up.Metadata)
	}
	run, _ := c.runs.ForStep(context.Background(), "wf_c", 0)
	if run.State != computer.RunStopped || !run.StopRequested {
		t.Fatalf("computer run = %+v", run)
	}
	if len(c.comp.acted()) != 0 || len(c.appr.minted()) != 0 {
		t.Fatalf("acted %v, minted %v", c.comp.acted(), c.appr.minted())
	}
	if got := c.comp.stopped(); len(got) == 0 || got[0] != run.ID {
		t.Fatalf("computer_stop = %v", got)
	}
	// A late Approve on the cancelled card does nothing.
	c.tap(CallbackApproveStep, map[string]any{"workflow_id": "wf_c", "step": 0, "revision": card.Metadata["revision"]})
	c.idle()
	if len(c.comp.acted()) != 0 {
		t.Fatal("the order ran after Cancel")
	}
}

func TestSpendLimitAutoApproves(t *testing.T) {
	c := newComputerEnv(t)
	c.s.Settings = withInts{c.set, map[string]int64{SettingAutoApproveCents: 2000}}
	c.llm.say("computer", checkoutReply, doneReply)
	c.launchRun("wf_c", "errand", computerStep("order a blue mug"))
	c.idle()
	if _, ok := c.cardTitled(stepTitlePrefix); ok {
		t.Fatalf("asked within the spend limit: %v", c.cards.titles())
	}
	if w := c.wf("wf_c"); w.State != "done" {
		t.Fatalf("state = %s %+v", w.State, w.Results)
	}
	if b := c.comp.acted(); len(b) != 1 || b[0].Actions[0].Approval == "" {
		t.Fatalf("batches = %+v", b)
	}
	prog, ok := c.cardTitled(progressTitlePrefix)
	if !ok {
		t.Fatalf("no progress card: %v", c.cards.titles())
	}
	up, _ := c.cards.update(c.cards.ids[slices.IndexFunc(c.cards.all(), func(cd Card) bool { return cd.Title == prog.Title })])
	if !strings.Contains(up.Body, "within your $20.00 spend limit") {
		t.Fatalf("progress card doesn't say it approved:\n%s", up.Body)
	}

	// The audit log says the spend limit approved it, not the user.
	if ev, ok := orderEvent(c); !ok || ev.Reason != "within your $20.00 spend limit" {
		t.Fatalf("audit = %+v", ev)
	}

	// Over the limit, it asks.
	c2 := newComputerEnv(t)
	c2.s.Settings = withInts{c2.set, map[string]int64{SettingAutoApproveCents: 1000}}
	c2.parkOnCheckout("errand")
	if len(c2.appr.minted()) != 0 || len(c2.comp.acted()) != 0 {
		t.Fatal("over the limit, something was approved")
	}

	// Within the limit, but the batch also sends a message: it asks, and nothing runs.
	c3 := newComputerEnv(t)
	c3.s.Settings = withInts{c3.set, map[string]int64{SettingAutoApproveCents: 2000}}
	c3.comp.els = append(c3.comp.els, computer.Element{Ref: "e6", Role: "button", Name: "Send message"})
	c3.policy.set(policy.ToolComputerTask+"|"+policy.TargetAction+computeruse.CatSend, policy.Verdict{Effect: policy.Ask, Reason: "computer task default (action:send)"})
	c3.llm.say("computer", `{"checkout": {"product": "Blue mug", "price": "$12.99", "currency": "USD"}, `+
		`"actions": [{"kind": "press", "ref": "e1", "intent": "Place the order"}, {"kind": "press", "ref": "e6", "intent": "Message the seller"}]}`, doneReply)
	c3.launchRun("wf_c", "errand", computerStep("order a blue mug"))
	c3.idle()
	card, ok := c3.cardTitled(stepTitlePrefix)
	if !ok || strings.Join(elementLabels(card.Metadata), ",") != "Approve,Cancel" {
		t.Fatalf("no confirmation for a batch that isn't only a payment: %v", c3.cards.titles())
	}
	if st := c3.wfState("wf_c"); st != "waiting" {
		t.Fatalf("state = %s", st)
	}
	if len(c3.appr.minted()) != 0 || len(c3.comp.acted()) != 0 {
		t.Fatalf("minted %v, acted %+v", c3.appr.minted(), c3.comp.acted())
	}
}

// orderEvent is the audit event of the "Place the order" action that ran.
func orderEvent(c *cenv) (policy.Event, bool) {
	for _, ev := range c.act.all() {
		if ev.Source == computer.SourceComputer && ev.Detail["intent"] == "Place the order" && ev.Outcome == policy.OutcomeRan {
			return ev, true
		}
	}
	return policy.Event{}, false
}

func TestPriceCents(t *testing.T) {
	for _, tc := range []struct {
		in    string
		cents int64
		ok    bool
	}{
		{"$12.99", 1299, true},
		{"12,99 €", 1299, true},
		{"1,299.00", 129900, true},
		{"1.299,00 €", 129900, true},
		{"USD 12.99", 1299, true},
		{"12.99 EUR", 1299, true},
		{"", 0, false},
		{"Free", 0, false},
		{"$1.2k", 0, false},
		{"$12.99 + $5.00 shipping", 0, false},
		{"$12.99/mo", 0, false},
	} {
		got, ok := priceCents(tc.in)
		if ok != tc.ok || (ok && got != tc.cents) {
			t.Errorf("priceCents(%q) = %d, %v; want %d, %v", tc.in, got, ok, tc.cents, tc.ok)
		}
	}
	// "$1.2k" is not approved under a $20 limit.
	c := newComputerEnv(t)
	c.s.Settings = withInts{c.set, map[string]int64{SettingAutoApproveCents: 2000}}
	if _, ok := c.s.withinSpendLimit(context.Background(), hh, computer.Outcome{
		Checkout: &computer.Checkout{Product: "Blue mug", Price: "$1.2k"},
		Pending:  []computer.PendingAction{{Category: computeruse.CatPay, Digest: "d"}},
	}); ok {
		t.Fatal("$1.2k approved under a $20 limit")
	}
}

func TestStopRunningComputerStep(t *testing.T) {
	c := newComputerEnv(t)
	c.llm.say("computer", `{"thought": "a", "actions": [{"kind": "press", "ref": "e2", "intent": "Open the cart"}]}`,
		`{"thought": "b", "actions": [{"kind": "press", "ref": "e4", "intent": "Go home"}]}`)
	tapped := make(chan CallbackResult, 1)
	c.comp.onAct = func(n int) {
		if n == 1 {
			tapped <- c.tap(CallbackStop, map[string]any{"workflow_id": "wf_c"})
		}
	}
	c.launchRun("wf_c", "errand", computerStep("browse"))
	c.idle()
	r := <-tapped
	if ib, _ := r.ContextData["inbox"].(map[string]any); !r.Success || ib["title"] != "🛑 Stopping the errand" {
		t.Fatalf("stop = %+v", r)
	}
	if n := len(c.comp.acted()); n != 1 {
		t.Fatalf("acted %d batches after Stop", n)
	}
	run, _ := c.runs.ForStep(context.Background(), "wf_c", 0)
	if run.State != computer.RunStopped || !run.StopRequested {
		t.Fatalf("computer run = %+v", run)
	}
	if got := c.comp.stopped(); len(got) == 0 || got[0] != run.ID {
		t.Fatalf("computer_stop = %v", got)
	}
	if st := c.wfState("wf_c"); st != "cancelled" {
		t.Fatalf("state = %s", st)
	}
	if _, ok := c.cardTitled("🛑 Errand stopped: "); !ok {
		t.Fatalf("cards = %v", c.cards.titles())
	}
}

func TestProgressCardUpdates(t *testing.T) {
	c := newComputerEnv(t)
	var intents []string
	var replies []string
	for i := 1; i <= 6; i++ {
		intent := "Look at item " + string(rune('0'+i))
		intents = append(intents, intent)
		replies = append(replies, `{"thought": "x", "actions": [{"kind": "press", "ref": "e3", "intent": "`+intent+`"}]}`)
	}
	c.llm.say("computer", append(replies, doneReply)...)
	var seen []Card
	c.comp.onAct = func(n int) {
		if n == 2 {
			if up, ok := c.cards.update("item-1"); ok {
				seen = append(seen, up)
			}
		}
	}
	c.launchRun("wf_c", "errand", computerStep("browse"))
	c.idle()
	prog := c.cards.all()[0]
	if !strings.HasPrefix(prog.Title, progressTitlePrefix) || prog.Metadata["workflow_id"] != "wf_c" || prog.Push {
		t.Fatalf("first card = %+v", prog)
	}
	if len(seen) != 1 {
		t.Fatal("the progress card wasn't updated while the step ran")
	}
	steps := seen[0].Metadata["steps"].([]any)
	st := steps[0].(map[string]any)
	if st["result"] != "Working in "+shopSite+": "+intents[0] || seen[0].Metadata["state"] != "running" {
		t.Fatalf("step line = %#v", st)
	}
	if got := elementLabels(seen[0].Metadata); len(got) != 1 || got[0] != "Stop" {
		t.Fatalf("buttons = %v", got)
	}
	// At the end: the last five intents, and the step done.
	up, _ := c.cards.update("item-1")
	st = up.Metadata["steps"].([]any)[0].(map[string]any)
	prg, _ := st["progress"].([]string)
	if !slices.Equal(prg, intents[1:]) || st["status"] != "ok" || up.Metadata["state"] != "done" {
		t.Fatalf("final step = %#v state %v", st, up.Metadata["state"])
	}
}

func TestLiveStartedStillDecides(t *testing.T) {
	c := newComputerEnv(t)
	if c.s.autonomous(context.Background(), hh) {
		t.Fatal("the household runs errands on its own; this is about a run the user started")
	}
	c.parkOnCheckout("errand")
	var targets []string
	for _, a := range c.policy.calls {
		if a.Tool == policy.ToolComputerTask {
			targets = append(targets, a.Target)
		}
	}
	if !slices.Contains(targets, policy.TargetAction+computeruse.CatPay) {
		t.Fatalf("policy saw %v", targets)
	}
}

func TestComputerAskThenContinue(t *testing.T) {
	c := newComputerEnv(t)
	c.llm.say("computer", `{"ask": "Which colour, blue or navy?", "reason": "unclear"}`,
		`{"thought": "x", "actions": [{"kind": "press", "ref": "e3", "intent": "Add the navy mug"}]}`, doneReply)
	c.launchRun("wf_c", "errand", computerStep("order a mug"))
	c.idle()
	card, ok := c.cardTitled(questionTitlePrefix)
	if !ok || !strings.Contains(card.Body, "Which colour, blue or navy?") || !card.Push {
		t.Fatalf("cards = %v", c.cards.titles())
	}
	if got := strings.Join(elementLabels(card.Metadata), ","); got != "Continue,Stop" {
		t.Fatalf("buttons = %s", got)
	}
	els := card.Metadata["interactive_elements"].([]any)
	data := els[0].(map[string]any)["data"].(map[string]any)
	data["instruction"] = "navy"
	if r := c.tap(CallbackAnswerStep, data); !r.Success {
		t.Fatalf("continue = %+v", r)
	}
	c.idle()
	if w := c.wf("wf_c"); w.State != "done" {
		t.Fatalf("state = %s %+v", w.State, w.Results)
	}
	reqs := c.llm.requests("computer")
	last := reqs[len(reqs)-2] // the round after the answer
	if !strings.Contains(last.Messages[1].Content.Parts[0].Text, "navy") {
		t.Fatal("the answer didn't reach the model")
	}
}

// A computer errand (only computer tasks and the computer's file tools) starts at once: no plan
// card, its one question is the checkout confirmation (user, 2026-10-20).
func TestComputerPlanStartsWithoutPlanCard(t *testing.T) {
	c := newComputerEnv(t)
	c.llm.say("plan", planJSON("Order a blue mug", step(policy.ToolComputerTask, "Order the mug",
		`{"goal": "order a blue mug", "target": "browser", "site": "`+shopSite+`"}`)))
	c.llm.say("computer", checkoutReply)
	p := c.draft("order me a blue mug", ptr(7), []MenuEntry{computerEntry()})
	if p.State != "launched" || p.WorkflowID == "" {
		t.Fatalf("plan = %+v", p)
	}
	for _, cd := range c.cards.all() {
		if cd.Category == planCategory && cd.Metadata["plan_id"] != nil {
			t.Fatalf("plan card posted: %+v", cd)
		}
	}
	w := c.wf(p.WorkflowID)
	if w.Kind != "errand" || w.State != "waiting" || w.WaitingOn != "approval" {
		t.Fatalf("run = %s %s %s", w.Kind, w.State, w.WaitingOn)
	}
	if _, ok := c.cardTitled(stepTitlePrefix); !ok {
		var titles []string
		for _, cd := range c.cards.all() {
			titles = append(titles, cd.Title)
		}
		t.Fatalf("no checkout confirmation: %v", titles)
	}
}

// A plan that mixes a computer task with other commands still asks with its plan card.
func TestMixedComputerPlanPostsPlanCard(t *testing.T) {
	c := newComputerEnv(t)
	weather := MenuEntry{Command: "get_weather", Description: "Weather"}
	c.llm.say("plan", planJSON("Weather and a mug", step("get_weather", "Weather", ""),
		step(policy.ToolComputerTask, "Order the mug", `{"goal": "order a blue mug", "target": "browser"}`)))
	p := c.draft("check the weather and order a mug", ptr(7), []MenuEntry{weather, computerEntry()})
	if p.State != "draft" {
		t.Fatalf("plan = %+v", p)
	}
	if cards := c.cards.all(); len(cards) != 1 || cards[0].Category != planCategory {
		t.Fatalf("cards = %+v", cards)
	}
}

func TestIsComputerPlan(t *testing.T) {
	ct := computerStep("x")
	for _, c := range []struct {
		steps []Step
		want  bool
	}{
		{[]Step{ct}, true},
		{[]Step{ct, {Command: cmdReadFile}, {Command: cmdReplan}}, true},
		{[]Step{ct, {Command: cmdWriteSheet}}, true},
		{[]Step{{Command: cmdReadFile}}, false},
		{[]Step{ct, {Command: "get_weather"}}, false},
		{[]Step{ct, {Command: cmdWriteSheet, IsRisky: true}}, false},
		{nil, false},
	} {
		if got := isComputerPlan(c.steps); got != c.want {
			t.Errorf("isComputerPlan(%+v) = %v", c.steps, got)
		}
	}
}

// A computer errand's replan that adds only the computer's file tools goes on without a card:
// the budget reads its statements and writes the spreadsheet in one run.
func TestComputerReplanContinuesWithoutCard(t *testing.T) {
	c := newComputerEnv(t)
	read := &fakeTool{name: cmdReadFile, def: toolDef(cmdReadFile, "Read a file", "path", mustObj(t, `{"type": "string"}`)),
		result: servertools.Obj("text", "2026-09-03,Dining,12.00")}
	c.tools.Register(read)
	c.llm.say("plan", planJSON("Budget", step(policy.ToolComputerTask, "Get the statement",
		`{"goal": "download the statement", "target": "browser", "site": "`+shopSite+`"}`),
		`{"command": "request_replan", "args": {"reason": "read what was downloaded"}, "label": "Decide"}`))
	c.llm.say("computer", doneReply)
	c.llm.say("replan", planJSON("Read it", step(cmdReadFile, "Read the statement", `{"path": "~/Downloads/statement.csv"}`)))
	c.llm.say("compose", "Dining was $12.")
	p := c.draft("do my budget", ptr(7), []MenuEntry{computerEntry()})
	c.idle()
	w := c.wf(p.WorkflowID)
	if w.State != "done" || len(w.Steps) != 3 {
		t.Fatalf("run = %s steps %+v", w.State, w.Steps)
	}
	if len(read.args) != 1 {
		t.Fatalf("read_computer_file ran %d times", len(read.args))
	}
	for _, cd := range c.cards.all() {
		if strings.HasPrefix(cd.Title, "🔀") {
			t.Fatalf("delta card: %+v", cd)
		}
	}
}

// A computer errand whose plan still gets a plan card (a desktop task, or other commands mixed
// in) isn't acked as started: the ack is said before the plan exists.
func TestRunErrandComputerAckWithPlanCard(t *testing.T) {
	c := newComputerEnv(t)
	tool := &RunErrandTool{Svc: func() *Service { return c.s }, NeedsComputer: func(string) bool { return true }}
	c.llm.say("plan", planJSON("Order and note it",
		step(policy.ToolComputerTask, "Order it", `{"goal": "order a blue mug", "target": "browser", "site": "`+shopSite+`"}`),
		step(policy.ToolComputerTask, "Note it", `{"goal": "write the order down in Notes", "target": "desktop", "app": "Notes"}`)))
	got := res(tool.Execute(context.Background(), servertools.Call{Args: servertools.Obj("goal", "order me a blue mug and note it")}, turnFor(7)))
	c.idle()
	if strings.Contains(got, "I'm starting") || !strings.Contains(got, "the plan to review first") {
		t.Fatalf("ack: %s", got)
	}
	planCard := false
	for _, cd := range c.cards.all() {
		planCard = planCard || strings.Contains(strings.Join(elementLabels(cd.Metadata), ","), "Run")
	}
	if !planCard {
		t.Fatalf("no plan card: %+v", c.cards.all())
	}
}

// run_errand's ack for a computer errand says it starts at once, not "Tap Run".
func TestRunErrandComputerAck(t *testing.T) {
	c := newComputerEnv(t)
	tool := &RunErrandTool{Svc: func() *Service { return c.s }, NeedsComputer: func(g string) bool { return strings.HasPrefix(g, "order") }}
	ack := func(goal string) string {
		return res(tool.Execute(context.Background(), servertools.Call{Args: servertools.Obj("goal", goal)}, turnFor(7)))
	}
	if got := ack("order me a blue mug"); got != `{"status": "accepted", "message": "`+computerAck+`"}` {
		t.Fatal(got)
	}
	if got := ack("check the weather"); !strings.Contains(got, "Tap Run") {
		t.Fatal(got)
	}
	c.s.Computer = nil // no computer task on the menu: the plan card's ack
	if got := ack("order me a blue mug"); !strings.Contains(got, "Tap Run") {
		t.Fatal(got)
	}
}

// Adjacent computer tasks on one site are one task: the planner's "find it" then "order it"
// would lose what the first found.
func TestMergeComputerSteps(t *testing.T) {
	ct := func(goal, site, target, success string) Step {
		a := servertools.Obj("goal", goal, "target", target, "site", site)
		if success != "" {
			a.Set("success", success)
		}
		return Step{Command: cmdComputer, Args: a, Label: goal}
	}
	steps := []Step{
		ct("find the best watch", "http://127.0.0.1:9/store/", "browser", "a product page"),
		ct("order it", "127.0.0.1:9", "browser", "an order confirmation page"),
		ct("download the statement", "bank.example", "browser", ""),
		{Command: cmdReadFile, Label: "read"},
		ct("download the card statement", "card.example", "browser", ""),
		ct("download the other one", "card.example", "desktop", ""),
		// One host, two sites the user named: the bank's and the card's statements.
		ct("download the bank statement", "http://127.0.0.1:9/bank/", "browser", ""),
		ct("download the card statement", "http://127.0.0.1:9/card/", "browser", ""),
	}
	got := mergeComputerSteps(steps)
	var lines []string
	for _, st := range got {
		lines = append(lines, st.Command+"|"+truthyStr(st.Args, "goal")+"|"+truthyStr(st.Args, "success")+"|"+st.Label)
	}
	want := []string{
		"computer_task|find the best watch, then order it|an order confirmation page|order it",
		"computer_task|download the statement||download the statement",
		"read_computer_file|||read",
		"computer_task|download the card statement||download the card statement",
		"computer_task|download the other one||download the other one",
		"computer_task|download the bank statement||download the bank statement",
		"computer_task|download the card statement||download the card statement",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("merged:\n%s", strings.Join(lines, "\n"))
	}
	if truthyStr(steps[0].Args, "goal") != "find the best watch" {
		t.Fatal("the planner's steps were changed in place")
	}
}

// The planner's reply is merged before the plan is made.
func TestPlanErrandMergesComputerSteps(t *testing.T) {
	c := newComputerEnv(t)
	c.llm.say("plan", planJSON("Order a watch",
		step(policy.ToolComputerTask, "Find it", `{"goal": "find the best watch", "target": "browser", "site": "`+shopSite+`"}`),
		step(policy.ToolComputerTask, "Order it", `{"goal": "order it", "target": "browser", "site": "`+shopSite+`"}`)))
	p, err := c.s.PlanErrand(context.Background(), "order me the best watch", []MenuEntry{computerEntry()})
	if err != nil || len(p.Steps) != 1 || truthyStr(p.Steps[0].Args, "goal") != "find the best watch, then order it" {
		t.Fatalf("plan = %+v %v", p, err)
	}
}

// A computer task whose goal names two web addresses is one task per address: the house model
// wrote one task for the bank's and the card's statements, and only the card's was downloaded.
func TestPlanErrandSplitsComputerTaskPerAddress(t *testing.T) {
	c := newComputerEnv(t)
	bank, card := "http://127.0.0.1:42683/bank/", "http://127.0.0.1:42683/card/"
	c.llm.say("plan", planJSON("Download bank and card statements, then build the spreadsheet",
		step(policy.ToolComputerTask, "Download bank and card statements", `{"goal": "download the statements from my bank (`+bank+`) and my card (`+card+`)", "target": "browser", "site": "`+bank+`", "success": "both statements downloaded"}`),
		step(cmdReplan, "Wait for files", `{"reason": "read the downloaded statements"}`)))
	menu := []MenuEntry{computerEntry(), {Command: cmdReplan, Description: "checkpoint"}}
	p, err := c.s.PlanErrand(context.Background(), "do my budget from "+bank+" and "+card, menu)
	if err != nil || len(p.Steps) != 3 {
		t.Fatalf("plan = %+v %v", p, err)
	}
	for i, site := range []string{bank, card} {
		st := p.Steps[i]
		goal := truthyStr(st.Args, "goal")
		if st.Command != cmdComputer || truthyStr(st.Args, "site") != site || truthyStr(st.Args, "target") != "browser" ||
			!strings.Contains(goal, "only the part at "+site) || truthyStr(st.Args, "success") != "" {
			t.Fatalf("step %d = %+v", i, st)
		}
		if got := startURL(truthyStr(st.Args, "site"), goal); got != site {
			t.Fatalf("step %d starts at %q", i, got)
		}
	}
	if p.Steps[2].Command != cmdReplan {
		t.Fatalf("steps %+v", p.Steps)
	}

	// One address, said twice or with a query, stays one task.
	c.llm.say("plan", planJSON("Order a watch",
		step(policy.ToolComputerTask, "Order it", `{"goal": "order the best watch at https://shop.example/watches (https://shop.example/watches/?q=watch)", "target": "browser", "site": "`+shopSite+`"}`)))
	if p, err := c.s.PlanErrand(context.Background(), "order me the best watch", []MenuEntry{computerEntry()}); err != nil || len(p.Steps) != 1 {
		t.Fatalf("plan = %+v %v", p, err)
	}
}

// A computer task starts where the user said: the step's site when it is a web address, else
// the first address on that site in the step's goal or the errand's.
func TestStartURL(t *testing.T) {
	for _, c := range []struct{ site, goal, errand, want string }{
		{"http://127.0.0.1:9/store/", "", "", "http://127.0.0.1:9/store/"},
		{"127.0.0.1:9", "order it from http://127.0.0.1:9/store/.", "", "http://127.0.0.1:9/store/"},
		{"127.0.0.1:9", "order it", "order me a watch at http://127.0.0.1:9/store/injection, thanks", "http://127.0.0.1:9/store/injection"},
		{"shop.example", "order it from http://other.example/x", "", ""},
		{"shop.example", "order it", "", ""},
		{"", "fill in https://forms.example/a?b=1", "", "https://forms.example/a?b=1"},
		{"ftp://x.example/", "", "", ""},
	} {
		if got := startURL(c.site, c.goal, c.errand); got != c.want {
			t.Errorf("startURL(%q, %q, %q) = %q, want %q", c.site, c.goal, c.errand, got, c.want)
		}
	}
}

// The handler opens a browser task at the web address the user gave: the step's site is only the
// host, the address is in the goal.
func TestComputerTaskStartsAtTheGoalsAddress(t *testing.T) {
	c := newComputerEnv(t)
	c.llm.say("plan", planJSON("Order a mug", step(policy.ToolComputerTask, "Order it",
		`{"goal": "order a blue mug from http://127.0.0.1:9/store/", "target": "browser", "site": "127.0.0.1:9"}`)))
	c.llm.say("computer", doneReply)
	c.draft("order me a blue mug from http://127.0.0.1:9/store/", ptr(7), []MenuEntry{computerEntry()})
	c.idle()
	c.comp.mu.Lock()
	defer c.comp.mu.Unlock()
	if len(c.comp.observes) == 0 || c.comp.observes[0].StartURL != "http://127.0.0.1:9/store/" ||
		c.comp.observes[0].Target.Site != "127.0.0.1:9" {
		t.Fatalf("observes %+v", c.comp.observes)
	}
}

// The replan after the downloads that reads them and writes the spreadsheet stops at a
// checkpoint in the spreadsheet's place, for what the reads find.
func TestReplanSpreadsheetAfterReadIsACheckpoint(t *testing.T) {
	c := newComputerEnv(t)
	c.llm.say("replan", planJSON("Read them",
		step(cmdReadFile, "Read the bank's", `{"path": "~/Downloads/bank-statement-2026-09.csv"}`),
		step(cmdReadFile, "Read the card's", `{"path": "~/Downloads/card-statement-2026-09.csv"}`),
		step(cmdWriteSheet, "Write the budget", `{"name": "Budget"}`)))
	menu := []MenuEntry{computerEntry(), {Command: cmdReadFile, Description: "read"}, {Command: cmdWriteSheet, Description: "sheet"}}
	p, err := c.s.ReplanFromProgress(context.Background(), "do my budget", nil, nil, "read the downloads", menu)
	if err != nil || len(p.Steps) != 3 || p.Steps[0].Command != cmdReadFile || p.Steps[1].Command != cmdReadFile ||
		p.Steps[2].Command != cmdReplan {
		t.Fatalf("plan = %+v %v", p, err)
	}
}

// A spreadsheet planned after reading files waits for what was read: it becomes a checkpoint.
func TestSpreadsheetAfterReadIsACheckpoint(t *testing.T) {
	read := Step{Command: cmdReadFile, Args: servertools.Obj("path", "~/Downloads/a.csv"), Label: "Read"}
	sheet := Step{Command: cmdWriteSheet, Args: servertools.Obj("name", "Budget"), Label: "Write it"}
	got := checkpointSpreadsheet([]Step{read, read, sheet, {Command: "get_weather"}})
	if len(got) != 3 || got[2].Command != cmdReplan || got[2].Label != "Write it" ||
		!strings.Contains(truthyStr(got[2].Args, "reason"), "write_spreadsheet") {
		t.Fatalf("steps %+v", got)
	}
	// A spreadsheet with nothing read before it in the plan (the replan after the reads) stays.
	if got := checkpointSpreadsheet([]Step{sheet}); len(got) != 1 || got[0].Command != cmdWriteSheet {
		t.Fatalf("steps %+v", got)
	}
}

// The checkpoint right after a computer task that downloaded files reads each of them, then
// waits for what they say, without asking the planner for the files' names.
func TestCheckpointReadsTheDownloads(t *testing.T) {
	c := newComputerEnv(t)
	read, _ := fileTools(t, c.tenv)
	c.comp.downloads = []computer.FileEntry{{Name: "statement-2026-09.csv", Bytes: 900}, {Name: "card-statement-2026-09.csv", Bytes: 800}}
	c.llm.say("plan", planJSON("Budget", step(policy.ToolComputerTask, "Get the statements",
		`{"goal": "download the statements", "target": "browser", "site": "`+shopSite+`"}`),
		`{"command": "request_replan", "args": {"reason": "read what was downloaded"}, "label": "Wait for files"}`))
	c.llm.say("computer", `{"actions": [{"kind": "press", "ref": "e2", "intent": "Download"}]}`, doneReply)
	c.llm.say("replan", planJSON("Nothing more"))
	c.llm.say("compose", "Read both.")
	p := c.draft("do my budget", ptr(7), c.s.FullMenu(context.Background(), nil, hh, ptr(7)))
	c.idle()
	w := c.wf(p.WorkflowID)
	if w.State != "done" || len(read.args) != 2 {
		t.Fatalf("run = %s, reads %v", w.State, read.args)
	}
	if !strings.Contains(read.args[0], "~/Downloads/card-statement-2026-09.csv") || !strings.Contains(read.args[1], "~/Downloads/statement-2026-09.csv") {
		t.Fatalf("reads %v", read.args)
	}
	if n := len(c.llm.requests("replan")); n != 1 {
		t.Fatalf("replans asked of the planner: %d (only the one after the reads)", n)
	}
	if last := w.Steps[len(w.Steps)-1]; last.Command != cmdReplan {
		t.Fatalf("steps %+v", w.Steps)
	}
}
