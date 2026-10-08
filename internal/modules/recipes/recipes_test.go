package recipes

import (
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestHealthAndInfo(t *testing.T) {
	e := setup(t)
	if o := e.obj(t, 200, "GET", "/health", nil); len(o) != 1 || o["status"] != "ok" {
		t.Fatalf("health %v", o)
	}
	if o := e.obj(t, 200, "GET", "/info", nil); o["service"] != "jarvis-recipes-server" {
		t.Fatalf("info %v", o)
	}
}

func TestAuth(t *testing.T) {
	e := setup(t)
	for _, p := range []struct{ method, path string }{
		{"GET", "/recipes"}, {"POST", "/recipes"}, {"GET", "/recipes/1"}, {"PATCH", "/recipes/1"},
		{"DELETE", "/recipes/1"}, {"GET", "/recipes/user/1"}, {"GET", "/recipes/stage/1"}, {"GET", "/tags"},
		{"POST", "/tags"}, {"GET", "/ingredients/stock"}, {"GET", "/units/stock"}, {"GET", "/recipes/jobs"},
	} {
		c, b := e.do(t, p.method, p.path, nil)
		if c != 401 || !strings.Contains(b, `"Not authenticated"`) {
			t.Fatalf("%s %s no token: %d %s", p.method, p.path, c, b)
		}
		e.expectDetail(t, 401, "Not authenticated", p.method, p.path, nil, "Authorization", "Basic Zm9vOmJhcg==")
		e.expectDetail(t, 401, "Invalid or expired token", p.method, p.path, nil, "Authorization", "Bearer nope")
	}
	r := wwwAuth(t, e, "GET", "/recipes")
	if r != "Bearer" {
		t.Fatalf("WWW-Authenticate %q", r)
	}
	e.expectDetail(t, 500, "Internal Server Error", "GET", "/recipes", nil, "Authorization", "Bearer boom")
	// #8: no auth, always 404.
	e.expectDetail(t, 404, "Core recipe not found", "GET", "/recipes/core/1", nil)
	e.expectDetail(t, 404, "Core recipe not found", "GET", "/recipes/core/abc", nil, tok(1, "A")...)
}

func wwwAuth(t *testing.T, e *env, method, p string) string {
	t.Helper()
	req, _ := http.NewRequest(method, p, nil)
	rec := &headerRecorder{h: http.Header{}}
	e.h.ServeHTTP(rec, req)
	return rec.h.Get("WWW-Authenticate")
}

type headerRecorder struct {
	h    http.Header
	code int
}

func (r *headerRecorder) Header() http.Header         { return r.h }
func (r *headerRecorder) Write(b []byte) (int, error) { return len(b), nil }
func (r *headerRecorder) WriteHeader(c int)           { r.code = c }

func TestRecipeCRUD(t *testing.T) {
	e := setup(t)
	e.hh.set(1, "A")
	h := tok(1, "A")

	r := e.create(t, h, map[string]any{
		"title": "pancakes", "description": "fluffy", "servings": "4", "prep_time_minutes": 10, "cook_time_minutes": 20,
		"source_type": "url", "source_url": "https://example.com/p", "image_url": "https://example.com/p.jpg",
		"ingredients": []map[string]any{
			{"text": "1 1/2 cups flour", "quantity_display": "1 1/2", "quantity_value": "99", "unit": "cups"},
			{"text": "1/2 tsp salt", "quantity_display": "1/2", "unit": "tsp"},
			{"text": "2 eggs", "quantity_display": "2"},
			{"text": "milk"},
			{"text": "nothing", "quantity_display": "1/0"},
			{"text": "nan", "quantity_display": "NaN"},
			{"text": "third", "quantity_display": "1/3"},
		},
		"steps": []map[string]any{{"step_number": 2, "text": "Cook."}, {"step_number": 1, "text": "Mix."}},
		"tags":  []string{"tag-b", "Tag-A", "tag-b"},
	})
	id := idOf(r)
	if r["user_id"] != "1" || r["title"] != "pancakes" || r["servings"] != 4.0 || r["source_type"] != "url" {
		t.Fatalf("create: %v", r)
	}
	// RD5: prep/cook are stored; total folds from them when absent.
	if r["prep_time_minutes"] != 10.0 || r["cook_time_minutes"] != 20.0 || r["total_time_minutes"] != 30.0 {
		t.Fatalf("times: %v %v %v", r["prep_time_minutes"], r["cook_time_minutes"], r["total_time_minutes"])
	}
	var qv []any
	for _, i := range r["ingredients"].([]any) {
		qv = append(qv, i.(map[string]any)["quantity_value"])
	}
	if fmt.Sprint(qv) != "[1.5000 0.5000 2.0000 <nil> <nil> <nil> 0.3333]" {
		t.Fatalf("quantity_value: %v", qv)
	}
	if ing := r["ingredients"].([]any)[0].(map[string]any); ing["text"] != "1 1/2 cups flour" || ing["unit"] != "cups" {
		t.Fatalf("ingredient order: %v", ing)
	}
	steps := r["steps"].([]any)
	if steps[0].(map[string]any)["text"] != "Mix." || steps[1].(map[string]any)["step_number"] != 2.0 {
		t.Fatalf("steps by number: %v", steps)
	}
	tags := r["tags"].([]any)
	if len(tags) != 2 || tags[0].(map[string]any)["name"] != "tag-b" || tags[1].(map[string]any)["name"] != "Tag-A" {
		t.Fatalf("tags in insertion order, deduped: %v", tags)
	}
	ts, _ := r["created_at"].(string)
	if !regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d{6})?$`).MatchString(ts) {
		t.Fatalf("created_at %q is not a naive timestamp", ts)
	}

	// #1: newest first.
	r2 := e.create(t, h, recipeBody("second", nil))
	if r2["source_type"] != "manual" || r2["prep_time_minutes"] != nil || r2["total_time_minutes"] != nil {
		t.Fatalf("defaults: %v", r2)
	}
	l := e.list(t, "/recipes", h...)
	if len(l) != 2 || idOf(l[0].(map[string]any)) != idOf(r2) || idOf(l[1].(map[string]any)) != id {
		t.Fatalf("list order: %v", l)
	}

	// #3
	e.obj(t, 200, "GET", path("/recipes/%d", id), nil, h...)
	e.expectDetail(t, 404, "Recipe not found", "GET", "/recipes/999", nil, h...)
	e.expectValidation(t, "path.recipe_id", "GET", "/recipes/abc", nil, h...)
	e.expectValidation(t, "path.recipe_id", "GET", "/recipes/jobs", nil, h...) // #16 not registered

	// #4: null leaves alone, lists replace, prep/cook fold only when total is absent.
	p := e.obj(t, 200, "PATCH", path("/recipes/%d", id), map[string]any{
		"title": "pancakes v2", "description": nil, "prep_time_minutes": 5,
		"ingredients": []map[string]any{{"text": "3 eggs", "quantity_display": "3"}},
		"steps":       []map[string]any{{"step_number": 1, "text": "Whisk."}, {"step_number": 2, "text": "Fry."}},
		"tags":        []string{"tag-a"},
	}, h...)
	if p["title"] != "pancakes v2" || p["description"] != "fluffy" || p["total_time_minutes"] != 5.0 ||
		p["prep_time_minutes"] != 5.0 || p["cook_time_minutes"] != 20.0 || p["servings"] != 4.0 {
		t.Fatalf("patch: %v", p)
	}
	if len(p["ingredients"].([]any)) != 1 || len(p["steps"].([]any)) != 2 || len(p["tags"].([]any)) != 1 {
		t.Fatalf("patch replaces lists: %v", p)
	}
	if p["tags"].([]any)[0].(map[string]any)["name"] != "Tag-A" {
		t.Fatalf("tags match case-insensitively: %v", p["tags"])
	}
	p = e.obj(t, 200, "PATCH", path("/recipes/%d", id), map[string]any{"total_time_minutes": 45, "cook_time_minutes": 99, "tags": []string{}}, h...)
	if p["total_time_minutes"] != 45.0 || p["cook_time_minutes"] != 99.0 || len(p["tags"].([]any)) != 0 || len(p["ingredients"].([]any)) != 1 {
		t.Fatalf("patch 2: %v", p)
	}
	e.expectDetail(t, 404, "Recipe not found", "PATCH", "/recipes/999", map[string]any{"title": "x"}, h...)
	e.expectValidation(t, "body.source_type", "PATCH", path("/recipes/%d", id), map[string]any{"source_type": "URL"}, h...)

	// #5
	if c, b := e.do(t, "DELETE", path("/recipes/%d", idOf(r2)), nil, h...); c != 204 || b != "" {
		t.Fatalf("delete: %d %q", c, b)
	}
	e.expectDetail(t, 404, "Recipe not found", "GET", path("/recipes/%d", idOf(r2)), nil, h...)
	e.expectDetail(t, 404, "Recipe not found", "DELETE", path("/recipes/%d", idOf(r2)), nil, h...)
	if n := e.count(t, `SELECT COUNT(*) FROM recipes_ingredients WHERE recipe_id = ?`, idOf(r2)); n != 0 {
		t.Fatalf("ingredients cascade: %d left", n)
	}
}

func TestDeleteRecipeUsedByPlan(t *testing.T) {
	e := setup(t)
	h := tok(1, "A")
	e.hh.set(1, "A")
	r := e.create(t, h, recipeBody("planned", nil))
	e.exec(t, `INSERT INTO recipes_meal_plans (id, user_id, household_id, start_date) VALUES (7, '1', 'A', '2026-10-01')`)
	e.exec(t, `INSERT INTO recipes_meal_plan_items (meal_plan_id, recipe_id, date, meal_type) VALUES (7, ?, '2026-10-01', 'dinner')`, idOf(r))
	if c, _ := e.do(t, "DELETE", path("/recipes/%d", idOf(r)), nil, h...); c != 204 {
		t.Fatalf("delete: %d", c)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM recipes_meal_plan_items`); n != 0 {
		t.Fatalf("plan item should go with the recipe, %d left", n)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM recipes_meal_plans`); n != 1 {
		t.Fatalf("the plan stays")
	}
}

func TestValidation(t *testing.T) {
	e := setup(t)
	h := tok(1, "")
	noIng := recipeBody("x", nil)
	delete(noIng, "ingredients")
	e.expectValidation(t, "body.ingredients", "POST", "/recipes", noIng, h...)
	e.expectValidation(t, "body.ingredients", "POST", "/recipes", recipeBody("x", map[string]any{"ingredients": []any{}}), h...)
	e.expectValidation(t, "body.steps", "POST", "/recipes", recipeBody("x", map[string]any{"steps": []any{}}), h...)
	e.expectValidation(t, "body.source_type", "POST", "/recipes", recipeBody("x", map[string]any{"source_type": "URL"}), h...)
	e.expectValidation(t, "body.source_type", "POST", "/recipes", recipeBody("x", map[string]any{"source_type": nil}), h...)
	e.expectValidation(t, "body.title", "POST", "/recipes", recipeBody("x", map[string]any{"title": 5}), h...)
	e.expectValidation(t, "body.servings", "POST", "/recipes", recipeBody("x", map[string]any{"servings": "four"}), h...)
	e.expectValidation(t, "body.servings", "POST", "/recipes", recipeBody("x", map[string]any{"servings": 1.5}), h...)
	e.expectValidation(t, "body.ingredients.0.text", "POST", "/recipes", recipeBody("x", map[string]any{"ingredients": []any{map[string]any{}}}), h...)
	e.expectValidation(t, "body.ingredients.0", "POST", "/recipes", recipeBody("x", map[string]any{"ingredients": []any{"flour"}}), h...)
	e.expectValidation(t, "body.ingredients.0.quantity_value", "POST", "/recipes",
		recipeBody("x", map[string]any{"ingredients": []any{map[string]any{"text": "a", "quantity_value": "lots"}}}), h...)
	e.expectValidation(t, "body.steps.0.step_number", "POST", "/recipes", recipeBody("x", map[string]any{"steps": []any{map[string]any{"text": "a"}}}), h...)
	e.expectValidation(t, "body.steps", "POST", "/recipes", recipeBody("x", map[string]any{"steps": []any{
		map[string]any{"step_number": 1, "text": "a"}, map[string]any{"step_number": 1, "text": "b"}}}), h...)
	e.expectValidation(t, "body.tags", "POST", "/recipes", recipeBody("x", map[string]any{"tags": nil}), h...)
	e.expectValidation(t, "body.tags.1", "POST", "/recipes", recipeBody("x", map[string]any{"tags": []any{"a", 2}}), h...)
	e.expectValidation(t, "body", "POST", "/recipes", "", h...)
	e.expectValidation(t, "body", "POST", "/recipes", "[]", h...)
	o := e.obj(t, 422, "POST", "/recipes", "{not json", h...)
	if o["error_code"] != "validation_error" || !strings.HasPrefix(o["details"].([]any)[0].(map[string]any)["field"].(string), "body.") {
		t.Fatalf("bad json 422: %v", o)
	}
	a := e.obj(t, 422, "GET", "/recipes/abc", nil, h...)["job_id"]
	b := e.obj(t, 422, "GET", "/recipes/abc", nil, h...)["job_id"]
	if a == b {
		t.Fatalf("job_id must be fresh per response")
	}
	// A string step_number and an int-like string servings are accepted (pydantic lax mode).
	r := e.create(t, h, recipeBody("lax", map[string]any{"servings": " 3 ", "steps": []any{map[string]any{"step_number": "1", "text": "a"}}}))
	if r["servings"] != 3.0 {
		t.Fatalf("lax int: %v", r["servings"])
	}
	// An empty tag name is the legacy 400 (raised by the get-or-create, so nothing is written).
	e.expectDetail(t, 400, "Tag name required", "POST", "/recipes", recipeBody("x", map[string]any{"tags": []string{" "}}), h...)
	if n := e.count(t, `SELECT COUNT(*) FROM recipes_recipes WHERE title = 'x'`); n != 0 {
		t.Fatalf("a failed create must roll back")
	}
}

func TestScoping(t *testing.T) {
	e := setup(t)
	// owner 1 in A; member 2 in B (solo, first) and A; outsider 3 in C.
	e.hh.set(1, "A")
	e.hh.set(2, "B", "A")
	e.hh.set(3, "C")
	owner, member, memberSolo, outsider := tok(1, "A"), tok(2, "A"), tok(2, "B"), tok(3, "C")

	r := e.create(t, owner, recipeBody("shared", nil))
	p := path("/recipes/%d", idOf(r))

	// Member sees and edits it, through either token (RD7).
	for _, h := range [][]string{member, memberSolo} {
		e.obj(t, 200, "GET", p, nil, h...)
		if o := e.obj(t, 200, "PATCH", p, map[string]any{"description": "by member"}, h...); o["user_id"] != "1" {
			t.Fatalf("authorship stays: %v", o)
		}
	}
	// #6 author only.
	e.obj(t, 200, "GET", path("/recipes/user/%d", idOf(r)), nil, owner...)
	e.expectDetail(t, 404, "Recipe not found", "GET", path("/recipes/user/%d", idOf(r)), nil, member...)
	e.expectValidation(t, "path.recipe_id", "GET", "/recipes/user/x", nil, owner...)

	// Outsider: 404 everywhere.
	if len(e.list(t, "/recipes", outsider...)) != 0 {
		t.Fatalf("outsider sees nothing")
	}
	e.expectDetail(t, 404, "Recipe not found", "GET", p, nil, outsider...)
	e.expectDetail(t, 404, "Recipe not found", "PATCH", p, map[string]any{"title": "x"}, outsider...)
	e.expectDetail(t, 404, "Recipe not found", "DELETE", p, nil, outsider...)
	e.expectDetail(t, 404, "Recipe not found", "GET", path("/recipes/user/%d", idOf(r)), nil, outsider...)

	// Writes go to the token's household.
	solo := e.create(t, memberSolo, recipeBody("member solo", nil))
	e.expectDetail(t, 404, "Recipe not found", "GET", path("/recipes/%d", idOf(solo)), nil, owner...)
	var hh string
	_ = e.d.Read.QueryRow(`SELECT household_id FROM recipes_recipes WHERE id = ?`, idOf(solo)).Scan(&hh)
	if hh != "B" {
		t.Fatalf("written to %q, want the token's household B", hh)
	}
	// The member's list is the union of both households.
	ids := map[int64]bool{}
	for _, x := range e.list(t, "/recipes", member...) {
		ids[idOf(x.(map[string]any))] = true
	}
	if !ids[idOf(r)] || !ids[idOf(solo)] || len(ids) != 2 {
		t.Fatalf("union: %v", ids)
	}
	// A stale claim for a household the user left writes to their first remaining one.
	e.hh.set(2, "B")
	stale := e.create(t, member, recipeBody("stale claim", nil))
	_ = e.d.Read.QueryRow(`SELECT household_id FROM recipes_recipes WHERE id = ?`, idOf(stale)).Scan(&hh)
	if hh != "B" {
		t.Fatalf("stale claim wrote to %q", hh)
	}
	// ...and no longer sees A's recipe.
	e.expectDetail(t, 404, "Recipe not found", "GET", p, nil, member...)

	// Pre-household (NULL) rows stay visible to their author only.
	e.exec(t, `INSERT INTO recipes_recipes (id, user_id, household_id, title) VALUES (500, '1', NULL, 'old')`)
	e.obj(t, 200, "GET", "/recipes/500", nil, owner...)
	e.hh.set(2, "B", "A")
	e.expectDetail(t, 404, "Recipe not found", "GET", "/recipes/500", nil, member...)

	// A user with no household writes and reads private rows.
	e.hh.set(4)
	priv := e.create(t, tok(4, ""), recipeBody("private", nil))
	if l := e.list(t, "/recipes", tok(4, "")...); len(l) != 1 || idOf(l[0].(map[string]any)) != idOf(priv) {
		t.Fatalf("no-household list: %v", l)
	}
	e.expectDetail(t, 404, "Recipe not found", "GET", path("/recipes/%d", idOf(priv)), nil, owner...)

	// A member deletes another member's recipe (shared kitchen).
	if c, _ := e.do(t, "DELETE", p, nil, member...); c != 204 {
		t.Fatalf("member delete: %d", c)
	}
}

func TestTags(t *testing.T) {
	e := setup(t)
	e.hh.set(1, "A")
	e.hh.set(3, "C")
	h := tok(1, "A")
	e.create(t, h, recipeBody("tagged", map[string]any{"tags": []string{"B-tag", "a-tag"}}))
	l := e.list(t, "/tags", h...)
	if len(l) != 2 || l[0].(map[string]any)["name"] != "a-tag" || l[1].(map[string]any)["name"] != "B-tag" {
		t.Fatalf("GET /tags by name: %v", l)
	}
	if len(e.list(t, "/tags", tok(3, "C")...)) != 0 {
		t.Fatalf("outsider sees no tags")
	}
	a := e.obj(t, 201, "POST", "/tags", map[string]string{"name": "  posted "}, h...)
	b := e.obj(t, 201, "POST", "/tags", map[string]string{"name": "POSTED"}, h...)
	if a["name"] != "posted" || idOf(a) != idOf(b) {
		t.Fatalf("POST /tags get-or-create (B9 fixed): %v %v", a, b)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM recipes_tags WHERE name = 'posted'`); n != 1 {
		t.Fatalf("tag committed once, got %d", n)
	}
	e.expectDetail(t, 400, "Tag name required", "POST", "/tags", map[string]string{"name": "  "}, h...)
	e.expectValidation(t, "body.name", "POST", "/tags", map[string]string{}, h...)
}

func TestStock(t *testing.T) {
	e := setup(t)
	h := tok(1, "")
	if n := len(e.list(t, "/ingredients/stock?q=&limit=1000", h...)); n != 198 {
		t.Fatalf("ingredients: %d", n)
	}
	units := e.list(t, "/units/stock?limit=100", h...)
	if len(units) != 51 {
		t.Fatalf("units: %d", len(units))
	}
	if n := len(e.list(t, "/ingredients/stock", h...)); n != 10 {
		t.Fatalf("default limit: %d", n)
	}
	for _, x := range e.list(t, "/ingredients/stock?q=SALT&limit=1000", h...) {
		if !strings.Contains(x.(map[string]any)["name"].(string), "salt") {
			t.Fatalf("q filter: %v", x)
		}
	}
	tb := e.list(t, "/units/stock?q=tbsp&limit=100", h...)
	if len(tb) != 1 || tb[0].(map[string]any)["name"] != "tablespoon" {
		t.Fatalf("abbreviation match: %v", tb)
	}
	nulls := 0
	for _, u := range units {
		if u.(map[string]any)["abbreviation"] == nil {
			nulls++
		}
	}
	if nulls == 0 {
		t.Fatalf("abbreviation should be null for some units")
	}
	e.expectValidation(t, "query.limit", "GET", "/ingredients/stock?limit=0", nil, h...)
	e.expectValidation(t, "query.limit", "GET", "/ingredients/stock?limit=1001", nil, h...)
	e.expectValidation(t, "query.limit", "GET", "/ingredients/stock?limit=x", nil, h...)
	e.expectValidation(t, "query.limit", "GET", "/units/stock?limit=101", nil, h...)

	// Re-seeding: skipped while the hash matches, and an upsert (no duplicates) when it doesn't.
	e.exec(t, `UPDATE recipes_stock_ingredients SET category = 'x' WHERE name = 'chicken breast'`)
	if err := e.m.seedStock(e.ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM recipes_stock_ingredients WHERE category = 'x'`); n != 1 {
		t.Fatalf("same hash should skip the upsert")
	}
	e.exec(t, `DELETE FROM recipes_meta`)
	if err := e.m.seedStock(e.ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM recipes_stock_ingredients`); n != 198 {
		t.Fatalf("reseed duplicated rows: %d", n)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM recipes_stock_ingredients WHERE category = 'x'`); n != 0 {
		t.Fatalf("reseed should restore the category")
	}
}

func TestStageRecipe(t *testing.T) {
	e := setup(t)
	now := time.Now()
	e.exec(t, `INSERT INTO recipes_users (user_id) VALUES ('1')`)
	e.exec(t, `INSERT INTO recipes_stage_recipes (id, user_id, title, ingredients, steps, tags, notes, yield_text, expires_at)
		VALUES (5, '1', 'Eggs', '[{"name":"egg"}]', '["Cook."]', '["breakfast"]', '[]', '2 servings', ?)`, ts(now.Add(time.Hour)))
	e.exec(t, `INSERT INTO recipes_stage_recipes (id, user_id, title, ingredients, steps, expires_at)
		VALUES (6, '1', 'Old', '[]', '[]', ?)`, ts(now.Add(-time.Hour)))
	o := e.obj(t, 200, "GET", "/recipes/stage/5", nil, tok(1, "")...)
	if o["id"] != "5" || o["yield"] != "2 servings" || o["prep_time_minutes"] != 0.0 ||
		o["ingredients"].([]any)[0].(map[string]any)["name"] != "egg" || o["tags"].([]any)[0] != "breakfast" {
		t.Fatalf("stage: %v", o)
	}
	e.expectDetail(t, 404, "Not found", "GET", "/recipes/stage/5", nil, tok(2, "")...)
	e.expectDetail(t, 410, "Stage recipe expired", "GET", "/recipes/stage/6", nil, tok(1, "")...)
	e.expectValidation(t, "path.stage_id", "GET", "/recipes/stage/x", nil, tok(1, "")...)
}

func TestCreateWithParseJob(t *testing.T) {
	e := setup(t)
	h := tok(1, "")
	e.exec(t, `INSERT INTO recipes_recipe_parse_jobs (id, user_id, job_type, status) VALUES ('j1', '1', 'ingestion', 'COMPLETE')`)
	e.exec(t, `INSERT INTO recipes_recipe_parse_jobs (id, user_id, job_type, status) VALUES ('j2', '1', 'ingestion', 'RUNNING')`)
	e.exec(t, `INSERT INTO recipes_recipe_parse_jobs (id, user_id, job_type, status) VALUES ('j3', '2', 'ingestion', 'COMPLETE')`)
	e.create(t, h, recipeBody("from job", map[string]any{"parse_job_id": "j1"}))
	var status string
	_ = e.d.Read.QueryRow(`SELECT status FROM recipes_recipe_parse_jobs WHERE id = 'j1'`).Scan(&status)
	if status != "COMMITTED" {
		t.Fatalf("job status %q", status)
	}
	e.expectDetail(t, 409, "Parse job not ready", "POST", "/recipes", recipeBody("again", map[string]any{"parse_job_id": "j1"}), h...)
	e.expectDetail(t, 409, "Parse job not ready", "POST", "/recipes", recipeBody("x", map[string]any{"parse_job_id": "j2"}), h...)
	e.expectDetail(t, 404, "Parse job not found", "POST", "/recipes", recipeBody("x", map[string]any{"parse_job_id": "j3"}), h...)
	e.expectDetail(t, 404, "Parse job not found", "POST", "/recipes", recipeBody("x", map[string]any{"parse_job_id": "nope"}), h...)
	if n := e.count(t, `SELECT COUNT(*) FROM recipes_recipes`); n != 1 {
		t.Fatalf("refused creates must not write: %d recipes", n)
	}
}

// TestOneScopingPredicate is §8 item 1: handlers never hand-write user/household filters; they
// go through caller.visible / caller.authorOnly (auth.go). The deletion hooks are exempt.
func TestOneScopingPredicate(t *testing.T) {
	files, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	raw := regexp.MustCompile(`(user_id|household_id)\s*(=|IN|IS)`)
	for _, f := range files {
		n := f.Name()
		// legacy_import.go is the operator's cutover import (no caller): it writes every scope.
		if !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") || n == "auth.go" || n == "hooks.go" || n == "legacy_import.go" {
			continue
		}
		b, err := os.ReadFile(n)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if raw.MatchString(line) {
				t.Errorf("%s:%d hand-written scope filter: %s", n, i+1, strings.TrimSpace(line))
			}
		}
	}
}
