package recipes

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
)

// Parse jobs (recipes_recipe_parse_jobs, §4.5) and their queue jobs (§6). This is the core R5
// needs for the grocery match job: create a row and its queue job in one transaction, the
// status transitions, and GET /recipes/jobs/{job_id} (#13). R6 adds the list, cancel and the
// hourly cleanup on top.
//
// The row is what clients see (its uuid); the queue job is internal. The queue payload is
// {"parse_job_id": "<uuid>"} and the dedup key recipes:<uuid>. A handler whose row is gone (an
// account deletion purged it; the hooks do not cancel queue rows) treats the job as done.

// Job statuses.
const (
	statusPending   = "PENDING"
	statusRunning   = "RUNNING"
	statusComplete  = "COMPLETE"
	statusError     = "ERROR"
	statusCanceled  = "CANCELED"
	statusCommitted = "COMMITTED"
	statusAbandoned = "ABANDONED"
)

// finalStatus are the states mark_running/mark_complete/mark_error never leave.
func finalStatus(s string) bool {
	return s == statusCanceled || s == statusCommitted || s == statusAbandoned
}

type jobPayload struct {
	ParseJobID string `json:"parse_job_id"`
}

// parseJob is a recipes_recipe_parse_jobs row.
type parseJob struct {
	ID, UserID, JobType, Status string
	HouseholdID                 sql.NullString
	JobData, Result             sql.NullString
	ErrorCode, ErrorMessage     sql.NullString
}

// createJob inserts a PENDING row of jobType for the caller and enqueues queueType for it, in
// tx. Call m.deps.Queue.Notify(queueType) after the commit.
func (m *Module) createJob(ctx context.Context, tx *sql.Tx, c caller, jobType, queueType string, data any) (string, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return "", err
	}
	id := newUUID()
	now := ts(m.now())
	if _, err := tx.ExecContext(ctx, `INSERT INTO recipes_recipe_parse_jobs (id, user_id, household_id, job_type, status,
		job_data, attempts, created_at, updated_at) VALUES (?, ?, ?, ?, 'PENDING', ?, 0, ?, ?)`,
		id, c.uid(), c.hh(), jobType, string(raw), now, now); err != nil {
		return "", err
	}
	payload, _ := json.Marshal(jobPayload{ParseJobID: id})
	qid, err := m.deps.Queue.EnqueueTx(ctx, tx, queueType, payload, queue.Options{DedupKey: "recipes:" + id})
	if err != nil {
		return "", err
	}
	_, err = tx.ExecContext(ctx, `UPDATE recipes_recipe_parse_jobs SET queue_job_id = ? WHERE id = ?`, qid, id)
	return id, err
}

// loadJob reads a row by id; ok is false when it does not exist.
func loadJob(ctx context.Context, q queryer, id string) (parseJob, bool, error) {
	var j parseJob
	err := q.QueryRowContext(ctx, `SELECT id, user_id, household_id, job_type, status, job_data, result_json,
		error_code, error_message FROM recipes_recipe_parse_jobs WHERE id = ?`, id).
		Scan(&j.ID, &j.UserID, &j.HouseholdID, &j.JobType, &j.Status, &j.JobData, &j.Result, &j.ErrorCode, &j.ErrorMessage)
	if errors.Is(err, sql.ErrNoRows) {
		return j, false, nil
	}
	return j, err == nil, err
}

// claimJob is the start of every handler: it loads the row named by the queue payload and marks
// it RUNNING (attempts + 1). run is false when there is nothing to do: the row is gone or the
// job was canceled, committed or abandoned.
func (m *Module) claimJob(ctx context.Context, qj queue.Job) (j parseJob, run bool, err error) {
	var p jobPayload
	if err := json.Unmarshal(qj.Payload, &p); err != nil || p.ParseJobID == "" {
		return j, false, queue.Permanent(errors.New("recipes: bad job payload"))
	}
	j, ok, err := loadJob(ctx, m.deps.DB.Read, p.ParseJobID)
	if err != nil || !ok || finalStatus(j.Status) {
		return j, false, err
	}
	now := ts(m.now())
	res, err := m.deps.DB.Write.ExecContext(ctx, `UPDATE recipes_recipe_parse_jobs SET status = 'RUNNING', started_at = ?,
		attempts = attempts + 1, updated_at = ? WHERE id = ? AND status NOT IN ('CANCELED', 'COMMITTED', 'ABANDONED')`,
		now, now, j.ID)
	if err != nil {
		return j, false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return j, false, nil
	}
	j.Status = statusRunning
	return j, true, nil
}

// markComplete stores result and sets COMPLETE with completed_at, unless the job was canceled,
// committed or abandoned meanwhile.
func (m *Module) markComplete(ctx context.Context, id string, result any) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	now := ts(m.now())
	_, err = m.deps.DB.Write.ExecContext(ctx, `UPDATE recipes_recipe_parse_jobs SET status = 'COMPLETE', result_json = ?,
		completed_at = ?, updated_at = ? WHERE id = ? AND status NOT IN ('CANCELED', 'COMMITTED', 'ABANDONED')`,
		string(raw), now, now, id)
	return err
}

// markError sets ERROR with the code and message, under the same guard.
func (m *Module) markError(ctx context.Context, id, code, msg string) error {
	now := ts(m.now())
	_, err := m.deps.DB.Write.ExecContext(ctx, `UPDATE recipes_recipe_parse_jobs SET status = 'ERROR', error_code = ?,
		error_message = ?, completed_at = ?, updated_at = ? WHERE id = ? AND status NOT IN ('CANCELED', 'COMMITTED', 'ABANDONED')`,
		code, msg, now, now, id)
	return err
}

// jobCaller rebuilds the author as a caller for a handler, which holds no token: their current
// memberships (RD7), writing to the job's household while they are still in it.
func (m *Module) jobCaller(ctx context.Context, j parseJob) (caller, error) {
	id, err := strconv.ParseInt(j.UserID, 10, 64)
	if err != nil {
		return caller{}, queue.Permanent(err)
	}
	return m.resolve(ctx, authn.User{ID: id, HouseholdID: j.HouseholdID.String})
}

// handleGetJob is GET /recipes/jobs/{job_id} (#13): author only; the wire key is "id".
func (m *Module) handleGetJob(w http.ResponseWriter, r *http.Request, c caller) {
	pred, args := c.authorOnly("")
	var j parseJob
	err := m.deps.DB.Read.QueryRowContext(r.Context(), `SELECT id, status, result_json, error_code, error_message
		FROM recipes_recipe_parse_jobs WHERE id = ? AND `+pred, append([]any{r.PathValue("job_id")}, args...)...).
		Scan(&j.ID, &j.Status, &j.Result, &j.ErrorCode, &j.ErrorMessage)
	if errors.Is(err, sql.ErrNoRows) {
		httpx.Error(w, http.StatusNotFound, "Job not found")
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"id": j.ID, "status": j.Status, "result": jobResult(j.Result), "error_code": nullStr(j.ErrorCode),
		"error_message": nullStr(j.ErrorMessage), "next_action": nil, "next_action_reason": nil,
	})
}

// jobResult is result_json as JSON, or null when empty (legacy: `result_json or None`).
func jobResult(ns sql.NullString) any {
	if !ns.Valid || ns.String == "" {
		return nil
	}
	var v any
	if json.Unmarshal([]byte(ns.String), &v) != nil {
		return nil
	}
	switch x := v.(type) {
	case nil:
		return nil
	case map[string]any:
		if len(x) == 0 {
			return nil
		}
	}
	return json.RawMessage(ns.String)
}
