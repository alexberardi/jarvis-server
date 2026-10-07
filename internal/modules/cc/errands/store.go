package errands

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

// Storage on cc_errand_plans (the DRAFT/definition) and cc_workflows (the RUN).

// dbTime is the schema's timestamp format: ISO-8601 UTC, ms, trailing Z.
func dbTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func hexID(prefix string) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

// planRow is one cc_errand_plans row.
type planRow struct {
	ID          string
	HouseholdID string
	UserID      *int64
	NodeID      string
	Goal        string
	Summary     string
	Steps       []Step
	State       string
	Revision    int
	InboxItemID string
	WorkflowID  string
}

const planCols = `id, household_id, user_id, node_id, goal, summary, steps, state, revision, inbox_item_id, workflow_id`

type rowScanner interface{ Scan(dest ...any) error }

func scanPlan(r rowScanner) (*planRow, error) {
	var p planRow
	var uid sql.NullInt64
	var node, summary, inbox, wf sql.NullString
	var steps string
	if err := r.Scan(&p.ID, &p.HouseholdID, &uid, &node, &p.Goal, &summary, &steps, &p.State, &p.Revision, &inbox, &wf); err != nil {
		return nil, err
	}
	if uid.Valid {
		v := uid.Int64
		p.UserID = &v
	}
	p.NodeID, p.Summary, p.InboxItemID, p.WorkflowID = node.String, summary.String, inbox.String, wf.String
	p.Steps = parseSteps(steps)
	return &p, nil
}

// getPlan loads a plan in a household (nil when absent: household-scoped, so another
// household's id reads as missing).
func (s *Service) getPlan(ctx context.Context, id, hh string) (*planRow, error) {
	p, err := scanPlan(s.DB.Read.QueryRowContext(ctx,
		`SELECT `+planCols+` FROM cc_errand_plans WHERE id = ? AND household_id = ?`, id, hh))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

func nullInt(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func nullText(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (s *Service) insertPlan(ctx context.Context, p *planRow) error {
	now := s.now()
	_, err := s.DB.Write.ExecContext(ctx, `INSERT INTO cc_errand_plans
		(id, household_id, user_id, node_id, goal, summary, steps, state, revision, created_at, updated_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'draft', 1, ?, ?, ?)`,
		p.ID, p.HouseholdID, nullInt(p.UserID), nullText(p.NodeID), p.Goal, p.Summary, stepsDumps(p.Steps),
		dbTime(now), dbTime(now), dbTime(now.Add(s.DraftTTL)))
	p.State, p.Revision = "draft", 1
	return err
}

func (s *Service) setPlanInbox(ctx context.Context, id, item string) {
	if _, err := s.DB.Write.ExecContext(ctx, `UPDATE cc_errand_plans SET inbox_item_id = ?, updated_at = ? WHERE id = ?`,
		item, dbTime(s.now()), id); err != nil {
		s.log().Warn("errands: store plan card id", "plan", id, "err", err)
	}
}

// workflowRow is one cc_workflows row (WorkflowRun).
type workflowRow struct {
	ID          string
	Kind        string
	HouseholdID string
	UserID      *int64
	NodeID      string
	Goal        string
	Title       string
	Steps       []Step
	Cursor      int
	Results     []Result
	Revision    int
	State       string
	WaitingOn   string
	InboxItemID string
}

// DisplayTitle is title or goal or "your errand".
func (w *workflowRow) DisplayTitle() string {
	return firstNonEmpty(w.Title, w.Goal, "your errand")
}

const wfCols = `id, kind, household_id, user_id, node_id, goal, title, steps, cursor, results_json, revision, state, waiting_on, inbox_item_id`

func (s *Service) loadWorkflow(ctx context.Context, id string) (*workflowRow, error) {
	var w workflowRow
	var uid sql.NullInt64
	var node, title, waiting, inbox sql.NullString
	var steps, results string
	err := s.DB.Read.QueryRowContext(ctx, `SELECT `+wfCols+` FROM cc_workflows WHERE id = ?`, id).Scan(
		&w.ID, &w.Kind, &w.HouseholdID, &uid, &node, &w.Goal, &title, &steps, &w.Cursor, &results, &w.Revision,
		&w.State, &waiting, &inbox)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if uid.Valid {
		v := uid.Int64
		w.UserID = &v
	}
	w.NodeID, w.Title, w.WaitingOn, w.InboxItemID = node.String, title.String, waiting.String, inbox.String
	w.Steps, w.Results = parseSteps(steps), parseResults(results)
	return &w, nil
}

// claimRunning is the atomic waiting→running claim: only the first trigger proceeds.
func (s *Service) claimRunning(ctx context.Context, id string, cursor int) (bool, error) {
	res, err := s.DB.Write.ExecContext(ctx, `UPDATE cc_workflows SET state = 'running', updated_at = ?
		WHERE id = ? AND state = 'waiting' AND cursor = ?`, dbTime(s.now()), id, cursor)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (s *Service) saveWaiting(ctx context.Context, id string, cursor int, results []Result, waitingOn string) error {
	_, err := s.DB.Write.ExecContext(ctx, `UPDATE cc_workflows
		SET state = 'waiting', cursor = ?, results_json = ?, waiting_on = ?, wake_at = NULL, updated_at = ?
		WHERE id = ?`, cursor, resultsDumps(results), nullText(waitingOn), dbTime(s.now()), id)
	return err
}

// saveTerminal lands a run terminal; only a running run moves (a cancel that won the race
// stays cancelled).
func (s *Service) saveTerminal(ctx context.Context, id, state, errText string, results []Result) error {
	_, err := s.DB.Write.ExecContext(ctx, `UPDATE cc_workflows
		SET state = ?, error = ?, results_json = ?, waiting_on = NULL, updated_at = ?
		WHERE id = ? AND state = 'running'`, state, nullText(errText), resultsDumps(results), dbTime(s.now()), id)
	return err
}

func (s *Service) saveSteps(ctx context.Context, id string, steps []Step, bumpRevision bool) error {
	q := `UPDATE cc_workflows SET steps = ?, updated_at = ? WHERE id = ?`
	if bumpRevision {
		q = `UPDATE cc_workflows SET steps = ?, revision = revision + 1, updated_at = ? WHERE id = ?`
	}
	_, err := s.DB.Write.ExecContext(ctx, q, stepsDumps(steps), dbTime(s.now()), id)
	return err
}
