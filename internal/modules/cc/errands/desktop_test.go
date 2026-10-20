package errands

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/policy"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// Desktop computer tasks (plan P3 piece 22): attended (C9), and paused while the user uses the
// computer.

func desktopStep(goal string) Step {
	args := pyjson.NewObject()
	args.Set("goal", goal)
	args.Set("target", "desktop")
	args.Set("app", "Notes")
	return Step{Command: policy.ToolComputerTask, Args: args, Label: "Add eggs to the note"}
}

func TestPausedParksWithCard(t *testing.T) {
	c := newComputerEnv(t)
	c.comp.pausedAt = 1
	act := `{"thought": "x", "actions": [{"kind": "press", "ref": "e5", "intent": "Open the note"}]}`
	c.llm.say("computer", act, act, doneReply)
	c.launchRun("wf_d", "errand", desktopStep("add eggs to the groceries note"))
	c.idle()
	if w := c.wf("wf_d"); w.State != "waiting" || w.WaitingOn != signalApproval {
		t.Fatalf("workflow %s/%s, want parked on the card", w.State, w.WaitingOn)
	}
	card, ok := c.cardTitled(pausedTitlePrefix)
	if !ok || !card.Push {
		t.Fatalf("cards = %v", c.cards.titles())
	}
	if !strings.Contains(card.Summary, "Paused because you used the computer") ||
		!strings.Contains(card.Body, "Paused because you used the computer") || strings.Contains(card.Body, "answer") {
		t.Fatalf("card = %q / %q", card.Summary, card.Body)
	}
	if _, ok := card.Metadata["editable_fields"]; ok {
		t.Fatal("the paused card asks for an answer")
	}
	if got := strings.Join(elementLabels(card.Metadata), ","); got != "Continue,Stop" {
		t.Fatalf("buttons = %s", got)
	}
	els := card.Metadata["interactive_elements"].([]any)
	if cb := els[1].(map[string]any)["callback"]; cb != CallbackStop {
		t.Fatalf("Stop's callback = %v", cb)
	}
	if r := c.tap(CallbackAnswerStep, els[0].(map[string]any)["data"].(map[string]any)); !r.Success {
		t.Fatalf("continue = %+v", r)
	}
	c.idle()
	if w := c.wf("wf_d"); w.State != "done" {
		t.Fatalf("state = %s %+v", w.State, w.Results)
	}
	b := c.comp.acted()
	if len(b) != 2 || b[0].Resume || !b[1].Resume || b[1].Target.Kind != "desktop" {
		t.Fatalf("batches = %+v, want the one after Continue to resume", b)
	}
	// Continue isn't an answer to a question: the model isn't told one.
	reqs := c.llm.requests("computer")
	if txt := reqs[1].Messages[1].Content.Parts[0].Text; strings.Contains(txt, "The user answered") {
		t.Fatalf("the resumed round says the user answered:\n%s", txt)
	}
}

func TestDesktopNeedsAttended(t *testing.T) {
	plan := planJSON("Add eggs", step(policy.ToolComputerTask, "Add eggs to the note",
		`{"goal": "add eggs to the groceries note", "target": "desktop", "app": "Notes"}`))
	menu := []MenuEntry{computerEntry()}
	draft := func(c *cenv, r DraftRequest) *planRow {
		c.t.Helper()
		r.HouseholdID, r.Goal, r.Menu = hh, "add eggs to the groceries note", menu
		if err := c.s.DraftErrand(context.Background(), r); err != nil {
			c.t.Fatal(err)
		}
		c.idle()
		return c.onlyPlan()
	}
	setup := func(t *testing.T) *cenv {
		c := newComputerEnv(t)
		c.set[SettingAutonomous] = true
		c.policy.set(policy.ToolComputerTask, policy.Verdict{Effect: policy.Allow, Reason: "household rule"})
		c.llm.say("plan", plan)
		c.llm.say("computer", doneReply)
		return c
	}

	// A schedule fired it: nobody is at the computer, so it waits for a tap on its plan card.
	c := setup(t)
	p := draft(c, DraftRequest{NodeID: "comp-1", UserID: ptr(7)})
	if p.State != "draft" || len(c.comp.acted()) != 0 {
		t.Fatalf("scheduled: plan %s, batches %d, cards %v", p.State, len(c.comp.acted()), c.cards.titles())
	}
	if titles := c.cards.titles(); len(titles) != 1 || !strings.HasPrefix(titles[0], planTitlePrefix) {
		t.Fatalf("scheduled: cards %v, want the plan card", titles)
	}

	// Asked for on another device (the kitchen speaker): not at that computer either.
	c = setup(t)
	p = draft(c, DraftRequest{NodeID: "node-1", UserID: ptr(7), TurnAt: time.Now()})
	if p.State != "draft" {
		t.Fatalf("asked elsewhere: plan %s", p.State)
	}

	// Asked for at the computer a moment ago: it runs on its own, on that computer.
	c = setup(t)
	p = draft(c, DraftRequest{NodeID: "comp-1", UserID: ptr(7), TurnAt: time.Now()})
	if p.State != "launched" {
		t.Fatalf("live: plan %s, cards %v", p.State, c.cards.titles())
	}
	if w := c.wf(p.WorkflowID); w.Kind != KindAutorun || w.State != "done" {
		t.Fatalf("live: workflow %s %s", w.Kind, w.State)
	}
	run, err := c.runs.ForStep(context.Background(), p.WorkflowID, 0)
	if err != nil || run.NodeID != "comp-1" || run.Target != "desktop" {
		t.Fatalf("live: run %+v, %v", run, err)
	}

	// Asked for long ago (the plan job waited): not attended any more.
	c = setup(t)
	p = draft(c, DraftRequest{NodeID: "comp-1", UserID: ptr(7), TurnAt: time.Now().Add(-time.Hour)})
	if p.State != "draft" {
		t.Fatalf("stale turn: plan %s", p.State)
	}
}

// With computer_task on the menu, the planner is told that work inside a desktop app is one
// computer_task, not desktop_open and desktop_type_text (a 9B planner chose those for "in the
// notes app, make a note"); without it the prompts are as they were.
func TestPlannerDesktopAppRule(t *testing.T) {
	menu := []MenuEntry{computerEntry()}
	for _, p := range []string{BuildPrompt("g", menu), BuildRefinePrompt("s", nil, "i", menu), BuildReplanPrompt("g", nil, "p", "r", menu)} {
		if !strings.Contains(p, desktopAppRule) {
			t.Fatalf("prompt lacks the desktop app rule:\n%s", p)
		}
	}
	if p := BuildPrompt("g", commandMenu()); strings.Contains(p, "DESKTOP APPS") {
		t.Fatal("desktop app rule without computer_task")
	}
}
