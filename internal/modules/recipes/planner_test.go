package recipes

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func day(n int) string { return time.Now().AddDate(0, 0, n).Format("2006-01-02") }

func planItem(date, meal string, id any) map[string]any {
	return map[string]any{"date": date, "meal_type": meal, "recipe_id": id}
}

func itemBy(items []any, key string, v any) map[string]any {
	for _, it := range items {
		m := it.(map[string]any)
		if fmt.Sprint(m[key]) == fmt.Sprint(v) {
			return m
		}
	}
	return nil
}

func TestPlannerLifecycle(t *testing.T) {
	e := setup(t)
	e.hh.set(1, "A")
	e.hh.set(2, "A")
	e.hh.set(3, "C")
	h, member, outsider := tok(1, "A"), tok(2, "A"), tok(3, "C")

	if o := e.obj(t, http.StatusOK, http.MethodGet, "/planner/current", nil, h...); len(o) != 0 {
		t.Fatalf("no plan: %v", o)
	}
	r1 := e.create(t, h, recipeBody("one", map[string]any{"total_time_minutes": 15, "image_url": "https://x/1.jpg"}))
	r2 := e.create(t, h, recipeBody("two", nil))
	r3 := e.create(t, member, recipeBody("three", nil))

	plan := e.obj(t, http.StatusOK, http.MethodPost, "/planner/commit", map[string]any{
		"name": "week", "start_date": day(5), "items": []any{
			planItem(day(3), "dinner", idOf(r1)),
			planItem(day(4), "dinner", fmt.Sprint(idOf(r2))), // lax int
			map[string]any{"date": day(4), "meal_type": "lunch", "recipe_id": idOf(r3), "source": "user"},
		},
	}, h...)
	pid := idOf(plan)
	if plan["name"] != "week" || plan["start_date"] != day(5) || plan["user_id"] != "1" {
		t.Fatalf("plan: %v", plan)
	}
	it1 := itemBy(plan["items"].([]any), "recipe_id", idOf(r1))
	if it1["title"] != "one" || it1["image_url"] != "https://x/1.jpg" || it1["total_time_minutes"].(float64) != 15 || it1["date"] != day(3) {
		t.Fatalf("item fields: %v", it1)
	}

	// Current / list / get.
	if o := e.obj(t, http.StatusOK, http.MethodGet, "/planner/current", nil, member...); idOf(o) != pid {
		t.Fatalf("member current: %v", o)
	}
	if o := e.obj(t, http.StatusOK, http.MethodGet, "/planner/current", nil, outsider...); len(o) != 0 {
		t.Fatalf("outsider current: %v", o)
	}
	l := e.list(t, "/planner/plans", h...)
	if len(l) != 1 {
		t.Fatalf("plans: %v", l)
	}
	s := l[0].(map[string]any)
	if s["start_date"] != day(3) || s["end_date"] != day(4) || s["meal_count"].(float64) != 3 || s["name"] != "week" {
		t.Fatalf("summary: %v", s)
	}
	if _, ok := s["created_at"].(string); !ok {
		t.Fatalf("created_at: %v", s)
	}
	e.expectDetail(t, http.StatusNotFound, "Meal plan not found", http.MethodGet, path("/planner/plans/%d", pid), nil, outsider...)
	e.expectValidation(t, "path.plan_id", http.MethodGet, "/planner/plans/abc", nil, h...)

	// Move: onto an occupied slot swaps.
	i1 := int64(itemBy(plan["items"].([]any), "recipe_id", idOf(r1))["id"].(float64))
	i2 := int64(itemBy(plan["items"].([]any), "recipe_id", idOf(r2))["id"].(float64))
	i3 := int64(itemBy(plan["items"].([]any), "recipe_id", idOf(r3))["id"].(float64))
	mp := path("/planner/plans/%d/items", pid)
	moved := e.obj(t, http.StatusOK, http.MethodPatch, mp, map[string]any{"moves": []any{
		map[string]any{"item_id": i1, "date": day(4), "meal_type": "dinner"}}}, member...)
	if a, b := itemBy(moved["items"].([]any), "id", i1), itemBy(moved["items"].([]any), "id", i2); a["date"] != day(4) || b["date"] != day(3) || b["meal_type"] != "dinner" {
		t.Fatalf("swap: %v", moved["items"])
	}
	if moved["start_date"] != day(3) {
		t.Fatalf("start_date: %v", moved["start_date"])
	}
	moved = e.obj(t, http.StatusOK, http.MethodPatch, mp, map[string]any{"moves": []any{
		map[string]any{"item_id": i2, "date": day(6), "meal_type": "breakfast"}}}, h...)
	if moved["start_date"] != day(4) {
		t.Fatalf("start_date after move: %v", moved["start_date"])
	}
	e.obj(t, http.StatusOK, http.MethodPatch, mp, map[string]any{"moves": []any{}}, h...)
	e.expectDetail(t, http.StatusConflict, "Two meals cannot be moved to the same day and meal type", http.MethodPatch, mp,
		map[string]any{"moves": []any{
			map[string]any{"item_id": i1, "date": day(9), "meal_type": "dinner"},
			map[string]any{"item_id": i3, "date": day(9), "meal_type": "dinner"}}}, h...)
	e.expectDetail(t, http.StatusNotFound, "Item 999 is not part of this plan", http.MethodPatch, mp,
		map[string]any{"moves": []any{map[string]any{"item_id": 999, "date": day(9), "meal_type": "dinner"}}}, h...)
	e.expectDetail(t, http.StatusNotFound, "Meal plan not found", http.MethodPatch, mp,
		map[string]any{"moves": []any{map[string]any{"item_id": i1, "date": day(9), "meal_type": "dinner"}}}, outsider...)

	// Deleting a recipe drops its item.
	e.json(t, http.StatusNoContent, http.MethodDelete, path("/recipes/%d", idOf(r3)), nil, h...)
	if got := e.obj(t, http.StatusOK, http.MethodGet, path("/planner/plans/%d", pid), nil, h...); len(got["items"].([]any)) != 2 {
		t.Fatalf("items after recipe delete: %v", got["items"])
	}

	// Commit errors.
	e.expectDetail(t, http.StatusNotFound, "Staged recipe 999 not found", http.MethodPost, "/planner/commit",
		map[string]any{"start_date": day(1), "items": []any{map[string]any{"date": day(1), "meal_type": "d", "recipe_id": 999, "source": "stage"}}}, h...)
	e.expectValidation(t, "body.items.0.source", http.MethodPost, "/planner/commit",
		map[string]any{"start_date": day(1), "items": []any{map[string]any{"date": day(1), "meal_type": "d", "recipe_id": 1, "source": "core"}}}, h...)
	e.expectValidation(t, "body.start_date", http.MethodPost, "/planner/commit", map[string]any{"items": []any{}}, h...)
	e.expectValidation(t, "body.items.0.date", http.MethodPost, "/planner/commit",
		map[string]any{"start_date": day(1), "items": []any{planItem("tomorrow", "d", idOf(r1))}}, h...)
	// B14: another household's or a missing recipe is 404, and nothing is written.
	foreign := e.create(t, outsider, recipeBody("foreign", nil))
	before := e.count(t, `SELECT COUNT(*) FROM recipes_meal_plans`)
	for _, id := range []int64{idOf(foreign), 99999} {
		e.expectDetail(t, http.StatusNotFound, "Recipe not found", http.MethodPost, "/planner/commit",
			map[string]any{"start_date": day(1), "items": []any{planItem(day(1), "dinner", idOf(r1)), planItem(day(1), "lunch", id)}}, h...)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM recipes_meal_plans`); n != before {
		t.Fatalf("a failed commit left a plan: %d → %d", before, n)
	}

	// Delete.
	e.expectDetail(t, http.StatusNotFound, "Meal plan not found", http.MethodDelete, path("/planner/plans/%d", pid), nil, outsider...)
	e.json(t, http.StatusNoContent, http.MethodDelete, path("/planner/plans/%d", pid), nil, h...)
	e.expectDetail(t, http.StatusNotFound, "Meal plan not found", http.MethodGet, path("/planner/plans/%d", pid), nil, h...)
	e.obj(t, http.StatusOK, http.MethodGet, path("/recipes/%d", idOf(r1)), nil, h...)
}

// TestMoveChain: the occupant search skips every moving item and sees earlier moves.
func TestMoveChain(t *testing.T) {
	e := setup(t)
	h := tok(1, "")
	r := e.create(t, h, recipeBody("r", nil))
	plan := e.obj(t, http.StatusOK, http.MethodPost, "/planner/commit", map[string]any{"start_date": day(1), "items": []any{
		planItem(day(1), "dinner", idOf(r)), planItem(day(2), "dinner", idOf(r)), planItem(day(3), "dinner", idOf(r)),
	}}, h...)
	ids := []int64{}
	for _, it := range plan["items"].([]any) {
		ids = append(ids, int64(it.(map[string]any)["id"].(float64)))
	}
	// A rotation: 1→2, 2→3, 3→1, all moving, so no swaps happen.
	got := e.obj(t, http.StatusOK, http.MethodPatch, path("/planner/plans/%d/items", idOf(plan)), map[string]any{"moves": []any{
		map[string]any{"item_id": ids[0], "date": day(2), "meal_type": "dinner"},
		map[string]any{"item_id": ids[1], "date": day(3), "meal_type": "dinner"},
		map[string]any{"item_id": ids[2], "date": day(1), "meal_type": "dinner"},
	}}, h...)
	for i, want := range []string{day(2), day(3), day(1)} {
		if d := itemBy(got["items"].([]any), "id", ids[i])["date"]; d != want {
			t.Errorf("item %d: %v, want %s", i, d, want)
		}
	}
	// A repeated item_id: the last move wins.
	got = e.obj(t, http.StatusOK, http.MethodPatch, path("/planner/plans/%d/items", idOf(plan)), map[string]any{"moves": []any{
		map[string]any{"item_id": ids[0], "date": day(7), "meal_type": "lunch"},
		map[string]any{"item_id": ids[0], "date": day(8), "meal_type": "lunch"},
	}}, h...)
	if it := itemBy(got["items"].([]any), "id", ids[0]); it["date"] != day(8) {
		t.Fatalf("last move wins: %v", it)
	}
}

type fixedClock map[string]string

func (f fixedClock) HouseholdTimezone(_ context.Context, hh string) string { return f[hh] }

// TestCurrentPlanHouseholdClock: "today" is the household's date, not the host's.
func TestCurrentPlanHouseholdClock(t *testing.T) {
	e := setup(t)
	e.hh.set(1, "A")
	h := tok(1, "A")
	// 2026-10-08 23:30 UTC is already the 9th in Auckland and still the 8th in Los Angeles.
	e.m.now = func() time.Time { return time.Date(2026, 10, 8, 23, 30, 0, 0, time.UTC) }
	r := e.create(t, h, recipeBody("r", nil))
	e.obj(t, http.StatusOK, http.MethodPost, "/planner/commit", map[string]any{"start_date": "2026-10-08",
		"items": []any{planItem("2026-10-08", "dinner", idOf(r))}}, h...)

	e.m.Clock = fixedClock{"A": "America/Los_Angeles"}
	if o := e.obj(t, http.StatusOK, http.MethodGet, "/planner/current", nil, h...); len(o) == 0 {
		t.Fatal("still the 8th in LA: the plan is current")
	}
	e.m.Clock = fixedClock{"A": "Pacific/Auckland"}
	if o := e.obj(t, http.StatusOK, http.MethodGet, "/planner/current", nil, h...); len(o) != 0 {
		t.Fatalf("already the 9th in Auckland: %v", o)
	}
	// Soonest first meal wins; a tie goes to the newer plan.
	p2 := e.obj(t, http.StatusOK, http.MethodPost, "/planner/commit", map[string]any{"start_date": "2026-10-12",
		"items": []any{planItem("2026-10-12", "dinner", idOf(r)), planItem("2026-10-20", "dinner", idOf(r))}}, h...)
	e.obj(t, http.StatusOK, http.MethodPost, "/planner/commit", map[string]any{"start_date": "2026-10-15",
		"items": []any{planItem("2026-10-15", "dinner", idOf(r))}}, h...)
	if o := e.obj(t, http.StatusOK, http.MethodGet, "/planner/current", nil, h...); idOf(o) != idOf(p2) {
		t.Fatalf("current: %v, want plan %d", o["id"], idOf(p2))
	}
}

// TestCommitStage materialises an author's staged recipe once per stage id.
func TestCommitStage(t *testing.T) {
	e := setup(t)
	e.hh.set(1, "A")
	e.hh.set(2, "A")
	h := tok(1, "A")
	e.exec(t, `INSERT INTO recipes_users (user_id) VALUES ('1')`)
	e.exec(t, `INSERT INTO recipes_stage_recipes (id, user_id, household_id, title, prep_time_minutes, cook_time_minutes,
		ingredients, steps, expires_at) VALUES (7, '1', 'A', 'Staged', 10, 20,
		'[{"text": "1 cup rice", "quantity_display": "1", "unit": "cup"}, {"text": ""}, "salt"]',
		'[{"text": "Cook."}, "Serve."]', ?)`, ts(time.Now().Add(time.Hour)))
	plan := e.obj(t, http.StatusOK, http.MethodPost, "/planner/commit", map[string]any{"start_date": day(1), "items": []any{
		map[string]any{"date": day(1), "meal_type": "dinner", "recipe_id": 7, "source": "stage"},
		map[string]any{"date": day(2), "meal_type": "dinner", "recipe_id": "7", "source": "stage"},
	}}, h...)
	items := plan["items"].([]any)
	rid := items[0].(map[string]any)["recipe_id"]
	if items[1].(map[string]any)["recipe_id"] != rid || items[0].(map[string]any)["title"] != "Staged" {
		t.Fatalf("one recipe per stage id: %v", items)
	}
	rec := e.obj(t, http.StatusOK, http.MethodGet, fmt.Sprintf("/recipes/%v", rid), nil, tok(2, "A")...)
	if rec["total_time_minutes"].(float64) != 30 || rec["source_type"] != "manual" || len(rec["ingredients"].([]any)) != 2 ||
		len(rec["steps"].([]any)) != 2 {
		t.Fatalf("materialised: %v", rec)
	}
	if q := rec["ingredients"].([]any)[0].(map[string]any)["quantity_value"]; q != "1.0000" {
		t.Fatalf("quantity_value: %v", q)
	}
	// Another member cannot materialise the author's stage.
	e.expectDetail(t, http.StatusNotFound, "Staged recipe 7 not found", http.MethodPost, "/planner/commit",
		map[string]any{"start_date": day(1), "items": []any{map[string]any{"date": day(1), "meal_type": "d", "recipe_id": 7, "source": "stage"}}}, tok(2, "A")...)
}
