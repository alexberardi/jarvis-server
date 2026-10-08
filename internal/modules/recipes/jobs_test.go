package recipes

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
	"time"
)

// insertJob writes a parse-job row directly. completed is an offset from now (nil: NULL).
func (e *env) insertJob(t *testing.T, id, user, hh, jobType, status string, completedAgo *time.Duration, result any) {
	t.Helper()
	var res, done any
	if result != nil {
		raw, _ := json.Marshal(result)
		res = string(raw)
	}
	if completedAgo != nil {
		done = ts(time.Now().Add(-*completedAgo))
	}
	var hhv any
	if hh != "" {
		hhv = hh
	}
	e.exec(t, `INSERT INTO recipes_recipe_parse_jobs (id, user_id, household_id, job_type, status, result_json,
		completed_at, job_data) VALUES (?, ?, ?, ?, ?, ?, ?, '{}')`, id, user, hhv, jobType, status, res, done)
}

func ago(d time.Duration) *time.Duration { return &d }

func jobStatus(t *testing.T, e *env, id string) string {
	t.Helper()
	var s string
	if err := e.d.Read.QueryRowContext(e.ctx, `SELECT status FROM recipes_recipe_parse_jobs WHERE id = ?`, id).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func listIDs(t *testing.T, e *env, path string, h []string) []string {
	t.Helper()
	o := e.obj(t, http.StatusOK, http.MethodGet, path, nil, h...)
	var ids []string
	for _, j := range o["jobs"].([]any) {
		ids = append(ids, j.(map[string]any)["id"].(string))
	}
	return ids
}

func TestJobList(t *testing.T) {
	e := setup(t)
	e.hh.set(1, "A")
	e.hh.set(2, "A")
	h := tok(1, "A")
	draft := map[string]any{
		"recipe_draft": map[string]any{"title": "Soup", "source": map[string]any{"type": "url", "source_url": "https://Example.com/soup"}},
		"pipeline":     map[string]any{"warnings": []any{"LLM fallback used; please verify ingredients."}, "source_url": "https://example.com/soup"},
	}
	e.insertJob(t, "j-new", "1", "A", jobTypeIngestion, statusComplete, ago(time.Minute), draft)
	e.insertJob(t, "j-old", "1", "A", jobTypeIngestion, statusComplete, ago(4*24*time.Hour), draft)
	e.insertJob(t, "j-img", "1", "A", jobTypeImage, statusComplete, ago(2*time.Minute),
		map[string]any{"recipe_draft": map[string]any{"title": "Crepes", "source": map[string]any{"type": "ocr"}}, "pipeline": nil})
	e.insertJob(t, "j-plan", "1", "A", jobTypeMealPlan, statusComplete, ago(time.Minute), map[string]any{"result": map[string]any{}})
	e.insertJob(t, "j-sku", "1", "A", jobTypeGroceryMatch, statusComplete, ago(time.Minute), map[string]any{"learned": []any{}})
	e.insertJob(t, "j-err", "1", "A", jobTypeIngestion, statusError, ago(time.Hour), nil)
	e.insertJob(t, "j-pend", "1", "A", jobTypeIngestion, statusPending, nil, nil)
	e.insertJob(t, "j-other", "2", "A", jobTypeIngestion, statusComplete, ago(time.Minute), draft)

	// RD10: imports only; the window drops j-old; newest completion first; author only.
	if got := listIDs(t, e, "/recipes/parse-url/jobs", h); !reflect.DeepEqual(got, []string{"j-new", "j-img"}) {
		t.Fatalf("default list: %v", got)
	}
	if got := listIDs(t, e, "/recipes/parse-url/jobs?include_expired=true", h); !reflect.DeepEqual(got, []string{"j-new", "j-img", "j-old"}) {
		t.Fatalf("include_expired: %v", got)
	}
	if got := listIDs(t, e, "/recipes/parse-url/jobs?status=ERROR", h); !reflect.DeepEqual(got, []string{"j-err"}) {
		t.Fatalf("status=ERROR: %v", got)
	}
	// An empty status lists every status, no window; NULL completed_at last.
	if got := listIDs(t, e, "/recipes/parse-url/jobs?status=", h); !reflect.DeepEqual(got, []string{"j-new", "j-img", "j-err", "j-old", "j-pend"}) {
		t.Fatalf("status=: %v", got)
	}
	e.expectValidation(t, "query.include_expired", http.MethodGet, "/recipes/parse-url/jobs?include_expired=maybe", nil, h...)

	// B6 fixed: preview and warnings read recipe_draft and pipeline.
	o := e.obj(t, http.StatusOK, http.MethodGet, "/recipes/parse-url/jobs", nil, h...)
	first := o["jobs"].([]any)[0].(map[string]any)
	want := map[string]any{
		"id": "j-new", "job_type": "ingestion", "url": nil, "status": "COMPLETE", "completed_at": first["completed_at"],
		"warnings": []any{"LLM fallback used; please verify ingredients."},
		"preview":  map[string]any{"title": "Soup", "source_host": "example.com"},
	}
	if !reflect.DeepEqual(first, want) {
		t.Fatalf("row: %v", first)
	}
	img := o["jobs"].([]any)[1].(map[string]any)
	if !reflect.DeepEqual(img["preview"], map[string]any{"title": "Crepes", "source_host": nil}) || len(img["warnings"].([]any)) != 0 {
		t.Fatalf("image row: %v", img)
	}
	errRow := e.obj(t, http.StatusOK, http.MethodGet, "/recipes/parse-url/jobs?status=ERROR", nil, h...)["jobs"].([]any)[0].(map[string]any)
	if errRow["preview"] != nil {
		t.Fatalf("no result → null preview: %v", errRow)
	}
}

func TestCancelJob(t *testing.T) {
	e := setup(t)
	e.hh.set(1, "A")
	e.hh.set(2, "A")
	h, member := tok(1, "A"), tok(2, "A")
	c, err := e.m.resolve(e.ctx, authUser(1, "A"))
	if err != nil {
		t.Fatal(err)
	}
	// A real queued job (an unregistered type stays queued) is cancelled with its row.
	var id string
	if err := e.d.Tx(e.ctx, func(tx *sql.Tx) error {
		var err error
		id, err = e.m.createJob(e.ctx, tx, c, jobTypeIngestion, "recipes.test_never", map[string]any{})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	e.expectDetail(t, http.StatusNotFound, "Job not found", http.MethodPost, "/recipes/jobs/"+id+"/cancel", nil, member...)
	got := e.obj(t, http.StatusOK, http.MethodPost, "/recipes/jobs/"+id+"/cancel", nil, h...)
	want := map[string]any{"id": id, "status": "CANCELED", "result": nil, "error_code": nil, "error_message": nil,
		"next_action": nil, "next_action_reason": nil}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cancel: %v", got)
	}
	var qid int64
	if err := e.d.Read.QueryRowContext(e.ctx, `SELECT queue_job_id FROM recipes_recipe_parse_jobs WHERE id = ?`, id).Scan(&qid); err != nil {
		t.Fatal(err)
	}
	info, err := e.q.Get(e.ctx, qid)
	if err != nil || info.State != "cancelled" {
		t.Fatalf("queue job: %+v %v", info, err)
	}
	e.expectDetail(t, http.StatusConflict, "Job cannot be canceled", http.MethodPost, "/recipes/jobs/"+id+"/cancel", nil, h...)

	for st, code := range map[string]int{
		statusRunning: http.StatusOK, statusComplete: http.StatusOK, statusError: http.StatusConflict,
		statusCommitted: http.StatusConflict, statusAbandoned: http.StatusConflict, statusCanceled: http.StatusConflict,
	} {
		jid := "j-" + st
		e.insertJob(t, jid, "1", "A", jobTypeIngestion, st, nil, map[string]any{"recipe_draft": map[string]any{}})
		e.json(t, code, http.MethodPost, "/recipes/jobs/"+jid+"/cancel", nil, h...)
		if code == http.StatusOK && jobStatus(t, e, jid) != statusCanceled {
			t.Fatalf("%s not canceled", st)
		}
	}
	// A handler finishing after the cancel does not resurrect the job.
	if err := e.m.markComplete(e.ctx, "j-RUNNING", map[string]any{"x": 1}); err != nil {
		t.Fatal(err)
	}
	if err := e.m.markError(e.ctx, "j-COMPLETE", "x", "y"); err != nil {
		t.Fatal(err)
	}
	if jobStatus(t, e, "j-RUNNING") != statusCanceled || jobStatus(t, e, "j-COMPLETE") != statusCanceled {
		t.Fatal("a canceled job was overwritten")
	}
}

func TestCommitWithJob(t *testing.T) {
	e := setup(t)
	e.hh.set(1, "A")
	e.hh.set(2, "A")
	h, member := tok(1, "A"), tok(2, "A")
	e.insertJob(t, "j1", "1", "A", jobTypeIngestion, statusComplete, ago(time.Minute), map[string]any{"recipe_draft": map[string]any{}})
	e.insertJob(t, "j2", "1", "A", jobTypeIngestion, statusRunning, nil, nil)
	e.expectDetail(t, http.StatusNotFound, "Parse job not found", http.MethodPost, "/recipes", recipeBody("x", map[string]any{"parse_job_id": "j1"}), member...)
	e.expectDetail(t, http.StatusConflict, "Parse job not ready", http.MethodPost, "/recipes", recipeBody("x", map[string]any{"parse_job_id": "j2"}), h...)
	e.create(t, h, recipeBody("x", map[string]any{"parse_job_id": "j1"}))
	if jobStatus(t, e, "j1") != statusCommitted || e.count(t, `SELECT COUNT(*) FROM recipes_recipe_parse_jobs WHERE id = 'j1' AND committed_at IS NOT NULL`) != 1 {
		t.Fatal("not committed")
	}
	e.expectDetail(t, http.StatusConflict, "Parse job not ready", http.MethodPost, "/recipes", recipeBody("x", map[string]any{"parse_job_id": "j1"}), h...)
	if e.count(t, `SELECT COUNT(*) FROM recipes_recipes`) != 1 {
		t.Fatal("a refused commit created a recipe")
	}
}

func TestCleanup(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	now := time.Now()
	old := ts(now.Add(-31 * 24 * time.Hour))
	e.exec(t, `INSERT INTO recipes_users (user_id) VALUES ('1')`)

	// Abandon: COMPLETE older than 3 days; ERROR/COMMITTED untouched.
	e.insertJob(t, "c-old", "1", "A", jobTypeIngestion, statusComplete, ago(4*24*time.Hour), nil)
	e.insertJob(t, "c-new", "1", "A", jobTypeIngestion, statusComplete, ago(time.Hour), nil)
	e.insertJob(t, "e-old", "1", "A", jobTypeIngestion, statusError, ago(4*24*time.Hour), nil)

	// Stage: expired rows go; their COMPLETE meal-plan job is abandoned (B21: not ERROR/COMMITTED).
	e.exec(t, `INSERT INTO recipes_stage_recipes (user_id, title, ingredients, steps, request_id, expires_at)
		VALUES ('1', 'gone', '[]', '[]', 'req-1', ?), ('1', 'kept', '[]', '[]', 'req-2', ?)`,
		ts(now.Add(-time.Minute)), ts(now.Add(time.Hour)))
	for id, st := range map[string]string{"p-complete": statusComplete, "p-error": statusError, "p-committed": statusCommitted} {
		e.exec(t, `INSERT INTO recipes_recipe_parse_jobs (id, user_id, job_type, status, job_data, completed_at)
			VALUES (?, '1', ?, ?, '{"request_id":"req-1"}', ?)`, id, jobTypeMealPlan, st, ts(now))
	}

	// Reaper: RUNNING past twice the lease → ERROR worker_lost.
	e.exec(t, `INSERT INTO recipes_recipe_parse_jobs (id, user_id, job_type, status, started_at) VALUES
		('r-stuck', '1', ?, 'RUNNING', ?), ('r-live', '1', ?, 'RUNNING', ?)`,
		jobTypeIngestion, ts(now.Add(-11*time.Minute)), jobTypeImage, ts(now.Add(-11*time.Minute)))

	// Old finished jobs go after 30 days.
	e.exec(t, `INSERT INTO recipes_recipe_parse_jobs (id, user_id, job_type, status, updated_at) VALUES
		('d-old', '1', ?, 'COMMITTED', ?)`, jobTypeIngestion, old)

	// Photo imports: originals and readings go after 30 days.
	put := func(key string) {
		if _, err := e.blobs.Put(ctx, key, bytes.NewReader([]byte("x")), "image/jpeg"); err != nil {
			t.Fatal(err)
		}
	}
	put("recipes/ingest/1/i-old/0.jpg")
	put("recipes/ingest/1/i-new/0.jpg")
	e.exec(t, `INSERT INTO recipes_recipe_ingestions (id, user_id, image_s3_keys, ocr_readings, created_at) VALUES
		('i-old', '1', '["recipes/ingest/1/i-old/0.jpg"]', '[]', ?), ('i-new', '1', '["recipes/ingest/1/i-new/0.jpg"]', '[]', ?)`,
		old, ts(now))

	// Editor uploads: a referenced one stays; an unreferenced one goes once a day old.
	put(mediaBlobPrefix + "used.jpg")
	put(mediaBlobPrefix + "orphan.jpg")
	e.exec(t, `INSERT INTO recipes_recipes (user_id, title, image_url) VALUES ('1', 'r', '/media/used.jpg')`)

	r := e.m.cleanup(ctx)
	// The media sweep sees fresh uploads (written just now): nothing deleted yet.
	if r.MediaDeleted != 0 {
		t.Fatalf("fresh media deleted: %+v", r)
	}
	want := cleanupResult{Abandoned: 1, StageDeleted: 1, StageAbandons: 1, Reaped: 1, Originals: 1, JobsDeleted: 1}
	if r != want {
		t.Fatalf("cleanup: %+v, want %+v", r, want)
	}
	for id, st := range map[string]string{
		"c-old": statusAbandoned, "c-new": statusComplete, "e-old": statusError, "p-complete": statusAbandoned,
		"p-error": statusError, "p-committed": statusCommitted, "r-stuck": statusError, "r-live": statusRunning,
	} {
		if got := jobStatus(t, e, id); got != st {
			t.Errorf("%s: %s, want %s", id, got, st)
		}
	}
	if e.count(t, `SELECT COUNT(*) FROM recipes_recipe_parse_jobs WHERE id = 'r-stuck' AND error_code = 'worker_lost'`) != 1 {
		t.Error("reaped without worker_lost")
	}
	if e.count(t, `SELECT COUNT(*) FROM recipes_stage_recipes`) != 1 || e.count(t, `SELECT COUNT(*) FROM recipes_recipe_ingestions`) != 1 {
		t.Error("stage/ingestion rows")
	}
	if _, err := e.blobs.Stat(ctx, "recipes/ingest/1/i-old/0.jpg"); err == nil {
		t.Error("old original kept")
	}
	if _, err := e.blobs.Stat(ctx, "recipes/ingest/1/i-new/0.jpg"); err != nil {
		t.Error("new original deleted")
	}

	// A day later the orphan goes and the used photo stays.
	n, err := e.m.purgeOrphanMedia(ctx, now.Add(25*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("orphan sweep: %d %v", n, err)
	}
	if _, err := e.blobs.Stat(ctx, mediaBlobPrefix+"used.jpg"); err != nil {
		t.Error("used photo deleted")
	}
	if _, err := e.blobs.Stat(ctx, mediaBlobPrefix+"orphan.jpg"); err == nil {
		t.Error("orphan kept")
	}
}
