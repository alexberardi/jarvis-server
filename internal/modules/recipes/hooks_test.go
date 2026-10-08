package recipes

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/blob"
)

// seedLeaver gives user 2 (member of A) shared and private data in household A, plus jobs,
// imports and staged recipes in A and in their other household B.
func seedLeaver(t *testing.T, e *env) {
	t.Helper()
	e.hh.set(1, "A")
	e.hh.set(2, "A", "B")
	exp := ts(time.Now().Add(time.Hour))
	for _, q := range []string{
		`INSERT INTO recipes_users (user_id) VALUES ('1'), ('2')`,
		// Shared in A, authored by 2; another in A by 1; private (no household) by 2; in B by 2.
		`INSERT INTO recipes_recipes (id, user_id, household_id, title, image_url) VALUES
			(1, '2', 'A', 'shared by leaver', '/media/shared.jpg'),
			(2, '1', 'A', 'owner recipe', NULL),
			(3, '2', NULL, 'private', '/media/private.jpg'),
			(4, '2', 'B', 'in B', '/media/b.jpg'),
			(5, '1', 'A', 'copy of private photo', '/media/copied.jpg'),
			(6, '2', NULL, 'private copy', '/media/copied.jpg')`,
		`INSERT INTO recipes_ingredients (recipe_id, text) VALUES (1, 'x'), (3, 'y')`,
		`INSERT INTO recipes_meal_plans (id, user_id, household_id, start_date) VALUES
			(1, '2', 'A', '2026-10-01'), (2, '2', NULL, '2026-10-01'), (3, '2', 'B', '2026-10-01')`,
		`INSERT INTO recipes_meal_plan_items (meal_plan_id, recipe_id, date, meal_type) VALUES (1, 2, '2026-10-01', 'dinner')`,
		`INSERT INTO recipes_staples (user_id, household_id, name) VALUES ('2', 'A', 'salt'), ('2', NULL, 'oil'), ('2', 'B', 'pepper')`,
		`INSERT INTO recipes_grocery_sku_map (user_id, household_id, ingredient_name, sku) VALUES
			('2', 'A', 'milk', '1'), ('2', NULL, 'eggs', '2'), ('2', 'B', 'rice', '3')`,
		`INSERT INTO recipes_recipe_parse_jobs (id, user_id, household_id, job_type) VALUES
			('jA', '2', 'A', 'ingestion'), ('jB', '2', 'B', 'image'), ('j1', '1', 'A', 'ingestion')`,
		`INSERT INTO recipes_recipe_ingestions (id, user_id, household_id, image_s3_keys) VALUES
			('iA', '2', 'A', '["recipes/ingest/2/iA/0.jpg"]'), ('iB', '2', 'B', '["recipes/ingest/2/iB/0.jpg"]')`,
		`INSERT INTO recipes_stage_recipes (user_id, household_id, title, ingredients, steps, expires_at) VALUES
			('2', 'A', 'sA', '[]', '[]', '` + exp + `'), ('2', 'B', 'sB', '[]', '[]', '` + exp + `')`,
	} {
		e.exec(t, q)
	}
	for _, k := range []string{"recipes/ingest/2/iA/0.jpg", "recipes/ingest/2/iB/0.jpg", "recipes/media/shared.jpg",
		"recipes/media/private.jpg", "recipes/media/b.jpg", "recipes/media/copied.jpg"} {
		if _, err := e.blobs.Put(e.ctx, k, strings.NewReader("img"), "image/jpeg"); err != nil {
			t.Fatal(err)
		}
	}
}

func (e *env) hook(t *testing.T, fn func(ctx context.Context, tx *sql.Tx) error) {
	t.Helper()
	if err := e.d.Tx(e.ctx, func(tx *sql.Tx) error { return fn(e.ctx, tx) }); err != nil {
		t.Fatal(err)
	}
}

func (e *env) blobExists(t *testing.T, key string) bool {
	t.Helper()
	_, err := e.blobs.Stat(e.ctx, key)
	if errors.Is(err, blob.ErrNotFound) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	return true
}

// waitBlobGone waits for the purge job to delete key.
func (e *env) waitBlobGone(t *testing.T, key string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for e.blobExists(t, key) {
		if time.Now().After(deadline) {
			t.Fatalf("blob %s was not purged", key)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (e *env) titles(t *testing.T) string {
	t.Helper()
	rows, err := e.d.Read.Query(`SELECT title FROM recipes_recipes ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		out = append(out, s)
	}
	return strings.Join(out, "|")
}

func TestPurgeUser(t *testing.T) {
	e := setup(t)
	seedLeaver(t, e)
	e.hook(t, func(ctx context.Context, tx *sql.Tx) error { return e.m.PurgeUser(ctx, tx, 2) })

	// RD4: household rows stay (dangling author), private rows go.
	if got := e.titles(t); got != "shared by leaver|owner recipe|in B|copy of private photo" {
		t.Fatalf("recipes left: %s", got)
	}
	for q, want := range map[string]int{
		`SELECT COUNT(*) FROM recipes_meal_plans`:                    2,
		`SELECT COUNT(*) FROM recipes_meal_plan_items`:               1,
		`SELECT COUNT(*) FROM recipes_staples`:                       2,
		`SELECT COUNT(*) FROM recipes_grocery_sku_map`:               2,
		`SELECT COUNT(*) FROM recipes_recipe_parse_jobs`:             1, // owner's
		`SELECT COUNT(*) FROM recipes_recipe_ingestions`:             0,
		`SELECT COUNT(*) FROM recipes_stage_recipes`:                 0,
		`SELECT COUNT(*) FROM recipes_users WHERE user_id = '2'`:     0,
		`SELECT COUNT(*) FROM recipes_ingredients WHERE recipe_id=3`: 0,
	} {
		if n := e.count(t, q); n != want {
			t.Errorf("%s = %d, want %d", q, n, want)
		}
	}
	e.waitBlobGone(t, "recipes/ingest/2/iA/0.jpg")
	e.waitBlobGone(t, "recipes/ingest/2/iB/0.jpg")
	e.waitBlobGone(t, "recipes/media/private.jpg")
	// The shared recipes' photos stay, and so does a photo another recipe still uses.
	for _, k := range []string{"recipes/media/shared.jpg", "recipes/media/b.jpg", "recipes/media/copied.jpg"} {
		if !e.blobExists(t, k) {
			t.Errorf("%s should stay", k)
		}
	}
	// The household still sees the leaver's shared recipe.
	e.obj(t, 200, "GET", "/recipes/1", nil, tok(1, "A")...)
}

func TestPurgeUserHousehold(t *testing.T) {
	e := setup(t)
	seedLeaver(t, e)
	e.hook(t, func(ctx context.Context, tx *sql.Tx) error { return e.m.PurgeUserHousehold(ctx, tx, 2, "A") })
	e.hh.set(2, "B")

	if got := e.titles(t); got != "shared by leaver|owner recipe|private|in B|copy of private photo|private copy" {
		t.Fatalf("recipes left: %s", got)
	}
	for q, want := range map[string]int{
		`SELECT COUNT(*) FROM recipes_recipe_parse_jobs WHERE user_id = '2'`: 1, // jB
		`SELECT COUNT(*) FROM recipes_recipe_ingestions`:                     1, // iB
		`SELECT COUNT(*) FROM recipes_stage_recipes`:                         1, // sB
		`SELECT COUNT(*) FROM recipes_staples`:                               3,
		`SELECT COUNT(*) FROM recipes_users WHERE user_id = '2'`:             1,
	} {
		if n := e.count(t, q); n != want {
			t.Errorf("%s = %d, want %d", q, n, want)
		}
	}
	e.waitBlobGone(t, "recipes/ingest/2/iA/0.jpg")
	if !e.blobExists(t, "recipes/ingest/2/iB/0.jpg") {
		t.Fatalf("B's import must stay")
	}
	// The leaver keeps their private recipe and B's, and no longer sees A's.
	e.obj(t, 200, "GET", "/recipes/3", nil, tok(2, "B")...)
	e.obj(t, 200, "GET", "/recipes/4", nil, tok(2, "B")...)
	e.expectDetail(t, 404, "Recipe not found", "GET", "/recipes/1", nil, tok(2, "A")...)
	// A keeps it.
	e.obj(t, 200, "GET", "/recipes/1", nil, tok(1, "A")...)
}

func TestPurgeHousehold(t *testing.T) {
	e := setup(t)
	seedLeaver(t, e)
	e.hook(t, func(ctx context.Context, tx *sql.Tx) error { return e.m.PurgeHousehold(ctx, tx, "A") })

	if got := e.titles(t); got != "private|in B|private copy" {
		t.Fatalf("recipes left: %s", got)
	}
	for _, tbl := range []string{"recipes_meal_plans", "recipes_staples", "recipes_grocery_sku_map",
		"recipes_recipe_parse_jobs", "recipes_recipe_ingestions", "recipes_stage_recipes"} {
		if n := e.count(t, `SELECT COUNT(*) FROM `+tbl+` WHERE household_id = 'A'`); n != 0 {
			t.Errorf("%s: %d rows of A left", tbl, n)
		}
		if n := e.count(t, `SELECT COUNT(*) FROM `+tbl+` WHERE household_id = 'B'`); n == 0 {
			t.Errorf("%s: B's rows must stay", tbl)
		}
	}
	if n := e.count(t, `SELECT COUNT(*) FROM recipes_meal_plan_items`); n != 0 {
		t.Fatalf("items of A's plan go")
	}
	e.waitBlobGone(t, "recipes/ingest/2/iA/0.jpg")
	e.waitBlobGone(t, "recipes/media/shared.jpg")
	// copied.jpg is still used by the private copy (recipe 6).
	for _, k := range []string{"recipes/ingest/2/iB/0.jpg", "recipes/media/b.jpg", "recipes/media/private.jpg", "recipes/media/copied.jpg"} {
		if !e.blobExists(t, k) {
			t.Errorf("%s should stay", k)
		}
	}
}

// A deletion that fails later in auth's transaction rolls the hook back, purge job included.
func TestPurgeRollsBackWithTx(t *testing.T) {
	e := setup(t)
	seedLeaver(t, e)
	err := e.d.Tx(e.ctx, func(tx *sql.Tx) error {
		if err := e.m.PurgeUser(e.ctx, tx, 2); err != nil {
			return err
		}
		return errors.New("auth failed later")
	})
	if err == nil {
		t.Fatal("expected the error")
	}
	if n := e.count(t, `SELECT COUNT(*) FROM recipes_recipes WHERE user_id = '2'`); n != 4 {
		t.Fatalf("rollback: %d of the leaver's recipes", n)
	}
	time.Sleep(50 * time.Millisecond)
	if !e.blobExists(t, "recipes/media/private.jpg") {
		t.Fatalf("no purge job may run for a rolled-back deletion")
	}
}
