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

// seedTaught stores a workflow of hh owned by owner, with a month param.
func seedTaught(t *testing.T, m *Module, hh string, owner int64, name string, shared bool, triggers ...string) TaughtWorkflow {
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

	// Offered only with a computer in the household and a known speaker.
	if prompts.ServerToolAllowed(toolRunWorkflow, prompts.ToolGates{SpeakerKnown: true}) ||
		!prompts.ServerToolAllowed(toolRunWorkflow, prompts.ToolGates{Computers: true, SpeakerKnown: true}) {
		t.Fatal("run_workflow gate")
	}
	if got := runTool(t, tool, `{"name": "my bank statement"}`, turn); got["error"] != "no_workflows" {
		t.Fatalf("nothing taught: %v", got)
	}
	bank := seedTaught(t, ve.m, voiceHH, 7, "Bank statement", false, "download my bank statement")
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
	// A replay of an older version doesn't overwrite an edit since.
	if err := store.Heal(ctx, w.ID, 1, []computer.Heal{heal}); err == nil || !strings.Contains(err.Error(), "changed since") {
		t.Fatalf("stale heal: %v", err)
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
