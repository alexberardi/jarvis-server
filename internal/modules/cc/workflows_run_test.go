package cc

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/computer"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/db"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
)

// Replaying taught workflows (plan P3 piece 31): the run_workflow voice tool, the Run button,
// the replay's heal store and routines' run_workflow steps.

// withErrandQueue gives the errand runner a queue (jobs are queued, not run).
func withErrandQueue(t *testing.T, e *env) {
	t.Helper()
	if e.m.deps.Queue == nil {
		if err := db.Migrate(context.Background(), e.d, queue.MigrationModule, queue.Migrations()); err != nil {
			t.Fatal(err)
		}
		e.m.deps.Queue = queue.New(e.d, e.m.deps.Log)
	}
	e.m.errands.Queue = e.m.deps.Queue
}

// seedTaught stores a workflow of hh owned by owner, with a month param, that has proved
// itself (a run of it has verified), as most tests' workflows have.
func seedTaught(t *testing.T, m *Module, hh string, owner int64, name string, shared bool, triggers ...string) TaughtWorkflow {
	t.Helper()
	w := seedUnproven(t, m, hh, owner, name, shared, triggers...)
	if _, err := m.deps.DB.Write.ExecContext(context.Background(), `UPDATE cc_taught_workflows SET proven = 1 WHERE id = ?`, w.ID); err != nil {
		t.Fatal(err)
	}
	w.Proven = true
	return w
}

// seedUnproven is a workflow as learning leaves it: never run.
func seedUnproven(t *testing.T, m *Module, hh string, owner int64, name string, shared bool, triggers ...string) TaughtWorkflow {
	t.Helper()
	w, err := m.createWorkflow(context.Background(), TaughtWorkflow{HouseholdID: hh, OwnerID: owner, Name: name, Shared: shared,
		Triggers: triggers, Params: []Param{{Name: "month", Kind: "month", Example: "September 2026"}},
		Steps: []WStep{{Intent: "Open the statements for {month}", Target: "browser", Site: "bank.example",
			Action:  computeruse.Action{Kind: "press", Intent: "Open the statements for {month}", Ref: computer.RecordedRef},
			Element: &RecordedElement{Role: "link", Name: "Statements", Selector: "#statements"}}},
		Success: computer.SuccessCheck{URLContains: "/statements"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// errandRows are the started errands: id → (user_id, steps JSON).
func errandRows(t *testing.T, e *env) map[string][2]string {
	t.Helper()
	rows, err := e.d.Read.Query(`SELECT id, COALESCE(user_id, ''), steps FROM cc_workflows`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string][2]string{}
	for rows.Next() {
		var id, uid, steps string
		if err := rows.Scan(&id, &uid, &steps); err != nil {
			t.Fatal(err)
		}
		out[id] = [2]string{uid, steps}
	}
	return out
}

func TestRunWorkflowByTrigger(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_8B)
	tool := &runWorkflowTool{m: ve.m}
	turn := ve.recTurn(7)

	// Offered only with a computer and a taught workflow in the household, and a known speaker.
	if prompts.ServerToolAllowed(toolRunWorkflow, prompts.ToolGates{SpeakerKnown: true, Workflows: true}) ||
		prompts.ServerToolAllowed(toolRunWorkflow, prompts.ToolGates{Computers: true, SpeakerKnown: true}) ||
		!prompts.ServerToolAllowed(toolRunWorkflow, prompts.ToolGates{Computers: true, SpeakerKnown: true, Workflows: true}) {
		t.Fatal("run_workflow gate")
	}
	if ve.m.hasWorkflows(context.Background(), voiceHH) {
		t.Fatal("hasWorkflows with none taught")
	}
	if got := runTool(t, tool, `{"name": "my bank statement"}`, turn); got["error"] != "no_workflows" {
		t.Fatalf("nothing taught: %v", got)
	}
	bank := seedTaught(t, ve.m, voiceHH, 7, "Bank statement", false, "download my bank statement")
	if !ve.m.hasWorkflows(context.Background(), voiceHH) || ve.m.hasWorkflows(context.Background(), "other-hh") {
		t.Fatal("hasWorkflows after one taught")
	}
	seedTaught(t, ve.m, voiceHH, 7, "Bank transfer", false)
	seedTaught(t, ve.m, voiceHH, 8, "Order ink", false) // another member's, not shared

	if got := runTool(t, tool, `{"name": "bank"}`, turn); got["status"] != "ask" || got["message"] != "Which one: “Bank statement” or “Bank transfer”?" {
		t.Fatalf("ambiguous: %v", got)
	}
	if got := runTool(t, tool, `{"name": "order ink"}`, turn); got["error"] != "not_found" ||
		got["message"] != `I don't have a saved workflow called "order ink". You have “Bank statement” and “Bank transfer”.` {
		t.Fatalf("not theirs: %v", got)
	}
	if got := runTool(t, tool, `{"name": "Download my bank statement"}`, turn); got["status"] != "ask" ||
		got["message"] != "Which month should I run “Bank statement” for (last time: September 2026)?" ||
		got["next"] != `Nothing has started yet. Ask the user this, then call run_workflow again with {"name":"Bank statement","params":{"month":"<their answer>"}}.` {
		t.Fatalf("missing month: %v", got)
	}
	// Computer control off: it says so, and starts nothing.
	if got := runTool(t, tool, `{"name": "Download my bank statement", "params": {"month": "October"}}`, turn); got["error"] != "computer_control_off" {
		t.Fatalf("no computer: %v", got)
	}

	ve.setComputerControl("on")
	withErrandQueue(t, ve.env)
	got := runTool(t, tool, `{"name": "Download my bank statement", "params": {"month": "October 2026"}}`, turn)
	if got["status"] != "started" || got["workflow"] != "Bank statement" {
		t.Fatalf("started: %v", got)
	}
	rows := errandRows(t, ve.env)
	if len(rows) != 1 {
		t.Fatalf("errands %v", rows)
	}
	for _, r := range rows {
		var steps []map[string]any
		if err := json.Unmarshal([]byte(r[1]), &steps); err != nil {
			t.Fatal(err)
		}
		args, _ := steps[0]["args"].(map[string]any)
		if r[0] != "7" || len(steps) != 1 || steps[0]["command"] != "computer_task" || args["taught_workflow_id"] != bank.ID ||
			args["params"].(map[string]any)["month"] != "October 2026" || args["workflow_id"] != nil {
			t.Fatalf("errand %v", r)
		}
	}
}

func TestRunWorkflowRoute(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_8B)
	admin := bearer(ve.auth.addUser(9, voiceHH, authn.RoleAdmin))
	member := bearer("tok-8")
	own := seedTaught(t, ve.m, voiceHH, 9, "Bank statement", false)
	shared := seedTaught(t, ve.m, voiceHH, 7, "Order ink", true)
	private := seedTaught(t, ve.m, voiceHH, 7, "Pay rent", false)
	run := func(id string) string { return wfBase + "/" + id + "/run" }

	ve.do("POST", run(private.ID), map[string]any{"params": map[string]any{"month": "May"}}, member).detail(404, "Workflow not found")
	// An admin manages a member's private workflow but doesn't run it.
	ve.do("POST", run(private.ID), map[string]any{"params": map[string]any{"month": "May"}}, admin).
		detail(403, "Only its owner can run a workflow that isn't shared")
	ve.do("POST", run(own.ID), nil, admin).detail(400, "Which month should I run “Bank statement” for?")
	ve.do("POST", run(own.ID), map[string]any{"params": map[string]any{"month": "May"}}, admin).want(409)

	ve.setComputerControl("on")
	withErrandQueue(t, ve.env)
	got := ve.do("POST", run(shared.ID), map[string]any{"params": map[string]any{"month": "May"}}, member).want(202).json()
	if got["status"] != "started" || !strings.HasPrefix(got["errand_id"].(string), "wf_") {
		t.Fatalf("run: %v", got)
	}
	if r := errandRows(t, ve.env)[got["errand_id"].(string)]; r[0] != "8" || !strings.Contains(r[1], shared.ID) {
		t.Fatalf("errand %v", r)
	}
}

func TestWorkflowHealStore(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_8B)
	ctx := context.Background()
	w := seedTaught(t, ve.m, voiceHH, 7, "Bank statement", false)
	store := taughtWorkflows{ve.m}
	heal := computer.Heal{Step: 0, Was: *w.Steps[0].Element, Element: computer.RecordedElement{Role: "button", Name: "Statements"}}
	if err := store.Heal(ctx, w.ID, w.Version, []computer.Heal{heal}); err != nil {
		t.Fatal(err)
	}
	got, _ := ve.m.getWorkflow(ctx, w.ID)
	if got.Version != 2 || *got.Steps[0].Element != heal.Element || got.Steps[0].Intent != w.Steps[0].Intent {
		t.Fatalf("healed %+v (v%d)", got.Steps[0], got.Version)
	}
	// A replay of an older version doesn't overwrite an edit since, even where the step's element
	// is still the one it saw.
	again := computer.Heal{Step: 0, Was: heal.Element, Element: computer.RecordedElement{Role: "link", Name: "Statements"}}
	if err := store.Heal(ctx, w.ID, 1, []computer.Heal{again}); err == nil || !strings.Contains(err.Error(), "(version 2, not 1)") {
		t.Fatalf("stale heal: %v", err)
	}
	if got, _ := ve.m.getWorkflow(ctx, w.ID); got.Version != 2 || *got.Steps[0].Element != heal.Element {
		t.Fatalf("a stale heal changed %+v (v%d)", got.Steps[0], got.Version)
	}
	// Visible: the user's own and the shared ones; with no user, the shared ones.
	seedTaught(t, ve.m, voiceHH, 8, "Order ink", true)
	seedTaught(t, ve.m, voiceHH, 8, "Pay rent", false)
	names := func(uid *int64) string {
		ws, err := store.Visible(ctx, voiceHH, uid)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, w := range ws {
			out = append(out, w.Name)
		}
		return strings.Join(out, ",")
	}
	seven := int64(7)
	if got := names(&seven); got != "Bank statement,Order ink" {
		t.Errorf("user 7 sees %s", got)
	}
	if got := names(nil); got != "Order ink" {
		t.Errorf("the household sees %s", got)
	}
}

// An unproven workflow is its owner's to start, themselves: another member, though it is shared
// with them, is told why not. One verified run later anyone it is shared with may run it.
func TestUnprovenWorkflowIsItsOwnersToRun(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_8B)
	owner := bearer(ve.auth.addUser(7, voiceHH, authn.RoleMember))
	member := bearer(ve.auth.addUser(8, voiceHH, authn.RoleMember))
	w := seedUnproven(t, ve.m, voiceHH, 7, "Order ink", true)
	ve.setComputerControl("on")
	withErrandQueue(t, ve.env)
	run := wfBase + "/" + w.ID + "/run"
	body := map[string]any{"params": map[string]any{"month": "May"}}

	got := ve.do("POST", run, body, member).want(409).json()
	if got["code"] != "unproven" || !strings.Contains(got["detail"].(string), "hasn't had its first watched run yet") {
		t.Fatalf("another member: %v", got)
	}
	started := ve.do("POST", run, body, owner).want(202).json()
	if r := errandRows(t, ve.env)[started["errand_id"].(string)]; r[0] != "7" || !strings.Contains(r[1], "__watched") {
		t.Fatalf("the owner's run isn't marked watched: %v", r)
	}
	if page := ve.do("GET", wfBase+"/"+w.ID, nil, owner).want(200).json(); page["proven"] != false {
		t.Fatalf("a new workflow reads proven: %v", page["proven"])
	}

	store := taughtWorkflows{ve.m}
	if err := store.Proved(context.Background(), w.ID, w.Version+1); err == nil {
		t.Fatal("proved a version the workflow isn't at")
	}
	if err := store.Proved(context.Background(), w.ID, w.Version); err != nil {
		t.Fatal(err)
	}
	ve.do("POST", run, body, member).want(202)
}

// What a run does decides whether a workflow is still proven: a new name, sharing or a note keeps
// it; another step, prerequisite, end state or guardrail doesn't. A run's own heal keeps it.
func TestEditsThatUnproveAWorkflow(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_8B)
	ctx := context.Background()
	fresh := func() TaughtWorkflow { return seedTaught(t, ve.m, voiceHH, 7, "Bank statement", false) }
	for name, tc := range map[string]struct {
		edit func(*TaughtWorkflow)
		want bool
	}{
		"rename":    {func(w *TaughtWorkflow) { w.Name = "Statements" }, true},
		"share":     {func(w *TaughtWorkflow) { w.Shared = true }, true},
		"note":      {func(w *TaughtWorkflow) { w.Skill.Notes = []computer.Note{{Text: "It signs out quickly"}} }, true},
		"reason":    {func(w *TaughtWorkflow) { w.Skill.Goal.Reason = "For the records" }, true},
		"step":      {func(w *TaughtWorkflow) { w.Steps[0].Intent = "Open the {month} page" }, false},
		"end state": {func(w *TaughtWorkflow) { w.Skill.Goal.EndState = "The page shows {month}" }, false},
		"success":   {func(w *TaughtWorkflow) { w.Success.URLContains = "/other" }, false},
		"prerequisite": {func(w *TaughtWorkflow) {
			w.Skill.Prerequisites = []computer.Prerequisite{{Kind: computer.PrereqSignedIn, Site: "bank.example"}}
		}, false},
		"guardrails": {func(w *TaughtWorkflow) { w.Skill.Guardrails = computer.GuardCreative }, false},
	} {
		w := fresh()
		tc.edit(&w)
		got, err := ve.m.updateWorkflow(ctx, w)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got.Proven != tc.want {
			t.Errorf("%s: proven %v, want %v", name, got.Proven, tc.want)
		}
		if err := ve.m.deleteWorkflow(ctx, w.ID); err != nil {
			t.Fatal(err)
		}
	}
	w := fresh()
	heal := computer.Heal{Step: 0, Was: *w.Steps[0].Element, Element: computer.RecordedElement{Role: "button", Name: "Statements"}}
	if err := (taughtWorkflows{ve.m}).Heal(ctx, w.ID, w.Version, []computer.Heal{heal}); err != nil {
		t.Fatal(err)
	}
	if got, _ := ve.m.getWorkflow(ctx, w.ID); !got.Proven {
		t.Error("a run's own heal unproved the workflow")
	}
}
