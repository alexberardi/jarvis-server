package recipes

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/queue"
	"github.com/alexberardi/jarvis-server/internal/platform/scheduler"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// The hourly recipes.cleanup job (§6; legacy scripts/run_cleanup.py, which nothing scheduled).
// Each pass:
//
//   - abandons COMPLETE jobs never committed within parse_job.abandon_minutes (ABANDONED);
//   - deletes expired stage recipes (72 h) and abandons the COMPLETE meal-plan jobs that staged
//     them (B21: legacy compared against "COMPLETED" and so abandoned ERROR/COMMITTED ones);
//   - reaps jobs stuck RUNNING past twice their queue lease → ERROR worker_lost (legacy never did);
//   - deletes photo imports (originals and OCR readings) 30 days after the import (RD6);
//   - deletes finished parse-job rows 30 days after they finished (nothing lists them by then);
//   - deletes editor uploads (recipes/media/*) more than a day old that no recipe shows (an
//     upload whose recipe was never saved; R3 left them forever).
//
// Every step is independent: one failing is logged and the others still run.

const (
	cleanupJobType = "recipes.cleanup"
	cleanupEvery   = time.Hour
	// retention is how long photo-import originals, OCR readings and finished job rows are kept.
	retention = 30 * 24 * time.Hour
	// orphanMediaAge is how old an unreferenced editor upload must be before it is deleted, so
	// a photo uploaded in an editor session that is still open survives.
	orphanMediaAge = 24 * time.Hour
)

// Queue leases per parse-job type; the reaper gives a RUNNING job twice its lease.
const (
	ingestLease   = 5 * time.Minute  // P1 + P1r with a model still loading
	imageLease    = 15 * time.Minute // OCR over 8 photos, then P2/P1r/P3
	mealPlanLease = 30 * time.Minute // one P4 call per slot, a week of slots
)

func jobLease(jobType string) time.Duration {
	switch jobType {
	case jobTypeIngestion:
		return ingestLease
	case jobTypeImage:
		return imageLease
	case jobTypeMealPlan:
		return mealPlanLease
	case jobTypeGroceryMatch:
		return matchTimeout + time.Minute
	}
	return mealPlanLease
}

// startCleanup schedules the hourly pass.
func (m *Module) startCleanup(ctx context.Context) error {
	if m.deps.Scheduler == nil || m.deps.Queue == nil {
		return nil
	}
	return m.deps.Scheduler.Ensure(ctx, scheduler.Trigger{
		Name: cleanupJobType, Kind: scheduler.KindInterval, JobType: cleanupJobType,
		Spec: scheduler.Spec{Every: cleanupEvery, StartNow: true},
	})
}

func (m *Module) runCleanup(ctx context.Context, _ queue.Job) ([]byte, error) {
	res := m.cleanup(ctx)
	out, _ := json.Marshal(res)
	return out, nil
}

// cleanupResult counts what one pass did.
type cleanupResult struct {
	Abandoned     int64 `json:"abandoned"`
	StageDeleted  int64 `json:"stage_deleted"`
	StageAbandons int64 `json:"stage_jobs_abandoned"`
	Reaped        int64 `json:"reaped"`
	Originals     int64 `json:"originals_deleted"`
	JobsDeleted   int64 `json:"jobs_deleted"`
	MediaDeleted  int64 `json:"media_deleted"`
}

func (m *Module) cleanup(ctx context.Context) cleanupResult {
	var r cleanupResult
	now := m.now()
	step := func(name string, f func() error) {
		if err := f(); err != nil && !errors.Is(err, context.Canceled) {
			m.deps.Log.Error("recipes: cleanup step failed", "step", name, "err", err)
		}
	}
	step("abandon", func() (err error) { r.Abandoned, err = m.abandonStale(ctx, now); return })
	step("stage", func() (err error) { r.StageDeleted, r.StageAbandons, err = m.purgeExpiredStage(ctx, now); return })
	step("reap", func() (err error) { r.Reaped, err = m.reapRunning(ctx, now); return })
	step("originals", func() (err error) { r.Originals, err = m.purgeOriginals(ctx, now); return })
	step("jobs", func() (err error) { r.JobsDeleted, err = m.purgeOldJobs(ctx, now); return })
	step("media", func() (err error) { r.MediaDeleted, err = m.purgeOrphanMedia(ctx, now); return })
	if r != (cleanupResult{}) {
		m.deps.Log.Info("recipes: cleanup", "abandoned", r.Abandoned, "stage_deleted", r.StageDeleted,
			"stage_jobs_abandoned", r.StageAbandons, "reaped", r.Reaped, "originals_deleted", r.Originals,
			"jobs_deleted", r.JobsDeleted, "media_deleted", r.MediaDeleted)
	}
	return r
}

// abandonStale is abandon_stale_jobs: COMPLETE jobs older than parse_job.abandon_minutes.
func (m *Module) abandonStale(ctx context.Context, now time.Time) (int64, error) {
	mins := m.settings.Int(ctx, SettingAbandonMinutes, settings.Scope{})
	res, err := m.deps.DB.Write.ExecContext(ctx, `UPDATE recipes_recipe_parse_jobs SET status = 'ABANDONED',
		abandoned_at = ?, updated_at = ? WHERE status = 'COMPLETE' AND completed_at IS NOT NULL AND completed_at < ?`,
		ts(now), ts(now), ts(now.Add(-time.Duration(mins)*time.Minute)))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// purgeExpiredStage is cleanup_expired_stage_recipes(mark_jobs=True), with B21 fixed: only
// COMPLETE meal-plan jobs whose staged recipes expired are abandoned.
func (m *Module) purgeExpiredStage(ctx context.Context, now time.Time) (deleted, abandoned int64, err error) {
	err = m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `DELETE FROM recipes_stage_recipes WHERE expires_at <= ? RETURNING request_id`, ts(now))
		if err != nil {
			return err
		}
		reqs := map[string]bool{}
		for rows.Next() {
			var rid sql.NullString
			if err := rows.Scan(&rid); err != nil {
				rows.Close()
				return err
			}
			deleted++
			if rid.Valid && rid.String != "" {
				reqs[rid.String] = true
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for rid := range reqs {
			res, err := tx.ExecContext(ctx, `UPDATE recipes_recipe_parse_jobs SET status = 'ABANDONED', abandoned_at = ?,
				updated_at = ? WHERE job_type = ? AND status = 'COMPLETE' AND json_extract(job_data, '$.request_id') = ?`,
				ts(now), ts(now), jobTypeMealPlan, rid)
			if err != nil {
				return err
			}
			n, _ := res.RowsAffected()
			abandoned += n
		}
		return nil
	})
	return deleted, abandoned, err
}

// reapRunning fails jobs RUNNING longer than twice their lease: the process died mid-job and
// the queue gave up on it, or the handler lost track of the row.
func (m *Module) reapRunning(ctx context.Context, now time.Time) (int64, error) {
	var total int64
	for _, jt := range []string{jobTypeIngestion, jobTypeImage, jobTypeMealPlan, jobTypeGroceryMatch} {
		res, err := m.deps.DB.Write.ExecContext(ctx, `UPDATE recipes_recipe_parse_jobs SET status = 'ERROR',
			error_code = 'worker_lost', error_message = 'The job stopped responding and was abandoned.',
			completed_at = ?, updated_at = ? WHERE job_type = ? AND status = 'RUNNING' AND started_at < ?`,
			ts(now), ts(now), jt, ts(now.Add(-2*jobLease(jt))))
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
	}
	return total, nil
}

// purgeOriginals deletes photo imports older than the retention (RD6): the originals in the
// blob store, then the row with its OCR readings.
func (m *Module) purgeOriginals(ctx context.Context, now time.Time) (int64, error) {
	rows, err := m.deps.DB.Read.QueryContext(ctx, `SELECT id, image_s3_keys FROM recipes_recipe_ingestions
		WHERE created_at < ?`, ts(now.Add(-retention)))
	if err != nil {
		return 0, err
	}
	type ing struct {
		id   string
		keys []string
	}
	var ings []ing
	for rows.Next() {
		var i ing
		var raw string
		if err := rows.Scan(&i.id, &raw); err != nil {
			rows.Close()
			return 0, err
		}
		_ = json.Unmarshal([]byte(raw), &i.keys)
		ings = append(ings, i)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	var n int64
	for _, i := range ings {
		if m.deps.Blobs != nil {
			for _, k := range i.keys {
				if err := m.deps.Blobs.Delete(ctx, k); err != nil {
					return n, err
				}
			}
		}
		if _, err := m.deps.DB.Write.ExecContext(ctx, `DELETE FROM recipes_recipe_ingestions WHERE id = ?`, i.id); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// purgeOldJobs deletes parse-job rows that finished more than the retention ago (terminal, or
// COMPLETE and never abandoned because the setting is longer).
func (m *Module) purgeOldJobs(ctx context.Context, now time.Time) (int64, error) {
	res, err := m.deps.DB.Write.ExecContext(ctx, `DELETE FROM recipes_recipe_parse_jobs
		WHERE status IN ('ERROR', 'CANCELED', 'COMMITTED', 'ABANDONED', 'COMPLETE') AND updated_at < ?`,
		ts(now.Add(-retention)))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// purgeOrphanMedia deletes editor uploads older than a day that no recipe shows.
func (m *Module) purgeOrphanMedia(ctx context.Context, now time.Time) (int64, error) {
	if m.deps.Blobs == nil {
		return 0, nil
	}
	objs, err := m.deps.Blobs.List(ctx, mediaBlobPrefix)
	if err != nil {
		return 0, err
	}
	var n int64
	for _, o := range objs {
		if !o.ModTime.Before(now.Add(-orphanMediaAge)) {
			continue
		}
		name := strings.TrimPrefix(o.Key, mediaBlobPrefix)
		var used int
		if err := m.deps.DB.Read.QueryRowContext(ctx, `SELECT COUNT(*) FROM recipes_recipes WHERE image_url = ?`,
			mediaPrefix+name).Scan(&used); err != nil {
			return n, err
		}
		if used > 0 {
			continue
		}
		if err := m.deps.Blobs.Delete(ctx, o.Key); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
