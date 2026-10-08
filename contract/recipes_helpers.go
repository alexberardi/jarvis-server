//go:build contract

package contract

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"os"
	"reflect"
	"sort"
	"testing"
	"time"
)

// Helpers and shapes for the jarvis-recipes-server contract (recipes_test.go,
// recipes_jobs_test.go; docs/recipes/00-inventory.md §3, §12 and step R0).
//
// Fixtures. Recipes scopes by the JWT's household_id claim (§4.1). The run shares one
// "kitchen": Owner and Member in Owner's household (Member's token comes from
// /auth/switch-household, since a login token carries the *first* membership, which for a
// throwaway user is its own solo household), and Outsider in a household of their own.
// Every recipe, plan, staple and SKU mapping a test creates is deleted through the recipes
// API before the users go. What the API cannot delete stays on the target, tagged by the
// throwaway user ids: parse-job rows (no delete route), the recipes `users` shadow row
// (recipes has no account purge, §5.2), global tags (fixed names, so they are reused rather
// than accumulating), and uploaded `/media` files.

// EnvRecipesSlow enables the slow recipes tier: photo import (OCR + LLM) and LLM meal-plan
// generation. Both need a real model on the target and take minutes.
const EnvRecipesSlow = "JARVIS_CONTRACT_RECIPES_SLOW"

// EnvRecipesPhoto is a JPEG of a printed recipe for the slow photo-import test.
const EnvRecipesPhoto = "JARVIS_CONTRACT_RECIPES_PHOTO"

// EnvRecipesAdminSecret is recipes' ADMIN_SECRET (legacy only). When the target's stock
// reference data is empty, the stock test seeds it once through /admin/static-data/seed.
const EnvRecipesAdminSecret = "JARVIS_CONTRACT_RECIPES_ADMIN_SECRET"

// Kitchen is the shared recipes household fixture.
type Kitchen struct {
	Owner, Member, Outsider *User
	// MemberH is Member's token switched into Owner's household.
	MemberH H
}

var sharedKitchen *Kitchen

// SharedKitchen creates the run's recipes household once; TestMain tears it down.
func SharedKitchen(t testing.TB) *Kitchen {
	t.Helper()
	tg := T(t)
	tg.NeedAdmin(t)
	tg.Need(t, Recipes)
	shared.mu.Lock()
	defer shared.mu.Unlock()
	if sharedKitchen != nil {
		return sharedKitchen
	}
	var users []*User
	for i := 0; i < 3; i++ {
		u, err := tg.registerUser()
		if err != nil {
			t.Fatalf("kitchen user: %v", err)
		}
		users = append(users, u)
		addSharedCleanup(func() error { return tg.deleteUser(u) })
	}
	owner, member, outsider := users[0], users[1], users[2]
	r, err := tg.do(Auth, http.MethodPost, "/households/"+owner.HouseholdID+"/members",
		map[string]any{"user_id": member.ID, "role": "member"}, owner.H())
	if err == nil {
		err = r.expect(http.StatusCreated, nil)
	}
	if err != nil {
		t.Fatalf("kitchen member: %v", err)
	}
	r, err = tg.do(Auth, http.MethodPost, "/auth/switch-household",
		map[string]string{"household_id": owner.HouseholdID}, member.H())
	var sw struct {
		AccessToken string `json:"access_token"`
	}
	if err == nil {
		err = r.expect(http.StatusOK, &sw)
	}
	if err != nil {
		t.Fatalf("kitchen switch-household: %v", err)
	}
	sharedKitchen = &Kitchen{Owner: owner, Member: member, Outsider: outsider, MemberH: Bearer(sw.AccessToken)}
	return sharedKitchen
}

// --- shapes (jarvis_recipes/app/schemas, api/routes) ---

// recipeQuantity is IngredientRead.quantity_value: a pydantic Decimal, serialised as a
// string. The column is Numeric(10,4), so values read back with four decimals ("1.5000").
var recipeQuantity = NullOr(Regexp(`^-?\d+(\.\d+)?$`))

var recipeIngredient = Obj{
	"id": Int, "text": String, "quantity_display": NullOr(String), "quantity_value": recipeQuantity, "unit": NullOr(String),
}

var recipeStep = Obj{"id": Int, "step_number": Int, "text": String}

var tagShape = Obj{"id": Int, "name": String}

// recipeRead is RecipeRead. prep/cook are always null on legacy (B1, RQ5).
var recipeRead = Obj{
	"id":                 Int,
	"user_id":            String,
	"title":              String,
	"description":        NullOr(String),
	"servings":           NullOr(Int),
	"prep_time_minutes":  NullOr(Int),
	"cook_time_minutes":  NullOr(Int),
	"total_time_minutes": NullOr(Int),
	"source_type":        OneOf("manual", "image", "url"),
	"source_url":         NullOr(String),
	"image_url":          NullOr(String),
	"created_at":         NullOr(TimestampNaive),
	"updated_at":         NullOr(TimestampNaive),
	"ingredients":        ArrayOf(recipeIngredient),
	"steps":              ArrayOf(recipeStep),
	"tags":               ArrayOf(tagShape),
}

// recipesValidation is main.py's custom 422 body. field is the dotted loc; message is
// pydantic's text (type only); job_id is a fresh uuid4 per response (§8 item 12).
func recipesValidation(field string) Matcher {
	item := Obj{"field": NullOr(String), "message": String}
	return MatchFunc(func(path string, v any) []string {
		errs := Obj{
			"error_code": Eq("validation_error"),
			"message":    Eq("Invalid request payload."),
			"details":    NonEmptyArrayOf(item),
			"job_id":     UUID,
		}.Match(path, v)
		if len(errs) > 0 {
			return errs
		}
		for _, d := range v.(map[string]any)["details"].([]any) {
			if d.(map[string]any)["field"] == field {
				return nil
			}
		}
		return mismatch(path+".details[*].field", show(field), v)
	})
}

var parseJobStatus = Obj{
	"id":                 String,
	"status":             OneOf("PENDING", "RUNNING", "COMPLETE", "ERROR", "CANCELED", "COMMITTED", "ABANDONED"),
	"result":             NullOr(Object),
	"error_code":         NullOr(String),
	"error_message":      NullOr(String),
	"next_action":        NullOr(String),
	"next_action_reason": NullOr(String),
}

var mealPlanItemRead = Obj{
	"id": Int, "date": Regexp(`^\d{4}-\d{2}-\d{2}$`), "meal_type": String, "recipe_id": Int,
	"title": NullOr(String), "image_url": NullOr(String), "total_time_minutes": NullOr(Int),
}

var mealPlanRead = Obj{
	"id": Int, "user_id": String, "name": NullOr(String), "start_date": Regexp(`^\d{4}-\d{2}-\d{2}$`),
	"items": ArrayOf(mealPlanItemRead),
}

var mealPlanSummary = Obj{
	"id": Int, "name": NullOr(String), "start_date": Regexp(`^\d{4}-\d{2}-\d{2}$`),
	"end_date": Regexp(`^\d{4}-\d{2}-\d{2}$`), "meal_count": Int, "created_at": TimestampNaive,
}

var randomSlot = Obj{
	"date": NullOr(Regexp(`^\d{4}-\d{2}-\d{2}$`)), "meal_type": String, "recipe_id": NullOr(Int),
	"title": NullOr(String), "image_url": NullOr(String), "total_time_minutes": NullOr(Int), "servings": NullOr(Int),
}

var skuMappingRead = Obj{
	"id": Int, "retailer": String, "ingredient_name": String, "sku": String,
	"product_name": NullOr(String), "unit_size": NullOr(String), "source": OneOf("manual", "llm"),
}

// --- helpers ---

// recipeBody is a minimal valid RecipeCreate, with extra fields merged in.
func recipeBody(title string, extra map[string]any) map[string]any {
	b := map[string]any{
		"title":       title,
		"ingredients": []map[string]any{{"text": "1 cup flour", "quantity_display": "1", "unit": "cup"}},
		"steps":       []map[string]any{{"step_number": 1, "text": "Mix."}},
	}
	for k, v := range extra {
		b[k] = v
	}
	return b
}

// NewRecipe creates a recipe as h and deletes it (as h) when the test ends; a 404 then is
// fine (the test deleted it).
func NewRecipe(t testing.TB, h H, body map[string]any) map[string]any {
	t.Helper()
	tg := T(t)
	obj := tg.Post(t, Recipes, "/recipes", body, h).Expect(http.StatusCreated, recipeRead).Object()
	id := int(mustFloat(obj["id"]))
	t.Cleanup(func() { cleanupRecipesPath(t, fmt.Sprintf("/recipes/%d", id), h) })
	return obj
}

// cleanupRecipesPath DELETEs path on recipes, accepting 204 or 404.
func cleanupRecipesPath(t testing.TB, path string, h H) {
	r, err := T(t).do(Recipes, http.MethodDelete, path, nil, h)
	if err != nil {
		t.Errorf("cleanup %s: %v", path, err)
		return
	}
	if r.Status != http.StatusNoContent && r.Status != http.StatusNotFound {
		t.Errorf("cleanup %s: %s", path, r.describe())
	}
}

// idOf is an object's integer "id".
func idOf(obj map[string]any) int { return int(mustFloat(obj["id"])) }

// arr decodes a JSON array response.
func arr(r *Resp) []any {
	a, ok := r.JSON().([]any)
	if !ok {
		r.Fatalf("want a JSON array")
	}
	return a
}

// day is today + n days as YYYY-MM-DD in the target's zone. Legacy uses the container's
// date.today() (UTC on the MBP); tests keep a day of slack where that matters.
func day(n int) string { return time.Now().UTC().AddDate(0, 0, n).Format("2006-01-02") }

// tinyJPEG is a 16x16 JPEG.
func tinyJPEG(t testing.TB) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for x := 0; x < 16; x++ {
		for y := 0; y < 16; y++ {
			img.Set(x, y, color.RGBA{uint8(x * 16), uint8(y * 16), 128, 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// postMultipart sends a multipart body to recipes.
func (tg *Target) postMultipart(t testing.TB, path string, h H, parts ...FormPart) *Resp {
	t.Helper()
	body, ct := MultipartBody(parts...)
	return tg.SlowDo(t, Recipes, http.MethodPost, path, ct, body, h).Resp
}

// imagePart is a JPEG form file.
func imagePart(name, filename string, data []byte) FormPart {
	return FormPart{Name: name, Filename: filename, ContentType: "image/jpeg", Data: data}
}

// pollJob polls GET path until the job's status is not PENDING/RUNNING, or the deadline.
func pollJob(t testing.TB, path string, h H, timeout time.Duration) map[string]any {
	t.Helper()
	tg := T(t)
	deadline := time.Now().Add(timeout)
	for {
		obj := tg.Get(t, Recipes, path, h).ExpectStatus(http.StatusOK).Object()
		st, _ := obj["status"].(string)
		if st != "PENDING" && st != "RUNNING" {
			return obj
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s still %s after %s (is the recipes worker running?)", path, st, timeout)
		}
		time.Sleep(time.Second)
	}
}

// keysOf is the sorted key set of a JSON object, for freezing open-ended result blobs.
func keysOf(v any) []string {
	m, _ := v.(map[string]any)
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// forgeHS256 signs claims with HS256 and secret (the PR #39 "change-me" forgery).
func forgeHS256(t testing.TB, secret string, claims map[string]any) string {
	t.Helper()
	enc := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(b)
	}
	signing := enc(map[string]string{"alg": "HS256", "typ": "JWT"}) + "." + enc(claims)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signing))
	return signing + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func slowRecipes(t testing.TB) {
	t.Helper()
	if os.Getenv(EnvRecipesSlow) == "" {
		t.Skipf("contract: %s is not set (slow recipes tier: needs a real model)", EnvRecipesSlow)
	}
}

// EqDeep is Eq for nested values: numbers anywhere (json.Number, int, float64) compare as
// float64, so a want literal can be written with Go ints.
func EqDeep(want any) Matcher {
	w := normJSON(want)
	return MatchFunc(func(path string, v any) []string {
		if !reflect.DeepEqual(normJSON(v), w) {
			return mismatch(path, show(want), v)
		}
		return nil
	})
}

func normJSON(v any) any {
	switch x := v.(type) {
	case json.Number:
		f, _ := x.Float64()
		return f
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = normJSON(e)
		}
		return out
	case []string:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = e
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = normJSON(e)
		}
		return out
	}
	return v
}
