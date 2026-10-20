package cc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/computer"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/errands"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// Replaying taught workflows (plan P3 piece 31): by voice (run_workflow), from the admin's Run
// button (POST /workflows/{id}/run), from a schedule (the errand planner's run_workflow) or a
// routine step (routines_run.go). Each starts an errand whose step replays the workflow on a
// computer of the household (computer.Loop.Replay); the errand's card shows how it goes.

const toolRunWorkflow = "run_workflow"

// taughtWorkflows are cc_taught_workflows as the errand runner and the replay see them.
type taughtWorkflows struct{ m *Module }

// Visible are the workflows uid sees in hh (their own and the shared ones); with no user, the
// shared ones (no workflow has owner 0).
func (t taughtWorkflows) Visible(ctx context.Context, hh string, uid *int64) ([]computer.Workflow, error) {
	var viewer int64
	if uid != nil {
		viewer = *uid
	}
	ws, err := t.m.listWorkflows(ctx, hh, viewer)
	if err != nil {
		return nil, err
	}
	out := make([]computer.Workflow, len(ws))
	for i, w := range ws {
		out[i] = w.replay()
	}
	return out, nil
}

// Heal stores the elements a replay healed, as long as nobody changed the workflow since it was
// loaded for the replay (their edit wins). The version goes up by one.
func (t taughtWorkflows) Heal(ctx context.Context, id string, version int, heals []computer.Heal) error {
	w, err := t.m.getWorkflow(ctx, id)
	switch {
	case err != nil:
		return err
	case w == nil:
		return errors.New("the workflow is gone")
	case w.Version != version:
		return fmt.Errorf("the workflow was changed since the replay started (version %d, not %d)", w.Version, version)
	}
	for _, h := range heals {
		if h.Step < 0 || h.Step >= len(w.Steps) || w.Steps[h.Step].Element == nil || *w.Steps[h.Step].Element != h.Was {
			return fmt.Errorf("step %d changed since the replay started", h.Step+1)
		}
		el := h.Element
		w.Steps[h.Step].Element = &el
	}
	got, err := t.m.updateWorkflow(ctx, *w)
	if err == nil {
		t.m.deps.Log.Info("cc: workflow healed", "workflow", id, "steps", len(heals), "version", got.Version)
	}
	return err
}

// replay is the workflow as the replay runs it.
func (w TaughtWorkflow) replay() computer.Workflow {
	return computer.Workflow{ID: w.ID, Name: w.Name, Slug: w.Slug, Triggers: w.Triggers, Params: w.Params,
		Steps: w.Steps, Success: w.Success, Version: w.Version}
}

// replayRefusal is why a replay can't start: a message for the user, and the HTTP status the
// Run button answers with.
type replayRefusal struct {
	code   string
	msg    string
	status int
}

// startReplay starts the errand that replays wf with params for uid (nil: a shared workflow run
// by the household, a routine's). It returns the errand's id, or why it can't start.
func (m *Module) startReplay(ctx context.Context, hh string, uid *int64, nodeID string, wf computer.Workflow, params map[string]string) (string, *replayRefusal) {
	if miss := wf.Missing(params); len(miss) > 0 {
		return "", &replayRefusal{"missing_params", fmt.Sprintf("Which %s should I run “%s” for?", strings.Join(miss, " and "), wf.Name),
			http.StatusBadRequest}
	}
	if ids, off := m.computerTargets(ctx, hh); len(ids) == 0 {
		return "", &replayRefusal{"computer_control_off", firstNonEmptyStr(off, "There's no computer with computer control on."), http.StatusConflict}
	}
	s := m.errands
	if s == nil || !s.Available() {
		return "", &replayRefusal{"unavailable", "Saved workflows can't run right now.", http.StatusServiceUnavailable}
	}
	goal := "Run “" + wf.Name + "”"
	var vals []string
	for _, p := range wf.Params {
		vals = append(vals, p.Name+": "+params[p.Name])
	}
	if len(vals) > 0 {
		goal += " (" + strings.Join(vals, ", ") + ")"
	}
	id, err := s.Start(ctx, errands.StartRequest{HouseholdID: hh, UserID: uid, NodeID: nodeID, Goal: goal,
		Steps: []errands.Step{errands.ReplayStep(wf, params)}})
	switch {
	case errors.Is(err, errands.ErrDisabled):
		return "", &replayRefusal{"errands_disabled", "Errands are turned off for this household.", http.StatusConflict}
	case err != nil:
		m.deps.Log.Error("cc: start workflow replay", "workflow", wf.ID, "err", err)
		return "", &replayRefusal{"task_failed", "I couldn't start “" + wf.Name + "”.", http.StatusInternalServerError}
	}
	m.deps.Log.Info("cc: workflow replay started", "workflow", wf.ID, "errand", id, "household", hh)
	return id, nil
}

func firstNonEmptyStr(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// --- voice ---

type runWorkflowTool struct{ m *Module }

func (*runWorkflowTool) Name() string { return toolRunWorkflow }

func (*runWorkflowTool) Definition() *pyjson.Object {
	return watchToolDefinition(toolRunWorkflow,
		"Run a task the user taught Jarvis (a saved workflow, recorded with 'watch me') on their computer, e.g. "+
			"'download my bank statement for October', 'run the bank statement workflow for March'. Call it with the "+
			"workflow's name or the user's phrase for it, and a value for each thing it needs; if it answers with a "+
			"question, ask the user and call it again with their answer.",
		servertools.Obj(
			"name", strProp("The saved workflow's name, or the user's words for it ('my bank statement')"),
			"params", servertools.Obj("type", "object", "description",
				`The values the workflow needs, by name, e.g. {"month": "October 2026"}. Leave out what the user didn't say.`),
		), "name")
}

func (t *runWorkflowTool) Execute(ctx context.Context, call servertools.Call, turn servertools.Turn) (any, error) {
	if e := toolSpeakerError(turn); e != nil {
		return e, nil
	}
	uid := turn.Speaker.UserID
	ws, err := taughtWorkflows{t.m}.Visible(ctx, turn.HouseholdID, &uid)
	if err != nil {
		return nil, err
	}
	name := argText(call, "name")
	m := computer.MatchWorkflows(ws, name)
	switch {
	case len(ws) == 0:
		return recordingRefusal("no_workflows", `You haven't taught me anything yet. Say "watch me" on your computer and do the task once.`), nil
	case len(m) == 0:
		return recordingRefusal("not_found", fmt.Sprintf("I don't have a saved workflow called %q. You have %s.", name, workflowList(ws, "and"))), nil
	case len(m) > 1:
		return servertools.Obj("status", "ask", "message", "Which one: "+workflowList(m, "or")+"?"), nil
	}
	wf := m[0]
	params := map[string]string{}
	if v, ok := call.Arg("params"); ok {
		params = errands.ParamValues(v)
	}
	if miss := wf.Missing(params); len(miss) > 0 {
		q := fmt.Sprintf("Which %s should I run “%s” for?", strings.Join(miss, " and "), wf.Name)
		for _, p := range wf.Params {
			if p.Name == miss[0] && p.Example != "" {
				q = strings.TrimSuffix(q, "?") + fmt.Sprintf(" (last time: %s)?", p.Example)
			}
		}
		return servertools.Obj("status", "ask", "missing", miss, "message", q), nil
	}
	if _, ref := t.m.startReplay(ctx, turn.HouseholdID, &uid, turn.NodeID, wf, params); ref != nil {
		return recordingRefusal(ref.code, ref.msg), nil
	}
	return servertools.Obj("status", "started", "workflow", wf.Name,
		"message", "Starting “"+wf.Name+"” on your computer. Its card shows how it goes."), nil
}

func workflowList(ws []computer.Workflow, last string) string {
	var ns []string
	for _, w := range ws {
		ns = append(ns, "“"+w.Name+"”")
	}
	return joinLast(ns, " "+last+" ")
}

// --- admin ---

// handleRunWorkflow is the Run button: it replays the workflow now, with body.params, for the
// caller. A household admin manages every workflow but runs only the ones they see as a member
// (their own and the shared ones): a member's private workflow is theirs to run.
func (m *Module) handleRunWorkflow(w http.ResponseWriter, r *http.Request, u authn.User) {
	wf, _, err := m.visibleWorkflow(r.Context(), u, r.PathValue("id"), false)
	if err != nil {
		m.writeErr(w, err)
		return
	}
	if wf.OwnerID != u.ID && !wf.Shared {
		detail(w, http.StatusForbidden, "Only its owner can run a workflow that isn't shared")
		return
	}
	var in struct {
		Params map[string]any `json:"params"`
	}
	if r.ContentLength != 0 {
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxWorkflowBody))
		dec.UseNumber()
		if err := dec.Decode(&in); err != nil {
			detail(w, http.StatusBadRequest, "invalid body: "+err.Error())
			return
		}
	}
	id, ref := m.startReplay(r.Context(), wf.HouseholdID, &u.ID, "", wf.replay(), errands.ParamValues(in.Params))
	if ref != nil {
		detailCode(w, ref.status, ref.msg, ref.code)
		return
	}
	httpx.WriteJSON(w, http.StatusAccepted, map[string]any{"status": "started", "errand_id": id})
}
