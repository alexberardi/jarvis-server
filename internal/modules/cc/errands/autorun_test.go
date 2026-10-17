package errands

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/policy"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

var (
	allowHousehold = policy.Verdict{Effect: policy.Allow, Rule: &policy.Rule{ID: 4, Tool: "get_weather", Effect: policy.Allow},
		Scope: policy.ScopeHousehold, Reason: "household rule"}
	allowTool = policy.Verdict{Effect: policy.Allow, Scope: "tool", Reason: "the tool declares run_without_confirmation"}
)

// autorunEnv is a running tenv with autonomy on and quick_search registered.
func autorunEnv(t *testing.T) (*tenv, []MenuEntry) {
	e := newTenv(t, true)
	e.set[SettingAutonomous] = true
	e.tools.Register(&fakeTool{name: "quick_search", def: toolDef("quick_search", "Search"),
		result: servertools.Obj("message", "5 results")})
	return e, append(weatherMenu(t), MenuEntry{Command: "quick_search", Args: pyjson.NewObject()})
}

func weatherAndSearch(e *tenv) {
	e.llm.say("plan", planJSON("Weather and search",
		step("get_weather", "Check weather", `{"resolved_datetimes": ["today"]}`),
		step("quick_search", "Search the web", `{"query": "mets score"}`)))
}

func hasTitlePrefix(cards []Card, prefix string) bool {
	for _, c := range cards {
		if strings.HasPrefix(c.Title, prefix) {
			return true
		}
	}
	return false
}

func (f *fakeCards) update(id string) (Card, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.updated[id]
	return c, ok
}

func stepStatuses(t *testing.T, md map[string]any) []string {
	t.Helper()
	steps, ok := md["steps"].([]any)
	if !ok {
		t.Fatalf("steps = %#v", md["steps"])
	}
	var out []string
	for _, s := range steps {
		out = append(out, s.(map[string]any)["status"].(string))
	}
	return out
}

func outcomes(evs []policy.Event) []string {
	var out []string
	for _, e := range evs {
		out = append(out, e.Outcome)
	}
	return out
}

func TestAutorunLaunchesWithRanCard(t *testing.T) {
	e, menu := autorunEnv(t)
	e.policy.set("get_weather", allowHousehold)
	e.policy.set("quick_search", allowTool)
	weatherAndSearch(e)
	p := e.draft("weather and search", ptr(7), menu)
	cards := e.cards.all()
	if hasTitlePrefix(cards, planTitlePrefix) || hasTitlePrefix(cards, "✅ Errand done") {
		t.Fatalf("cards = %v", e.cards.titles())
	}
	if len(cards) != 1 {
		t.Fatalf("cards = %v", e.cards.titles())
	}
	ran := cards[0]
	if ran.Title != "🤖 Ran on its own: Weather and search" || !ran.Gated || ran.Category != CategoryAutonomy || !ran.Push ||
		ran.DedupeKey != "autorun:"+p.WorkflowID || p.InboxItemID != e.cards.ids[0] {
		t.Fatalf("ran card = %+v, plan %+v", ran, p)
	}
	md := ran.Metadata
	if md["card_type"] != "autonomy_ran" || md["state"] != "running" || md["workflow_id"] != p.WorkflowID || md["plan_id"] != p.ID {
		t.Fatalf("metadata = %s", mustJSON(md))
	}
	if els, _ := md["interactive_elements"].([]any); len(els) != 1 || els[0].(map[string]any)["callback"] != CallbackStop {
		t.Fatalf("elements = %s", mustJSON(md["interactive_elements"]))
	}
	if got := strings.Join(stepStatuses(t, md), ","); got != "pending,pending" {
		t.Fatalf("initial steps = %s", got)
	}
	if e.wfState(p.WorkflowID) != "done" || e.planState(p.ID) != "launched" {
		t.Fatalf("wf %s plan %s", e.wfState(p.WorkflowID), e.planState(p.ID))
	}
	w := e.wf(p.WorkflowID)
	if w.Kind != KindAutorun || w.InboxItemID != p.InboxItemID {
		t.Fatalf("workflow = %+v", w)
	}
	up, ok := e.cards.update(p.InboxItemID)
	if !ok {
		t.Fatal("ran card not updated")
	}
	if up.Metadata["state"] != "done" || strings.Join(stepStatuses(t, up.Metadata), ",") != "ok,ok" || up.Title != ran.Title {
		t.Fatalf("final card = %s", mustJSON(up.Metadata))
	}
	if _, ok := up.Metadata["interactive_elements"]; ok {
		t.Fatal("a done card keeps its Stop button")
	}
	if !strings.Contains(up.Body, "2. ✅ Search the web — 5 results") || !strings.Contains(up.Body, "- get_weather: household rule") {
		t.Fatalf("body = %q", up.Body)
	}
	evs := e.act.all()
	if strings.Join(outcomes(evs), ",") != "launched,ran,ran" || evs[1].Source != policy.SourceErrandStep ||
		evs[1].SourceID != p.WorkflowID || evs[1].Tool != "get_weather" || *evs[1].RuleID != 4 || evs[0].Outcome != policy.OutcomeLaunched || evs[0].Source != policy.SourceErrandPlan ||
		evs[0].SourceID != p.ID || evs[0].Detail["workflow_id"] != p.WorkflowID {
		t.Fatalf("events = %+v", evs)
	}
}

func TestAutorunOffKeepsPlanCard(t *testing.T) {
	e, menu := autorunEnv(t)
	e.set[SettingAutonomous] = false
	e.policy.set("get_weather", allowHousehold)
	e.policy.set("quick_search", allowTool)
	weatherAndSearch(e)
	p := e.draft("weather and search", ptr(7), menu)
	if titles := e.cards.titles(); len(titles) != 1 || titles[0] != planTitlePrefix+"Weather and search" || p.State != "draft" {
		t.Fatalf("cards = %v, plan %+v", titles, p)
	}
	if e.policy.n() != 0 || len(e.act.all()) != 0 {
		t.Fatalf("policy calls %d, events %+v", e.policy.n(), e.act.all())
	}
}

func TestAutorunAskKeepsPlanCard(t *testing.T) {
	e, menu := autorunEnv(t)
	e.policy.set("get_weather", allowHousehold)
	weatherAndSearch(e)
	p := e.draft("weather and search", ptr(7), menu)
	if titles := e.cards.titles(); len(titles) != 1 || titles[0] != planTitlePrefix+"Weather and search" || p.State != "draft" {
		t.Fatalf("cards = %v, plan %+v", titles, p)
	}
	evs := e.act.all()
	if len(evs) != 1 || evs[0].Outcome != policy.OutcomeAsked || evs[0].Tool != "quick_search" || evs[0].Effect != policy.Ask ||
		evs[0].Reason != "no rule; asks by default" || evs[0].SourceID != p.ID {
		t.Fatalf("events = %+v", evs)
	}
}

func TestAutorunBlockPostsBlockedCard(t *testing.T) {
	e, menu := autorunEnv(t)
	e.policy.set("get_weather", policy.Verdict{Effect: policy.Block, Scope: policy.ScopeHousehold, Reason: "household rule",
		Rule: &policy.Rule{ID: 9, Tool: "get_weather", Effect: policy.Block, Note: "never at night"}})
	e.policy.set("quick_search", allowTool)
	weatherAndSearch(e)
	p := e.draft("weather and search", ptr(7), menu)
	cards := e.cards.all()
	if len(cards) != 1 || cards[0].Title != "🚫 Errand blocked: Weather and search" || cards[0].Category != planCategory ||
		!cards[0].Push || cards[0].Metadata["state"] != "blocked" || cards[0].Metadata["plan_id"] != p.ID {
		t.Fatalf("cards = %+v", cards)
	}
	want := "I didn't run this errand because your household's rules block:\n\n- Check weather (get_weather): never at night" +
		"\n\nA household admin can change the rules."
	if cards[0].Body != want {
		t.Fatalf("body = %q", cards[0].Body)
	}
	if p.State != "cancelled" {
		t.Fatalf("plan = %+v", p)
	}
	evs := e.act.all()
	if len(evs) != 1 || evs[0].Outcome != policy.OutcomeBlocked || evs[0].Tool != "get_weather" || evs[0].RuleID == nil ||
		*evs[0].RuleID != 9 || evs[0].Effect != policy.Block || evs[0].SourceID != p.ID {
		t.Fatalf("events = %+v", evs)
	}
	if len(e.nodes.ran()) != 0 {
		t.Fatal("a blocked plan ran")
	}
}

func TestAutorunStepCap(t *testing.T) {
	e, menu := autorunEnv(t)
	for _, c := range []string{"get_weather", "get_news", "remember", "quick_search"} {
		e.policy.set(c, allowTool)
	}
	e.llm.say("plan", planJSON("Four things", step("get_weather", "Weather", ""), step("get_news", "News", ""),
		step("remember", "Remember", `{"content": "x"}`), step("quick_search", "Search", `{"query": "x"}`)))
	p := e.draft("four things", ptr(7), menu)
	if titles := e.cards.titles(); len(titles) != 1 || titles[0] != planTitlePrefix+"Four things" || p.State != "draft" {
		t.Fatalf("cards = %v, plan %+v", titles, p)
	}
	if got := outcomes(e.act.all()); len(got) != 1 || got[0] != policy.OutcomeAsked {
		t.Fatalf("events = %v", got)
	}
}

func TestAutorunStopWhileRunning(t *testing.T) {
	e, menu := autorunEnv(t)
	e.policy.set("get_weather", allowTool)
	e.policy.set("get_news", allowTool)
	hold := make(chan struct{})
	e.nodes.hold = hold
	e.llm.say("plan", planJSON("Weather and news", step("get_weather", "Weather", ""), step("get_news", "News", "")))
	if err := e.s.DraftErrand(context.Background(), DraftRequest{HouseholdID: hh, NodeID: "node-1", UserID: ptr(7),
		Goal: "weather and news", Menu: menu}); err != nil {
		t.Fatal(err)
	}
	e.waitFor("the first step", func() bool { return e.nodes.entered.Load() == 1 })
	p := e.onlyPlan()
	r := e.tap(CallbackStop, map[string]any{"workflow_id": p.WorkflowID, "revision": 1})
	ib, _ := r.ContextData["inbox"].(map[string]any)
	if !r.Success || ib["title"] != "🛑 Stopping the errand" || ib["summary"] != "I'll stop “Weather and news” before its next step." {
		t.Fatalf("stop = %+v", r)
	}
	close(hold)
	e.idle()
	if got := e.nodes.ran(); len(got) != 1 || got[0] != "get_weather" {
		t.Fatalf("ran = %v", got)
	}
	if st := e.wfState(p.WorkflowID); st != "cancelled" {
		t.Fatalf("state = %s", st)
	}
	up, _ := e.cards.update(p.InboxItemID)
	if up.Metadata["state"] != "stopped" || strings.Join(stepStatuses(t, up.Metadata), ",") != "ok,pending" ||
		up.Summary != "Stopped before “News”." {
		t.Fatalf("card = %+v", up)
	}
	if _, ok := up.Metadata["interactive_elements"]; ok {
		t.Fatal("a stopped card keeps its Stop button")
	}
	// The step that ran before the Stop is logged too (checkpoint 2).
	if got := outcomes(e.act.all()); len(got) != 3 || got[0] != policy.OutcomeLaunched || got[1] != policy.OutcomeRan ||
		got[2] != policy.OutcomeStopped {
		t.Fatalf("events = %v", got)
	}
	if ev := e.act.all()[2]; ev.UserID == nil || *ev.UserID != 7 {
		t.Fatalf("stopped event = %+v", ev)
	}
	if hasTitlePrefix(e.cards.all(), "✅") || hasTitlePrefix(e.cards.all(), "⚠️") {
		t.Fatalf("cards = %v", e.cards.titles())
	}
}

func TestRanCardMetadata(t *testing.T) {
	c := RanCard{HouseholdID: hh, State: "running", WorkflowID: "wf_1", PlanID: "pl_1", SourceKind: "errand", SourceLabel: "Errand",
		Revision: 1,
		Steps: []RanStep{{Label: "Check weather", Command: "get_weather", Status: "ok", Result: "sunny"},
			{Label: "Search the web", Command: "quick_search", Status: "pending"}},
		Rules: []RanRule{
			RanRuleOf("get_weather", policy.Verdict{Effect: policy.Allow, Scope: policy.ScopeHousehold, Reason: "household rule",
				Rule: &policy.Rule{ID: 4, Note: "fine any time"}}),
			RanRuleOf("quick_search", allowTool),
		}}
	want := `{"card_type":"autonomy_ran","household_id":"hh-1","interactive_elements":[` +
		`{"callback":"stop_errand","command":"errand","data":{"revision":1,"workflow_id":"wf_1"},"id":"stop-errand","label":"Stop","target":"server"}],` +
		`"plan_id":"pl_1","rules":[` +
		`{"effect":"allow","note":"fine any time","reason":"household rule","rule_id":4,"scope":"household","tool":"get_weather"},` +
		`{"effect":"allow","note":"","reason":"the tool declares run_without_confirmation","rule_id":null,"scope":"tool","tool":"quick_search"}],` +
		`"source":{"kind":"errand","label":"Errand"},"state":"running","steps":[` +
		`{"command":"get_weather","label":"Check weather","result":"sunny","status":"ok"},` +
		`{"command":"quick_search","label":"Search the web","result":"","status":"pending"}],"workflow_id":"wf_1"}`
	if got := mustJSON(RanCardMetadata(c)); got != want {
		t.Fatalf("metadata\n got %s\nwant %s", got, want)
	}
	body := "I ran this on its own because your household's rules allow it.\n\n1. ✅ Check weather — sunny\n2. ⏳ Search the web\n\n" +
		"Allowed by:\n- get_weather: household rule — “fine any time”\n- quick_search: the tool declares run_without_confirmation"
	if got := RanCardBody(c); got != body {
		t.Fatalf("body = %q", got)
	}
	// Finished: no Stop; an automation has no ids; empty lists stay lists.
	done := mustJSON(RanCardMetadata(RanCard{HouseholdID: hh, State: "done", SourceKind: "automation", SourceLabel: "I leave home"}))
	if done != `{"card_type":"autonomy_ran","household_id":"hh-1","rules":[],"source":{"kind":"automation","label":"I leave home"},"state":"done","steps":[]}` {
		t.Fatal(done)
	}
}

func TestAutorunStopWhileWaiting(t *testing.T) {
	e, _ := autorunEnv(t)
	e.policy.set("get_weather", allowTool)
	now := dbTime(time.Now())
	if _, err := e.d.Write.Exec(`INSERT INTO cc_workflows (id, kind, household_id, user_id, node_id, goal, title, steps, cursor,
		results_json, revision, state, waiting_on, inbox_item_id, created_at, updated_at)
		VALUES ('wf_w', ?, ?, 7, 'node-1', 'weather', 'Weather', ?, 0, '[]', 1, 'waiting', NULL, 'item-9', ?, ?)`,
		KindAutorun, hh, stepsDumps([]Step{{Command: "get_weather", Args: pyjson.NewObject(), Label: "Weather"}}), now, now); err != nil {
		t.Fatal(err)
	}
	r := e.tap(CallbackStop, map[string]any{"workflow_id": "wf_w"})
	if ib, _ := r.ContextData["inbox"].(map[string]any); !r.Success || ib["title"] != "🗑️ Errand stopped" {
		t.Fatalf("stop = %+v", r)
	}
	if e.wfState("wf_w") != "cancelled" {
		t.Fatal(e.wfState("wf_w"))
	}
	up, ok := e.cards.update("item-9")
	if !ok || up.Metadata["state"] != "stopped" || strings.Join(stepStatuses(t, up.Metadata), ",") != "pending" {
		t.Fatalf("card = %+v", up)
	}
	if got := outcomes(e.act.all()); len(got) != 1 || got[0] != policy.OutcomeStopped {
		t.Fatalf("events = %v", got)
	}
}
