package errands

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/policy"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

var (
	askHousehold   = policy.Verdict{Effect: policy.Ask, Rule: &policy.Rule{ID: 6, Effect: policy.Ask}, Scope: policy.ScopeHousehold, Reason: "household rule"}
	blockHousehold = policy.Verdict{Effect: policy.Block, Rule: &policy.Rule{ID: 5, Effect: policy.Block, Note: "no web searches"},
		Scope: policy.ScopeHousehold, Reason: "household rule"}
)

// stepPolicy is the fakePolicy at plan time; at step time, step (when set) answers first.
type stepPolicy struct {
	*fakePolicy
	step func(a policy.Action) (policy.Verdict, bool)
}

func (p *stepPolicy) Decide(ctx context.Context, hh string, a policy.Action) policy.Verdict {
	v := p.fakePolicy.Decide(ctx, hh, a)
	if a.Source == policy.SourceErrandStep && p.step != nil {
		if sv, ok := p.step(a); ok {
			return sv
		}
	}
	return v
}

// atStep makes tool answer v at step time only.
func atStep(e *tenv, tool string, v policy.Verdict) {
	e.s.Policy = &stepPolicy{fakePolicy: e.policy, step: func(a policy.Action) (policy.Verdict, bool) {
		return v, a.Tool == tool
	}}
}

func stepCard(t *testing.T, e *tenv) Card {
	t.Helper()
	var found []Card
	for _, c := range e.cards.all() {
		if strings.HasPrefix(c.Title, stepTitlePrefix) {
			found = append(found, c)
		}
	}
	if len(found) != 1 {
		t.Fatalf("step cards = %v", e.cards.titles())
	}
	return found[0]
}

func eventsOf(e *tenv, outcome string) []policy.Event {
	var out []policy.Event
	for _, ev := range e.act.all() {
		if ev.Source == policy.SourceErrandStep && ev.Outcome == outcome {
			out = append(out, ev)
		}
	}
	return out
}

func TestStepBlockFailsTappedRun(t *testing.T) {
	e, menu := autorunEnv(t)
	weatherAndSearch(e) // both steps ask (the default): a plan card
	p := e.draft("weather and search", ptr(7), menu)
	if !hasTitlePrefix(e.cards.all(), planTitlePrefix) {
		t.Fatalf("cards = %v", e.cards.titles())
	}
	e.policy.set("quick_search", blockHousehold)
	wf := launch(t, e, p)
	e.idle()
	w := e.wf(wf)
	if w.State != "partial" || len(w.Results) != 2 || !w.Results[0].Success ||
		w.Results[1].Error != "blocked by a household rule: no web searches" || w.Results[1].Success {
		t.Fatalf("run = %+v", w)
	}
	bl := eventsOf(e, policy.OutcomeBlocked)
	if len(bl) != 1 || bl[0].Tool != "quick_search" || bl[0].SourceID != wf || bl[0].RuleID == nil || *bl[0].RuleID != 5 {
		t.Fatalf("events = %+v", e.act.all())
	}
	if hasTitlePrefix(e.cards.all(), stepTitlePrefix) {
		t.Fatal("a tapped run posted a step card")
	}
}

func TestStepAskDoesNotParkTappedRun(t *testing.T) {
	e, menu := autorunEnv(t)
	weatherAndSearch(e)
	e.llm.say("compose", "Done.")
	p := e.draft("weather and search", ptr(7), menu)
	wf := launch(t, e, p)
	e.idle()
	if e.wfState(wf) != "done" {
		t.Fatalf("state = %s", e.wfState(wf))
	}
	if e.policy.n() < 4 { // two at plan time, two at step time
		t.Fatalf("policy calls = %d", e.policy.n())
	}
	if hasTitlePrefix(e.cards.all(), stepTitlePrefix) || len(eventsOf(e, policy.OutcomeAsked)) != 0 {
		t.Fatalf("cards %v events %+v", e.cards.titles(), e.act.all())
	}
}

// parkedAutorun drafts an autorun plan whose second step (quick_search) asks at step time.
func parkedAutorun(t *testing.T, e *tenv, menu []MenuEntry) *planRow {
	t.Helper()
	e.policy.set("get_weather", allowHousehold)
	e.policy.set("quick_search", allowTool)
	atStep(e, "quick_search", askHousehold)
	weatherAndSearch(e)
	p := e.draft("weather and search", ptr(7), menu)
	if p.WorkflowID == "" {
		t.Fatalf("not autorun: %v", e.cards.titles())
	}
	return p
}

func TestStepAskParksAutorun(t *testing.T) {
	e, menu := autorunEnv(t)
	p := parkedAutorun(t, e, menu)
	w := e.wf(p.WorkflowID)
	if w.State != "waiting" || w.WaitingOn != signalApproval || w.Cursor != 1 || w.Revision != 2 || len(w.Results) != 1 {
		t.Fatalf("run = %+v", w)
	}
	c := stepCard(t, e)
	if c.Title != "✋ Errand needs your OK: Search the web" ||
		c.Summary != "“Weather and search” wants to run “Search the web”; your household's rules say to ask first." ||
		c.Category != planCategory || !c.Push {
		t.Fatalf("card = %+v", c)
	}
	md := c.Metadata
	els, _ := md["interactive_elements"].([]any)
	if md["workflow_id"] != w.ID || md["step"] != 1 || md["revision"] != 2 || len(els) != 2 ||
		els[0].(map[string]any)["callback"] != CallbackApproveStep || els[1].(map[string]any)["callback"] != CallbackStop {
		t.Fatalf("metadata = %s", mustJSON(md))
	}
	up, ok := e.cards.update(p.InboxItemID)
	if !ok || up.Metadata["state"] != "waiting" || strings.Join(stepStatuses(t, up.Metadata), ",") != "ok,pending" {
		t.Fatalf("ran card = %+v", up)
	}
	if ev := eventsOf(e, policy.OutcomeAsked); len(ev) != 1 || ev[0].Tool != "quick_search" {
		t.Fatalf("events = %+v", e.act.all())
	}
	data := els[0].(map[string]any)["data"].(map[string]any)
	r := e.tap(CallbackApproveStep, map[string]any{"workflow_id": data["workflow_id"], "step": float64(1), "revision": float64(2)})
	if ib, _ := r.ContextData["inbox"].(map[string]any); !r.Success || ib["title"] != "👍 Approved" ||
		ib["summary"] != "“Search the web” will run now." {
		t.Fatalf("approve = %+v", r)
	}
	e.idle()
	w = e.wf(p.WorkflowID)
	if w.State != "done" || len(w.Results) != 2 || !w.Results[1].Success || !w.Steps[1].Approved {
		t.Fatalf("after approve = %+v", w)
	}
	if up, _ := e.cards.update(p.InboxItemID); up.Metadata["state"] != "done" {
		t.Fatalf("ran card = %s", mustJSON(up.Metadata))
	}
	if ran := eventsOf(e, policy.OutcomeRan); len(ran) != 2 || ran[1].Tool != "quick_search" || *ran[1].RuleID != 6 {
		t.Fatalf("events = %+v", e.act.all())
	}
}

func TestStepApproveStale(t *testing.T) {
	e, menu := autorunEnv(t)
	p := parkedAutorun(t, e, menu)
	if r := e.tap(CallbackApproveStep, map[string]any{"workflow_id": p.WorkflowID, "step": "1", "revision": float64(1)}); r.Success ||
		r.Error != "This errand was updated — open the latest card." {
		t.Fatalf("stale = %+v", r)
	}
	if r := e.tap(CallbackApproveStep, map[string]any{"workflow_id": p.WorkflowID, "step": 0, "revision": float64(2)}); !r.Success ||
		r.ContextData["inbox"].(map[string]any)["summary"] != "That step was already handled." {
		t.Fatalf("wrong step = %+v", r)
	}
	if r := e.tap(CallbackApproveStep, map[string]any{"workflow_id": p.WorkflowID}); r.Error != "Missing step" {
		t.Fatalf("no step = %+v", r)
	}
	if r := e.tap(CallbackApproveStep, map[string]any{"step": 1}); r.Error != "Missing workflow_id" {
		t.Fatalf("no workflow = %+v", r)
	}
	if e.wfState(p.WorkflowID) != "waiting" {
		t.Fatal(e.wfState(p.WorkflowID))
	}
}

func TestStepConfirmDeadline(t *testing.T) {
	e, menu := autorunEnv(t)
	e.s.ApprovalDeadline = 50 * time.Millisecond
	p := parkedAutorun(t, e, menu)
	e.waitFor("run ended", func() bool { return e.wfState(p.WorkflowID) == "partial" })
	w := e.wf(p.WorkflowID)
	if len(w.Results) != 2 || w.Results[1].Error != "nobody approved this step in time" || w.Results[1].Command != "quick_search" {
		t.Fatalf("run = %+v", w)
	}
	up, _ := e.cards.update(p.InboxItemID)
	if up.Metadata["state"] != "partial" {
		t.Fatalf("ran card = %s", mustJSON(up.Metadata))
	}
	if got := strings.Join(stepStatuses(t, up.Metadata), ","); got != "ok,failed" {
		t.Fatalf("steps = %s", got)
	}
}

func TestReplanStepsRechecked(t *testing.T) {
	e, menu := autorunEnv(t)
	e.nodes.report = true
	e.nodes.commands = []*pyjson.Object{mustObj(t, `{"command_name": "get_weather"}`)}
	e.policy.set("get_weather", allowHousehold)
	// The added step (tomorrow's weather) asks at step time.
	e.s.Policy = &stepPolicy{fakePolicy: e.policy, step: func(a policy.Action) (policy.Verdict, bool) {
		return askHousehold, strings.Contains(pyjson.Dumps(a.Args, true), "tomorrow")
	}}
	e.llm.say("plan", planJSON("Weather then decide",
		step("get_weather", "Check weather", `{"resolved_datetimes": ["today"]}`),
		`{"command": "request_replan", "args": {"reason": "check tomorrow too"}, "label": "Decide"}`))
	e.llm.say("replan", planJSON("More weather", step("get_weather", "Check tomorrow", `{"resolved_datetimes": ["tomorrow"]}`)))
	p := e.draft("weather and decide", ptr(7), menu)
	if p.WorkflowID == "" {
		t.Fatalf("not autorun: %v", e.cards.titles())
	}
	e.idle()
	w := e.wf(p.WorkflowID)
	if w.State != "waiting" || w.Cursor != 2 || len(w.Steps) != 3 || w.Revision != 3 {
		t.Fatalf("run = %+v", w)
	}
	if c := stepCard(t, e); c.Title != "✋ Errand needs your OK: Check tomorrow" {
		t.Fatalf("card = %+v", c)
	}
	up, _ := e.cards.update(p.InboxItemID)
	if up.Metadata["card_type"] != "autonomy_ran" || up.Metadata["state"] != "waiting" {
		t.Fatalf("ran card = %+v", up)
	}
	// The checkpoint's own deadline (same cursor, an older revision) is not this wait's.
	runJob(t, e.s.runDeadlineJob, deadlineJob{WorkflowID: w.ID, Step: 1, Kind: signalApproval, Revision: 1})
	if e.wfState(w.ID) != "waiting" {
		t.Fatalf("old deadline ended the wait: %s", e.wfState(w.ID))
	}
	runJob(t, e.s.runDeadlineJob, deadlineJob{WorkflowID: w.ID, Step: 1, Kind: signalApproval, Confirm: true, Revision: 3})
	if w := e.wf(w.ID); w.State == "waiting" || w.Results[len(w.Results)-1].Error != "nobody approved this step in time" {
		t.Fatalf("confirm deadline = %+v", w)
	}
}

func TestAutorunWideningReplanKeepsRanCard(t *testing.T) {
	e, menu := autorunEnv(t)
	e.set[settingWebSearch] = true // the replan's menu offers quick_search
	e.nodes.report = true
	e.nodes.commands = []*pyjson.Object{mustObj(t, `{"command_name": "get_weather"}`)}
	e.policy.set("get_weather", allowHousehold)
	e.llm.say("plan", planJSON("Weather then decide",
		step("get_weather", "Check weather", ""),
		`{"command": "request_replan", "args": {"reason": "search if rain"}, "label": "Decide"}`))
	e.llm.say("replan", planJSON("Search", step("quick_search", "Search deals", "")))
	p := e.draft("weather and decide", ptr(7), menu)
	e.idle()
	if e.wfState(p.WorkflowID) != "waiting" || !hasTitlePrefix(e.cards.all(), replanTitlePrefix) {
		t.Fatalf("state %s cards %v", e.wfState(p.WorkflowID), e.cards.titles())
	}
	if up, _ := e.cards.update(p.InboxItemID); up.Metadata["card_type"] != "autonomy_ran" {
		t.Fatalf("ran card overwritten: %+v", up)
	}
	if w := e.wf(p.WorkflowID); w.InboxItemID != p.InboxItemID {
		t.Fatalf("inbox item = %s", w.InboxItemID)
	}
}

func TestStepCheckOff(t *testing.T) {
	e, menu := autorunEnv(t)
	e.set[SettingAutonomous] = false
	weatherAndSearch(e)
	e.llm.say("compose", "Done.")
	p := e.draft("weather and search", ptr(7), menu)
	wf := launch(t, e, p)
	e.idle()
	if e.wfState(wf) != "done" || e.policy.n() != 0 {
		t.Fatalf("state %s, policy calls %d", e.wfState(wf), e.policy.n())
	}
}
