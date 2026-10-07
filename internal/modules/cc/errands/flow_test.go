package errands

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
)

func weatherMenu(t *testing.T) []MenuEntry {
	return []MenuEntry{
		{Command: "get_weather", Description: "Weather", Args: mustObj(t, `{"resolved_datetimes": "days"}`)},
		{Command: "get_news", Description: "News", Args: pyjson.NewObject()},
		{Command: "make_phone_call", Description: "Call", Args: mustObj(t, `{"business": "who", "goal": "why"}`), IsRisky: true},
		{Command: "remember", Description: "Remember", Args: mustObj(t, `{"content": "what"}`)},
	}
}

func TestPlanCardContract(t *testing.T) {
	steps := []Step{{Command: "get_weather", Args: mustObj(t, `{"resolved_datetimes": ["today"]}`), Label: "Check weather"}}
	got := mustJSON(PlanCardMetadata("pl_1", 2, steps, hh))
	want := `{"editable_fields":[{"data_key":"instruction","initial":"","input_type":"text","label":"Tell me what to change","required":true}],` +
		`"household_id":"hh-1","interactive_elements":[` +
		`{"callback":"approve_errand_plan","command":"errand","data":{"plan_id":"pl_1","revision":2},"id":"run-errand","label":"Run","target":"server"},` +
		`{"callback":"refine_errand_plan","command":"errand","data":{"instruction":"","plan_id":"pl_1","revision":2},"id":"revise-errand","label":"Revise","target":"server"},` +
		`{"callback":"discard_errand_plan","command":"errand","data":{"plan_id":"pl_1","revision":2},"id":"cancel-errand","label":"Cancel","target":"server"}],` +
		`"plan_id":"pl_1","revision":2,"steps":[{"command":"get_weather","args":{"resolved_datetimes":["today"]},"label":"Check weather","is_risky":false}]}`
	if got != want {
		t.Fatalf("plan card metadata\n got %s\nwant %s", got, want)
	}
	if strings.Contains(got, "editor_schema") {
		t.Fatal("editor_schema must not be set")
	}
	got = mustJSON(ReplanCardMetadata("wf_1", 3, steps, hh))
	want = `{"household_id":"hh-1","interactive_elements":[` +
		`{"callback":"approve_replan","command":"errand","data":{"revision":3,"workflow_id":"wf_1"},"id":"approve-replan","label":"Approve","target":"server"},` +
		`{"callback":"stop_errand","command":"errand","data":{"revision":3,"workflow_id":"wf_1"},"id":"stop-errand","label":"Stop","target":"server"}],` +
		`"revision":3,"steps":[{"command":"get_weather","args":{"resolved_datetimes":["today"]},"label":"Check weather","is_risky":false}],"workflow_id":"wf_1"}`
	if got != want {
		t.Fatalf("delta card metadata\n got %s\nwant %s", got, want)
	}
	body := planCardBody("Weather", []Step{{Command: "get_weather", Label: "Check weather"}, {Command: "get_news"}})
	if body != "Weather\n\n**Plan:**\n1. Check weather\n2. get_news\n\nTap **Run** to start it. To change it, tell me what to change above and tap **Revise**, or tap **Cancel** to discard." {
		t.Fatalf("body = %q", body)
	}
}

func TestDraftPostsPlanCard(t *testing.T) {
	e := newTenv(t, true)
	e.llm.say("plan", planJSON("Check the weather", step("get_weather", "Check weather", `{"resolved_datetimes": ["today"]}`)))
	p := e.draft("check the weather", ptr(7), weatherMenu(t))
	if p.State != "draft" || p.Revision != 1 || p.Summary != "Check the weather" || p.NodeID != "node-1" || *p.UserID != 7 {
		t.Fatalf("plan = %+v", p)
	}
	cards := e.cards.all()
	if len(cards) != 1 || cards[0].Title != "🗒️ Errand plan: Check the weather" || cards[0].Category != "errand_plan" ||
		!cards[0].Push || cards[0].TargetType != "user" || p.InboxItemID != e.cards.ids[0] {
		t.Fatalf("cards = %+v, plan item %q", cards, p.InboxItemID)
	}
	// The draft TTL job is queued 24 h out.
	var n int
	_ = e.d.Read.QueryRow(`SELECT COUNT(*) FROM platform_jobs WHERE type = ? AND state = 'queued'`, JobExpire).Scan(&n)
	if n != 1 {
		t.Fatalf("expire jobs = %d", n)
	}
}

func TestDraftFailuresPostHonestCards(t *testing.T) {
	e := newTenv(t, true)
	e.llm.say("plan", planJSON("x", step("nope", "n", "")))
	if err := e.s.DraftErrand(context.Background(), DraftRequest{HouseholdID: hh, NodeID: "node-1", Goal: "do magic",
		Menu: weatherMenu(t)}); err != nil {
		t.Fatal(err)
	}
	e.idle()
	e.llm.fail("plan", context.DeadlineExceeded)
	_ = e.s.DraftErrand(context.Background(), DraftRequest{HouseholdID: hh, NodeID: "node-1", Goal: "again"})
	e.idle()
	cards := e.cards.all()
	if len(cards) != 2 {
		t.Fatalf("cards = %+v", cards)
	}
	if cards[0].Title != "🗒️ Couldn't plan that errand" || cards[0].Summary != `I couldn't turn "do magic" into a plan I can run.` ||
		cards[0].Body != `I couldn't turn "do magic" into a plan I can run — try rephrasing it.` || cards[0].TargetType != "household" {
		t.Fatalf("rephrase card = %+v", cards[0])
	}
	if cards[1].Summary != "I hit a snag planning that errand." {
		t.Fatalf("snag card = %+v", cards[1])
	}
}

func TestDraftResourcesMenuFromNode(t *testing.T) {
	e := newTenv(t, true)
	e.nodes.report = true
	e.nodes.commands = []*pyjson.Object{mustObj(t, `{"command_name": "water_plants", "description": "Water"}`)}
	e.llm.say("plan", planJSON("Water", step("water_plants", "Water", "")))
	p := e.draft("water the plants", nil, nil)
	if len(p.Steps) != 1 || p.Steps[0].Command != "water_plants" {
		t.Fatalf("plan = %+v", p)
	}
	if !strings.Contains(*e.llm.requests("plan")[0].Messages[0].Content.Text, "- water_plants: Water | args: {}") {
		t.Fatal("menu not sourced from the node")
	}
}

func TestDraftDisabled(t *testing.T) {
	e := newTenv(t, false)
	e.set[SettingEnabled] = false
	if err := e.s.DraftErrand(context.Background(), DraftRequest{HouseholdID: hh, Goal: "x"}); err != ErrDisabled {
		t.Fatalf("err = %v", err)
	}
}

func launch(t *testing.T, e *tenv, p *planRow) string {
	t.Helper()
	r := e.tap(CallbackApprove, map[string]any{"plan_id": p.ID, "revision": float64(p.Revision)})
	if !r.Success {
		t.Fatalf("approve: %+v", r)
	}
	wf := e.onlyPlan().WorkflowID
	if wf == "" {
		t.Fatal("no workflow linked")
	}
	return wf
}

func TestRunNodeAndServerStepsToCompletion(t *testing.T) {
	e := newTenv(t, true)
	search := &fakeTool{name: "quick_search", def: toolDef("quick_search", "Search"),
		result: servertools.Obj("sources", []any{"a", "b"}, "status", "accepted")}
	e.tools.Register(search)
	menu := append(weatherMenu(t), MenuEntry{Command: "quick_search", Args: pyjson.NewObject()})
	e.llm.say("plan", planJSON("Weather and search",
		step("get_weather", "Check weather", `{"resolved_datetimes": ["tomorrow"]}`),
		step("quick_search", "Search", `{"query": "pizza"}`)))
	e.llm.say("compose", "  It will rain tomorrow, and I found 2 sources.  ")
	e.nodes.outputs["get_weather"] = servertools.Obj("success", true, "forecast", "rain")
	p := e.draft("weather and search", ptr(7), menu)
	wf := launch(t, e, p)
	e.idle()
	if st := e.wfState(wf); st != "done" {
		t.Fatalf("state = %s", st)
	}
	if e.planState(p.ID) != "launched" {
		t.Fatal("plan not launched")
	}
	calls := e.nodes.calls
	if len(calls) != 1 || calls[0].node != "node-1" || calls[0].voice != "weather and search" || *calls[0].user != 7 {
		t.Fatalf("node calls = %+v", calls)
	}
	// The date key resolved to ISO in the household zone.
	if !strings.Contains(calls[0].args, `"resolved_datetimes": ["2`) {
		t.Fatalf("args = %s", calls[0].args)
	}
	if len(search.turns) != 1 || search.turns[0].HouseholdID != hh || search.turns[0].Speaker.UserID != 7 ||
		search.turns[0].Utterance != "weather and search" || !strings.HasPrefix(search.turns[0].ConversationID, "errand-") {
		t.Fatalf("tool turn = %+v", search.turns)
	}
	cards := e.cards.all()
	last := cards[len(cards)-1]
	if last.Title != "✅ Errand done: Weather and search" || last.Summary != "It will rain tomorrow, and I found 2 sources." ||
		last.Category != "errand" || mustJSON(last.Metadata) != `{"household_id":"hh-1","status":"success"}` {
		t.Fatalf("completion = %+v", last)
	}
	compose := e.llm.requests("compose")[0]
	if compose.Label != "background" || *compose.MaxTokens != 400 || *compose.ReasoningBudget != 0 || *compose.Temperature != 0.3 {
		t.Fatalf("compose req = %+v", compose)
	}
	cp := *compose.Messages[0].Content.Text
	if !strings.Contains(cp, "- Check weather [ok]: get_weather ok") && !strings.Contains(cp, "- Check weather [ok]: forecast=rain") {
		t.Fatalf("compose prompt:\n%s", cp)
	}
	if !strings.Contains(cp, "- Search [ok]: 2 sources") {
		t.Fatalf("compose prompt:\n%s", cp)
	}
	// Second Run tap is a friendly no-op.
	r := e.tap(CallbackApprove, map[string]any{"plan_id": p.ID, "revision": "1"})
	if !r.Success || r.ContextData["inbox"].(map[string]any)["summary"] != "This errand is already launched." {
		t.Fatalf("second tap = %+v", r)
	}
}

func TestFailFastAndFallbackSummary(t *testing.T) {
	e := newTenv(t, true)
	e.llm.say("plan", planJSON("Two steps", step("get_weather", "Weather", ""), step("get_news", "News", "")))
	e.nodes.outputs["get_weather"] = servertools.Obj("success", false)
	p := e.draft("two", nil, weatherMenu(t))
	wf := launch(t, e, p)
	e.idle()
	if e.wfState(wf) != "failed" {
		t.Fatalf("state = %s", e.wfState(wf))
	}
	if got := e.nodes.ran(); len(got) != 1 {
		t.Fatalf("ran %v after a failure", got)
	}
	last := e.cards.all()[len(e.cards.all())-1]
	if last.Title != "⚠️ Errand couldn't finish: Two steps" || last.Summary != "Weather: the command reported a failure" ||
		last.TargetType != "household" {
		t.Fatalf("card = %+v", last)
	}
	var errText string
	_ = e.d.Read.QueryRow(`SELECT error FROM cc_workflows WHERE id = ?`, wf).Scan(&errText)
	if errText != "Weather: the command reported a failure" {
		t.Fatalf("error = %q", errText)
	}
}

func TestStaleRevisionAndScope(t *testing.T) {
	e := newTenv(t, true)
	e.llm.say("plan", planJSON("P", step("get_news", "News", "")))
	p := e.draft("news", nil, weatherMenu(t))
	if r := e.tap(CallbackApprove, map[string]any{"plan_id": p.ID, "revision": float64(2)}); r.Success ||
		r.Error != "This plan was updated — open the latest card to run it." {
		t.Fatalf("stale = %+v", r)
	}
	r := e.s.Callbacks()[CallbackApprove](context.Background(), CallbackContext{HouseholdID: "other", Data: map[string]any{"plan_id": p.ID}})
	if r.Success || r.Error != "Errand plan not found" {
		t.Fatalf("cross-household = %+v", r)
	}
	if r := e.tap(CallbackApprove, map[string]any{}); r.Error != "Missing plan_id" {
		t.Fatalf("missing = %+v", r)
	}
	if !revisionsMatch("garbage", 1) || !revisionsMatch(nil, 4) || revisionsMatch("2", 1) {
		t.Fatal("lenient revision")
	}
}

func TestDiscardDraft(t *testing.T) {
	e := newTenv(t, true)
	e.llm.say("plan", planJSON("P", step("get_news", "News", "")))
	p := e.draft("news", nil, weatherMenu(t))
	r := e.tap(CallbackDiscard, map[string]any{"plan_id": p.ID})
	if !r.Success || mustJSON(r.ContextData) != `{"inbox":{"metadata":{"household_id":"hh-1"},"summary":"P","title":"🗑️ Errand discarded"}}` {
		t.Fatalf("discard = %+v", r)
	}
	r = e.tap(CallbackDiscard, map[string]any{"plan_id": p.ID})
	if r.ContextData["inbox"].(map[string]any)["summary"] != "This errand is already cancelled — nothing to discard." {
		t.Fatalf("second discard = %+v", r)
	}
	if r := e.tap(CallbackDiscard, map[string]any{"plan_id": "pl_missing"}); !r.Success || r.ContextData != nil {
		t.Fatalf("missing = %+v", r)
	}
}

func phoneOutcome(wf string, step int, state, outcome string) CallOutcome {
	return CallOutcome{SessionID: "s", WorkflowID: wf, Step: step, State: state, ContactName: "Pharmacy", OutcomeJSON: outcome}
}

func phoneErrand(t *testing.T, e *tenv) (*planRow, string) {
	t.Helper()
	e.phone.enabled = true
	e.llm.say("plan", planJSON("Calls",
		step("make_phone_call", "Call pharmacy", `{"business": "Pharmacy", "goal": "refill"}`),
		step("make_phone_call", "Call office", `{"business": "Office", "goal": "book"}`)))
	p := e.draft("call pharmacy then office", ptr(7), weatherMenu(t))
	wf := launch(t, e, p)
	e.idle()
	w := e.wf(wf)
	if w.State != "waiting" || w.WaitingOn != "phone_call" || w.Cursor != 1 {
		t.Fatalf("after call 1: %+v", w)
	}
	if len(e.phone.placed) != 1 || e.phone.placed[0].Business != "Pharmacy" || e.phone.placed[0].WorkflowID != wf ||
		e.phone.placed[0].Step != 0 || e.phone.placed[0].PriorContext != "" {
		t.Fatalf("placed = %+v", e.phone.placed)
	}
	return p, wf
}

func TestPhoneSuspendResumeChain(t *testing.T) {
	e := newTenv(t, true)
	_, wf := phoneErrand(t, e)
	// A stale or non-terminal signal is ignored.
	_ = e.s.OnCallTerminal(context.Background(), phoneOutcome(wf, 0, "dialing", ""))
	_ = e.s.OnCallTerminal(context.Background(), phoneOutcome(wf, 5, "done", `{"goal_achieved": true}`))
	e.idle()
	if e.wf(wf).Cursor != 1 {
		t.Fatal("stale signal moved the run")
	}
	_ = e.s.OnCallTerminal(context.Background(), phoneOutcome(wf, 0, "done", `{"goal_achieved": true, "summary": "Refill ready", "facts": ["Lipitor", 20]}`))
	e.idle()
	w := e.wf(wf)
	if w.State != "waiting" || w.Cursor != 2 || len(w.Results) != 1 || w.Results[0].Summary != "Refill ready (confirmed: Lipitor, 20)" {
		t.Fatalf("after call 1 done: %+v", w)
	}
	if len(e.phone.placed) != 2 || e.phone.placed[1].PriorContext != "Earlier in this errand (background — use only if the business asks or it's relevant):\n- Call pharmacy: Refill ready (confirmed: Lipitor, 20)" {
		t.Fatalf("second call brief = %q", e.phone.placed[1].PriorContext)
	}
	// The same terminal outcome again is idempotent.
	_ = e.s.OnCallTerminal(context.Background(), phoneOutcome(wf, 0, "done", `{"goal_achieved": true}`))
	e.llm.say("compose", "Both calls went well.")
	_ = e.s.OnCallTerminal(context.Background(), phoneOutcome(wf, 1, "done", `{"goal_achieved": true}`))
	e.idle()
	if e.wfState(wf) != "done" {
		t.Fatalf("state = %s", e.wfState(wf))
	}
}

func TestPhoneGoalNotAchievedFailsFast(t *testing.T) {
	for _, c := range []struct{ outcome, want string }{
		{`{"goal_achieved": false}`, "reached Pharmacy but the goal wasn't achieved"},
		{`{"summary": "hm"}`, "reached Pharmacy but couldn't confirm the goal was achieved"},
	} {
		e := newTenv(t, true)
		_, wf := phoneErrand(t, e)
		_ = e.s.OnCallTerminal(context.Background(), phoneOutcome(wf, 0, "done", c.outcome))
		e.idle()
		if e.wfState(wf) != "failed" || len(e.phone.placed) != 1 {
			t.Fatalf("%s: state %s, placed %d", c.outcome, e.wfState(wf), len(e.phone.placed))
		}
		last := e.cards.all()[len(e.cards.all())-1]
		if last.Summary != "Call pharmacy: "+c.want {
			t.Fatalf("summary = %q", last.Summary)
		}
	}
	e := newTenv(t, true)
	_, wf := phoneErrand(t, e)
	o := phoneOutcome(wf, 0, "declined", "")
	_ = e.s.OnCallTerminal(context.Background(), o)
	e.idle()
	if last := e.cards.all()[len(e.cards.all())-1]; last.Summary != "Call pharmacy: the call was declined" {
		t.Fatalf("declined summary = %q", last.Summary)
	}
}

func TestPhoneStepWithoutPhoneFails(t *testing.T) {
	e := newTenv(t, true)
	e.phone.fail = true
	e.llm.say("plan", planJSON("Call", step("make_phone_call", "Call", `{"business": "X", "goal": "y"}`)))
	p := e.draft("call x", nil, weatherMenu(t))
	wf := launch(t, e, p)
	e.idle()
	if e.wfState(wf) != "failed" || e.cards.all()[len(e.cards.all())-1].Summary != "Call: couldn't set up the call" {
		t.Fatalf("state %s, cards %v", e.wfState(wf), e.cards.titles())
	}
}

func TestCancelWaitingRunDeclinesCalls(t *testing.T) {
	e := newTenv(t, true)
	p, wf := phoneErrand(t, e)
	r := e.tap(CallbackDiscard, map[string]any{"plan_id": p.ID})
	if !r.Success || mustJSON(r.ContextData["inbox"].(map[string]any)["summary"]) != `"Calls I won't place the remaining call(s)."` {
		t.Fatalf("cancel = %+v", r)
	}
	if e.wfState(wf) != "cancelled" || e.planState(p.ID) != "cancelled" || len(e.phone.declined) != 1 {
		t.Fatal("not cancelled")
	}
	// A late outcome can't resume a cancelled run.
	_ = e.s.OnCallTerminal(context.Background(), phoneOutcome(wf, 0, "done", `{"goal_achieved": true}`))
	e.idle()
	if e.wfState(wf) != "cancelled" || len(e.phone.placed) != 1 {
		t.Fatal("cancelled run resumed")
	}
}

func checkpointErrand(t *testing.T, e *tenv) (*planRow, string) {
	t.Helper()
	// The replan's menu is re-sourced from the node ∪ server tools: a remember continuation is a
	// new command (widens); another get_weather is not.
	e.nodes.report = true
	e.nodes.commands = []*pyjson.Object{mustObj(t, `{"command_name": "get_weather"}`)}
	e.tools.Register(&fakeTool{name: "remember", def: toolDef("remember", "Remember", "content", mustObj(t, `{"type": "string"}`)),
		result: servertools.Obj("status", "saved")})
	e.llm.say("plan", planJSON("Weather then decide",
		step("get_weather", "Check weather", ""),
		`{"command": "request_replan", "args": {"reason": "remind if rain"}, "label": "Decide"}`))
	p := e.draft("check weather and if rain remind me", ptr(7), weatherMenu(t))
	return p, launch(t, e, p)
}

func TestReplanInEnvelopeContinuesSilently(t *testing.T) {
	e := newTenv(t, true)
	e.llm.say("replan", planJSON("More weather", step("get_weather", "Check again", "")))
	e.llm.say("compose", "Done.")
	_, wf := checkpointErrand(t, e)
	e.idle()
	w := e.wf(wf)
	if w.State != "done" {
		t.Fatalf("state = %+v", w)
	}
	if got := e.nodes.ran(); len(got) != 2 {
		t.Fatalf("ran = %v", got)
	}
	if len(w.Steps) != 3 || w.Steps[2].Label != "Check again" || w.Revision != 2 {
		t.Fatalf("steps = %+v rev %d", w.Steps, w.Revision)
	}
	// The control step stays out of the summary prompt.
	if strings.Contains(*e.llm.requests("compose")[0].Messages[0].Content.Text, "Decide") {
		t.Fatal("control step narrated")
	}
	for _, c := range e.cards.all() {
		if strings.HasPrefix(c.Title, "🔀") {
			t.Fatal("delta card posted for an in-envelope change")
		}
	}
}

func TestReplanWideningNeedsApproval(t *testing.T) {
	e := newTenv(t, true)
	e.llm.say("replan", planJSON("Remind", step("remember", "Remember umbrella", `{"content": "umbrella"}`)))
	_, wf := checkpointErrand(t, e)
	e.idle()
	w := e.wf(wf)
	if w.State != "waiting" || w.WaitingOn != "approval" || w.InboxItemID == "" {
		t.Fatalf("run = %+v", w)
	}
	var delta Card
	for _, c := range e.cards.all() {
		if strings.HasPrefix(c.Title, "🔀 Errand update: ") {
			delta = c
		}
	}
	if delta.Title != "🔀 Errand update: Weather then decide" || delta.Summary != "remind if rain" ||
		!strings.Contains(delta.Body, "**Proposed additional steps:**\n1. Remember umbrella") {
		t.Fatalf("delta = %+v", delta)
	}
	if r := e.tap(CallbackApproveRepl, map[string]any{"workflow_id": wf, "revision": float64(9)}); r.Success {
		t.Fatalf("stale approve = %+v", r)
	}
	e.llm.say("compose", "Done.")
	if r := e.tap(CallbackApproveRepl, map[string]any{"workflow_id": wf, "revision": float64(1)}); !r.Success {
		t.Fatalf("approve = %+v", r)
	}
	e.idle()
	if e.wfState(wf) != "done" || len(e.wf(wf).Steps) != 3 {
		t.Fatalf("after approve: %+v", e.wf(wf))
	}
	if r := e.tap(CallbackApproveRepl, map[string]any{"workflow_id": wf, "revision": float64(2)}); !r.Success ||
		r.ContextData["inbox"].(map[string]any)["summary"] != "That change was already handled." {
		t.Fatalf("second approve = %+v", r)
	}
}

func TestReplanStop(t *testing.T) {
	e := newTenv(t, true)
	e.llm.say("replan", planJSON("Remind", step("remember", "Remember umbrella", "")))
	p, wf := checkpointErrand(t, e)
	e.idle()
	r := e.tap(CallbackStop, map[string]any{"workflow_id": wf})
	if !r.Success || r.ContextData["inbox"].(map[string]any)["title"] != "🗑️ Errand stopped" ||
		r.ContextData["inbox"].(map[string]any)["summary"] != "Weather then decide stopped. I won't place the remaining call(s)." {
		t.Fatalf("stop = %+v", r)
	}
	if e.wfState(wf) != "cancelled" || e.planState(p.ID) != "cancelled" {
		t.Fatal("not stopped")
	}
}

func TestReplanFailureContinues(t *testing.T) {
	e := newTenv(t, true)
	e.llm.fail("replan", context.Canceled)
	e.llm.say("compose", "Done.")
	_, wf := checkpointErrand(t, e)
	e.idle()
	if e.wfState(wf) != "done" {
		t.Fatalf("parked: %s", e.wfState(wf))
	}
}

func runJob(t *testing.T, h func(context.Context, queue.Job) ([]byte, error), payload any) {
	t.Helper()
	b, _ := json.Marshal(payload)
	if _, err := h(context.Background(), queue.Job{Payload: b}); err != nil {
		t.Fatal(err)
	}
}

func TestApprovalDeadlineFails(t *testing.T) {
	e := newTenv(t, true)
	e.llm.say("replan", planJSON("Remind", step("remember", "Remember umbrella", "")))
	_, wf := checkpointErrand(t, e)
	e.idle()
	var n int
	_ = e.d.Read.QueryRow(`SELECT COUNT(*) FROM platform_jobs WHERE type = ? AND state = 'queued'`, JobDeadline).Scan(&n)
	if n != 1 {
		t.Fatalf("deadline jobs = %d", n)
	}
	runJob(t, e.s.runDeadlineJob, deadlineJob{WorkflowID: wf, Step: 1, Kind: "approval"})
	if e.wfState(wf) != "partial" {
		t.Fatalf("state = %s", e.wfState(wf))
	}
	last := e.cards.all()[len(e.cards.all())-1]
	if last.Title != "⚠️ Errand finished with issues: Weather then decide" ||
		last.Summary != "Check weather: get_weather ok • Decide: the change wasn't approved in time" {
		t.Fatalf("card = %+v", last)
	}
}

func TestPhoneDeadlineFromConfirm(t *testing.T) {
	e := newTenv(t, true)
	_, wf := phoneErrand(t, e)
	e.s.Now = func() time.Time { return mustTime(t, "2030-10-07T12:00:00Z") }
	// Confirmed 10 min ago: re-armed for confirm + 60 min.
	e.phone.status["sess-Pharmacy"] = CallOutcome{State: "dialing", ContactName: "Pharmacy", ConfirmedAt: mustTime(t, "2030-10-07T11:50:00Z")}
	runJob(t, e.s.runDeadlineJob, deadlineJob{WorkflowID: wf, Step: 0, Kind: "phone_call", SessionID: "sess-Pharmacy"})
	if e.wfState(wf) != "waiting" {
		t.Fatal("failed before the deadline")
	}
	var at int64
	_ = e.d.Read.QueryRow(`SELECT run_at FROM platform_jobs WHERE type = ? AND state = 'queued' ORDER BY id DESC LIMIT 1`, JobDeadline).Scan(&at)
	if at != mustTime(t, "2030-10-07T12:50:00Z").UnixMilli() {
		t.Fatalf("re-armed at %d", at)
	}
	// Past the deadline: fail-fast, undialled calls declined.
	e.phone.status["sess-Pharmacy"] = CallOutcome{State: "in_call", ContactName: "Pharmacy", ConfirmedAt: mustTime(t, "2030-10-07T10:00:00Z")}
	runJob(t, e.s.runDeadlineJob, deadlineJob{WorkflowID: wf, Step: 0, Kind: "phone_call", SessionID: "sess-Pharmacy"})
	if e.wfState(wf) != "failed" || len(e.phone.declined) != 1 {
		t.Fatalf("state %s declined %v", e.wfState(wf), e.phone.declined)
	}
	if last := e.cards.all()[len(e.cards.all())-1]; last.Summary != "Call pharmacy: the call wasn't completed in time" {
		t.Fatalf("summary = %q", last.Summary)
	}
}

func TestPhoneDeadlineSafetyNetResumes(t *testing.T) {
	e := newTenv(t, true)
	_, wf := phoneErrand(t, e)
	e.phone.status["sess-Pharmacy"] = CallOutcome{State: "done", ContactName: "Pharmacy", OutcomeJSON: `{"goal_achieved": true}`}
	runJob(t, e.s.runDeadlineJob, deadlineJob{WorkflowID: wf, Step: 0, Kind: "phone_call", SessionID: "sess-Pharmacy"})
	e.idle()
	if w := e.wf(wf); w.Cursor != 2 || len(e.phone.placed) != 2 {
		t.Fatalf("safety net didn't resume: %+v", w)
	}
}

func TestRefineUpdatesCardInPlace(t *testing.T) {
	e := newTenv(t, true)
	e.llm.say("plan", planJSON("Email", step("get_news", "News", "")))
	p := e.draft("news", nil, weatherMenu(t))
	if r := e.tap(CallbackRefine, map[string]any{"plan_id": p.ID, "revision": 1.0, "instruction": "  "}); r.Error != "Tell me what to change first." {
		t.Fatalf("empty = %+v", r)
	}
	e.llm.say("refine", planJSON("News and weather", step("get_news", "News", ""), step("get_weather", "Weather", "")))
	if r := e.tap(CallbackRefine, map[string]any{"plan_id": p.ID, "revision": 1.0, "instruction": "add weather"}); !r.Success {
		t.Fatalf("refine = %+v", r)
	}
	q := e.onlyPlan()
	if q.Revision != 2 || len(q.Steps) != 2 || q.Summary != "News and weather" {
		t.Fatalf("plan = %+v", q)
	}
	up, ok := e.cards.updated[p.InboxItemID]
	if !ok || up.Title != "🗒️ Errand plan: News and weather" || up.Metadata["revision"] != 2 || len(e.cards.all()) != 1 {
		t.Fatalf("card not updated in place: %+v", e.cards.updated)
	}
	e.llm.say("refine", "garbage")
	if r := e.tap(CallbackRefine, map[string]any{"plan_id": p.ID, "revision": 2.0, "instruction": "x"}); r.Error != "I couldn't apply that change — try saying it differently." {
		t.Fatalf("bad refine = %+v", r)
	}
	if r := e.tap(CallbackRefine, map[string]any{"plan_id": p.ID, "revision": 1.0, "instruction": "x"}); r.Error != "This plan was updated — open the latest card to change it." {
		t.Fatalf("stale refine = %+v", r)
	}
}

func TestExpireDraft(t *testing.T) {
	e := newTenv(t, true)
	e.llm.say("plan", planJSON("P", step("get_news", "News", "")))
	p := e.draft("news", nil, weatherMenu(t))
	runJob(t, e.s.runExpireJob, expireJob{PlanID: p.ID, HouseholdID: hh})
	if e.planState(p.ID) != "expired" {
		t.Fatal("not expired")
	}
	if up := e.cards.updated[p.InboxItemID]; up.Title != "🗒️ Errand plan expired: P" || up.Metadata["interactive_elements"] != nil {
		t.Fatalf("expired card = %+v", up)
	}
	if r := e.tap(CallbackApprove, map[string]any{"plan_id": p.ID}); !r.Success ||
		r.ContextData["inbox"].(map[string]any)["summary"] != "This errand is already expired." {
		t.Fatalf("run expired = %+v", r)
	}
}

func TestRecoverFailsOrphanedRuns(t *testing.T) {
	e := newTenv(t, false)
	ctx := context.Background()
	_, err := e.d.Write.ExecContext(ctx, `INSERT INTO cc_workflows (id, kind, household_id, user_id, goal, title, steps,
		results_json, state, created_at, updated_at) VALUES
		('wf_a', 'errand', ?, 7, 'g', 'Mid', '[]', '[{"command": "x", "label": "X", "success": true}]', 'running', 'now', 'now'),
		('wf_b', 'errand', ?, NULL, 'g', NULL, '[]', '[]', 'running', 'now', 'now'),
		('wf_c', 'errand', ?, NULL, 'g', NULL, '[]', '[]', 'waiting', 'now', 'now')`, hh, hh, hh)
	if err != nil {
		t.Fatal(err)
	}
	if n := e.s.Recover(ctx); n != 2 {
		t.Fatalf("recovered %d", n)
	}
	if e.wfState("wf_a") != "partial" || e.wfState("wf_b") != "failed" || e.wfState("wf_c") != "waiting" {
		t.Fatal("wrong states")
	}
	titles := e.cards.titles()
	if len(titles) != 2 || titles[0] != "⚠️ Errand finished with issues: Mid" || titles[1] != "⚠️ Errand couldn't finish: g" {
		t.Fatalf("titles = %v", titles)
	}
	if e.cards.all()[0].Summary != "This errand was interrupted by a restart and couldn't be finished." {
		t.Fatal("summary")
	}
}

func TestPurgeUser(t *testing.T) {
	e := newTenv(t, false)
	ctx := context.Background()
	_, _ = e.d.Write.ExecContext(ctx, `INSERT INTO cc_errand_plans (id, household_id, user_id, goal, steps, state, created_at, updated_at)
		VALUES ('pl_a', ?, 7, 'g', '[]', 'draft', 'n', 'n'), ('pl_b', ?, 8, 'g', '[]', 'draft', 'n', 'n')`, hh, hh)
	_, _ = e.d.Write.ExecContext(ctx, `INSERT INTO cc_workflows (id, kind, household_id, user_id, goal, steps, state, created_at, updated_at)
		VALUES ('wf_a', 'errand', ?, 7, 'g', '[]', 'done', 'n', 'n'), ('wf_b', 'errand', ?, 7, 'g', '[]', 'waiting', 'n', 'n')`, hh, hh)
	_ = e.s.DraftErrand(ctx, DraftRequest{HouseholdID: hh, UserID: ptr(7), Goal: "x"})
	_ = e.s.DraftErrand(ctx, DraftRequest{HouseholdID: hh, UserID: ptr(8), Goal: "y"})
	tx, err := e.d.Write.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := PurgeUser(ctx, tx, 7, true); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var plans, owned, cancelledJobs int
	_ = e.d.Read.QueryRow(`SELECT COUNT(*) FROM cc_errand_plans`).Scan(&plans)
	_ = e.d.Read.QueryRow(`SELECT COUNT(*) FROM cc_workflows WHERE user_id IS NOT NULL`).Scan(&owned)
	_ = e.d.Read.QueryRow(`SELECT COUNT(*) FROM platform_jobs WHERE state = 'cancelled'`).Scan(&cancelledJobs)
	if plans != 1 || owned != 0 || cancelledJobs != 1 || e.wfState("wf_b") != "cancelled" || e.wfState("wf_a") != "done" {
		t.Fatalf("plans %d owned %d cancelled jobs %d wf_b %s", plans, owned, cancelledJobs, e.wfState("wf_b"))
	}
}
