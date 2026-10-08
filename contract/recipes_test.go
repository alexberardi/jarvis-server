//go:build contract

package contract

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"
)

// Contract for jarvis-recipes-server's synchronous routes (docs/recipes/00-inventory.md §3,
// step R0): every KEEP/CHANGE route the jarvis-recipes-mobile app calls, frozen against the
// legacy Python server. The job routes are in recipes_jobs_test.go; helpers and shapes in
// recipes_helpers.go.
//
// Conventions (docs/contract/README.md):
//   - `// LEGACY-BUG:` marks behaviour the spec fixes (§10 "Fix"). Those cases branch on
//     Jarvisd(): legacy must still behave as frozen, jarvisd as decided.
//   - `// RDn:` names a product decision in docs/recipes/QUESTIONS.md that changes legacy
//     behaviour; those also branch on Jarvisd().
//   - `// CHANGE #n:` names the §3 route row whose fate is CHANGE.

// recipesAuthProbes are user-JWT routes the app calls, one per router.
var recipesAuthProbes = []struct{ method, path string }{
	{http.MethodGet, "/recipes"},
	{http.MethodPost, "/recipes"},
	{http.MethodGet, "/recipes/1"},
	{http.MethodGet, "/recipes/jobs/x"},
	{http.MethodGet, "/recipes/parse-url/jobs"},
	{http.MethodPost, "/recipes/parse-payload/async"},
	{http.MethodGet, "/tags"},
	{http.MethodGet, "/ingredients/stock"},
	{http.MethodGet, "/units/stock"},
	{http.MethodGet, "/planner/plans"},
	{http.MethodGet, "/planner/current"},
	{http.MethodPost, "/meal-plans/random"},
	{http.MethodGet, "/meal-plans/generate/jobs/x"},
	{http.MethodGet, "/shopping-list?start_date=2026-01-01&end_date=2026-01-02"},
	{http.MethodGet, "/staples"},
	{http.MethodGet, "/grocery/sku-map"},
	{http.MethodPost, "/grocery/cart?start_date=2026-01-01&end_date=2026-01-02"},
}

// TestRecipesAuth freezes the two 401 details (§3 "Auth"). Auth runs before body
// validation, so the probes send no body.
func TestRecipesAuth(t *testing.T) {
	tg := T(t)
	tg.Need(t, Recipes)
	tg.NeedAdmin(t)
	k := SharedKitchen(t)

	// PR #39 lesson (§9): an HS256 token signed with the old "change-me" default must not verify.
	changeMe := forgeHS256(t, "change-me", map[string]any{
		"sub": fmt.Sprint(k.Owner.ID), "email": k.Owner.Email, "household_id": k.Owner.HouseholdID,
		"exp": 4102444800, "iat": 1700000000,
	})
	none := "eyJhbGciOiJub25lIiwidHlwIjoiSldUIn0." + strings.Split(changeMe, ".")[1] + "."

	for _, p := range recipesAuthProbes {
		t.Run(p.method+" "+p.path, func(t *testing.T) {
			tg.Do(t, Recipes, p.method, p.path, nil).
				ExpectError(http.StatusUnauthorized, "Not authenticated").
				ExpectHeaderVal("WWW-Authenticate", "Bearer")
			tg.Do(t, Recipes, p.method, p.path, nil, H{"Authorization": "Basic Zm9vOmJhcg=="}).
				ExpectError(http.StatusUnauthorized, "Not authenticated")
			for _, bad := range []string{"not.a.jwt", changeMe, none} {
				tg.Do(t, Recipes, p.method, p.path, nil, Bearer(bad)).
					ExpectError(http.StatusUnauthorized, "Invalid or expired token")
			}
		})
	}

	// #8: the core-recipe stub needs no auth and always 404s.
	tg.Get(t, Recipes, "/recipes/core/1").ExpectError(http.StatusNotFound, "Core recipe not found")
	tg.Get(t, Recipes, "/recipes/core/abc", k.Owner.H()).ExpectError(http.StatusNotFound, "Core recipe not found")
}

// TestRecipesCRUD covers #1–#5 and the RecipeRead details in §3.1.
func TestRecipesCRUD(t *testing.T) {
	tg := T(t)
	k := SharedKitchen(t)
	h := k.Owner.H()

	r := NewRecipe(t, h, map[string]any{
		"title":             "contract pancakes",
		"description":       "fluffy",
		"servings":          4,
		"prep_time_minutes": 10,
		"cook_time_minutes": 20,
		"source_type":       "url",
		"source_url":        "https://example.com/pancakes",
		"image_url":         "https://example.com/p.jpg",
		"ingredients": []map[string]any{
			// The server re-parses quantity_value from quantity_display and ignores the client's.
			{"text": "1 1/2 cups flour", "quantity_display": "1 1/2", "quantity_value": "99", "unit": "cups"},
			{"text": "1/2 tsp salt", "quantity_display": "1/2", "unit": "tsp"},
			{"text": "2 eggs", "quantity_display": "2"},
			{"text": "milk"},
			{"text": "1/0 of nothing", "quantity_display": "1/0"},
		},
		"steps": []map[string]any{{"step_number": 2, "text": "Cook."}, {"step_number": 1, "text": "Mix."}},
		"tags":  []string{"contract-tag-b", "contract-tag-a"},
	})
	id := idOf(r)
	if r["user_id"] != fmt.Sprint(k.Owner.ID) {
		t.Fatalf("user_id: want %q (a string), got %v", fmt.Sprint(k.Owner.ID), r["user_id"])
	}
	// prep and cook fold into total when total is absent.
	expectFields(t, r, map[string]any{
		"title": "contract pancakes", "servings": 4, "total_time_minutes": 30, "source_type": "url",
		"source_url": "https://example.com/pancakes", "image_url": "https://example.com/p.jpg",
	})
	expectPrepCook(t, r, 10, 20)
	// quantity_value is a decimal string from a Numeric(10,4) column; "1/0" is null.
	expectQuantities(t, r, []any{"1.5000", "0.5000", "2.0000", nil, nil})
	ings := r["ingredients"].([]any)
	if ings[0].(map[string]any)["text"] != "1 1/2 cups flour" || ings[3].(map[string]any)["text"] != "milk" {
		t.Fatalf("ingredients keep insertion order: %v", show(ings))
	}
	steps := r["steps"].([]any)
	if mustFloat(steps[0].(map[string]any)["step_number"]) != 1 || steps[0].(map[string]any)["text"] != "Mix." {
		t.Fatalf("steps are ordered by step_number: %v", show(steps))
	}
	// A recipe's tags come back in insertion order on legacy; unordered by contract.
	if names := tagNames(r); !sameFold(sorted(names), []string{"contract-tag-a", "contract-tag-b"}) {
		t.Fatalf("tags: %v", names)
	}

	// #1: the list carries it, newest first.
	r2 := NewRecipe(t, h, recipeBody("contract second", nil))
	list := arr(tg.Get(t, Recipes, "/recipes", h).Expect(http.StatusOK, ArrayOf(recipeRead)))
	if len(list) < 2 || idOf(list[0].(map[string]any)) != idOf(r2) || idOf(list[1].(map[string]any)) != id {
		t.Fatalf("GET /recipes: want [%d, %d, …] (created_at desc), got %v", idOf(r2), id, show(list))
	}

	// #3
	tg.Get(t, Recipes, fmt.Sprintf("/recipes/%d", id), h).Expect(http.StatusOK, recipeRead)
	tg.Get(t, Recipes, "/recipes/999999999", h).ExpectError(http.StatusNotFound, "Recipe not found")
	tg.Get(t, Recipes, "/recipes/abc", h).Expect(http.StatusUnprocessableEntity, recipesValidation("path.recipe_id"))
	// #16 is CUT and deliberately not registered on jarvisd: GET /recipes/jobs falls through
	// to #3 and answers the same 422 as legacy, where #3 shadows it.
	tg.Get(t, Recipes, "/recipes/jobs", h).Expect(http.StatusUnprocessableEntity, recipesValidation("path.recipe_id"))

	// #4 PATCH: null leaves a field alone; lists replace wholesale; prep/cook fold into total
	// only when total is absent.
	p := tg.Do(t, Recipes, http.MethodPatch, fmt.Sprintf("/recipes/%d", id), map[string]any{
		"title": "contract pancakes v2", "description": nil, "prep_time_minutes": 5,
		"ingredients": []map[string]any{{"text": "3 eggs", "quantity_display": "3"}},
		"steps":       []map[string]any{{"step_number": 1, "text": "Whisk."}, {"step_number": 2, "text": "Fry."}},
		"tags":        []string{"contract-tag-a"},
	}, h).Expect(http.StatusOK, recipeRead).Object()
	expectFields(t, p, map[string]any{
		"title": "contract pancakes v2", "description": "fluffy", "total_time_minutes": 5, "servings": 4,
	})
	expectPrepCook(t, p, 5, 20)
	expectQuantities(t, p, []any{"3.0000"})
	if len(p["steps"].([]any)) != 2 || len(tagNames(p)) != 1 {
		t.Fatalf("PATCH replaces steps and tags: %v", show(p))
	}
	p = tg.Do(t, Recipes, http.MethodPatch, fmt.Sprintf("/recipes/%d", id), map[string]any{
		"total_time_minutes": 45, "cook_time_minutes": 99, "tags": []string{},
	}, h).Expect(http.StatusOK, recipeRead).Object()
	expectFields(t, p, map[string]any{"total_time_minutes": 45, "title": "contract pancakes v2"})
	expectPrepCook(t, p, 5, 99)
	if len(tagNames(p)) != 0 || len(p["ingredients"].([]any)) != 1 {
		t.Fatalf("PATCH: empty tags clears, absent ingredients keeps: %v", show(p))
	}
	tg.Do(t, Recipes, http.MethodPatch, "/recipes/999999999", map[string]any{"title": "x"}, h).
		ExpectError(http.StatusNotFound, "Recipe not found")

	// #5
	tg.Do(t, Recipes, http.MethodDelete, fmt.Sprintf("/recipes/%d", idOf(r2)), nil, h).
		ExpectStatus(http.StatusNoContent).ExpectEmpty()
	tg.Get(t, Recipes, fmt.Sprintf("/recipes/%d", idOf(r2)), h).ExpectError(http.StatusNotFound, "Recipe not found")
	tg.Do(t, Recipes, http.MethodDelete, fmt.Sprintf("/recipes/%d", idOf(r2)), nil, h).
		ExpectError(http.StatusNotFound, "Recipe not found")
}

// TestRecipesQuantityEdgeCases freezes parse_quantity_display's odd inputs through the API.
func TestRecipesQuantityEdgeCases(t *testing.T) {
	k := SharedKitchen(t)
	r := NewRecipe(t, k.Owner.H(), recipeBody("contract quantities", map[string]any{
		"ingredients": []map[string]any{
			{"text": "a", "quantity_display": "0.25"},
			{"text": "b", "quantity_display": "1e3"},
			{"text": "c", "quantity_display": " 2 "},
			{"text": "d", "quantity_display": "1/3"},
			{"text": "e", "quantity_display": "one"},
		},
	}))
	// "1e3" parses as a Decimal (B2 is about NaN/exponents; only NaN is fixed to null).
	expectQuantities(t, r, []any{"0.2500", "1000.0000", "2.0000", "0.3333", nil})
}

// TestRecipesValidation freezes the custom 422 shape and its `field` for the cases the app
// can hit (§3 "Error shapes", §11.3).
func TestRecipesValidation(t *testing.T) {
	tg := T(t)
	k := SharedKitchen(t)
	h := k.Owner.H()

	noIngredients := recipeBody("x", nil)
	delete(noIngredients, "ingredients")
	tg.Post(t, Recipes, "/recipes", noIngredients, h).Expect(http.StatusUnprocessableEntity, recipesValidation("body.ingredients"))
	tg.Post(t, Recipes, "/recipes", recipeBody("x", map[string]any{"ingredients": []any{}}), h).
		Expect(http.StatusUnprocessableEntity, recipesValidation("body.ingredients"))
	tg.Post(t, Recipes, "/recipes", recipeBody("x", map[string]any{"steps": []any{}}), h).
		Expect(http.StatusUnprocessableEntity, recipesValidation("body.steps"))
	tg.Post(t, Recipes, "/recipes", recipeBody("x", map[string]any{"source_type": "URL"}), h).
		Expect(http.StatusUnprocessableEntity, recipesValidation("body.source_type"))
	tg.Post(t, Recipes, "/recipes", "{not json", h).ExpectStatus(http.StatusUnprocessableEntity).
		ExpectShape(Obj{"error_code": Eq("validation_error"), "message": Eq("Invalid request payload."),
			"details": NonEmptyArrayOf(Obj{"field": NullOr(String), "message": String}), "job_id": UUID})
	tg.Get(t, Recipes, "/shopping-list?start_date=bad&end_date=2026-01-01", h).
		Expect(http.StatusUnprocessableEntity, recipesValidation("query.start_date"))
	tg.Get(t, Recipes, "/shopping-list?start_date=2026-01-01", h).
		Expect(http.StatusUnprocessableEntity, recipesValidation("query.end_date"))
	tg.Get(t, Recipes, "/planner/plans/abc", h).Expect(http.StatusUnprocessableEntity, recipesValidation("path.plan_id"))

	// job_id is fresh per response (§8 item 12).
	a := tg.Get(t, Recipes, "/recipes/abc", h).Object()["job_id"]
	b := tg.Get(t, Recipes, "/recipes/abc", h).Object()["job_id"]
	if a == b {
		t.Fatalf("422 job_id should be a fresh uuid per response, got %v twice", a)
	}
}

// TestRecipesScoping covers §4.1: household members share the box, outsiders get 404, and
// the author-only reads.
func TestRecipesScoping(t *testing.T) {
	tg := T(t)
	k := SharedKitchen(t)
	owner, member, outsider := k.Owner.H(), k.MemberH, k.Outsider.H()

	r := NewRecipe(t, owner, recipeBody("contract shared", nil))
	path := fmt.Sprintf("/recipes/%d", idOf(r))

	// The member sees and edits it.
	if findBy(arr(tg.Get(t, Recipes, "/recipes", member).ExpectStatus(http.StatusOK)), "id", idOf(r)) == nil {
		t.Fatalf("member should see the household's recipe %d", idOf(r))
	}
	tg.Get(t, Recipes, path, member).Expect(http.StatusOK, recipeRead)
	tg.Do(t, Recipes, http.MethodPatch, path, map[string]any{"description": "edited by member"}, member).
		Expect(http.StatusOK, Open{"description": Eq("edited by member"), "user_id": Eq(fmt.Sprint(k.Owner.ID))})

	// #6 GET /recipes/user/{id} is author-only.
	tg.Get(t, Recipes, "/recipes/user/"+fmt.Sprint(idOf(r)), owner).Expect(http.StatusOK, recipeRead)
	tg.Get(t, Recipes, "/recipes/user/"+fmt.Sprint(idOf(r)), member).ExpectError(http.StatusNotFound, "Recipe not found")

	// The outsider gets 404 everywhere, never 403.
	if findBy(arr(tg.Get(t, Recipes, "/recipes", outsider).ExpectStatus(http.StatusOK)), "id", idOf(r)) != nil {
		t.Fatalf("outsider must not see recipe %d", idOf(r))
	}
	tg.Get(t, Recipes, path, outsider).ExpectError(http.StatusNotFound, "Recipe not found")
	tg.Do(t, Recipes, http.MethodPatch, path, map[string]any{"title": "x"}, outsider).ExpectError(http.StatusNotFound, "Recipe not found")
	tg.Do(t, Recipes, http.MethodDelete, path, nil, outsider).ExpectError(http.StatusNotFound, "Recipe not found")
	tg.Get(t, Recipes, "/recipes/user/"+fmt.Sprint(idOf(r)), outsider).ExpectError(http.StatusNotFound, "Recipe not found")

	// The member belongs to two households: their solo one (the claim in their login token,
	// the first membership) and the kitchen. A recipe in the solo household:
	solo := NewRecipe(t, k.Member.H(), recipeBody("contract member solo", nil))
	soloPath := fmt.Sprintf("/recipes/%d", idOf(solo))
	tg.Get(t, Recipes, soloPath, owner).ExpectError(http.StatusNotFound, "Recipe not found")
	tg.Get(t, Recipes, soloPath, outsider).ExpectError(http.StatusNotFound, "Recipe not found")
	if Jarvisd() {
		// RD7: reads are the union of every household the caller belongs to, whichever one
		// the token names; members may edit rows of any of their households.
		tg.Get(t, Recipes, path, k.Member.H()).Expect(http.StatusOK, recipeRead)
		tg.Get(t, Recipes, soloPath, member).Expect(http.StatusOK, recipeRead)
		tg.Do(t, Recipes, http.MethodPatch, path, map[string]any{"description": "edited via solo token"}, k.Member.H()).
			Expect(http.StatusOK, Open{"description": Eq("edited via solo token")})
		ids := map[int]bool{}
		for _, e := range arr(tg.Get(t, Recipes, "/recipes", k.Member.H()).ExpectStatus(http.StatusOK)) {
			ids[idOf(e.(map[string]any))] = true
		}
		if !ids[idOf(r)] || !ids[idOf(solo)] {
			t.Fatalf("RD7: the member's list should hold both households' recipes (%d, %d), got %v", idOf(r), idOf(solo), ids)
		}
	} else {
		// LEGACY-BUG (RD7): scoping is the token's household only, so each token sees one box.
		tg.Get(t, Recipes, path, k.Member.H()).ExpectError(http.StatusNotFound, "Recipe not found")
		tg.Get(t, Recipes, soloPath, member).ExpectError(http.StatusNotFound, "Recipe not found")
		if findBy(arr(tg.Get(t, Recipes, "/recipes", k.Member.H()).ExpectStatus(http.StatusOK)), "id", idOf(r)) != nil {
			t.Fatalf("legacy: the solo-household token must not list the kitchen's recipe")
		}
	}

	// A member's recipe is the household's too, authored by the member; the member deletes it.
	mr := NewRecipe(t, member, recipeBody("contract by member", nil))
	tg.Get(t, Recipes, fmt.Sprintf("/recipes/%d", idOf(mr)), owner).
		Expect(http.StatusOK, Open{"user_id": Eq(fmt.Sprint(k.Member.ID))})
	tg.Do(t, Recipes, http.MethodDelete, path, nil, member).ExpectStatus(http.StatusNoContent)
}

// TestRecipesTags covers #23 and #24.
func TestRecipesTags(t *testing.T) {
	tg := T(t)
	k := SharedKitchen(t)
	h := k.Owner.H()

	NewRecipe(t, h, recipeBody("contract tagged", map[string]any{"tags": []string{"contract-tag-b", "Contract-Tag-A"}}))
	// #23: tags on visible recipes, ordered by name; global, matched case-insensitively.
	tags := arr(tg.Get(t, Recipes, "/tags", h).Expect(http.StatusOK, ArrayOf(tagShape)))
	var names []string
	for _, tg := range tags {
		names = append(names, tg.(map[string]any)["name"].(string))
	}
	if !sameFold(names, []string{"contract-tag-a", "contract-tag-b"}) {
		t.Fatalf("GET /tags: want the two tags on the household's recipes, got %v", names)
	}
	if len(arr(tg.Get(t, Recipes, "/tags", k.Outsider.H()).ExpectStatus(http.StatusOK))) != 0 {
		t.Fatalf("GET /tags: an outsider with no recipes sees no tags")
	}

	// #24 POST /tags: global get-or-create.
	a := tg.Post(t, Recipes, "/tags", map[string]string{"name": "  contract-tag-posted "}, h).
		Expect(http.StatusCreated, Obj{"id": Int, "name": Eq("contract-tag-posted")}).Object()
	b := tg.Post(t, Recipes, "/tags", map[string]string{"name": "CONTRACT-TAG-POSTED"}, h).
		Expect(http.StatusCreated, tagShape).Object()
	if Jarvisd() {
		if idOf(a) != idOf(b) {
			t.Fatalf("POST /tags is get-or-create: ids %d and %d", idOf(a), idOf(b))
		}
	} else if idOf(a) == idOf(b) {
		// LEGACY-BUG B9 (CHANGE #24): the tag is only flushed, never committed, so each call
		// mints a new (phantom) id from the sequence.
		t.Fatalf("legacy POST /tags never commits; expected a fresh phantom id, got %d twice", idOf(a))
	}
	tg.Post(t, Recipes, "/tags", map[string]string{"name": "   "}, h).ExpectError(http.StatusBadRequest, "Tag name required")
	tg.Post(t, Recipes, "/tags", map[string]string{}, h).Expect(http.StatusUnprocessableEntity, recipesValidation("body.name"))
}

// TestRecipesStock covers #27 and #28 (the editor's pickers).
func TestRecipesStock(t *testing.T) {
	tg := T(t)
	k := SharedKitchen(t)
	h := k.Owner.H()

	ings := arr(tg.Get(t, Recipes, "/ingredients/stock?q=&limit=1000", h).
		Expect(http.StatusOK, ArrayOf(Obj{"id": Int, "name": String})))
	if len(ings) == 0 && !Jarvisd() {
		secret := os.Getenv(EnvRecipesAdminSecret)
		if secret == "" {
			t.Skipf("stock reference data is empty on the target; set %s to seed it once", EnvRecipesAdminSecret)
		}
		// Reference data, seeded once and not cleaned up (§12).
		tg.Post(t, Recipes, "/admin/static-data/seed", nil, H{"X-Admin-Secret": secret}).
			Expect(http.StatusOK, Obj{"ingredients_inserted": Int, "ingredients_updated": Int, "units_inserted": Int, "units_updated": Int})
		ings = arr(tg.Get(t, Recipes, "/ingredients/stock?q=&limit=1000", h).ExpectStatus(http.StatusOK))
	}
	units := arr(tg.Get(t, Recipes, "/units/stock?limit=100", h).
		Expect(http.StatusOK, ArrayOf(Obj{"id": Int, "name": String, "abbreviation": NullOr(String)})))
	// The embedded static_data (§5.3): 198 ingredients, 51 units.
	if len(ings) != 198 || len(units) != 51 {
		t.Fatalf("stock: want 198 ingredients and 51 units, got %d and %d", len(ings), len(units))
	}
	// Ordered by name, but in the database's collation: Postgres en_US puts "blackberries"
	// before "black pepper" (spaces ignored), SQLite's BINARY does not. The picker sorts
	// nothing itself and nobody relies on the tie-break, so the order is not frozen.
	// Defaults and filters.
	if n := len(arr(tg.Get(t, Recipes, "/ingredients/stock", h).ExpectStatus(http.StatusOK))); n != 10 {
		t.Fatalf("default limit is 10, got %d", n)
	}
	for _, e := range arr(tg.Get(t, Recipes, "/ingredients/stock?q=SALT&limit=1000", h).ExpectStatus(http.StatusOK)) {
		if !strings.Contains(strings.ToLower(e.(map[string]any)["name"].(string)), "salt") {
			t.Fatalf("q is a case-insensitive substring match: %v", e)
		}
	}
	cup := arr(tg.Get(t, Recipes, "/units/stock?q=tbsp&limit=100", h).ExpectStatus(http.StatusOK))
	if len(cup) == 0 {
		t.Fatalf("units q matches the abbreviation too (tbsp)")
	}
	for _, q := range []string{"limit=0", "limit=1001"} {
		tg.Get(t, Recipes, "/ingredients/stock?"+q, h).Expect(http.StatusUnprocessableEntity, recipesValidation("query.limit"))
	}
	tg.Get(t, Recipes, "/units/stock?limit=101", h).Expect(http.StatusUnprocessableEntity, recipesValidation("query.limit"))
}

// TestRecipesStaples covers #43–#45.
func TestRecipesStaples(t *testing.T) {
	tg := T(t)
	k := SharedKitchen(t)
	h := k.Owner.H()

	a := tg.Post(t, Recipes, "/staples", map[string]string{"name": "2 tbsp Olive Oil, divided"}, h).
		Expect(http.StatusCreated, Obj{"id": Int, "name": Eq("olive oil")}).Object()
	t.Cleanup(func() { cleanupRecipesPath(t, fmt.Sprintf("/staples/%d", idOf(a)), h) })
	// Idempotent, 201 again, also for another member of the household.
	tg.Post(t, Recipes, "/staples", map[string]string{"name": "olive oil"}, h).
		Expect(http.StatusCreated, Obj{"id": Eq(idOf(a)), "name": Eq("olive oil")})
	tg.Post(t, Recipes, "/staples", map[string]string{"name": "OLIVE OIL"}, k.MemberH).
		Expect(http.StatusCreated, Obj{"id": Eq(idOf(a)), "name": Eq("olive oil")})
	s := tg.Post(t, Recipes, "/staples", map[string]string{"name": "Salt and pepper to taste"}, k.MemberH).
		Expect(http.StatusCreated, Obj{"id": Int, "name": Eq("salt and pepper")}).Object()
	t.Cleanup(func() { cleanupRecipesPath(t, fmt.Sprintf("/staples/%d", idOf(s)), h) })

	list := arr(tg.Get(t, Recipes, "/staples", h).Expect(http.StatusOK, ArrayOf(Obj{"id": Int, "name": String})))
	if len(list) != 2 || list[0].(map[string]any)["name"] != "olive oil" || list[1].(map[string]any)["name"] != "salt and pepper" {
		t.Fatalf("GET /staples: want [olive oil, salt and pepper] by name, got %v", show(list))
	}
	if len(arr(tg.Get(t, Recipes, "/staples", k.Outsider.H()).ExpectStatus(http.StatusOK))) != 0 {
		t.Fatalf("an outsider sees no staples")
	}

	tg.Post(t, Recipes, "/staples", map[string]string{"name": ""}, h).Expect(http.StatusUnprocessableEntity, recipesValidation("body.name"))
	tg.Post(t, Recipes, "/staples", map[string]string{"name": strings.Repeat("x", 201)}, h).
		Expect(http.StatusUnprocessableEntity, recipesValidation("body.name"))
	// A code-raised 422 uses the plain detail shape.
	tg.Post(t, Recipes, "/staples", map[string]string{"name": "   "}, h).ExpectError(http.StatusUnprocessableEntity, "A staple needs a name")

	tg.Do(t, Recipes, http.MethodDelete, fmt.Sprintf("/staples/%d", idOf(s)), nil, k.Outsider.H()).ExpectError(http.StatusNotFound, "Staple not found")
	tg.Do(t, Recipes, http.MethodDelete, fmt.Sprintf("/staples/%d", idOf(s)), nil, h).ExpectStatus(http.StatusNoContent).ExpectEmpty()
	tg.Do(t, Recipes, http.MethodDelete, fmt.Sprintf("/staples/%d", idOf(s)), nil, h).ExpectError(http.StatusNotFound, "Staple not found")
}

// TestRecipesSkuMap covers #46–#48.
func TestRecipesSkuMap(t *testing.T) {
	tg := T(t)
	k := SharedKitchen(t)
	h := k.Owner.H()
	t.Cleanup(func() { cleanupSkuMap(t, h) })

	tg.Get(t, Recipes, "/grocery/sku-map", h).Expect(http.StatusOK, ArrayOf(skuMappingRead))
	// The app's ProductPicker sends no retailer. The name is stored stripped and lowercased.
	m := tg.Do(t, Recipes, http.MethodPut, "/grocery/sku-map", map[string]any{
		"ingredient_name": " Olive Oil ", "sku": "111", "product_name": "Great Value Olive Oil", "unit_size": "17 oz",
	}, h).Expect(http.StatusOK, Obj{
		"id": Int, "retailer": Eq("walmart"), "ingredient_name": Eq("olive oil"), "sku": Eq("111"),
		"product_name": Eq("Great Value Olive Oil"), "unit_size": Eq("17 oz"), "source": Eq("manual"),
	}).Object()
	// Upsert: same row, new values; omitted optionals become null.
	tg.Do(t, Recipes, http.MethodPut, "/grocery/sku-map", map[string]any{
		"ingredient_name": "olive oil", "sku": "222", "retailer": "walmart",
	}, k.MemberH).Expect(http.StatusOK, Obj{
		"id": Eq(idOf(m)), "retailer": Eq("walmart"), "ingredient_name": Eq("olive oil"), "sku": Eq("222"),
		"product_name": Null, "unit_size": Null, "source": Eq("manual"),
	})
	// raw=true normalises a recipe line into the shopping key.
	b := tg.Do(t, Recipes, http.MethodPut, "/grocery/sku-map", map[string]any{
		"ingredient_name": "2 lb Ground Beef (93/7 or leaner)", "raw": true, "sku": "333",
	}, h).Expect(http.StatusOK, Open{"ingredient_name": Eq("ground beef")}).Object()

	list := arr(tg.Get(t, Recipes, "/grocery/sku-map?retailer=walmart", h).Expect(http.StatusOK, ArrayOf(skuMappingRead)))
	if len(list) != 2 || list[0].(map[string]any)["ingredient_name"] != "ground beef" {
		t.Fatalf("GET /grocery/sku-map: want [ground beef, olive oil], got %v", show(list))
	}
	if len(arr(tg.Get(t, Recipes, "/grocery/sku-map", k.Outsider.H()).ExpectStatus(http.StatusOK))) != 0 {
		t.Fatalf("an outsider sees no mappings")
	}

	tg.Get(t, Recipes, "/grocery/sku-map?retailer=target", h).Expect(http.StatusUnprocessableEntity, recipesValidation("query.retailer"))
	tg.Do(t, Recipes, http.MethodPut, "/grocery/sku-map", map[string]any{"ingredient_name": "x", "sku": ""}, h).
		Expect(http.StatusUnprocessableEntity, recipesValidation("body.sku"))
	tg.Do(t, Recipes, http.MethodPut, "/grocery/sku-map", map[string]any{"sku": "1"}, h).
		Expect(http.StatusUnprocessableEntity, recipesValidation("body.ingredient_name"))

	tg.Do(t, Recipes, http.MethodDelete, fmt.Sprintf("/grocery/sku-map/%d", idOf(b)), nil, k.Outsider.H()).ExpectError(http.StatusNotFound, "Mapping not found")
	tg.Do(t, Recipes, http.MethodDelete, fmt.Sprintf("/grocery/sku-map/%d", idOf(b)), nil, h).ExpectStatus(http.StatusNoContent).ExpectEmpty()
	tg.Do(t, Recipes, http.MethodDelete, fmt.Sprintf("/grocery/sku-map/%d", idOf(b)), nil, h).ExpectError(http.StatusNotFound, "Mapping not found")
}

// cleanupSkuMap deletes every mapping h can see (the kitchen's are all this run's).
func cleanupSkuMap(t testing.TB, h H) {
	tg := T(t)
	r, err := tg.do(Recipes, http.MethodGet, "/grocery/sku-map", nil, h)
	if err != nil || r.Status != http.StatusOK {
		t.Errorf("cleanup sku-map list: %v", err)
		return
	}
	var rows []struct {
		ID int `json:"id"`
	}
	_ = r.expect(http.StatusOK, &rows)
	for _, row := range rows {
		cleanupRecipesPath(t, fmt.Sprintf("/grocery/sku-map/%d", row.ID), h)
	}
}

// commitPlan commits a plan as h and deletes it when the test ends.
func commitPlan(t testing.TB, h H, body map[string]any) map[string]any {
	t.Helper()
	obj := T(t).Post(t, Recipes, "/planner/commit", body, h).Expect(http.StatusOK, mealPlanRead).Object()
	t.Cleanup(func() { cleanupRecipesPath(t, fmt.Sprintf("/planner/plans/%d", idOf(obj)), h) })
	return obj
}

func planItem(date, meal string, recipeID any) map[string]any {
	return map[string]any{"date": date, "meal_type": meal, "recipe_id": recipeID}
}

// TestRecipesPlanner covers #36–#41.
func TestRecipesPlanner(t *testing.T) {
	tg := T(t)
	k := SharedKitchen(t)
	h := k.Owner.H()

	// "Today" is the container's date on legacy (UTC on the MBP); a plan two days out is
	// current under any zone.
	tg.Get(t, Recipes, "/planner/current", h).Expect(http.StatusOK, Obj{})
	if n := len(arr(tg.Get(t, Recipes, "/planner/plans", h).ExpectStatus(http.StatusOK))); n != 0 {
		t.Fatalf("the kitchen should start with no plans, got %d", n)
	}

	r1 := NewRecipe(t, h, recipeBody("contract plan one", map[string]any{"total_time_minutes": 15, "image_url": "https://example.com/1.jpg"}))
	r2 := NewRecipe(t, h, recipeBody("contract plan two", nil))
	r3 := NewRecipe(t, h, recipeBody("contract plan three", nil))

	// #36 commit: 200 (not 201); recipe_id is lax (the string "5" is an int).
	plan := commitPlan(t, h, map[string]any{
		"name": "contract week", "start_date": day(5),
		"items": []any{
			planItem(day(3), "dinner", idOf(r1)),
			planItem(day(4), "dinner", fmt.Sprint(idOf(r2))),
			map[string]any{"date": day(4), "meal_type": "lunch", "recipe_id": idOf(r3), "source": "user"},
		},
	})
	pid := idOf(plan)
	expectFields(t, plan, map[string]any{"name": "contract week", "start_date": day(5), "user_id": fmt.Sprint(k.Owner.ID)})
	it := findBy(plan["items"].([]any), "recipe_id", idOf(r1))
	if it == nil || it["title"] != "contract plan one" || it["image_url"] != "https://example.com/1.jpg" || mustFloat(it["total_time_minutes"]) != 15 {
		t.Fatalf("items carry the recipe's title, image and time: %v", show(plan["items"]))
	}

	// #37 current, #38 list, #39 get.
	tg.Get(t, Recipes, "/planner/current", h).Expect(http.StatusOK, All(mealPlanRead, Open{"id": Eq(pid)}))
	tg.Get(t, Recipes, "/planner/current", k.MemberH).Expect(http.StatusOK, Open{"id": Eq(pid)})
	tg.Get(t, Recipes, "/planner/current", k.Outsider.H()).Expect(http.StatusOK, Obj{})
	list := arr(tg.Get(t, Recipes, "/planner/plans", h).Expect(http.StatusOK, ArrayOf(mealPlanSummary)))
	if len(list) != 1 {
		t.Fatalf("GET /planner/plans: want 1, got %v", show(list))
	}
	// start/end are the span of the items, not the stored start_date.
	expectFields(t, list[0].(map[string]any), map[string]any{"id": pid, "name": "contract week", "start_date": day(3), "end_date": day(4), "meal_count": 3})
	tg.Get(t, Recipes, fmt.Sprintf("/planner/plans/%d", pid), k.MemberH).Expect(http.StatusOK, Open{"id": Eq(pid)})
	tg.Get(t, Recipes, fmt.Sprintf("/planner/plans/%d", pid), k.Outsider.H()).ExpectError(http.StatusNotFound, "Meal plan not found")
	tg.Get(t, Recipes, "/planner/plans/999999999", h).ExpectError(http.StatusNotFound, "Meal plan not found")

	// #41 move: onto an occupied slot swaps; start_date becomes the earliest item.
	i1 := idOf(findBy(plan["items"].([]any), "recipe_id", idOf(r1)))
	i2 := idOf(findBy(plan["items"].([]any), "recipe_id", idOf(r2)))
	i3 := idOf(findBy(plan["items"].([]any), "recipe_id", idOf(r3)))
	movePath := fmt.Sprintf("/planner/plans/%d/items", pid)
	moved := tg.Do(t, Recipes, http.MethodPatch, movePath, map[string]any{
		"moves": []any{map[string]any{"item_id": i1, "date": day(4), "meal_type": "dinner"}},
	}, k.MemberH).Expect(http.StatusOK, mealPlanRead).Object()
	expectFields(t, findBy(moved["items"].([]any), "id", i1), map[string]any{"date": day(4), "meal_type": "dinner"})
	expectFields(t, findBy(moved["items"].([]any), "id", i2), map[string]any{"date": day(3), "meal_type": "dinner"})
	expectFields(t, moved, map[string]any{"start_date": day(3)})
	moved = tg.Do(t, Recipes, http.MethodPatch, movePath, map[string]any{
		"moves": []any{map[string]any{"item_id": i2, "date": day(6), "meal_type": "breakfast"}},
	}, h).Expect(http.StatusOK, mealPlanRead).Object()
	expectFields(t, moved, map[string]any{"start_date": day(4)})
	tg.Do(t, Recipes, http.MethodPatch, movePath, map[string]any{"moves": []any{}}, h).Expect(http.StatusOK, Open{"id": Eq(pid)})
	tg.Do(t, Recipes, http.MethodPatch, movePath, map[string]any{"moves": []any{
		map[string]any{"item_id": i1, "date": day(9), "meal_type": "dinner"},
		map[string]any{"item_id": i3, "date": day(9), "meal_type": "dinner"},
	}}, h).ExpectError(http.StatusConflict, "Two meals cannot be moved to the same day and meal type")
	tg.Do(t, Recipes, http.MethodPatch, movePath, map[string]any{"moves": []any{
		map[string]any{"item_id": 999999999, "date": day(9), "meal_type": "dinner"},
	}}, h).ExpectError(http.StatusNotFound, "Item 999999999 is not part of this plan")
	tg.Do(t, Recipes, http.MethodPatch, movePath, map[string]any{"moves": []any{
		map[string]any{"item_id": i1, "date": day(9), "meal_type": "dinner"},
	}}, k.Outsider.H()).ExpectError(http.StatusNotFound, "Meal plan not found")

	// Deleting a recipe that a plan uses.
	del := tg.Do(t, Recipes, http.MethodDelete, fmt.Sprintf("/recipes/%d", idOf(r3)), nil, h)
	if Jarvisd() {
		// §4.2: the item goes with the recipe; the plan is not repaired.
		del.ExpectStatus(http.StatusNoContent)
		got := tg.Get(t, Recipes, fmt.Sprintf("/planner/plans/%d", pid), h).Expect(http.StatusOK, mealPlanRead).Object()
		if len(got["items"].([]any)) != 2 {
			t.Fatalf("deleting a recipe should drop its plan item: %v", show(got["items"]))
		}
	} else {
		// LEGACY-BUG: the ORM nulls meal_plan_items.recipe_id instead of letting the FK
		// cascade (NotNullViolation): 500, nothing is deleted, and uvicorn drops the
		// keep-alive connection.
		del.ExpectStatus(http.StatusInternalServerError)
		tg.HTTP.CloseIdleConnections()
		tg.Get(t, Recipes, fmt.Sprintf("/recipes/%d", idOf(r3)), h).ExpectStatus(http.StatusOK)
	}

	// Commit errors.
	tg.Post(t, Recipes, "/planner/commit", map[string]any{"start_date": day(1), "items": []any{
		map[string]any{"date": day(1), "meal_type": "dinner", "recipe_id": 999999999, "source": "stage"},
	}}, h).ExpectError(http.StatusNotFound, "Staged recipe 999999999 not found")
	tg.Post(t, Recipes, "/planner/commit", map[string]any{"start_date": day(1), "items": []any{
		map[string]any{"date": day(1), "meal_type": "dinner", "recipe_id": idOf(r1), "source": "core"},
	}}, h).Expect(http.StatusUnprocessableEntity, recipesValidation("body.items.0.source"))
	tg.Post(t, Recipes, "/planner/commit", map[string]any{"items": []any{}}, h).
		Expect(http.StatusUnprocessableEntity, recipesValidation("body.start_date"))

	// CHANGE #36 (B14): a source:"user" recipe must be visible to the caller.
	foreign := NewRecipe(t, k.Outsider.H(), recipeBody("contract outsider's", nil))
	foreignBody := map[string]any{"start_date": day(1), "items": []any{planItem(day(1), "dinner", idOf(foreign))}}
	missingBody := map[string]any{"start_date": day(1), "items": []any{planItem(day(1), "dinner", 999999999)}}
	if Jarvisd() {
		tg.Post(t, Recipes, "/planner/commit", foreignBody, h).ExpectError(http.StatusNotFound, "Recipe not found")
		tg.Post(t, Recipes, "/planner/commit", missingBody, h).ExpectError(http.StatusNotFound, "Recipe not found")
	} else {
		// LEGACY-BUG B14: another household's recipe is accepted, and its title leaks into
		// the plan; a nonexistent id is an FK violation, 500.
		leak := commitPlan(t, h, foreignBody)
		if it := leak["items"].([]any)[0].(map[string]any); it["title"] != "contract outsider's" {
			t.Fatalf("legacy commit leaks the foreign recipe's title: %v", show(it))
		}
		tg.Post(t, Recipes, "/planner/commit", missingBody, h).ExpectStatus(http.StatusInternalServerError)
		tg.HTTP.CloseIdleConnections()
	}

	// #40 delete.
	tg.Do(t, Recipes, http.MethodDelete, fmt.Sprintf("/planner/plans/%d", pid), nil, k.Outsider.H()).ExpectError(http.StatusNotFound, "Meal plan not found")
	tg.Do(t, Recipes, http.MethodDelete, fmt.Sprintf("/planner/plans/%d", pid), nil, h).ExpectStatus(http.StatusNoContent).ExpectEmpty()
	tg.Get(t, Recipes, fmt.Sprintf("/planner/plans/%d", pid), h).ExpectError(http.StatusNotFound, "Meal plan not found")
	// Recipes stay.
	tg.Get(t, Recipes, fmt.Sprintf("/recipes/%d", idOf(r1)), h).ExpectStatus(http.StatusOK)
}

// TestRecipesRandomPlan covers #33 and #34.
func TestRecipesRandomPlan(t *testing.T) {
	tg := T(t)
	k := SharedKitchen(t)
	h := k.Owner.H()

	a := NewRecipe(t, h, recipeBody("contract random a", map[string]any{"tags": []string{"contract-tag-dinner"}, "servings": 2}))
	b := NewRecipe(t, k.MemberH, recipeBody("contract random b", nil))
	slots := []any{
		map[string]any{"date": day(1), "meal_type": "contract-tag-dinner"},
		map[string]any{"date": day(2), "meal_type": "dinner"},
		map[string]any{"date": day(3), "meal_type": "dinner"},
	}
	res := tg.Post(t, Recipes, "/meal-plans/random", map[string]any{"slots": slots}, h).
		Expect(http.StatusOK, Obj{"slots": ArrayOf(randomSlot), "incomplete": Eq(true)}).Object()
	got := res["slots"].([]any)
	// The tagged recipe is preferred for its meal type; no repeats; the third slot is empty.
	expectFields(t, got[0].(map[string]any), map[string]any{"date": day(1), "meal_type": "contract-tag-dinner", "recipe_id": idOf(a), "title": "contract random a", "servings": 2})
	expectFields(t, got[1].(map[string]any), map[string]any{"recipe_id": idOf(b)})
	expectFields(t, got[2].(map[string]any), map[string]any{"date": day(3), "meal_type": "dinner", "recipe_id": nil, "title": nil})

	res = tg.Post(t, Recipes, "/meal-plans/random", map[string]any{"slots": slots[:1], "exclude_recipe_ids": []int{idOf(a)}}, h).
		Expect(http.StatusOK, Obj{"slots": ArrayOf(randomSlot), "incomplete": Eq(false)}).Object()
	expectFields(t, res["slots"].([]any)[0].(map[string]any), map[string]any{"recipe_id": idOf(b)})
	tg.Post(t, Recipes, "/meal-plans/random", map[string]any{"slots": []any{}}, h).
		Expect(http.StatusOK, Obj{"slots": Eq([]any{}), "incomplete": Eq(false)})
	tg.Post(t, Recipes, "/meal-plans/random", map[string]any{"slots": []any{map[string]any{"date": "x", "meal_type": "dinner"}}}, h).
		Expect(http.StatusUnprocessableEntity, recipesValidation("body.slots.0.date"))

	// #34 reroll: one slot, date null, meal_type echoed or "".
	tg.Post(t, Recipes, "/meal-plans/random/reroll", map[string]any{"meal_type": "dinner", "exclude_recipe_ids": []int{idOf(a)}}, h).
		Expect(http.StatusOK, All(randomSlot, Open{"date": Null, "meal_type": Eq("dinner"), "recipe_id": Eq(idOf(b))}))
	tg.Post(t, Recipes, "/meal-plans/random/reroll", map[string]any{"exclude_recipe_ids": []int{idOf(b)}, "tags": []string{"nope"}}, h).
		Expect(http.StatusOK, All(randomSlot, Open{"date": Null, "meal_type": Eq(""), "recipe_id": Eq(idOf(a))}))
	tg.Post(t, Recipes, "/meal-plans/random/reroll", map[string]any{"exclude_recipe_ids": []int{idOf(a), idOf(b)}}, h).
		ExpectError(http.StatusConflict, "No other recipe available to swap in. Add more recipes, or clear a slot.")
	tg.Post(t, Recipes, "/meal-plans/random/reroll", map[string]any{}, k.Outsider.H()).
		ExpectError(http.StatusConflict, "No other recipe available to swap in. Add more recipes, or clear a slot.")
}

// TestRecipesShoppingAndCart covers #42 and #49 (with the map from #47).
func TestRecipesShoppingAndCart(t *testing.T) {
	tg := T(t)
	k := SharedKitchen(t)
	h := k.Owner.H()
	t.Cleanup(func() { cleanupSkuMap(t, h) })

	r1 := NewRecipe(t, h, recipeBody("contract chili", map[string]any{"ingredients": []map[string]any{
		{"text": "2 lb ground beef", "quantity_display": "2", "unit": "lb"},
		{"text": "1 cup Olive Oil, divided", "quantity_display": "1", "unit": "Cup "},
		{"text": "Salt to taste"},
		{"text": "2 cloves garlic, minced", "quantity_display": "2", "unit": "cloves"},
	}}))
	r2 := NewRecipe(t, h, recipeBody("contract tacos", map[string]any{"ingredients": []map[string]any{
		{"text": "1 1/2 lb Ground Beef (93/7 or leaner)", "quantity_display": "1 1/2", "unit": "LB"},
		{"text": "2 tbsp olive oil", "quantity_display": "2", "unit": "tbsp"},
		{"text": "salt"},
	}}))
	r3 := NewRecipe(t, h, recipeBody("contract out of range", map[string]any{"ingredients": []map[string]any{
		{"text": "1 lb ground beef", "quantity_display": "1", "unit": "lb"},
	}}))
	commitPlan(t, h, map[string]any{"start_date": day(1), "items": []any{
		planItem(day(1), "dinner", idOf(r1)),
		planItem(day(2), "dinner", idOf(r2)),
		planItem(day(20), "dinner", idOf(r3)), // outside the range below
	}})
	staple := tg.Post(t, Recipes, "/staples", map[string]string{"name": "salt"}, h).ExpectStatus(http.StatusCreated).Object()
	t.Cleanup(func() { cleanupRecipesPath(t, fmt.Sprintf("/staples/%d", idOf(staple)), h) })

	q := "?start_date=" + day(0) + "&end_date=" + day(7)
	amount := Obj{"unit": NullOr(String), "quantity": NullOr(Num), "unparsed": ArrayOf(String)}
	list := tg.Get(t, Recipes, "/shopping-list"+q, k.MemberH).Expect(http.StatusOK, Obj{
		"start_date": Eq(day(0)), "end_date": Eq(day(7)), "plan_count": Eq(1),
		"items": ArrayOf(Obj{"name": String, "amounts": ArrayOf(amount), "recipes": ArrayOf(String), "is_staple": Bool}),
	}).Object()
	// Grouped by key, then by lowercased unit; summed when parsed, verbatim otherwise; no unit
	// conversion; sorted by name; staples flagged, not hidden.
	want := []any{
		map[string]any{"name": "garlic", "amounts": []any{map[string]any{"unit": "cloves", "quantity": 2.0, "unparsed": []any{}}}, "recipes": []any{"contract chili"}, "is_staple": false},
		map[string]any{"name": "ground beef", "amounts": []any{map[string]any{"unit": "lb", "quantity": 3.5, "unparsed": []any{}}}, "recipes": []any{"contract chili", "contract tacos"}, "is_staple": false},
		map[string]any{"name": "olive oil", "amounts": []any{
			map[string]any{"unit": "cup", "quantity": 1.0, "unparsed": []any{}},
			map[string]any{"unit": "tbsp", "quantity": 2.0, "unparsed": []any{}},
		}, "recipes": []any{"contract chili", "contract tacos"}, "is_staple": false},
		map[string]any{"name": "salt", "amounts": []any{map[string]any{"unit": nil, "quantity": nil, "unparsed": []any{"Salt to taste", "salt"}}}, "recipes": []any{"contract chili", "contract tacos"}, "is_staple": true},
	}
	if errs := EqDeep(want).Match("$.items", list["items"]); len(errs) > 0 {
		t.Fatalf("shopping list items:\n  %s\n  got %s", strings.Join(errs, "\n  "), show(list["items"]))
	}
	tg.Get(t, Recipes, "/shopping-list?start_date="+day(30)+"&end_date="+day(31), h).
		Expect(http.StatusOK, Open{"plan_count": Eq(0), "items": Eq([]any{})})
	tg.Get(t, Recipes, "/shopping-list"+q, k.Outsider.H()).Expect(http.StatusOK, Open{"plan_count": Eq(0), "items": Eq([]any{})})

	cartShape := Obj{
		"retailer": Eq("walmart"), "url": NullOr(String),
		"items":        ArrayOf(Obj{"ingredient_name": String, "sku": String, "quantity": Int, "product_name": NullOr(String), "unit_size": NullOr(String), "source": String}),
		"unmatched":    ArrayOf(Obj{"ingredient_name": String, "amount_display": String, "recipes": ArrayOf(String)}),
		"match_job_id": NullOr(String),
	}
	// Empty map: nothing matched, no url, and no match pass (nothing to learn from).
	cart := tg.Post(t, Recipes, "/grocery/cart"+q, nil, h).Expect(http.StatusOK, cartShape).Object()
	expectFields(t, cart, map[string]any{"url": nil, "match_job_id": nil})
	if errs := EqDeep([]any{
		map[string]any{"ingredient_name": "garlic", "amount_display": "2 cloves", "recipes": []any{"contract chili"}},
		map[string]any{"ingredient_name": "ground beef", "amount_display": "3.5 lb", "recipes": []any{"contract chili", "contract tacos"}},
		map[string]any{"ingredient_name": "olive oil", "amount_display": "1 cup, 2 tbsp", "recipes": []any{"contract chili", "contract tacos"}},
	}).Match("$.unmatched", cart["unmatched"]); len(errs) > 0 {
		t.Fatalf("cart unmatched (staples excluded):\n  %s\n  got %s", strings.Join(errs, "\n  "), show(cart["unmatched"]))
	}

	// Map everything that is not a staple: pack quantity = ceil(qty/size) when the units
	// match, else 1; no match job when nothing is unmatched.
	for _, m := range []map[string]any{
		{"ingredient_name": "ground beef", "sku": "1001", "unit_size": "1 lb", "product_name": "Beef 1lb"},
		{"ingredient_name": "olive oil", "sku": "1002", "unit_size": "17 oz"},
		{"ingredient_name": "garlic", "sku": "10 03"},
	} {
		tg.Do(t, Recipes, http.MethodPut, "/grocery/sku-map", m, h).ExpectStatus(http.StatusOK)
	}
	cart = tg.Post(t, Recipes, "/grocery/cart"+q+"&retailer=walmart", nil, k.MemberH).Expect(http.StatusOK, cartShape).Object()
	expectFields(t, cart, map[string]any{
		"url":          "https://affil.walmart.com/cart/addToCart?items=10%2003_1,1001_4,1002_1",
		"match_job_id": nil, "unmatched": []any{},
	})
	expectFields(t, cart["items"].([]any)[1].(map[string]any), map[string]any{
		"ingredient_name": "ground beef", "sku": "1001", "quantity": 4, "product_name": "Beef 1lb", "unit_size": "1 lb", "source": "manual",
	})

	tg.Post(t, Recipes, "/grocery/cart?start_date="+day(7)+"&end_date="+day(0), nil, h).
		ExpectError(http.StatusUnprocessableEntity, "end_date must not be before start_date")
	tg.Post(t, Recipes, "/grocery/cart"+q+"&retailer=target", nil, h).Expect(http.StatusUnprocessableEntity, recipesValidation("query.retailer"))
	tg.Post(t, Recipes, "/grocery/cart?start_date="+day(0), nil, h).Expect(http.StatusUnprocessableEntity, recipesValidation("query.end_date"))
}

// TestRecipesMedia covers #20 and #22.
func TestRecipesMedia(t *testing.T) {
	tg := T(t)
	k := SharedKitchen(t)
	img := tinyJPEG(t)

	// CHANGE #20/#22: jarvisd stores the upload in the blob store; the wire is unchanged.
	// RD3: image_url stays relative ("/media/<hex>.jpg") on the wire; the app resolves it
	// against the recipes base URL (B15 is fixed in the app, not here).
	d := tg.postMultipart(t, "/recipes/import/image", k.Owner.H(), imagePart("file", "photo.jpg", img)).Expect(http.StatusOK, Obj{
		"title":       Eq("Draft from image"),
		"ingredients": Eq([]any{"1 cup ingredient A", "2 tbsp ingredient B"}),
		"steps":       Eq([]any{"Step 1: placeholder", "Step 2: placeholder"}),
		"tags":        Eq([]any{}),
		"image_url":   Regexp(`^/media/[0-9a-f]{32}\.jpg$`),
	}).Object()
	// The file is served unauthenticated (128-bit random names), with static-file headers.
	m := tg.Get(t, Recipes, d["image_url"].(string)).ExpectStatus(http.StatusOK).ExpectMediaType("image/jpeg")
	if !bytes.Equal(m.Body, img) {
		t.Fatalf("GET %s: body differs from the upload (%d vs %d bytes)", d["image_url"], len(m.Body), len(img))
	}
	if m.HeaderVal("ETag") == "" || m.HeaderVal("Last-Modified") == "" {
		t.Fatalf("GET /media: want ETag and Last-Modified, got %v", m.Header)
	}
	tg.Get(t, Recipes, "/media/"+strings.Repeat("0", 32)+".jpg").ExpectError(http.StatusNotFound, "Not Found")

	tg.postMultipart(t, "/recipes/import/image", k.Owner.H(), FormField("other", "x")).
		Expect(http.StatusUnprocessableEntity, recipesValidation("body.file"))
	tg.postMultipart(t, "/recipes/import/image", nil, imagePart("file", "photo.jpg", img)).
		ExpectError(http.StatusUnauthorized, "Not authenticated")
}

// --- assertion helpers ---

// expectFields checks top-level values of obj (numbers compared as float64).
func expectFields(t testing.TB, obj map[string]any, want map[string]any) {
	t.Helper()
	if obj == nil {
		t.Fatalf("expectFields: object is nil (want %v)", want)
	}
	var errs []string
	for k, w := range want {
		errs = append(errs, EqDeep(w).Match("$."+k, obj[k])...)
	}
	if len(errs) > 0 {
		t.Fatalf("%s\n  in %s", strings.Join(errs, "\n  "), show(obj))
	}
}

func expectQuantities(t testing.TB, recipe map[string]any, want []any) {
	t.Helper()
	ings := recipe["ingredients"].([]any)
	var got []any
	for _, i := range ings {
		got = append(got, i.(map[string]any)["quantity_value"])
	}
	if errs := EqDeep(want).Match("$.ingredients[*].quantity_value", got); len(errs) > 0 {
		t.Fatalf("quantity_value: want %v, got %v", want, got)
	}
}

func tagNames(recipe map[string]any) []string {
	var out []string
	for _, tg := range recipe["tags"].([]any) {
		out = append(out, tg.(map[string]any)["name"].(string))
	}
	return out
}

// sameFold reports whether got equals want element-wise, ignoring case (tags are global and
// get-or-created case-insensitively, so the stored casing is whoever created it first).
func sameFold(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if !strings.EqualFold(got[i], want[i]) {
			return false
		}
	}
	return true
}

// expectPrepCook: RD5 (B1) adds prep/cook columns on jarvisd; legacy never stores them.
func expectPrepCook(t testing.TB, recipe map[string]any, prep, cook int) {
	t.Helper()
	if Jarvisd() {
		expectFields(t, recipe, map[string]any{"prep_time_minutes": prep, "cook_time_minutes": cook})
		return
	}
	// LEGACY-BUG B1: folded into total and dropped, so always null.
	expectFields(t, recipe, map[string]any{"prep_time_minutes": nil, "cook_time_minutes": nil})
}

func sorted(s []string) []string {
	out := append([]string(nil), s...)
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i]) < strings.ToLower(out[j]) })
	return out
}
