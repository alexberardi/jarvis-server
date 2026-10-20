package cc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/computer"
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// Taught workflows (docs/agent/COMPUTER_CONTROL.md, plan P3 piece 26): a task Jarvis learnt from
// a recording (piece 30) and replays (piece 31). A workflow is household data with an owner:
// the owner sees it, and so does every member of its household when it is shared; only the
// owner or a household admin edits or deletes it. Another household never sees it (404).
// The admin SPA's Workflows page (piece 27) uses the routes below.

// Limits on a workflow.
const (
	maxWorkflowName     = 100
	maxWorkflowTriggers = 20
	maxWorkflowParams   = 20
	maxWorkflowSteps    = 200
	maxWorkflowBody     = 1 << 20
)

// Param kinds.
var paramKinds = []string{"date", "month", "text", "number", "file"}

// riskCategories are the categories a step may carry ("" is none).
var riskCategories = []string{
	computeruse.CatPay, computeruse.CatSubmitThirdParty, computeruse.CatSend, computeruse.CatDelete,
	computeruse.CatSharing, computeruse.CatInstall, computeruse.CatSubscribe, computeruse.CatUnsubscribe,
	computeruse.CatSecuritySettings, computeruse.CatCaptcha, computeruse.CatSensitiveInput,
}

// TaughtWorkflow is one row of cc_taught_workflows.
type TaughtWorkflow struct {
	ID          string                `json:"id"`
	HouseholdID string                `json:"household_id"`
	OwnerID     int64                 `json:"owner_id"`
	Name        string                `json:"name"`
	Slug        string                `json:"slug"`
	Triggers    []string              `json:"triggers"`
	Params      []Param               `json:"params"`
	Steps       []WStep               `json:"steps"`
	Success     computer.SuccessCheck `json:"success"`
	Shared      bool                  `json:"shared"`
	RecordingID string                `json:"recording_id,omitempty"`
	Version     int                   `json:"version"`
	CreatedAt   time.Time             `json:"created_at"`
	UpdatedAt   time.Time             `json:"updated_at"`
}

// Param, WStep, StepWindow and RecordedElement are the computer package's, which replays them (piece 31).
type (
	Param           = computer.WorkflowParam
	WStep           = computer.WorkflowStep
	StepWindow      = computer.StepWindow
	RecordedElement = computer.RecordedElement
)

// Validate checks a workflow before it is stored. Each step's action must pass
// computeruse.Action.Validate; errors name the field ("steps[2]: ...").
func (w *TaughtWorkflow) Validate() error {
	w.Name = strings.TrimSpace(w.Name)
	switch {
	case w.Name == "":
		return errors.New("name is required")
	case utf8.RuneCountInString(w.Name) > maxWorkflowName:
		return fmt.Errorf("name is longer than %d characters", maxWorkflowName)
	case len(w.Triggers) > maxWorkflowTriggers:
		return fmt.Errorf("at most %d triggers", maxWorkflowTriggers)
	case len(w.Params) > maxWorkflowParams:
		return fmt.Errorf("at most %d params", maxWorkflowParams)
	case len(w.Steps) == 0:
		return errors.New("steps: a workflow needs at least one step")
	case len(w.Steps) > maxWorkflowSteps:
		return fmt.Errorf("steps: at most %d", maxWorkflowSteps)
	}
	for i, t := range w.Triggers {
		if w.Triggers[i] = strings.TrimSpace(t); w.Triggers[i] == "" {
			return fmt.Errorf("triggers[%d]: empty", i)
		}
	}
	seen := map[string]bool{}
	for i, p := range w.Params {
		switch {
		case strings.TrimSpace(p.Name) == "":
			return fmt.Errorf("params[%d]: name is required", i)
		case seen[p.Name]:
			return fmt.Errorf("params[%d]: duplicate name %q", i, p.Name)
		case !slices.Contains(paramKinds, p.Kind):
			return fmt.Errorf("params[%d]: kind must be one of %s", i, strings.Join(paramKinds, ", "))
		}
		seen[p.Name] = true
	}
	for i, s := range w.Steps {
		if strings.TrimSpace(s.Intent) == "" {
			return fmt.Errorf("steps[%d]: intent is required", i)
		}
		if s.Target != "browser" && s.Target != "desktop" {
			return fmt.Errorf("steps[%d]: target must be browser or desktop", i)
		}
		if s.Risk != "" && !slices.Contains(riskCategories, s.Risk) {
			return fmt.Errorf("steps[%d]: unknown risk %q", i, s.Risk)
		}
		if err := s.Action.Validate(); err != nil {
			return fmt.Errorf("steps[%d]: %w", i, err)
		}
	}
	return nil
}

// --- store ---

const workflowColumns = `id, household_id, owner_id, name, slug, trigger_phrases, params, steps, success,
	shared, recording_id, version, created_at, updated_at`

func scanWorkflow(sc interface{ Scan(...any) error }) (TaughtWorkflow, error) {
	var (
		w                                 TaughtWorkflow
		trig, params, steps, succ, ca, ua string
		rec                               sql.NullString
	)
	if err := sc.Scan(&w.ID, &w.HouseholdID, &w.OwnerID, &w.Name, &w.Slug, &trig, &params, &steps, &succ,
		&w.Shared, &rec, &w.Version, &ca, &ua); err != nil {
		return TaughtWorkflow{}, err
	}
	for _, f := range []struct {
		raw string
		v   any
	}{{trig, &w.Triggers}, {params, &w.Params}, {steps, &w.Steps}, {succ, &w.Success}} {
		if err := json.Unmarshal([]byte(f.raw), f.v); err != nil {
			return TaughtWorkflow{}, fmt.Errorf("workflow %s: %w", w.ID, err)
		}
	}
	w.RecordingID = rec.String
	w.CreatedAt, w.UpdatedAt = parseTS(ca), parseTS(ua)
	return w, nil
}

// workflowJSON is the JSON columns of w, in column order.
func workflowJSON(w TaughtWorkflow) (trig, params, steps, succ string, err error) {
	enc := func(v any, empty string) string {
		if err != nil {
			return ""
		}
		var b []byte
		b, err = json.Marshal(v)
		if string(b) == "null" {
			return empty
		}
		return string(b)
	}
	return enc(w.Triggers, "[]"), enc(w.Params, "[]"), enc(w.Steps, "[]"), enc(w.Success, "{}"), err
}

// uniqueWorkflowSlug is the name's slug (as slugify, fallback "workflow"), with _2, _3... when
// another workflow of the household has it (except the workflow itself, on a rename).
func (m *Module) uniqueWorkflowSlug(ctx context.Context, hh, name, selfID string) (string, error) {
	base := strings.Trim(slugRE.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "_"), "_")
	if base == "" {
		base = "workflow"
	}
	slug := base
	for n := 2; ; n++ {
		var id string
		err := m.deps.DB.Read.QueryRowContext(ctx, `SELECT id FROM cc_taught_workflows WHERE household_id = ? AND slug = ?`, hh, slug).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && id == selfID) {
			return slug, nil
		}
		if err != nil {
			return "", err
		}
		slug = fmt.Sprintf("%s_%d", base, n)
	}
}

// createWorkflow validates and stores a new workflow (version 1) and returns it.
func (m *Module) createWorkflow(ctx context.Context, w TaughtWorkflow) (TaughtWorkflow, error) {
	if w.HouseholdID == "" || w.OwnerID == 0 {
		return TaughtWorkflow{}, errors.New("a workflow needs a household and an owner")
	}
	if err := w.Validate(); err != nil {
		return TaughtWorkflow{}, err
	}
	slug, err := m.uniqueWorkflowSlug(ctx, w.HouseholdID, w.Name, "")
	if err != nil {
		return TaughtWorkflow{}, err
	}
	trig, params, steps, succ, err := workflowJSON(w)
	if err != nil {
		return TaughtWorkflow{}, err
	}
	now := m.now().UTC().Truncate(time.Millisecond)
	w.ID, w.Slug, w.Version, w.CreatedAt, w.UpdatedAt = "tw_"+randHex(8), slug, 1, now, now
	_, err = m.deps.DB.Write.ExecContext(ctx, `INSERT INTO cc_taught_workflows (`+workflowColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		w.ID, w.HouseholdID, w.OwnerID, w.Name, w.Slug, trig, params, steps, succ, w.Shared,
		sql.NullString{String: w.RecordingID, Valid: w.RecordingID != ""}, w.Version, dbTime(now), dbTime(now))
	return w, err
}

// getWorkflow is one workflow by id (nil when there is none).
func (m *Module) getWorkflow(ctx context.Context, id string) (*TaughtWorkflow, error) {
	w, err := scanWorkflow(m.deps.DB.Read.QueryRowContext(ctx, `SELECT `+workflowColumns+` FROM cc_taught_workflows WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &w, nil
}

// listWorkflows is the household's workflows that viewer sees: their own and the shared ones.
func (m *Module) listWorkflows(ctx context.Context, hh string, viewer int64) ([]TaughtWorkflow, error) {
	rows, err := m.deps.DB.Read.QueryContext(ctx, `SELECT `+workflowColumns+` FROM cc_taught_workflows
		WHERE household_id = ? AND (owner_id = ? OR shared = 1) ORDER BY name, id`, hh, viewer)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TaughtWorkflow{}
	for rows.Next() {
		w, err := scanWorkflow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// updateWorkflow validates and stores w's editable fields (name, triggers, params, steps,
// success, shared), re-slugs it on a rename and bumps its version. It returns the stored row.
func (m *Module) updateWorkflow(ctx context.Context, w TaughtWorkflow) (TaughtWorkflow, error) {
	if err := w.Validate(); err != nil {
		return TaughtWorkflow{}, err
	}
	slug, err := m.uniqueWorkflowSlug(ctx, w.HouseholdID, w.Name, w.ID)
	if err != nil {
		return TaughtWorkflow{}, err
	}
	trig, params, steps, succ, err := workflowJSON(w)
	if err != nil {
		return TaughtWorkflow{}, err
	}
	now := m.now().UTC().Truncate(time.Millisecond)
	if _, err := m.deps.DB.Write.ExecContext(ctx, `UPDATE cc_taught_workflows SET name = ?, slug = ?,
		trigger_phrases = ?, params = ?, steps = ?, success = ?, shared = ?, version = version + 1, updated_at = ?
		WHERE id = ?`, w.Name, slug, trig, params, steps, succ, w.Shared, dbTime(now), w.ID); err != nil {
		return TaughtWorkflow{}, err
	}
	got, err := m.getWorkflow(ctx, w.ID)
	if err != nil {
		return TaughtWorkflow{}, err
	}
	if got == nil {
		return TaughtWorkflow{}, sql.ErrNoRows
	}
	return *got, nil
}

func (m *Module) deleteWorkflow(ctx context.Context, id string) error {
	_, err := m.deps.DB.Write.ExecContext(ctx, `DELETE FROM cc_taught_workflows WHERE id = ?`, id)
	return err
}

// --- access ---

// workflowAccess is the caller's standing towards one workflow: whether they see it and whether
// they may change it. A non-member of its household sees nothing.
func (m *Module) workflowAccess(ctx context.Context, u authn.User, w TaughtWorkflow) (see, edit bool, err error) {
	role, member, err := m.Auth.HouseholdRole(ctx, u.ID, w.HouseholdID)
	if err != nil || !member {
		return false, false, err
	}
	own := w.OwnerID == u.ID
	return own || w.Shared, own || roleRank(role) >= roleRank(authn.RoleAdmin), nil
}

// visibleWorkflow loads the path's workflow for u: 404 when it doesn't exist or u doesn't see it,
// 403 when edit is asked and u may only see it.
func (m *Module) visibleWorkflow(ctx context.Context, u authn.User, id string, edit bool) (*TaughtWorkflow, bool, error) {
	w, err := m.getWorkflow(ctx, id)
	if err != nil {
		return nil, false, err
	}
	if w == nil {
		return nil, false, fail(http.StatusNotFound, "Workflow not found")
	}
	see, canEdit, err := m.workflowAccess(ctx, u, *w)
	switch {
	case err != nil:
		return nil, false, err
	case !see && !canEdit:
		return nil, false, fail(http.StatusNotFound, "Workflow not found")
	case edit && !canEdit:
		return nil, false, fail(http.StatusForbidden, "Only the workflow's owner or a household admin can change it")
	}
	return w, canEdit, nil
}

// --- routes ---

func (m *Module) registerWorkflows(mux *http.ServeMux) {
	const v0 = "/api/v0"
	mux.HandleFunc("GET "+v0+"/workflows", m.user(m.handleListWorkflows))
	mux.HandleFunc("GET "+v0+"/workflows/{id}", m.user(m.handleGetWorkflow))
	mux.HandleFunc("PUT "+v0+"/workflows/{id}", m.user(m.handlePutWorkflow))
	mux.HandleFunc("DELETE "+v0+"/workflows/{id}", m.user(m.handleDeleteWorkflow))
	mux.HandleFunc("POST "+v0+"/workflows/{id}/run", m.user(m.handleRunWorkflow))
}

// workflowOut is a workflow as the API returns it: the row plus whether the caller may change it.
type workflowOut struct {
	TaughtWorkflow
	Editable bool `json:"editable"`
}

func (m *Module) handleListWorkflows(w http.ResponseWriter, r *http.Request, u authn.User) {
	hh, ok := householdQuery(w, r)
	if !ok {
		return
	}
	role, member, err := m.Auth.HouseholdRole(r.Context(), u.ID, hh)
	if err != nil {
		m.internalError(w, err)
		return
	}
	if !member {
		detail(w, http.StatusForbidden, "User is not a member of this household")
		return
	}
	ws, err := m.listWorkflows(r.Context(), hh, u.ID)
	if err != nil {
		m.internalError(w, err)
		return
	}
	admin := roleRank(role) >= roleRank(authn.RoleAdmin)
	out := make([]workflowOut, 0, len(ws))
	for _, wf := range ws {
		out = append(out, workflowOut{wf, admin || wf.OwnerID == u.ID})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"workflows": out})
}

func (m *Module) handleGetWorkflow(w http.ResponseWriter, r *http.Request, u authn.User) {
	wf, edit, err := m.visibleWorkflow(r.Context(), u, r.PathValue("id"), false)
	if err != nil {
		m.writeErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, workflowOut{*wf, edit})
}

// workflowPut is PUT's body: the editable fields, all replaced.
type workflowPut struct {
	Name     string                `json:"name"`
	Triggers []string              `json:"triggers"`
	Params   []Param               `json:"params"`
	Steps    []WStep               `json:"steps"`
	Success  computer.SuccessCheck `json:"success"`
	Shared   bool                  `json:"shared"`
}

func (m *Module) handlePutWorkflow(w http.ResponseWriter, r *http.Request, u authn.User) {
	wf, _, err := m.visibleWorkflow(r.Context(), u, r.PathValue("id"), true)
	if err != nil {
		m.writeErr(w, err)
		return
	}
	var in workflowPut
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxWorkflowBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		detail(w, http.StatusBadRequest, "invalid workflow: "+err.Error())
		return
	}
	wf.Name, wf.Triggers, wf.Params, wf.Steps, wf.Success, wf.Shared = in.Name, in.Triggers, in.Params, in.Steps, in.Success, in.Shared
	if err := wf.Validate(); err != nil {
		detail(w, http.StatusBadRequest, err.Error())
		return
	}
	got, err := m.updateWorkflow(r.Context(), *wf)
	if err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, workflowOut{got, true})
}

func (m *Module) handleDeleteWorkflow(w http.ResponseWriter, r *http.Request, u authn.User) {
	wf, _, err := m.visibleWorkflow(r.Context(), u, r.PathValue("id"), true)
	if err != nil {
		m.writeErr(w, err)
		return
	}
	if err := m.deleteWorkflow(r.Context(), wf.ID); err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "deleted", "id": wf.ID})
}
