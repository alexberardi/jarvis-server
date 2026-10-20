package errands

import (
	"context"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/computer"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// Taught workflows in errands (plan P3 piece 31): the planner's run_workflow and the
// computer_task that carries a taught workflow both replay it with computer.Loop.Replay.

type fakeTaught struct {
	ws    []computer.Workflow
	asked []*int64
}

func (f *fakeTaught) Visible(_ context.Context, _ string, uid *int64) ([]computer.Workflow, error) {
	f.asked = append(f.asked, uid)
	return f.ws, nil
}

// cartWorkflow opens the shop for a {month} and presses Add to cart.
func cartWorkflow() computer.Workflow {
	return computer.Workflow{ID: "tw_1", Name: "Bank statement", Slug: "bank_statement", Version: 1,
		Triggers: []string{"download my bank statement"},
		Params:   []computer.WorkflowParam{{Name: "month", Kind: "month"}},
		Steps: []computer.WorkflowStep{
			{Intent: "Open the statements for {month}", Target: "browser", Site: shopSite,
				Action: computeruse.Action{Kind: "navigate", Intent: "Open the statements for {month}", URL: "https://" + shopSite + "/checkout?month={month}"}},
			{Intent: "Download the CSV", Target: "browser", Site: shopSite,
				Action:  computeruse.Action{Kind: "press", Intent: "Download the CSV", Ref: computer.RecordedRef},
				Element: &computer.RecordedElement{Role: "button", Name: "Add to cart"}},
		},
		Success: computer.SuccessCheck{URLContains: "/checkout"},
	}
}

func (c *cenv) taught(ws ...computer.Workflow) *fakeTaught {
	f := &fakeTaught{ws: ws}
	c.s.Computer.Taught = f
	return f
}

func TestScheduleRunsWorkflow(t *testing.T) {
	c := newComputerEnv(t)
	c.taught(cartWorkflow(), computer.Workflow{ID: "tw_2", Name: "Order ink", Steps: cartWorkflow().Steps[1:]})
	ctx := context.Background()

	// The planner's menu offers run_workflow with the workflows the speaker may run.
	var entry *MenuEntry
	for _, m := range c.s.FullMenu(ctx, nil, hh, ptr(7)) {
		if m.Command == cmdRunWorkflow {
			entry = &m
		}
	}
	if entry == nil || !strings.Contains(entry.Description, `"Bank statement" (params: month); "Order ink"`) {
		t.Fatalf("run_workflow entry %+v", entry)
	}

	// A schedule's fire drafts the goal (doc 08: DraftErrand without a menu); the planner sees
	// run_workflow and plans it.
	c.llm.say("plan", planJSON("Bank statement for October",
		step(cmdRunWorkflow, "Download the bank statement", `{"name": "bank statement", "params": {"month": "October 2026"}}`)))
	p := c.draft("run workflow bank statement with month October 2026", ptr(7), nil)
	if len(p.Steps) != 1 || p.Steps[0].Command != cmdRunWorkflow {
		t.Fatalf("plan %+v", p.Steps)
	}
	if reqs := c.llm.requests("plan"); len(reqs) != 1 || !strings.Contains(mustJSON(reqs[0].Messages), "run_workflow") {
		t.Fatal("the planner wasn't offered run_workflow")
	}

	// Run: the step replays the workflow target-first, with no model call, and checks it.
	if r := c.tap(CallbackApprove, map[string]any{"plan_id": p.ID, "revision": float64(p.Revision)}); !r.Success {
		t.Fatalf("approve %+v", r)
	}
	c.idle()
	p = c.onlyPlan()
	if st := c.wfState(p.WorkflowID); st != "done" {
		t.Fatalf("errand %s", st)
	}
	if n := len(c.llm.requests("computer")); n != 0 {
		t.Fatalf("model called %d times", n)
	}
	b := c.comp.acted()
	if len(b) != 2 || b[0].Actions[0].URL != "https://shop.example/checkout?month=October+2026" || b[1].Actions[0].Ref != "e3" {
		t.Fatalf("batches %+v", b)
	}
	res := c.wf(p.WorkflowID).Results
	if len(res) != 1 || !res[0].Success || !strings.HasPrefix(res[0].Message, `Done, checked: the address contains "/checkout". Ran "Bank statement".`) {
		t.Fatalf("results %+v", res)
	}
}

func TestStartReplaysTaughtWorkflow(t *testing.T) {
	c := newComputerEnv(t)
	f := c.taught(cartWorkflow())
	ctx := context.Background()
	id, err := c.s.Start(ctx, StartRequest{HouseholdID: hh, UserID: ptr(7), Goal: "Run “Bank statement”",
		Steps: []Step{ReplayStep(cartWorkflow(), map[string]string{"month": "May"})}})
	if err != nil {
		t.Fatal(err)
	}
	c.idle()
	w := c.wf(id)
	if w.State != "done" || w.Steps[0].Command != cmdComputer || truthyStr(w.Steps[0].Args, ArgTaughtWorkflow) != "tw_1" ||
		w.Steps[0].Label != "Run “Bank statement”" {
		t.Fatalf("errand %+v", w)
	}
	if u := f.asked[len(f.asked)-1]; u == nil || *u != 7 {
		t.Fatalf("looked up for %v", u)
	}
	if b := c.comp.acted(); len(b) != 2 || !strings.HasSuffix(b[0].Actions[0].URL, "month=May") {
		t.Fatalf("batches %+v", b)
	}

	// A workflow the user no longer sees, or a name that matches nothing or several, fails the
	// step and says why.
	for args, want := range map[string]string{
		`{"taught_workflow_id": "tw_gone", "params": {"month": "May"}}`: "That saved workflow is gone, or isn't shared with you.",
		`{"name": "pay rent"}`:       `There's no saved workflow called "pay rent".`,
		`{"name": "bank statement"}`: `I need the month to run "Bank statement".`,
	} {
		o, _ := pyjson.Loads(args)
		cmd := cmdRunWorkflow
		if strings.Contains(args, "taught") {
			cmd = cmdComputer
		}
		id, err := c.s.Start(ctx, StartRequest{HouseholdID: hh, UserID: ptr(7), Goal: "x",
			Steps: []Step{{Command: cmd, Args: o.(*pyjson.Object), Label: "x"}}})
		if err != nil {
			t.Fatal(err)
		}
		c.idle()
		if res := c.wf(id).Results; len(res) != 1 || res[0].Error != want {
			t.Errorf("%s: %+v", args, res)
		}
	}
	c.taught(cartWorkflow(), computer.Workflow{ID: "tw_3", Name: "Bank transfer"})
	id, _ = c.s.Start(ctx, StartRequest{HouseholdID: hh, UserID: ptr(7), Goal: "x",
		Steps: []Step{{Command: cmdRunWorkflow, Args: mustObjArgs(`{"name": "bank"}`), Label: "x"}}})
	c.idle()
	if res := c.wf(id).Results; len(res) != 1 || res[0].Error != `More than one saved workflow matches "bank": “Bank statement” or “Bank transfer”.` {
		t.Errorf("ambiguous: %+v", res)
	}
}

func mustObjArgs(s string) *pyjson.Object {
	o, _ := pyjson.Loads(s)
	return o.(*pyjson.Object)
}

func TestParamValues(t *testing.T) {
	for in, want := range map[string]string{
		`{"month": "October 2026", "n": 3, "x": null}`: "month=October 2026 n=3",
		`"{\"month\": \"May\"}"`:                       "month=May",
		`"May"`:                                        "",
	} {
		v, err := pyjson.Loads(in)
		if err != nil {
			t.Fatal(err)
		}
		got := ParamValues(v)
		var parts []string
		for _, k := range []string{"month", "n", "x"} {
			if s, ok := got[k]; ok {
				parts = append(parts, k+"="+s)
			}
		}
		if strings.Join(parts, " ") != want {
			t.Errorf("%s: %v", in, got)
		}
	}
}
