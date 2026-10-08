package recipes

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	neturl "net/url"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// The job list (#15) and cancel (#17) on top of the parse-job core in jobs.go (R6).

// Job types (recipe_parse_jobs.job_type).
const (
	jobTypeIngestion    = "ingestion"
	jobTypeImage        = "image"
	jobTypeMealPlan     = "meal_plan_generate"
	jobTypeGroceryMatch = "grocery_match"
)

// importJobTypes are the job types the list shows (RD10: the Mailbox lists imports only;
// meal-plan jobs stay pollable by id, SKU-match jobs are invisible). Legacy listed every type.
var importJobTypes = []string{jobTypeIngestion, jobTypeImage}

// handleListJobs is GET /recipes/parse-url/jobs (#15): the caller's import jobs, newest
// completion first, at most 50. status defaults to COMPLETE; with COMPLETE and include_expired
// false, only jobs completed within parse_job.abandon_minutes are listed. An empty status lists
// every status with no window (legacy: `if status:`).
func (m *Module) handleListJobs(w http.ResponseWriter, r *http.Request, c caller) {
	qv := newQueryVals(r)
	status := statusComplete
	if vals, ok := qv.q["status"]; ok && len(vals) > 0 {
		status = vals[len(vals)-1]
	}
	includeExpired := qv.boolean("include_expired", false)
	if !qv.done(w) {
		return
	}
	ctx := r.Context()
	pred, args := c.authorOnly("")
	where := pred + ` AND job_type IN ('` + strings.Join(importJobTypes, `','`) + `')`
	if status != "" {
		where += ` AND status = ?`
		args = append(args, status)
	}
	if !includeExpired && status == statusComplete {
		mins := m.settings.Int(ctx, SettingAbandonMinutes, settings.Scope{})
		where += ` AND completed_at IS NOT NULL AND completed_at >= ?`
		args = append(args, ts(m.now().Add(-time.Duration(mins)*time.Minute)))
	}
	rows, err := m.deps.DB.Read.QueryContext(ctx, `SELECT id, job_type, url, status, completed_at, result_json
		FROM recipes_recipe_parse_jobs WHERE `+where+`
		ORDER BY completed_at IS NULL, completed_at DESC LIMIT 50`, args...)
	if err != nil {
		m.internalError(w, err)
		return
	}
	defer rows.Close()
	jobs := []map[string]any{}
	for rows.Next() {
		var id, jobType, st string
		var url, completed, result sql.NullString
		if err := rows.Scan(&id, &jobType, &url, &st, &completed, &result); err != nil {
			m.internalError(w, err)
			return
		}
		var done any
		if completed.Valid {
			done = pyNaive(completed.String)
		}
		warnings, preview := listPreview(result)
		jobs = append(jobs, map[string]any{
			"id": id, "job_type": jobType, "url": nullStr(url), "status": st, "completed_at": done,
			"warnings": warnings, "preview": preview,
		})
	}
	if err := rows.Err(); err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
}

// listPreview is a list row's warnings and preview from result_json. B6 fixed: legacy read
// result.recipe and result.warnings, which no import result has, so they were always null and
// []; these read recipe_draft and pipeline. preview is null without a result object.
func listPreview(raw sql.NullString) ([]any, any) {
	warnings := []any{}
	var res map[string]any
	if !raw.Valid || json.Unmarshal([]byte(raw.String), &res) != nil || len(res) == 0 {
		return warnings, nil
	}
	pipe, _ := res["pipeline"].(map[string]any)
	if w, ok := pipe["warnings"].([]any); ok {
		warnings = w
	}
	draft, _ := res["recipe_draft"].(map[string]any)
	var title any
	if t, ok := draft["title"].(string); ok {
		title = t
	}
	src, _ := pipe["source_url"].(string)
	if source, ok := draft["source"].(map[string]any); ok {
		if s, ok := source["source_url"].(string); ok && s != "" {
			src = s
		}
	}
	var host any
	if src != "" {
		if u, err := neturl.Parse(src); err == nil && u.Hostname() != "" {
			host = strings.ToLower(u.Hostname())
		}
	}
	return warnings, map[string]any{"title": title, "source_host": host}
}

// handleCancelJob is POST /recipes/jobs/{job_id}/cancel (#17): PENDING, RUNNING or COMPLETE
// become CANCELED (the answer carries no result); anything else is 409. The queue job is
// cancelled too, so pending work never starts (legacy ran it and skipped the write); a running
// handler finds the row CANCELED and skips its write (the guards in markComplete/markError).
func (m *Module) handleCancelJob(w http.ResponseWriter, r *http.Request, c caller) {
	ctx := r.Context()
	id := r.PathValue("job_id")
	pred, args := c.authorOnly("")
	now := ts(m.now())
	err := m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		var status string
		err := tx.QueryRowContext(ctx, `SELECT status FROM recipes_recipe_parse_jobs WHERE id = ? AND `+pred,
			append([]any{id}, args...)...).Scan(&status)
		if errors.Is(err, sql.ErrNoRows) {
			return &httpError{http.StatusNotFound, "Job not found"}
		}
		if err != nil {
			return err
		}
		if status != statusPending && status != statusRunning && status != statusComplete {
			return &httpError{http.StatusConflict, "Job cannot be canceled"}
		}
		_, err = tx.ExecContext(ctx, `UPDATE recipes_recipe_parse_jobs SET status = 'CANCELED', canceled_at = ?,
			updated_at = ? WHERE id = ?`, now, now, id)
		return err
	})
	if err != nil {
		m.writeErr(w, err)
		return
	}
	if m.deps.Queue != nil {
		if _, err := m.deps.Queue.CancelByDedupPrefix(ctx, "recipes:"+id); err != nil {
			m.deps.Log.Warn("recipes: cancel queue job", "parse_job_id", id, "err", err)
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"id": id, "status": statusCanceled, "result": nil, "error_code": nil, "error_message": nil,
		"next_action": nil, "next_action_reason": nil,
	})
}
