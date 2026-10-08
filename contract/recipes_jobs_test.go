//go:build contract

package contract

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"
)

// Contract for jarvis-recipes-server's job routes (docs/recipes/00-inventory.md §3.2, §3.4,
// §4.3–§4.5, step R0): the URL preflight, the webview import pipeline end to end, the job
// list and cancel matrix, photo-import validation, and meal-plan job polling. The legacy
// target needs its RQ worker (`parse-worker` container on the MBP) for anything that polls.
//
// Slow tier (JARVIS_CONTRACT_RECIPES_SLOW=1, a real model on the target): LLM meal-plan
// generation, the grocery SKU-match job, and photo import (JARVIS_CONTRACT_RECIPES_PHOTO).

// soupJSONLD is the schema.org block the WebView would collect.
const soupJSONLD = `{"@context":"https://schema.org","@type":"Recipe","name":"Contract Soup",` +
	`"description":"A soup.","recipeYield":"4 servings","prepTime":"PT15M","cookTime":"PT30M",` +
	`"totalTime":"PT45M","recipeIngredient":["2 cups water","1 tsp salt","1 onion, diced"],` +
	`"recipeInstructions":[{"@type":"HowToStep","text":"Boil the water."},{"@type":"HowToStep","text":"Season."}],` +
	`"keywords":"soup, easy","image":"https://example.com/soup.jpg"}`

var draftIngredient = Obj{"name": String, "quantity": NullOr(String), "unit": NullOr(String), "notes": Null}

// webviewResult is mark_complete's result_json for an ingestion job (§3.2).
var webviewResult = Obj{
	"recipe_draft": Obj{
		"title": String, "description": NullOr(String), "ingredients": ArrayOf(draftIngredient),
		"steps": ArrayOf(String), "prep_time_minutes": NullOr(Int), "cook_time_minutes": NullOr(Int),
		"total_time_minutes": NullOr(Int), "servings": NullOr(Int), "tags": ArrayOf(String),
		"source": Obj{"type": Eq("url"), "source_url": NullOr(String), "image_url": NullOr(String)},
	},
	"pipeline": Obj{
		"parser_strategy": NullOr(String), "used_llm": NullOr(Bool), "warnings": ArrayOf(String),
		"source_url": NullOr(String), "error_code": NullOr(String), "error_message": NullOr(String),
		"next_action": NullOr(String), "next_action_reason": NullOr(String), "raw_pipeline": NullOr(Object),
	},
}

var jobListItem = Obj{
	"id": UUID, "job_type": String, "url": NullOr(String), "status": String,
	"completed_at": NullOr(TimestampNaive), "warnings": ArrayOf(String),
	"preview": NullOr(Obj{"title": NullOr(String), "source_host": NullOr(String)}),
}

// submitWebview posts a parse-payload job as h and returns its id.
func submitWebview(t testing.TB, h H, input map[string]any) string {
	t.Helper()
	obj := T(t).Post(t, Recipes, "/recipes/parse-payload/async", map[string]any{"input": input}, h).
		Expect(http.StatusOK, Obj{"id": UUID, "status": Eq("PENDING")}).Object()
	return obj["id"].(string)
}

func soupInput() map[string]any {
	return map[string]any{
		"source_type": "client_webview", "source_url": "https://example.com/contract-soup",
		"jsonld_blocks": []string{soupJSONLD}, "html_snippet": "<main><h1>Contract Soup</h1></main>",
		"extracted_at": "2026-10-08T12:00:00Z", "client": "contract",
	}
}

const jobPoll = 60 * time.Second

// TestRecipesParseURLAsync covers #11 (§4.3 step 1). The returned id is not a job (§8 item 7).
func TestRecipesParseURLAsync(t *testing.T) {
	tg := T(t)
	k := SharedKitchen(t)
	h := k.Owner.H()

	for _, u := range []string{"http://127.0.0.1/", "http://localhost:7030/", "http://10.0.0.1/recipe"} {
		tg.Post(t, Recipes, "/recipes/parse-url/async", map[string]string{"url": u}, h).
			Expect(http.StatusBadRequest, Obj{"detail": Obj{
				"error_code": Eq("invalid_url"), "message": Eq("Host is blocked (localhost/private)."),
				"status_code": Null, "job_id": UUID,
			}})
	}
	r := tg.Post(t, Recipes, "/recipes/parse-url/async", map[string]any{"url": "https://example.com/", "use_llm_fallback": false}, h).
		Expect(http.StatusOK, Obj{
			"id": UUID, "status": Eq("PENDING"), "result": Null, "error_code": Null, "error_message": Null,
			"next_action": Eq("webview_extract"), "next_action_reason": Eq("webview_required"),
		}).Object()
	tg.Get(t, Recipes, "/recipes/jobs/"+r["id"].(string), h).ExpectError(http.StatusNotFound, "Job not found")

	tg.Post(t, Recipes, "/recipes/parse-url/async", map[string]string{"url": "ftp://example.com/x"}, h).
		Expect(http.StatusUnprocessableEntity, recipesValidation("body.url"))
	tg.Post(t, Recipes, "/recipes/parse-url/async", map[string]string{}, h).
		Expect(http.StatusUnprocessableEntity, recipesValidation("body.url"))
}

// TestRecipesWebviewImport drives #12 → #13 → #15 → #2 with parse_job_id (§4.3 steps 2–5).
func TestRecipesWebviewImport(t *testing.T) {
	tg := T(t)
	k := SharedKitchen(t)
	h := k.Owner.H()

	id := submitWebview(t, h, soupInput())
	job := pollJob(t, "/recipes/jobs/"+id, h, jobPoll)
	r := tg.Get(t, Recipes, "/recipes/jobs/"+id, h).Expect(http.StatusOK, All(parseJobStatus, Open{
		"id": Eq(id), "status": Eq("COMPLETE"), "result": webviewResult,
		"error_code": Null, "error_message": Null, "next_action": Null, "next_action_reason": Null,
	})).Object()
	_ = job
	res := r["result"].(map[string]any)
	draft := res["recipe_draft"].(map[string]any)
	expectFields(t, draft, map[string]any{
		"title": "Contract Soup", "description": "A soup.", "servings": 4,
		"steps": []any{"Boil the water.", "Season."},
	})
	if n := len(draft["ingredients"].([]any)); n != 3 {
		t.Fatalf("recipe_draft.ingredients: want 3, got %v", show(draft["ingredients"]))
	}
	expectFields(t, res["pipeline"].(map[string]any), map[string]any{"parser_strategy": "client_json_ld", "warnings": []any{}})

	// Author-only: the member, though in the household, cannot see the job.
	tg.Get(t, Recipes, "/recipes/jobs/"+id, k.MemberH).ExpectError(http.StatusNotFound, "Job not found")
	tg.Get(t, Recipes, "/recipes/jobs/"+id, k.Outsider.H()).ExpectError(http.StatusNotFound, "Job not found")
	// The meal-plan poller only answers for its own job type.
	tg.Get(t, Recipes, "/meal-plans/generate/jobs/"+id, h).ExpectError(http.StatusNotFound, "Job not found")

	// #15 the job list (recipe list badge, Mailbox).
	list := tg.Get(t, Recipes, "/recipes/parse-url/jobs", h).Expect(http.StatusOK, Obj{"jobs": ArrayOf(jobListItem)}).Object()
	item := findBy(list["jobs"].([]any), "id", id)
	if item == nil {
		t.Fatalf("job %s should be listed: %v", id, show(list))
	}
	expectFields(t, item, map[string]any{"job_type": "ingestion", "url": nil, "status": "COMPLETE", "warnings": []any{}})
	if Jarvisd() {
		// CHANGE #15 (B6): preview reads recipe_draft and pipeline.
		expectFields(t, item, map[string]any{"preview": map[string]any{"title": "Contract Soup", "source_host": "example.com"}})
	} else {
		// LEGACY-BUG B6: preview reads result.recipe, which no longer exists, so both are null.
		expectFields(t, item, map[string]any{"preview": map[string]any{"title": nil, "source_host": nil}})
	}
	if findBy(tg.Get(t, Recipes, "/recipes/parse-url/jobs", k.MemberH).ExpectStatus(http.StatusOK).Object()["jobs"].([]any), "id", id) != nil {
		t.Fatalf("the job list is author-only")
	}

	// #2 with parse_job_id: commit marks the job COMMITTED; a second commit is 409.
	body := recipeBody("Contract Soup", map[string]any{"source_type": "url", "source_url": "https://example.com/contract-soup", "parse_job_id": id})
	tg.Post(t, Recipes, "/recipes", body, k.MemberH).ExpectError(http.StatusNotFound, "Parse job not found")
	NewRecipe(t, h, body)
	tg.Get(t, Recipes, "/recipes/jobs/"+id, h).Expect(http.StatusOK, Open{"status": Eq("COMMITTED")})
	tg.Post(t, Recipes, "/recipes", body, h).ExpectError(http.StatusConflict, "Parse job not ready")
	tg.Post(t, Recipes, "/recipes", recipeBody("x", map[string]any{"parse_job_id": "00000000-0000-4000-8000-000000000000"}), h).
		ExpectError(http.StatusNotFound, "Parse job not found")
	// A committed job leaves the default (COMPLETE) list; it is listed by status.
	if findBy(tg.Get(t, Recipes, "/recipes/parse-url/jobs", h).ExpectStatus(http.StatusOK).Object()["jobs"].([]any), "id", id) != nil {
		t.Fatalf("a COMMITTED job is not in the default list")
	}
	if findBy(tg.Get(t, Recipes, "/recipes/parse-url/jobs?status=COMMITTED", h).ExpectStatus(http.StatusOK).Object()["jobs"].([]any), "id", id) == nil {
		t.Fatalf("?status=COMMITTED should list the job")
	}

	// #12 validation.
	tg.Post(t, Recipes, "/recipes/parse-payload/async", map[string]any{}, h).Expect(http.StatusUnprocessableEntity, recipesValidation("body.input"))
	tg.Post(t, Recipes, "/recipes/parse-payload/async", map[string]any{"input": map[string]any{"source_type": "email"}}, h).
		Expect(http.StatusUnprocessableEntity, recipesValidation("body.input.source_type"))
}

// TestRecipesJobErrors freezes the error outcomes that need no model (§4.3).
func TestRecipesJobErrors(t *testing.T) {
	tg := T(t)
	k := SharedKitchen(t)
	h := k.Owner.H()

	empty := submitWebview(t, h, map[string]any{"source_type": "client_webview", "source_url": "https://example.com/x"})
	img := submitWebview(t, h, map[string]any{"source_type": "image_upload", "images": []any{
		map[string]any{"filename": "a.jpg", "content_type": "image/jpeg", "data_base64": "/9j/4AAQ"},
	}})
	for _, c := range []struct{ id, code, msg string }{
		{empty, "invalid_payload", "no content to parse"},
		{img, "not_implemented", "image_ingestion_not_implemented"},
	} {
		pollJob(t, "/recipes/jobs/"+c.id, h, jobPoll)
		tg.Get(t, Recipes, "/recipes/jobs/"+c.id, h).Expect(http.StatusOK, All(parseJobStatus, Open{
			"status": Eq("ERROR"), "result": Null, "error_code": Eq(c.code), "error_message": Eq(c.msg),
		}))
		tg.Post(t, Recipes, "/recipes/jobs/"+c.id+"/cancel", nil, h).ExpectError(http.StatusConflict, "Job cannot be canceled")
	}
	list := tg.Get(t, Recipes, "/recipes/parse-url/jobs?status=ERROR", h).Expect(http.StatusOK, Obj{"jobs": ArrayOf(jobListItem)}).Object()
	if findBy(list["jobs"].([]any), "id", empty) == nil {
		t.Fatalf("?status=ERROR should list the failed job: %v", show(list))
	}
	tg.Post(t, Recipes, "/recipes", recipeBody("x", map[string]any{"parse_job_id": empty}), h).ExpectError(http.StatusConflict, "Parse job not ready")
}

// TestRecipesCancel is the cancel matrix for #17 (§4.5).
func TestRecipesCancel(t *testing.T) {
	tg := T(t)
	k := SharedKitchen(t)
	h := k.Owner.H()
	canceled := Obj{
		"id": UUID, "status": Eq("CANCELED"), "result": Null, "error_code": Null, "error_message": Null,
		"next_action": Null, "next_action_reason": Null,
	}

	// Submitted and canceled at once: whether the worker has reached it (PENDING, RUNNING or
	// COMPLETE), cancel answers 200 CANCELED, and the cancel sticks.
	id := submitWebview(t, h, soupInput())
	tg.Post(t, Recipes, "/recipes/jobs/"+id+"/cancel", nil, k.MemberH).ExpectError(http.StatusNotFound, "Job not found")
	tg.Post(t, Recipes, "/recipes/jobs/"+id+"/cancel", nil, h).Expect(http.StatusOK, canceled)
	time.Sleep(3 * time.Second)
	tg.Get(t, Recipes, "/recipes/jobs/"+id, h).Expect(http.StatusOK, Open{"status": Eq("CANCELED")})
	tg.Post(t, Recipes, "/recipes/jobs/"+id+"/cancel", nil, h).ExpectError(http.StatusConflict, "Job cannot be canceled")
	tg.Post(t, Recipes, "/recipes", recipeBody("x", map[string]any{"parse_job_id": id}), h).ExpectError(http.StatusConflict, "Parse job not ready")

	// COMPLETE → CANCELED drops the result.
	done := submitWebview(t, h, soupInput())
	pollJob(t, "/recipes/jobs/"+done, h, jobPoll)
	tg.Post(t, Recipes, "/recipes/jobs/"+done+"/cancel", nil, h).Expect(http.StatusOK, All(canceled, Open{"id": Eq(done)}))

	// COMMITTED → 409.
	committed := submitWebview(t, h, soupInput())
	pollJob(t, "/recipes/jobs/"+committed, h, jobPoll)
	NewRecipe(t, h, recipeBody("contract committed", map[string]any{"parse_job_id": committed}))
	tg.Post(t, Recipes, "/recipes/jobs/"+committed+"/cancel", nil, h).ExpectError(http.StatusConflict, "Job cannot be canceled")

	tg.Post(t, Recipes, "/recipes/jobs/00000000-0000-4000-8000-000000000000/cancel", nil, h).ExpectError(http.StatusNotFound, "Job not found")
	tg.Get(t, Recipes, "/recipes/jobs/00000000-0000-4000-8000-000000000000", h).ExpectError(http.StatusNotFound, "Job not found")
}

// TestRecipesFromImageValidation covers #19's refusals, which create nothing.
func TestRecipesFromImageValidation(t *testing.T) {
	tg := T(t)
	k := SharedKitchen(t)
	h := k.Owner.H()
	jpg := tinyJPEG(t)

	tg.postMultipart(t, "/recipes/from-image/jobs", h, FormField("title_hint", "x")).
		Expect(http.StatusUnprocessableEntity, recipesValidation("body.images"))
	var nine []FormPart
	for i := 0; i < 9; i++ {
		nine = append(nine, imagePart("images", fmt.Sprintf("%d.jpg", i), jpg))
	}
	tg.postMultipart(t, "/recipes/from-image/jobs", h, nine...).ExpectError(http.StatusBadRequest, "Too many images (max 8)")
	tg.postMultipart(t, "/recipes/from-image/jobs", h, imagePart("images", "a.jpg", nil)).ExpectError(http.StatusBadRequest, "Empty image upload")
	tg.postMultipart(t, "/recipes/from-image/jobs", h, imagePart("images", "a.jpg", []byte("not an image at all"))).
		ExpectError(http.StatusBadRequest, "Unrecognized image file")
	// image.max_bytes defaults to 10 MiB.
	tg.postMultipart(t, "/recipes/from-image/jobs", h, imagePart("images", "big.jpg", bytes.Repeat([]byte{0xff}, 10*1024*1024+1))).
		ExpectError(http.StatusRequestEntityTooLarge, "Image too large")
	tg.postMultipart(t, "/recipes/from-image/jobs", nil, imagePart("images", "a.jpg", jpg)).ExpectError(http.StatusUnauthorized, "Not authenticated")
}

// TestRecipesMealPlanJobRoutes covers what #31/#32 and #7 answer without a model.
func TestRecipesMealPlanJobRoutes(t *testing.T) {
	tg := T(t)
	k := SharedKitchen(t)
	h := k.Owner.H()

	tg.Get(t, Recipes, "/meal-plans/generate/jobs/00000000-0000-4000-8000-000000000000", h).ExpectError(http.StatusNotFound, "Job not found")
	tg.Post(t, Recipes, "/meal-plans/generate/jobs", map[string]any{"days": []any{}}, h).
		Expect(http.StatusUnprocessableEntity, recipesValidation("body.days"))
	tg.Post(t, Recipes, "/meal-plans/generate/jobs", map[string]any{"days": []any{
		map[string]any{"date": day(1), "meals": map[string]any{"dinner": map[string]any{"servings": 0}}},
	}}, h).Expect(http.StatusUnprocessableEntity, recipesValidation("body.days.0.meals.dinner.servings"))
	tg.Post(t, Recipes, "/meal-plans/generate/jobs", map[string]any{"days": []any{
		map[string]any{"date": day(1), "meals": map[string]any{}},
	}}, h).Expect(http.StatusUnprocessableEntity, recipesValidation("body.days.0.meals"))

	// #7 stage recipes: author-only, 404 "Not found" (the 410 needs a 72 h-old stage row).
	tg.Get(t, Recipes, "/recipes/stage/999999999", h).ExpectError(http.StatusNotFound, "Not found")
	tg.Get(t, Recipes, "/recipes/stage/abc", h).Expect(http.StatusUnprocessableEntity, recipesValidation("path.stage_id"))
}

// --- slow tier ---

var mealSelection = Obj{
	"source": OneOf("user", "core", "stage"), "recipe_id": NullOr(String), "confidence": NullOr(Num),
	"matched_tags": ArrayOf(String), "warnings": ArrayOf(String),
	"alternatives": ArrayOf(Obj{
		"source": OneOf("user", "core", "stage"), "recipe_id": String, "title": String, "confidence": Num,
		"reason": NullOr(String), "matched_tags": ArrayOf(String),
	}),
}

var mealSlotResult = Obj{
	"servings": Int, "tags": ArrayOf(String), "note": NullOr(String), "is_meal_prep": Bool,
	"repeat": NullOr(Obj{"mode": String, "count": Int}), "selection": NullOr(mealSelection),
}

// TestRecipesMealPlanGenerate covers #31 → #32 with the target's real model.
func TestRecipesMealPlanGenerate(t *testing.T) {
	slowRecipes(t)
	tg := T(t)
	k := SharedKitchen(t)
	h := k.Owner.H()
	NewRecipe(t, h, recipeBody("contract weeknight pasta", map[string]any{"tags": []string{"contract-tag-dinner"}}))

	// The app also sends pinned_recipe_id and allow_external_recipes, which are ignored.
	sub := tg.Post(t, Recipes, "/meal-plans/generate/jobs", map[string]any{
		"days":             []any{map[string]any{"date": day(1), "meals": map[string]any{"dinner": map[string]any{"servings": 2}}}},
		"pinned_recipe_id": nil, "allow_external_recipes": false,
	}, h).Expect(http.StatusAccepted, Obj{"job_id": UUID, "request_id": UUID}).Object()
	path := "/meal-plans/generate/jobs/" + sub["job_id"].(string)
	tg.Get(t, Recipes, path, k.MemberH).ExpectError(http.StatusNotFound, "Job not found")
	pollJob(t, path, h, slowTimeout())
	r := tg.Get(t, Recipes, path, h).Expect(http.StatusOK, Obj{
		"id": Eq(sub["job_id"]), "status": Eq("COMPLETE"), "error_code": Null, "error_message": Null,
		"result": Obj{
			"result":              Obj{"days": NonEmptyArrayOf(Obj{"date": Eq(day(1)), "meals": MapOf(mealSlotResult)})},
			"slot_failures_count": Int,
		},
	}).Object()
	sel, _ := r["result"].(map[string]any)["result"].(map[string]any)["days"].([]any)[0].(map[string]any)["meals"].(map[string]any)["dinner"].(map[string]any)["selection"].(map[string]any)
	if sel == nil {
		return
	}
	if Jarvisd() && sel["source"] != "user" {
		// RD2: core (stock) recipes are no longer candidates, so nothing is staged.
		t.Fatalf("RD2: jarvisd picks only from the household's box, got %v", show(sel))
	}
	if sel["source"] == "stage" {
		tg.Get(t, Recipes, "/recipes/stage/"+sel["recipe_id"].(string), h).Expect(http.StatusOK, Obj{
			"id": String, "title": String, "description": NullOr(String), "yield": NullOr(String),
			"prep_time_minutes": NullOr(Int), "cook_time_minutes": NullOr(Int), "ingredients": Any, "steps": Any,
			"tags": Any, "notes": Any,
		})
		tg.Get(t, Recipes, "/recipes/stage/"+sel["recipe_id"].(string), k.MemberH).ExpectError(http.StatusNotFound, "Not found")
	}
}

// TestRecipesGroceryMatchJob covers #49's match_job_id and the background pass (RD1 keeps it).
func TestRecipesGroceryMatchJob(t *testing.T) {
	slowRecipes(t)
	tg := T(t)
	k := SharedKitchen(t)
	h := k.Owner.H()

	r := NewRecipe(t, h, recipeBody("contract burgers", map[string]any{"ingredients": []map[string]any{
		{"text": "1 lb 90/10 ground beef", "quantity_display": "1", "unit": "lb"},
		{"text": "4 burger buns", "quantity_display": "4"},
	}}))
	commitPlan(t, h, map[string]any{"start_date": day(1), "items": []any{planItem(day(1), "dinner", idOf(r))}})
	// Mappings are deleted only after the job is terminal, so a late LLM write cannot outlive
	// the cleanup (registered first, so it runs last).
	t.Cleanup(func() { cleanupSkuMap(t, h) })
	tg.Do(t, Recipes, http.MethodPut, "/grocery/sku-map", map[string]any{"ingredient_name": "ground beef", "sku": "2001", "unit_size": "1 lb"}, h).ExpectStatus(http.StatusOK)

	cart := tg.Post(t, Recipes, "/grocery/cart?start_date="+day(0)+"&end_date="+day(2), nil, h).ExpectStatus(http.StatusOK).Object()
	jobID, _ := cart["match_job_id"].(string)
	if errs := UUID.Match("$.match_job_id", cart["match_job_id"]); len(errs) > 0 {
		t.Fatalf("unmatched items and a non-empty map queue a match pass: %v", show(cart))
	}
	pollJob(t, "/recipes/jobs/"+jobID, h, slowTimeout())
	tg.Get(t, Recipes, "/recipes/jobs/"+jobID, h).Expect(http.StatusOK, All(parseJobStatus, Open{
		"status": Eq("COMPLETE"),
		"result": Obj{
			"learned":    ArrayOf(Obj{"ingredient_name": String, "sku": String, "product_name": NullOr(String)}),
			"attempted":  Eq([]any{"90/10 ground beef", "burger buns"}),
			"unresolved": ArrayOf(String),
		},
	}))
	// A manual mapping is never overwritten by the pass.
	for _, m := range arr(tg.Get(t, Recipes, "/grocery/sku-map", h).ExpectStatus(http.StatusOK)) {
		if mm := m.(map[string]any); mm["ingredient_name"] == "ground beef" && mm["source"] != "manual" {
			t.Fatalf("the manual mapping was overwritten: %v", show(mm))
		}
	}
}

// TestRecipesFromImageJob covers #19 → #13 with a real photo, OCR and model.
func TestRecipesFromImageJob(t *testing.T) {
	slowRecipes(t)
	photo := os.Getenv(EnvRecipesPhoto)
	if photo == "" {
		t.Skipf("contract: %s is not set (a JPEG of a printed recipe)", EnvRecipesPhoto)
	}
	data, err := os.ReadFile(photo)
	if err != nil {
		t.Fatal(err)
	}
	tg := T(t)
	k := SharedKitchen(t)
	h := k.Owner.H()

	sub := tg.postMultipart(t, "/recipes/from-image/jobs?title_hint=contract&tier_max=3", h, imagePart("images", "page.jpg", data)).
		Expect(http.StatusAccepted, Obj{"ingestion_id": UUID, "job_id": UUID}).Object()
	path := "/recipes/jobs/" + sub["job_id"].(string)
	pollJob(t, path, h, slowTimeout())
	r := tg.Get(t, Recipes, path, h).Expect(http.StatusOK, parseJobStatus).Object()
	switch r["status"] {
	case "COMPLETE":
		// The OCR path writes RecipeDraft.model_dump() directly (§3.2), so its source differs
		// from the URL path's.
		tg.Get(t, Recipes, path, h).Expect(http.StatusOK, Open{"result": Obj{
			"recipe_draft": Open{"title": String, "ingredients": Array, "steps": Array, "source": Object},
			"pipeline":     NullOr(Object),
		}})
	case "ERROR":
		tg.Get(t, Recipes, path, h).Expect(http.StatusOK, Open{"error_code": NonEmptyString, "error_message": String})
	default:
		t.Fatalf("photo job ended %v", r["status"])
	}
}
