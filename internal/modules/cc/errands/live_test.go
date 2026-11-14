package errands

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/computer"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/policy"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
)

// Asked by voice at the computer, work in an app is done in the app's window at once; typed in
// chat it keeps its plan card (user, 2026-11-13: "write a note in LibreOffice and save it"
// became a write_document plan waiting on the phone; they expected to watch it happen).
func TestVoiceAtTheComputerWorksInTheApp(t *testing.T) {
	goal := "write a note in LibreOffice to walk the dogs and save it"
	plan := planJSON("Write the note", step(policy.ToolComputerTask, "Write and save the note",
		`{"goal": "write a note to walk the dogs and save it", "target": "desktop", "app": "LibreOffice Writer"}`))
	menu := []MenuEntry{computerEntry(), {Command: cmdWriteDoc, Description: "Write a document file"}}
	draft := func(r DraftRequest) (*cenv, *planRow, string) {
		t.Helper()
		c := newComputerEnv(t)
		c.llm.say("plan", plan)
		c.llm.say("computer", doneReply)
		r.HouseholdID, r.Goal, r.Menu, r.UserID = hh, goal, menu, ptr(7)
		if err := c.s.DraftErrand(context.Background(), r); err != nil {
			t.Fatal(err)
		}
		c.idle()
		return c, c.onlyPlan(), *c.llm.requests("plan")[0].Messages[0].Content.Text
	}

	// By voice at the computer: the planner isn't offered the file tool, the task starts with
	// no plan card, on that computer, as a desktop task. Autonomy is off: no rules were needed.
	c, p, prompt := draft(DraftRequest{NodeID: "comp-1", TurnAt: time.Now(), Live: true})
	if !strings.Contains(prompt, "IN AN APP:") || strings.Contains(prompt, "DOCUMENTS:") || strings.Contains(prompt, "- "+cmdWriteDoc) {
		t.Fatalf("live prompt offers the file tool or lacks the rule:\n%s", prompt)
	}
	if p.State != "launched" {
		t.Fatalf("live: plan %s, cards %v", p.State, c.cards.titles())
	}
	for _, title := range c.cards.titles() {
		if strings.HasPrefix(title, planTitlePrefix) {
			t.Fatalf("live: a plan card was posted: %v", c.cards.titles())
		}
	}
	run, err := c.runs.ForStep(context.Background(), p.WorkflowID, 0)
	if err != nil || run.NodeID != "comp-1" || run.Target != "desktop" {
		t.Fatalf("live: run %+v, %v", run, err)
	}

	// Typed in chat at the same computer: the documents rule and a plan card, as before.
	c, p, prompt = draft(DraftRequest{NodeID: "comp-1", TurnAt: time.Now()})
	if !strings.Contains(prompt, "DOCUMENTS:") || strings.Contains(prompt, "IN AN APP:") {
		t.Fatalf("chat prompt:\n%s", prompt)
	}
	if p.State != "draft" || len(c.comp.acted()) != 0 {
		t.Fatalf("chat: plan %s, batches %d", p.State, len(c.comp.acted()))
	}

	// Spoken to another device, or long ago: nobody is known to be at the computer.
	for name, r := range map[string]DraftRequest{
		"another device": {NodeID: "node-1", TurnAt: time.Now(), Live: true},
		"stale turn":     {NodeID: "comp-1", TurnAt: time.Now().Add(-time.Hour), Live: true},
	} {
		if _, p, prompt := draft(r); p.State != "draft" || strings.Contains(prompt, "IN AN APP:") {
			t.Fatalf("%s: plan %s", name, p.State)
		}
	}
}

// A live plan that isn't only a computer task (the planner added something else) still asks.
func TestLiveMixedPlanStillAsks(t *testing.T) {
	c := newComputerEnv(t)
	c.llm.say("plan", planJSON("Note then timer",
		step(policy.ToolComputerTask, "Write the note", `{"goal": "write a note", "target": "desktop", "app": "Notes"}`),
		step("set_timer", "Set a timer", `{"duration_seconds": 60}`)))
	menu := []MenuEntry{computerEntry(), {Command: "set_timer", Description: "Set a timer"}}
	if err := c.s.DraftErrand(context.Background(), DraftRequest{HouseholdID: hh, Goal: "write a note and set a timer",
		Menu: menu, UserID: ptr(7), NodeID: "comp-1", TurnAt: time.Now(), Live: true}); err != nil {
		t.Fatal(err)
	}
	c.idle()
	if p := c.onlyPlan(); p.State != "draft" || len(c.comp.acted()) != 0 {
		t.Fatalf("plan %s, batches %d", p.State, len(c.comp.acted()))
	}
}

// A desktop task that writes from "this page" gets the page open in that computer's browser.
func TestDesktopTaskReadsTheOpenPage(t *testing.T) {
	c := newComputerEnv(t)
	var asked []string
	c.s.Computer.OpenPage = func(_ context.Context, _ string, nodeID string, _ *int64, refs ...string) (computer.Source, bool, error) {
		asked = append(asked, nodeID)
		for _, r := range refs {
			if strings.Contains(r, "this page") || r == "the open page" {
				return computer.Source{URL: "https://example.org/wiki/Velmora", Text: "Velmora is a town on Lake Orrin."}, true, nil
			}
		}
		return computer.Source{}, false, nil
	}
	c.s.Computer.Source = func(context.Context, string, string) (computer.Source, error) {
		t.Fatal("searched for the open page")
		return computer.Source{}, nil
	}
	c.llm.say("computer", doneReply)
	st := desktopStep("summarize this page into a document called Velmora")
	st.Args.Set("source", "the open page")
	c.launchRun("wf_p", "errand", st)
	c.idle()
	if w := c.wf("wf_p"); w.State != "done" {
		t.Fatalf("state %s %+v", w.State, w.Results)
	}
	if len(asked) != 1 || asked[0] != "comp-1" {
		t.Fatalf("open page asked of %v", asked)
	}
	txt := c.llm.requests("computer")[0].Messages[1].Content.Parts[0].Text
	if !strings.Contains(txt, "Lake Orrin") || !strings.Contains(txt, "example.org/wiki/Velmora") {
		t.Fatalf("the task's prompt lacks the page:\n%s", txt)
	}
}

// run_errand on a turn spoken at a computer drafts a live plan and says it starts on screen; the
// same goal from a chat or a speaker keeps the plan-card ack and isn't live.
func TestRunErrandMarksVoiceAtTheComputerLive(t *testing.T) {
	c := newComputerEnv(t)
	tool := &RunErrandTool{Svc: func() *Service { return c.s }}
	args := mustObj(t, `{"goal": "write a note in LibreOffice to walk the dogs and save it"}`)
	payloads := func() []string {
		rows, err := c.d.Read.Query(`SELECT payload FROM platform_jobs WHERE type = ? ORDER BY id`, JobPlan)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var p string
			_ = rows.Scan(&p)
			out = append(out, p)
		}
		return out
	}
	turn := turnFor(7)
	got := res(tool.Execute(context.Background(), servertools.Call{Args: args}, turn))
	if strings.Contains(got, "on your screen now") {
		t.Fatalf("chat ack: %s", got)
	}
	turn.DesktopVoice = true
	got = res(tool.Execute(context.Background(), servertools.Call{Args: args}, turn))
	if !strings.Contains(got, "on your screen now") {
		t.Fatalf("voice ack: %s", got)
	}
	p := payloads()
	if len(p) != 2 || strings.Contains(p[0], `"live"`) || !strings.Contains(p[1], `"live":true`) {
		t.Fatalf("payloads %v", p)
	}
}

// A document step planned for work in an app the goal names is a task in that app (suite on
// Qwen3.5-9B, 2026-11-13: "make a note in the Notes app" was planned as write_document and
// failed as "I can write .odt … not plain text notes").
func TestAppWorkIsNotADocument(t *testing.T) {
	menu := []MenuEntry{computerEntry(), {Command: cmdWriteDoc}}
	doc := Step{Command: cmdWriteDoc, Label: "Create Groceries note", Args: servertools.Obj("path", "Groceries", "instructions", "milk, eggs, bread")}
	goal := "In the Jarvis Test Notes app, make a new note titled Groceries with milk, eggs and bread in it, and save it."
	got := appWorkNotDocument(goal, []Step{doc}, menu)
	if len(got) != 1 || got[0].Command != cmdComputer || truthyStr(got[0].Args, "target") != "desktop" ||
		truthyStr(got[0].Args, "app") != "Jarvis Test Notes" || truthyStr(got[0].Args, "goal") != goal || got[0].Label != doc.Label {
		t.Fatalf("got %+v", got)
	}
	// Planned as a to-do item (or a memory): nothing in the plan works in the app, so the task does.
	todo := Step{Command: "todo_list", Label: "Add note to Jarvis Test Notes", Args: servertools.Obj("action", "add", "tasks", "Plumber: Thursday at 10am")}
	got = appWorkNotDocument("Add a note titled Plumber with the time Thursday at 10am in the Jarvis Test Notes app.", []Step{todo}, menu)
	if len(got) != 1 || got[0].Command != cmdComputer || truthyStr(got[0].Args, "app") != "Jarvis Test Notes" {
		t.Fatalf("todo plan: %+v", got)
	}
	// Nothing is made in the app: another command may well be how it's done.
	play := Step{Command: "music", Args: servertools.Obj("action", "play", "query", "jazz")}
	if got := appWorkNotDocument("play some jazz in the Spotify app", []Step{play}, menu); len(got) != 1 || got[0].Command != "music" {
		t.Fatalf("a play request: %+v", got)
	}
	for name, c := range map[string]struct {
		goal string
		menu []MenuEntry
	}{
		"a document":           {"Summarize this page into a LibreOffice document called Velmora", menu},
		"an office app":        {"In the Writer app, write a letter to the plumber and save it", menu},
		"no app named":         {"make a note titled Groceries and save it", menu},
		"no computer on offer": {goal, []MenuEntry{{Command: cmdWriteDoc}}},
	} {
		if got := appWorkNotDocument(c.goal, []Step{doc}, c.menu); len(got) != 1 || got[0].Command != cmdWriteDoc {
			t.Errorf("%s: %+v", name, got)
		}
	}
}

// A task whose goal names a relative date is given the date worked out (suite on Qwen3.5-9B,
// 2026-11-13: told only today's date, it took "last month" for the month it was in).
func TestComputerTaskGetsItsDatesWorkedOut(t *testing.T) {
	c := newComputerEnv(t)
	c.s.Now = func() time.Time { return time.Date(2026, 11, 13, 21, 0, 0, 0, time.UTC) }
	c.llm.say("computer", doneReply)
	c.launchRun("wf_dt", "errand", computerStep("download last month's statement"))
	c.idle()
	txt := c.llm.requests("computer")[0].Messages[1].Content.Parts[0].Text
	if !strings.Contains(txt, "last month = October 2026 (2026-10-01 to 2026-10-31)") {
		t.Fatalf("the prompt doesn't work the date out:\n%s", txt[:min(len(txt), 600)])
	}
	c2 := newComputerEnv(t)
	c2.llm.say("computer", doneReply)
	c2.launchRun("wf_nd", "errand", computerStep("order the blue mug"))
	c2.idle()
	if txt := c2.llm.requests("computer")[0].Messages[1].Content.Parts[0].Text; strings.Contains(txt, "Dates in the task") {
		t.Fatal("a goal with no dates got a dates line")
	}
}
