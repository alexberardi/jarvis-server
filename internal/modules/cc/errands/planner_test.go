package errands

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

func mustObj(t *testing.T, s string) *pyjson.Object {
	t.Helper()
	v, err := pyjson.Loads(s)
	if err != nil {
		t.Fatal(err)
	}
	return v.(*pyjson.Object)
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestNodeMenuDenyListAndArgs(t *testing.T) {
	cmds := []*pyjson.Object{
		mustObj(t, `{"command_name": "get_weather", "description": "Weather", "parameters": [{"name": "resolved_datetimes", "description": "days"}, {"name": "units", "type": "string"}, {"description": "nameless"}], "is_risky": false}`),
		mustObj(t, `{"command_name": "tell_joke", "description": "joke"}`),
		mustObj(t, `{"command_name": "run_errand"}`),
		mustObj(t, `{"command_name": "  "}`),
		mustObj(t, `{"command_name": "order_pizza", "is_risky": true}`),
	}
	menu := NodeMenu(cmds)
	if len(menu) != 2 || menu[0].Command != "get_weather" || menu[1].Command != "order_pizza" {
		t.Fatalf("menu = %+v", menu)
	}
	if got := pyjson.Dumps(menu[0].Args, true); got != `{"resolved_datetimes": "days", "units": "string"}` {
		t.Fatalf("args = %s", got)
	}
	if !menu[1].IsRisky || menu[1].Description != "" {
		t.Fatalf("risky entry = %+v", menu[1])
	}
}

func TestFullMenuGatesAndServerWins(t *testing.T) {
	e := newTenv(t, false)
	e.tools.Register(&fakeTool{name: "make_phone_call", def: toolDef("make_phone_call", "Call a business",
		"business", mustObj(t, `{"type": "string"}`), "goal", mustObj(t, `{"type": "string"}`))})
	e.tools.Register(&fakeTool{name: "quick_search", def: toolDef("quick_search", "Search", "query", mustObj(t, `{"type": "string", "description": "what"}`))})
	e.tools.Register(&fakeTool{name: "remember", def: toolDef("remember", "Remember", "content", mustObj(t, `{"type": "string"}`))})
	node := []*pyjson.Object{mustObj(t, `{"command_name": "quick_search", "description": "node search"}`),
		mustObj(t, `{"command_name": "get_news", "description": "News"}`)}
	ctx := context.Background()

	names := func(m []MenuEntry) string {
		var out []string
		for _, c := range m {
			out = append(out, c.Command)
		}
		return strings.Join(out, ",")
	}
	// Defaults: phone off, web search off, no speaker → only the node commands.
	if got := names(e.s.FullMenu(ctx, node, hh, nil)); got != "quick_search,get_news" {
		t.Fatalf("default menu = %s", got)
	}
	e.phone.enabled = true
	e.set[settingWebSearch] = true
	m := e.s.FullMenu(ctx, node, hh, ptr(7))
	// The server quick_search wins over the node's; recall is not registered (skipped).
	if got := names(m); got != "get_news,make_phone_call,quick_search,remember" {
		t.Fatalf("menu = %s", got)
	}
	if !m[1].IsRisky || m[2].IsRisky {
		t.Fatalf("risk flags: %+v", m)
	}
	if got := pyjson.Dumps(m[2].Args, true); got != `{"query": "what"}` {
		t.Fatalf("server args = %s", got)
	}
	// Memory needs memory.enabled.
	e.set[settingMemoryEnabled] = false
	if got := names(e.s.FullMenu(ctx, nil, hh, ptr(7))); got != "make_phone_call,quick_search" {
		t.Fatalf("memory off = %s", got)
	}
}

func TestBuildPromptBytes(t *testing.T) {
	p := BuildPrompt("check the weather", commandMenu()[:1])
	for _, want := range []string{
		"You are Jarvis's errand planner. Turn the user's goal into an ordered list of steps, choosing ONLY from the available commands below.",
		"Create a SEPARATE step for EACH distinct action",
		`with a single checkpoint step {"command": "request_replan", "args": {"reason": "<what to decide once the earlier steps have run>"}, "label": "<short label>"} and DO NOT`,
		"Available commands:\n- get_weather: Weather for the user's location on a given day. | args: {\"resolved_datetimes\": \"list of day keywords, e.g. [\\\"today\\\"]\"}\n\nUser's goal: check the weather\n\nDo NOT invent",
		"No markdown.\n\nThink step by step: list each action the user wants and its order, then output ONLY the final JSON object and nothing after it.",
	} {
		if !strings.Contains(p, want) {
			t.Fatalf("prompt lacks %q:\n%s", want, p)
		}
	}
	if !strings.HasSuffix(p, "nothing after it.") {
		t.Fatal("closing directive is not last")
	}
	// Non-ASCII goes through json.dumps(ensure_ascii) in the menu.
	m := []MenuEntry{{Command: "x", Description: "d", Args: mustObj(t, `{"a": "café"}`)}}
	if !strings.Contains(BuildPrompt("g", m), `| args: {"a": "caf\u00e9"}`) {
		t.Fatal("menu args not ensure_ascii")
	}
}

func TestPlanValidatesAndStripsThink(t *testing.T) {
	e := newTenv(t, false)
	e.llm.say("plan", "<think>reasoning {not json}</think>\n```json\n"+planJSON("Weather then joke",
		step("get_weather", "Check weather", `{"resolved_datetimes": ["today"]}`),
		step("launch_rockets", "Bad", ""),
		`{"command": "request_replan", "args": {"reason": "if rain"}}`)+"\n```")
	plan, err := e.s.PlanErrand(context.Background(), "goal", nil) // fallback menu
	if err != nil {
		t.Fatal(err)
	}
	if plan.Summary != "Weather then joke" || len(plan.Steps) != 2 {
		t.Fatalf("plan = %+v", plan)
	}
	if plan.Steps[1].Command != "request_replan" || plan.Steps[1].Label != "request_replan" || plan.Steps[1].IsRisky {
		t.Fatalf("checkpoint = %+v", plan.Steps[1])
	}
	req := e.llm.requests("plan")[0]
	if req.Label != "background" || *req.Temperature != 0 || *req.MaxTokens != 6000 || req.ReasoningBudget != nil {
		t.Fatalf("planner request = %+v", req)
	}

	// Thinking ran out the budget (jarvis-dev, Qwen3-8B): one retry without thinking.
	n := len(e.llm.requests("plan"))
	e.llm.say("plan", thoughtOut, planJSON("Weather then timer", step("get_weather", "Weather", "")))
	if plan, err := e.s.PlanErrand(context.Background(), "goal", nil); err != nil || plan.Summary != "Weather then timer" {
		t.Fatalf("retry: %+v %v", plan, err)
	}
	reqs := e.llm.requests("plan")[n:]
	if len(reqs) != 2 || reqs[0].ReasoningBudget != nil || reqs[1].ReasoningBudget == nil || *reqs[1].ReasoningBudget != 0 {
		t.Fatalf("retry requests: %+v", reqs)
	}
	e.llm.say("plan", thoughtOut, thoughtOut)
	if _, err := e.s.PlanErrand(context.Background(), "goal", nil); !errors.Is(err, ErrPlan) {
		t.Fatalf("both thought out: %v", err)
	}

	for _, bad := range []string{"", "not json at all", planJSON("x", step("nope", "n", ""))} {
		e.llm.say("plan", bad)
		if _, err := e.s.PlanErrand(context.Background(), "goal", nil); !errors.Is(err, ErrPlan) {
			t.Fatalf("%q: err = %v", bad, err)
		}
	}
}

func TestRefineKeepsCurrentCommands(t *testing.T) {
	e := newTenv(t, false)
	current := []Step{{Command: "custom_cmd", Args: mustObj(t, `{"a": 1}`), Label: "Custom"}}
	e.llm.say("refine", planJSON("Revised", step("custom_cmd", "Custom", `{"a": 2}`), step("get_news", "News", "")))
	plan, err := e.s.RefinePlan(context.Background(), "Old", current, "add news", nil)
	if err != nil || len(plan.Steps) != 2 {
		t.Fatalf("plan = %+v, %v", plan, err)
	}
	p := *e.llm.requests("refine")[0].Messages[0].Content.Text
	if !strings.Contains(p, `Current plan: Old
Current steps: [{"command": "custom_cmd", "args": {"a": 1}, "label": "Custom", "is_risky": false}]`) ||
		!strings.Contains(p, "The user wants this change: add news\n\n") {
		t.Fatalf("refine prompt:\n%s", p)
	}
}

func TestReplanMayBeEmptyAndSeesResults(t *testing.T) {
	e := newTenv(t, false)
	e.llm.say("replan", `{"summary": "", "steps": []}`)
	done := []Step{{Command: "get_weather", Args: pyjson.NewObject(), Label: "Weather"}, {Command: "noop", Args: pyjson.NewObject()}}
	results := []Result{{Command: "get_weather", Success: true, Data: mustObj(t, `{"success": true, "forecast": "rain", "pct": 90, "hourly": [1, 2]}`)}}
	plan, err := e.s.ReplanFromProgress(context.Background(), "goal", done, results, "if rain", nil)
	if err != nil || len(plan.Steps) != 0 || plan.Summary != "continue" {
		t.Fatalf("plan = %+v, %v", plan, err)
	}
	p := *e.llm.requests("replan")[0].Messages[0].Content.Text
	if !strings.Contains(p, "What happened:\n- Weather: forecast=rain; pct=90; hourly=[1, 2]\n- noop: no result\n\nWhy we paused to re-plan: if rain\n\n") {
		t.Fatalf("replan prompt:\n%s", p)
	}
}

func TestWidensEnvelopeTruthTable(t *testing.T) {
	call := func(biz string, risky bool) Step {
		return Step{Command: "make_phone_call", Args: mustObj(t, `{"business": "`+biz+`"}`), IsRisky: risky}
	}
	w := Step{Command: "get_weather", Args: pyjson.NewObject()}
	cases := []struct {
		name              string
		approved, amended []Step
		want              bool
	}{
		{"identical", []Step{w}, []Step{w}, false},
		{"removal", []Step{w, call("A", true)}, []Step{w}, false},
		{"new capability", []Step{w}, []Step{w, {Command: "get_news", Args: pyjson.NewObject()}}, true},
		{"new counterparty", []Step{call("A", false)}, []Step{call("A", false), call("B", false)}, true},
		{"same counterparty", []Step{call("A", false)}, []Step{call(" A ", false), call("A", false)}, false},
		{"extra risky to known party", []Step{call("A", true)}, []Step{call("A", true), call("A", true)}, true},
		{"reorder", []Step{w, call("A", true)}, []Step{call("A", true), w}, false},
	}
	for _, c := range cases {
		if got := WidensEnvelope(c.approved, c.amended); got != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
}

func TestAutorunGateDormant(t *testing.T) {
	ok, _ := IsAutorunEligible([]Step{{Command: "get_drive_time", Args: pyjson.NewObject()}, {Command: "reminder", Args: pyjson.NewObject()}})
	if !ok {
		t.Fatal("allowlisted plan not eligible")
	}
	for _, steps := range [][]Step{
		nil,
		{{Command: "get_weather", Args: pyjson.NewObject()}},
		{{Command: "reminder", Args: pyjson.NewObject(), IsRisky: true}},
		{{Command: "reminder", Args: mustObj(t, `{"business": "X"}`)}},
		{{Command: "reminder"}, {Command: "reminder"}, {Command: "reminder"}, {Command: "reminder"}},
	} {
		if ok, _ := IsAutorunEligible(steps); ok {
			t.Fatalf("%+v eligible", steps)
		}
	}
}

func TestResolveStepArgs(t *testing.T) {
	prior := []Result{{Command: "get_drive_time", Success: true, Data: mustObj(t, `{"duration_minutes": 20, "eta": "x"}`)}}
	now := mustTime(t, "2026-10-07T12:00:00Z")
	args := mustObj(t, `{"text": "leave", "relative_minutes": {"$leave_by": {"event_start": "2026-10-07T13:00:00+00:00", "drive_from_step": 0, "buffer_minutes": 5}}, "eta": {"$from_step": {"step": 0, "field": "eta"}}}`)
	out, skip := ResolveStepArgs(args, prior, now)
	if skip != "" || pyjson.Dumps(out, true) != `{"text": "leave", "relative_minutes": 35, "eta": "x"}` {
		t.Fatalf("out = %s skip=%q", pyjson.Dumps(out, true), skip)
	}
	late := mustObj(t, `{"m": {"$leave_by": {"event_start": "2026-10-07T12:10:00Z", "drive_from_step": 0}}}`)
	if _, skip := ResolveStepArgs(late, prior, now); !strings.HasPrefix(skip, "departure time already passed") {
		t.Fatalf("skip = %q", skip)
	}
}
