package recipes

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/modules/notifications"
	"github.com/alexberardi/jarvis-server/internal/modules/ocr"
	"github.com/alexberardi/jarvis-server/internal/modules/recipes/ocrq"
)

// fakeNotifier records inbox items and pushes.
type fakeNotifier struct {
	mu     sync.Mutex
	items  []notifications.NewInboxItem
	pushes []notifications.Notification
}

func (f *fakeNotifier) CreateInboxItem(_ context.Context, _ *sql.Tx, in notifications.NewInboxItem) (notifications.InboxItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.items = append(f.items, in)
	return notifications.InboxItem{ID: "inbox-1"}, nil
}

func (f *fakeNotifier) Notify(_ context.Context, _ *sql.Tx, _ string, n notifications.Notification) (notifications.Delivery, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pushes = append(f.pushes, n)
	return notifications.Delivery{}, nil
}

// wait returns the first n pushes once they arrived.
func (f *fakeNotifier) wait(t *testing.T, n int) ([]notifications.NewInboxItem, []notifications.Notification) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.mu.Lock()
		items, pushes := append([]notifications.NewInboxItem(nil), f.items...), append([]notifications.Notification(nil), f.pushes...)
		f.mu.Unlock()
		if len(pushes) >= n {
			return items, pushes
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d pushes, want %d", len(pushes), n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// chatImportEnv is a recipes env with the pancake OCR reading, a model that structures it,
// and a notifier.
func chatImportEnv(t *testing.T) (*env, *fakeOCR, *fakeNotifier) {
	t.Helper()
	e := setup(t)
	conf := 91.0
	f := &fakeOCR{readings: []ocr.Reading{{Engine: "tesseract", Results: []ocr.ImageReading{{Index: 0, Text: recipeCard, Confidence: &conf}}}}}
	e.m.OCR = f
	e.m.LLM = &fakeLLM{reply: func(llm.ChatRequest) (string, error) {
		return `{"title": "Grandma's Pancakes", "description": "Fluffy pancakes.", "ingredients": [` +
			`{"name": "flour", "quantity": "1", "unit": "cup"}, {"name": "milk", "quantity": "1", "unit": "cup"}, ` +
			`{"name": "eggs", "quantity": "2", "unit": "whole", "notes": "room temperature"}], ` +
			`"steps": ["Whisk the dry ingredients.", "Cook on a hot griddle."], "servings": "4 servings"}`, nil
	}}
	n := &fakeNotifier{}
	e.m.Notify = n
	return e, f, n
}

func TestImportRecipePhotosSavesRecipe(t *testing.T) {
	e, f, n := chatImportEnv(t)
	e.hh.set(1, "A", "B")
	e.hh.set(2, "A")
	jpg := markedJPEG(t, 64, 64)
	jobID, err := e.m.ImportRecipePhotos(e.ctx, 1, "B", [][]byte{jpg, jpg})
	if err != nil || len(jobID) != 36 {
		t.Fatalf("import: %q %v", jobID, err)
	}
	items, pushes := n.wait(t, 1)

	// The recipe: authored by the user, in the chat's household, from the draft.
	var id int64
	var uid, title, src string
	var hh, servings sql.NullString
	if err := e.d.Read.QueryRow(`SELECT id, user_id, household_id, title, source_type, servings FROM recipes_recipes`).
		Scan(&id, &uid, &hh, &title, &src, &servings); err != nil {
		t.Fatal(err)
	}
	if uid != "1" || hh.String != "B" || title != "Grandma's Pancakes" || src != "image" || servings.String != "4" {
		t.Fatalf("recipe %d: %s %v %q %s %v", id, uid, hh, title, src, servings)
	}
	rec := e.obj(t, http.StatusOK, http.MethodGet, path("/recipes/%d", id), nil, tok(1, "B")...)
	ings, steps := rec["ingredients"].([]any), rec["steps"].([]any)
	if len(ings) != 3 || len(steps) != 2 {
		t.Fatalf("recipe %v", rec)
	}
	eggs := ings[2].(map[string]any)
	if eggs["text"] != "eggs — room temperature" || eggs["quantity_display"] != "2" || eggs["unit"] != "whole" {
		t.Fatalf("ingredient %v", eggs)
	}
	if s := steps[1].(map[string]any); s["step_number"] != 2.0 || s["text"] != "Cook on a hot griddle." {
		t.Fatalf("step %v", s)
	}
	// Same pipeline as the app's import: every page went to OCR, the ingestion succeeded, and
	// the job is COMMITTED with the recipe id (so it leaves the review list).
	if f.images != 2 {
		t.Fatalf("OCR saw %d images", f.images)
	}
	job := e.obj(t, http.StatusOK, http.MethodGet, "/recipes/jobs/"+jobID, nil, tok(1, "")...)
	if job["status"] != statusCommitted || job["result"].(map[string]any)["recipe_id"] != float64(id) {
		t.Fatalf("job %v", job)
	}
	if e.count(t, `SELECT COUNT(*) FROM recipes_recipe_ingestions WHERE status = 'SUCCEEDED' AND household_id = 'B'`) != 1 {
		t.Fatal("ingestion")
	}
	if l := e.obj(t, http.StatusOK, http.MethodGet, "/recipes/parse-url/jobs", nil, tok(1, "")...)["jobs"].([]any); len(l) != 0 {
		t.Fatalf("a saved import is still waiting for review: %v", l)
	}
	// Household rules: user 2 (A only) doesn't see B's recipe.
	if e.count(t, `SELECT COUNT(*) FROM recipes_recipes WHERE household_id = 'A'`) != 0 {
		t.Fatal("wrong household")
	}
	e.expectDetail(t, http.StatusNotFound, "Recipe not found", http.MethodGet, path("/recipes/%d", id), nil, tok(2, "A")...)

	// The user is told: an inbox item with the recipe and a push to them.
	if len(items) != 1 || items[0].HouseholdID != "B" || *items[0].UserID != 1 || items[0].Title != "Recipe saved: Grandma's Pancakes" ||
		!strings.Contains(items[0].Body, "- 2 whole eggs — room temperature") || !strings.Contains(items[0].Body, "2. Cook on a hot griddle.") {
		t.Fatalf("inbox %+v", items)
	}
	p := pushes[0]
	if p.TargetType != "user" || p.TargetID != "1" || p.Data["type"] != "recipe_saved" || p.Data["recipe_id"] != id ||
		p.Data["inbox_item_id"] != "inbox-1" {
		t.Fatalf("push %+v", p)
	}
}

// TestImportRecipePhotosHousehold: the write household follows resolve (RD7), as a token
// naming the chat's household would.
func TestImportRecipePhotosHousehold(t *testing.T) {
	for _, c := range []struct {
		name   string
		member []string
		claim  string
		want   sql.NullString
	}{
		{"member of the chat's household", []string{"A", "B"}, "B", sql.NullString{String: "B", Valid: true}},
		{"no longer a member: first membership", []string{"A"}, "Z", sql.NullString{String: "A", Valid: true}},
		{"no household: private", nil, "Z", sql.NullString{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e, _, n := chatImportEnv(t)
			e.hh.set(1, c.member...)
			if _, err := e.m.ImportRecipePhotos(e.ctx, 1, c.claim, [][]byte{markedJPEG(t, 32, 32)}); err != nil {
				t.Fatal(err)
			}
			n.wait(t, 1)
			var hh sql.NullString
			if err := e.d.Read.QueryRow(`SELECT household_id FROM recipes_recipes WHERE user_id = '1'`).Scan(&hh); err != nil {
				t.Fatal(err)
			}
			if hh != c.want {
				t.Fatalf("household %v, want %v", hh, c.want)
			}
		})
	}
}

func TestImportRecipePhotosFailure(t *testing.T) {
	e, f, n := chatImportEnv(t)
	e.hh.set(1, "A")
	f.mu.Lock()
	f.readings = []ocr.Reading{{Engine: "tesseract", Results: []ocr.ImageReading{{Index: 0, Text: "Crepe\n3 eggs"}}}}
	f.mu.Unlock()
	jobID, err := e.m.ImportRecipePhotos(e.ctx, 1, "A", [][]byte{markedJPEG(t, 32, 32)})
	if err != nil {
		t.Fatal(err)
	}
	items, pushes := n.wait(t, 1)
	if e.count(t, `SELECT COUNT(*) FROM recipes_recipes`) != 0 {
		t.Fatal("a recipe was saved from an unreadable photo")
	}
	if job := e.obj(t, http.StatusOK, http.MethodGet, "/recipes/jobs/"+jobID, nil, tok(1, "")...); job["error_code"] != "quality_gate_failed" {
		t.Fatalf("job %v", job)
	}
	if items[0].Title != "Recipe not saved" || items[0].Summary != gateFailedMsg || pushes[0].Data["type"] != "recipe_import_failed" ||
		pushes[0].TargetID != "1" {
		t.Fatalf("failure notice %+v %+v", items, pushes)
	}
}

func TestImportRecipePhotosRefusals(t *testing.T) {
	e, _, _ := chatImportEnv(t)
	e.hh.set(1, "A")
	jpg := markedJPEG(t, 32, 32)
	var nine [][]byte
	for range 9 {
		nine = append(nine, jpg)
	}
	for _, c := range []struct {
		photos [][]byte
		msg    string
	}{
		{nil, "no photo to import"},
		{nine, "too many photos: at most 8 pages per recipe"},
		{[][]byte{jpg, []byte("<html>")}, "image 2 is not a readable photo"},
		{[][]byte{{}}, "image 1 is not a readable photo"},
	} {
		_, err := e.m.ImportRecipePhotos(e.ctx, 1, "A", c.photos)
		var pe *PhotoError
		if !errors.As(err, &pe) || pe.UserMessage() != c.msg {
			t.Errorf("%q: %v", c.msg, err)
		}
	}
	if _, err := e.m.ImportRecipePhotos(e.ctx, 0, "A", [][]byte{jpg}); err == nil {
		t.Error("no user accepted")
	}
	if e.count(t, `SELECT COUNT(*) FROM recipes_recipe_parse_jobs`) != 0 {
		t.Fatal("a refused import created a job")
	}
}

// TestAppPhotoImportUnchanged: the app's route still leaves the draft for review and tells
// nobody (the save is the chat's).
func TestAppPhotoImportUnchanged(t *testing.T) {
	e, _, n := chatImportEnv(t)
	h := tok(1, "")
	code, o := e.postPhotos(t, "/recipes/from-image/jobs", h, markedJPEG(t, 32, 32))
	if code != http.StatusAccepted {
		t.Fatal(code)
	}
	if job := e.waitJob(t, o["job_id"].(string), h); job["status"] != statusComplete {
		t.Fatalf("job %v", job)
	}
	time.Sleep(50 * time.Millisecond)
	n.mu.Lock()
	defer n.mu.Unlock()
	if e.count(t, `SELECT COUNT(*) FROM recipes_recipes`) != 0 || len(n.pushes) != 0 {
		t.Fatal("the app's import saved or notified")
	}
}

func TestDraftRecipe(t *testing.T) {
	s := func(v string) *string { return &v }
	d := &ocrq.Draft{Title: "  Soup ", Servings: s("4-6"), Prep: 10, Cook: 20,
		Ingredients: []ocrq.Ingredient{{Name: "water", Quantity: s("2"), Unit: s(" l ")}, {Name: "  "}, {Name: "salt", Quantity: s(" "), Notes: s("to taste")},
			{Name: "Cloves garlic", Quantity: s("6"), Unit: s("cloves")}, {Name: "cloves", Unit: s("cloves")}, {Name: "lbs beef", Unit: s("lb")}},
		Steps: []string{"Boil.", " ", "Season."}, Tags: []string{"soup"}}
	in := draftRecipe(d)
	if *in.title != "Soup" || *in.sourceType != "image" || *in.servings != 4 || *in.foldTotal() != 30 || in.tags[0] != "soup" {
		t.Fatalf("recipe %+v", in)
	}
	if len(in.ingredients) != 5 || *in.ingredients[0].unit != "l" || in.ingredients[1].text != "salt — to taste" ||
		in.ingredients[1].quantityDisplay != nil || in.ingredients[2].text != "garlic" || in.ingredients[3].text != "cloves" ||
		in.ingredients[4].text != "lbs beef" {
		t.Fatalf("ingredients %+v", in.ingredients)
	}
	if len(in.steps) != 2 || in.steps[1].number != 2 || in.steps[1].text != "Season." {
		t.Fatalf("steps %+v", in.steps)
	}
	for _, v := range []string{"", "about four", "0"} {
		if leadingInt(&v) != nil {
			t.Errorf("servings %q parsed", v)
		}
	}
}
