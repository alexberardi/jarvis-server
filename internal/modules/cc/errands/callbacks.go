package errands

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/platform/queue"
)

// Server-plane callbacks: the card taps (command "errand"). POST /callbacks (doc 13) dispatches
// a tap here with the tapping user's household (membership already checked). Every tap is
// household-scoped: another household's plan or run reads as missing. Any member may tap
// (D21, 09.Q9). Single-use taps answer success with an "already handled" inbox note (§7.8).
//
// Behaviour change (§11): Run validates, creates the run and queues its first drive, then
// returns; the drive no longer runs inside the callback, so /callbacks/{id}/status completes
// at once instead of after the run's first suspension or its end.

// CallbackContext is ServerCallbackContext.
type CallbackContext struct {
	JobID          string
	HouseholdID    string
	UserID         int64
	Data           map[string]any // the button's data (+ the merged instruction for Revise)
	NavigationType string
}

// CallbackResult is ServerCallbackResult.
type CallbackResult struct {
	Success     bool
	Error       string
	ContextData map[string]any
}

// CallbackFunc handles one tap.
type CallbackFunc func(ctx context.Context, c CallbackContext) CallbackResult

// Callbacks maps each errand callback name to its handler (register_errand_callbacks). The
// legacy edit-goal replan_errand_plan callback is cut (no card renders it, 09.Q8).
func (s *Service) Callbacks() map[string]CallbackFunc {
	return map[string]CallbackFunc{
		CallbackApprove:     s.handleApprove,
		CallbackRefine:      s.handleRefine,
		CallbackDiscard:     s.handleDiscard,
		CallbackApproveRepl: s.handleApproveReplan,
		CallbackStop:        s.handleStop,
	}
}

func fail(msg string) CallbackResult { return CallbackResult{Error: msg} }

func dataStr(d map[string]any, k string) string {
	s, _ := d[k].(string)
	return s
}

// revisionsMatch is _revisions_match: only a definitely different revision is stale; a
// missing or unparseable one passes (mobile may stringify the int).
func revisionsMatch(card any, row int) bool {
	switch x := card.(type) {
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return true
		}
		return int(x) == row
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return int(n) == row
		}
		if f, err := x.Float64(); err == nil {
			return int(f) == row
		}
		return true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(x))
		if err != nil {
			return true
		}
		return n == row
	case bool:
		if x {
			return row == 1
		}
		return row == 0
	case int:
		return x == row
	case int64:
		return int(x) == row
	}
	return true
}

func handled(hh, summary string) CallbackResult {
	return CallbackResult{Success: true, ContextData: inbox(hh, "Errand already handled", summary)}
}

// handleApprove is the Run tap: the draft spawns a run (state waiting, no signal) and its first
// drive is queued in the same transaction.
func (s *Service) handleApprove(ctx context.Context, c CallbackContext) CallbackResult {
	if !s.Available() {
		return fail("Errands are unavailable right now.")
	}
	planID := dataStr(c.Data, "plan_id")
	if planID == "" {
		return fail("Missing plan_id")
	}
	p, err := s.getPlan(ctx, planID, c.HouseholdID)
	if err != nil {
		return fail("Couldn't load the errand plan.")
	}
	if p == nil {
		return fail("Errand plan not found")
	}
	if p.State != "draft" {
		return handled(c.HouseholdID, "This errand is already "+p.State+".")
	}
	if !revisionsMatch(c.Data["revision"], p.Revision) {
		return fail("This plan was updated — open the latest card to run it.")
	}
	if len(p.Steps) == 0 {
		_, _ = s.DB.Write.ExecContext(ctx, `UPDATE cc_errand_plans SET state = 'failed', error = 'no steps to run',
			updated_at = ? WHERE id = ? AND state = 'draft'`, dbTime(s.now()), p.ID)
		return fail("This errand has no steps to run.")
	}
	wfID := hexID("wf_")
	now := dbTime(s.now())
	raced := false
	err = s.DB.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE cc_errand_plans SET state = 'launched', workflow_id = ?, confirmed_at = ?,
			updated_at = ? WHERE id = ? AND household_id = ? AND state = 'draft'`, wfID, now, now, p.ID, c.HouseholdID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			raced = true
			return nil
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO cc_workflows (id, kind, household_id, user_id, node_id, goal, title,
			steps, cursor, results_json, revision, state, waiting_on, created_at, updated_at)
			VALUES (?, 'errand', ?, ?, ?, ?, ?, ?, 0, '[]', 1, 'waiting', NULL, ?, ?)`,
			wfID, c.HouseholdID, nullInt(p.UserID), nullText(p.NodeID), firstNonEmpty(p.Goal, p.Summary),
			nullText(firstNonEmpty(p.Summary, p.Goal)), stepsDumps(p.Steps), now, now); err != nil {
			return err
		}
		b, _ := json.Marshal(resumeJob{WorkflowID: wfID, Step: -1, Kind: signalStart})
		_, err = s.Queue.EnqueueTx(ctx, tx, JobResume, b, queue.Options{})
		return err
	})
	if err != nil {
		s.log().Error("errands: launch run", "plan", p.ID, "err", err)
		return fail("I couldn't start that errand — try again in a bit.")
	}
	if raced {
		return handled(c.HouseholdID, "This errand is already launched.")
	}
	s.Queue.Notify(JobResume)
	s.log().Info("errands: plan launched", "plan", p.ID, "workflow", wfID)
	return CallbackResult{Success: true}
}

// cancelWaiting atomically cancels a waiting run (only a parked run is safe to cancel; a
// running drive would overwrite it).
func (s *Service) cancelWaiting(ctx context.Context, workflowID string) (bool, error) {
	res, err := s.DB.Write.ExecContext(ctx, `UPDATE cc_workflows SET state = 'cancelled', updated_at = ?
		WHERE id = ? AND state = 'waiting'`, dbTime(s.now()), workflowID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// handleDiscard is the Cancel tap: a draft is cancelled; a launched errand whose run is waiting
// is cancelled with its undialled calls declined; anything else is an honest no-op.
func (s *Service) handleDiscard(ctx context.Context, c CallbackContext) CallbackResult {
	if s.DB == nil {
		return fail("Errands are unavailable right now.")
	}
	p, err := s.getPlan(ctx, dataStr(c.Data, "plan_id"), c.HouseholdID)
	if err != nil {
		return fail("Couldn't load the errand plan.")
	}
	if p == nil {
		return CallbackResult{Success: true}
	}
	label := firstNonEmpty(p.Summary, p.Goal)
	stateWord := p.State
	switch {
	case p.State == "draft":
		res, err := s.DB.Write.ExecContext(ctx, `UPDATE cc_errand_plans SET state = 'cancelled', updated_at = ?
			WHERE id = ? AND state = 'draft'`, dbTime(s.now()), p.ID)
		if err != nil {
			return fail("Couldn't discard the errand.")
		}
		if n, _ := res.RowsAffected(); n == 1 {
			return CallbackResult{Success: true, ContextData: inbox(c.HouseholdID, "🗑️ Errand discarded", label)}
		}
		stateWord = "launched"
	case p.State == "launched" && p.WorkflowID != "":
		w, err := s.loadWorkflow(ctx, p.WorkflowID)
		if err != nil {
			return fail("Couldn't load the errand.")
		}
		stateWord = "done"
		if w != nil {
			stateWord = w.State
			if w.State == "waiting" {
				ok, err := s.cancelWaiting(ctx, w.ID)
				if err != nil {
					return fail("Couldn't cancel the errand.")
				}
				if ok {
					_, _ = s.DB.Write.ExecContext(ctx, `UPDATE cc_errand_plans SET state = 'cancelled', updated_at = ?
						WHERE id = ?`, dbTime(s.now()), p.ID)
					note := " A call already in progress may still finish."
					if s.declineCalls(ctx, w.ID) {
						note = " I won't place the remaining call(s)."
					}
					return CallbackResult{Success: true, ContextData: inbox(c.HouseholdID, "🗑️ Errand cancelled", label+note)}
				}
				stateWord = "running"
			}
		}
	}
	return handled(c.HouseholdID, "This errand is already "+stateWord+" — nothing to discard.")
}

func (s *Service) workflowIn(ctx context.Context, id, hh string) (*workflowRow, error) {
	if id == "" {
		return nil, nil
	}
	w, err := s.loadWorkflow(ctx, id)
	if err != nil || w == nil || w.HouseholdID != hh {
		return nil, err
	}
	return w, nil
}

// handleApproveReplan is the delta card's Approve: splice the proposal into the run and resume.
func (s *Service) handleApproveReplan(ctx context.Context, c CallbackContext) CallbackResult {
	if !s.Available() {
		return fail("Errands are unavailable right now.")
	}
	id := dataStr(c.Data, "workflow_id")
	if id == "" {
		return fail("Missing workflow_id")
	}
	w, err := s.workflowIn(ctx, id, c.HouseholdID)
	if err != nil {
		return fail("Couldn't load the errand.")
	}
	if w == nil {
		return fail("Errand not found")
	}
	if w.State != "waiting" || w.WaitingOn != signalApproval {
		return handled(c.HouseholdID, "That change was already handled.")
	}
	if !revisionsMatch(c.Data["revision"], w.Revision) {
		return fail("This change was already updated — open the latest card.")
	}
	step := w.Cursor - 1
	if ok, err := s.applyReplan(ctx, w.ID, step); err != nil || !ok {
		return fail("Couldn't apply the change.")
	}
	if err := s.enqueueResume(ctx, resumeJob{WorkflowID: w.ID, Step: step, Kind: signalApproval}); err != nil {
		return fail("Couldn't apply the change.")
	}
	s.log().Info("errands: replan approved", "workflow", w.ID, "resume_at", step+1)
	return CallbackResult{Success: true}
}

// handleStop is the delta card's Stop: cancel the waiting run here.
func (s *Service) handleStop(ctx context.Context, c CallbackContext) CallbackResult {
	if s.DB == nil {
		return fail("Errands are unavailable right now.")
	}
	w, err := s.workflowIn(ctx, dataStr(c.Data, "workflow_id"), c.HouseholdID)
	if err != nil {
		return fail("Couldn't load the errand.")
	}
	if w == nil {
		return CallbackResult{Success: true}
	}
	if w.State == "waiting" {
		ok, err := s.cancelWaiting(ctx, w.ID)
		if err != nil {
			return fail("Couldn't stop the errand.")
		}
		if ok {
			note := ""
			if s.declineCalls(ctx, w.ID) {
				note = " I won't place the remaining call(s)."
			}
			_, _ = s.DB.Write.ExecContext(ctx, `UPDATE cc_errand_plans SET state = 'cancelled', updated_at = ?
				WHERE workflow_id = ?`, dbTime(s.now()), w.ID)
			return CallbackResult{Success: true, ContextData: inbox(c.HouseholdID, "🗑️ Errand stopped",
				firstNonEmpty(w.Title, w.Goal, "The errand")+" stopped."+note)}
		}
		w.State = "running"
	}
	return handled(c.HouseholdID, "This errand is already "+w.State+".")
}

// handleRefine is the Revise tap: revise the current plan from the typed instruction, bump the
// revision and update the same card in place. The planner call (background label) runs in the
// callback, as legacy did; the callback dispatcher runs it off the request.
func (s *Service) handleRefine(ctx context.Context, c CallbackContext) CallbackResult {
	if s.DB == nil {
		return fail("Errands are unavailable right now.")
	}
	planID := dataStr(c.Data, "plan_id")
	instruction := strings.TrimSpace(dataStr(c.Data, "instruction"))
	if planID == "" {
		return fail("Missing plan_id")
	}
	if instruction == "" {
		return fail("Tell me what to change first.")
	}
	p, err := s.getPlan(ctx, planID, c.HouseholdID)
	if err != nil {
		return fail("Couldn't load the errand plan.")
	}
	if p == nil {
		return fail("Errand plan not found")
	}
	if p.State != "draft" {
		return handled(c.HouseholdID, "This errand is already "+p.State+" — it can't be changed.")
	}
	if !revisionsMatch(c.Data["revision"], p.Revision) {
		return fail("This plan was updated — open the latest card to change it.")
	}
	menu := s.resolveNodeMenu(ctx, p.NodeID, c.HouseholdID, p.UserID)
	plan, err := s.RefinePlan(ctx, firstNonEmpty(p.Summary, p.Goal), p.Steps, instruction, menu)
	if errors.Is(err, ErrPlan) {
		return fail("I couldn't apply that change — try saying it differently.")
	}
	if err != nil {
		s.log().Error("errands: refine planner call failed", "err", err)
		return fail("I hit a snag revising that — try again in a bit.")
	}
	res, err := s.DB.Write.ExecContext(ctx, `UPDATE cc_errand_plans SET summary = ?, steps = ?, revision = revision + 1,
		updated_at = ? WHERE id = ? AND state = 'draft' AND revision = ?`,
		plan.Summary, stepsDumps(plan.Steps), dbTime(s.now()), p.ID, p.Revision)
	if err != nil {
		return fail("I hit a snag revising that — try again in a bit.")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fail("This plan was updated — open the latest card to change it.")
	}
	p.Summary, p.Steps, p.Revision = plan.Summary, plan.Steps, p.Revision+1
	if id := s.postPlanCard(ctx, p); id != "" && id != p.InboxItemID {
		s.setPlanInbox(ctx, p.ID, id)
	}
	s.log().Info("errands: plan refined", "plan", p.ID, "revision", p.Revision)
	return CallbackResult{Success: true}
}
