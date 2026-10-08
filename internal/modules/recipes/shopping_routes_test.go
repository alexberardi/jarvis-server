package recipes

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestRandomPlan(t *testing.T) {
	e := setup(t)
	e.hh.set(1, "A")
	e.hh.set(2, "A")
	h, member, outsider := tok(1, "A"), tok(2, "A"), tok(3, "")
	a := e.create(t, h, recipeBody("a", map[string]any{"tags": []string{"Tag-Dinner"}, "servings": 2}))
	b := e.create(t, member, recipeBody("b", nil))
	e.create(t, outsider, recipeBody("not ours", nil))

	slots := []any{
		map[string]any{"date": day(1), "meal_type": "tag-dinner"},
		map[string]any{"date": day(2), "meal_type": "dinner"},
		map[string]any{"date": day(3), "meal_type": "dinner"},
	}
	for i := 0; i < 10; i++ { // random order must not matter
		res := e.obj(t, http.StatusOK, http.MethodPost, "/meal-plans/random", map[string]any{"slots": slots}, h...)
		got := res["slots"].([]any)
		s0, s1, s2 := got[0].(map[string]any), got[1].(map[string]any), got[2].(map[string]any)
		if s0["recipe_id"].(float64) != float64(idOf(a)) || s0["title"] != "a" || s0["servings"].(float64) != 2 || s0["date"] != day(1) {
			t.Fatalf("slot 0: %v", s0)
		}
		if s1["recipe_id"].(float64) != float64(idOf(b)) || s2["recipe_id"] != nil || s2["title"] != nil || s2["date"] != day(3) ||
			s2["meal_type"] != "dinner" || res["incomplete"] != true {
			t.Fatalf("slots: %v", res)
		}
	}
	res := e.obj(t, http.StatusOK, http.MethodPost, "/meal-plans/random",
		map[string]any{"slots": slots[:1], "exclude_recipe_ids": []int64{idOf(a)}}, h...)
	if res["slots"].([]any)[0].(map[string]any)["recipe_id"].(float64) != float64(idOf(b)) || res["incomplete"] != false {
		t.Fatalf("exclude: %v", res)
	}
	if res := e.obj(t, http.StatusOK, http.MethodPost, "/meal-plans/random", map[string]any{"slots": []any{}}, h...); len(res["slots"].([]any)) != 0 || res["incomplete"] != false {
		t.Fatalf("empty: %v", res)
	}
	e.expectValidation(t, "body.slots.0.date", http.MethodPost, "/meal-plans/random",
		map[string]any{"slots": []any{map[string]any{"date": "x", "meal_type": "dinner"}}}, h...)
	e.expectValidation(t, "body.exclude_recipe_ids.0", http.MethodPost, "/meal-plans/random",
		map[string]any{"slots": []any{}, "exclude_recipe_ids": []any{"x"}}, h...)

	r := e.obj(t, http.StatusOK, http.MethodPost, "/meal-plans/random/reroll",
		map[string]any{"meal_type": "dinner", "exclude_recipe_ids": []int64{idOf(a)}}, h...)
	if r["date"] != nil || r["meal_type"] != "dinner" || r["recipe_id"].(float64) != float64(idOf(b)) {
		t.Fatalf("reroll: %v", r)
	}
	// Tags that match nothing relax to the meal type, then the whole box.
	r = e.obj(t, http.StatusOK, http.MethodPost, "/meal-plans/random/reroll",
		map[string]any{"exclude_recipe_ids": []int64{idOf(b)}, "tags": []string{"nope"}}, h...)
	if r["meal_type"] != "" || r["recipe_id"].(float64) != float64(idOf(a)) {
		t.Fatalf("reroll relaxed: %v", r)
	}
	// Slot tags win over the meal type.
	for i := 0; i < 5; i++ {
		r = e.obj(t, http.StatusOK, http.MethodPost, "/meal-plans/random/reroll",
			map[string]any{"meal_type": "breakfast", "tags": []string{"TAG-dinner"}}, member...)
		if r["recipe_id"].(float64) != float64(idOf(a)) {
			t.Fatalf("tags first: %v", r)
		}
	}
	const none = "No other recipe available to swap in. Add more recipes, or clear a slot."
	e.expectDetail(t, http.StatusConflict, none, http.MethodPost, "/meal-plans/random/reroll",
		map[string]any{"exclude_recipe_ids": []int64{idOf(a), idOf(b)}}, h...)
	e.expectDetail(t, http.StatusConflict, none, http.MethodPost, "/meal-plans/random/reroll", map[string]any{}, tok(9, "")...)
}

func TestShoppingList(t *testing.T) {
	e := setup(t)
	e.hh.set(1, "A")
	e.hh.set(2, "A")
	h, member := tok(1, "A"), tok(2, "A")
	r1 := e.create(t, h, recipeBody("chili", map[string]any{"ingredients": []map[string]any{
		{"text": "2 lb ground beef", "quantity_display": "2", "unit": "lb"},
		{"text": "1 cup Olive Oil, divided", "quantity_display": "1", "unit": "Cup "},
		{"text": "Salt to taste"},
		{"text": "1/3 cup broth", "quantity_display": "1/3", "unit": "cup"},
	}}))
	r2 := e.create(t, member, recipeBody("tacos", map[string]any{"ingredients": []map[string]any{
		{"text": "1 1/2 lb Ground Beef (93/7 or leaner)", "quantity_display": "1 1/2", "unit": "LB"},
		{"text": "salt"},
		{"text": "1/3 cup broth", "quantity_display": "1/3", "unit": "cup"},
	}}))
	e.obj(t, http.StatusOK, http.MethodPost, "/planner/commit", map[string]any{"start_date": day(1), "items": []any{
		planItem(day(1), "dinner", idOf(r1)),
		planItem(day(2), "dinner", idOf(r2)),
		planItem(day(20), "dinner", idOf(r1)), // out of range
	}}, h...)
	e.obj(t, http.StatusOK, http.MethodPost, "/planner/commit", map[string]any{"start_date": day(2), "items": []any{
		planItem(day(2), "lunch", idOf(r2)),
	}}, member...)
	e.obj(t, http.StatusCreated, http.MethodPost, "/staples", map[string]any{"name": "salt"}, h...)

	q := "?start_date=" + day(0) + "&end_date=" + day(7)
	got := e.obj(t, http.StatusOK, http.MethodGet, "/shopping-list"+q, nil, member...)
	raw, _ := json.Marshal(got["items"])
	want := `[{"amounts":[{"quantity":0.9999,"unit":"cup","unparsed":[]}],"is_staple":false,"name":"broth","recipes":["chili","tacos"]},` +
		`{"amounts":[{"quantity":5,"unit":"lb","unparsed":[]}],"is_staple":false,"name":"ground beef","recipes":["chili","tacos"]},` +
		`{"amounts":[{"quantity":1,"unit":"cup","unparsed":[]}],"is_staple":false,"name":"olive oil","recipes":["chili"]},` +
		`{"amounts":[{"quantity":null,"unit":null,"unparsed":["Salt to taste","salt","salt"]}],"is_staple":true,"name":"salt","recipes":["chili","tacos"]}]`
	if string(raw) != want {
		t.Fatalf("items:\n got %s\nwant %s", raw, want)
	}
	if got["plan_count"].(float64) != 2 || got["start_date"] != day(0) || got["end_date"] != day(7) {
		t.Fatalf("list: %v", got)
	}
	empty := e.obj(t, http.StatusOK, http.MethodGet, "/shopping-list?start_date="+day(30)+"&end_date="+day(31), nil, h...)
	if empty["plan_count"].(float64) != 0 || len(empty["items"].([]any)) != 0 {
		t.Fatalf("empty range: %v", empty)
	}
	if o := e.obj(t, http.StatusOK, http.MethodGet, "/shopping-list"+q, nil, tok(3, "")...); o["plan_count"].(float64) != 0 {
		t.Fatalf("outsider: %v", o)
	}
	o := e.expectValidation(t, "query.start_date", http.MethodGet, "/shopping-list?start_date=bad", nil, h...)
	if len(o["details"].([]any)) != 2 {
		t.Fatalf("both query errors reported: %v", o["details"])
	}
}

func TestStaples(t *testing.T) {
	e := setup(t)
	e.hh.set(1, "A")
	e.hh.set(2, "A")
	h, member, outsider := tok(1, "A"), tok(2, "A"), tok(3, "")
	a := e.obj(t, http.StatusCreated, http.MethodPost, "/staples", map[string]any{"name": "2 tbsp Olive Oil, divided"}, h...)
	if a["name"] != "olive oil" {
		t.Fatalf("normalised: %v", a)
	}
	for _, hdr := range [][]string{h, member} {
		for _, n := range []string{"olive oil", "OLIVE OIL"} {
			if b := e.obj(t, http.StatusCreated, http.MethodPost, "/staples", map[string]any{"name": n}, hdr...); idOf(b) != idOf(a) {
				t.Fatalf("idempotent: %v", b)
			}
		}
	}
	s := e.obj(t, http.StatusCreated, http.MethodPost, "/staples", map[string]any{"name": "Salt and pepper to taste"}, member...)
	if s["name"] != "salt and pepper" {
		t.Fatalf("staple: %v", s)
	}
	if l := e.list(t, "/staples", h...); len(l) != 2 || l[0].(map[string]any)["name"] != "olive oil" {
		t.Fatalf("list: %v", l)
	}
	if l := e.list(t, "/staples", outsider...); len(l) != 0 {
		t.Fatalf("outsider: %v", l)
	}
	e.expectValidation(t, "body.name", http.MethodPost, "/staples", map[string]any{"name": ""}, h...)
	e.expectValidation(t, "body.name", http.MethodPost, "/staples", map[string]any{"name": strings.Repeat("x", 201)}, h...)
	e.expectDetail(t, http.StatusUnprocessableEntity, "A staple needs a name", http.MethodPost, "/staples", map[string]any{"name": "   "}, h...)

	// Pre-household rows: two visible rows share a name. The list shows the oldest; DELETE by
	// either id removes both.
	e.exec(t, `INSERT INTO recipes_staples (id, user_id, household_id, name) VALUES (50, '1', NULL, 'flour'), (40, '2', 'A', 'flour')`)
	l := e.list(t, "/staples", h...)
	var flour []any
	for _, x := range l {
		if x.(map[string]any)["name"] == "flour" {
			flour = append(flour, x.(map[string]any)["id"])
		}
	}
	if !reflect.DeepEqual(flour, []any{40.0}) {
		t.Fatalf("deduped to the oldest: %v", flour)
	}
	e.expectDetail(t, http.StatusNotFound, "Staple not found", http.MethodDelete, "/staples/50", nil, outsider...)
	e.json(t, http.StatusNoContent, http.MethodDelete, "/staples/50", nil, h...)
	if n := e.count(t, `SELECT COUNT(*) FROM recipes_staples WHERE name = 'flour'`); n != 0 {
		t.Fatalf("delete by name left %d", n)
	}
	e.expectDetail(t, http.StatusNotFound, "Staple not found", http.MethodDelete, "/staples/50", nil, h...)
	e.expectValidation(t, "path.staple_id", http.MethodDelete, "/staples/x", nil, h...)

	// The caller's own row in a household they left is invisible; adding the name again moves
	// it to their current household instead of failing on UNIQUE(user_id, name).
	e.exec(t, `INSERT INTO recipes_staples (user_id, household_id, name) VALUES ('1', 'OLD', 'rice')`)
	r := e.obj(t, http.StatusCreated, http.MethodPost, "/staples", map[string]any{"name": "rice"}, h...)
	if e.count(t, `SELECT COUNT(*) FROM recipes_staples WHERE name = 'rice' AND household_id = 'A' AND id = ?`, idOf(r)) != 1 {
		t.Fatal("rice staple not in A")
	}
}
