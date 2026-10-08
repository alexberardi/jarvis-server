package recipes

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

func goldenFile(t *testing.T, parts ...string) []byte {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(append([]string{filepath.Dir(file), "..", "..", "..", "fixtures", "golden", "recipes"}, parts...)...))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestGoldenP4(t *testing.T) {
	var g struct {
		Inputs struct {
			Slot        json.RawMessage `json:"slot"`
			Preferences json.RawMessage `json:"preferences"`
			Recent      json.RawMessage `json:"recent_meals"`
			Candidates  []struct {
				ID          string   `json:"id"`
				Source      string   `json:"source"`
				Title       string   `json:"title"`
				Tags        []string `json:"tags"`
				Description *string  `json:"description"`
				Prep        int64    `json:"prep_time_minutes"`
				Cook        *int64   `json:"cook_time_minutes"`
			} `json:"candidates"`
		} `json:"inputs"`
		Requests []struct {
			Body struct {
				Temperature float64 `json:"temperature"`
				MaxTokens   int     `json:"max_tokens"`
				Messages    []struct {
					Content string `json:"content"`
				} `json:"messages"`
			} `json:"body"`
		} `json:"requests"`
	}
	if err := json.Unmarshal(goldenFile(t, "prompts", "P4_meal_plan_select.json"), &g); err != nil {
		t.Fatal(err)
	}
	py := func(raw json.RawMessage) any {
		v, err := pyjson.Loads(string(raw))
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	var cands []candidate
	for _, c := range g.Inputs.Candidates {
		cands = append(cands, candidate{ID: c.ID, Source: c.Source, Title: c.Title, Tags: c.Tags, Description: c.Description,
			Prep: c.Prep, Cook: c.Cook})
	}
	if len(cands) <= maxCandidates {
		t.Fatal("golden does not exercise the 25-candidate cap")
	}
	b := g.Requests[0].Body
	if b.Temperature != 0.2 || b.MaxTokens != p4MaxTokens {
		t.Fatalf("params %+v", b)
	}
	if b.Messages[0].Content != p4System {
		t.Errorf("P4 system:\n got %q\nwant %q", p4System, b.Messages[0].Content)
	}
	if got := p4User(py(g.Inputs.Slot), py(g.Inputs.Preferences), py(g.Inputs.Recent), cands); got != b.Messages[1].Content {
		t.Errorf("P4 user:\n got %q\nwant %q", got, b.Messages[1].Content)
	}
}

func TestGoldenMealPlanSelect(t *testing.T) {
	var g struct {
		Rows []struct {
			Name  string `json:"name"`
			Reply struct {
				Content *string `json:"content"`
			} `json:"reply"`
			Out json.RawMessage `json:"out"`
		} `json:"meal_plan_select"`
	}
	if err := json.Unmarshal(goldenFile(t, "llm_parse.json"), &g); err != nil {
		t.Fatal(err)
	}
	cands := []candidate{{ID: "1", Title: "A"}, {ID: "2", Title: "B"}, {ID: "3", Title: "C"}, {ID: "core_1", Title: "D"}}
	ran := 0
	for _, r := range g.Rows {
		if r.Reply.Content == nil {
			continue // proxy_error / http_500: HTTP failures; in process every model failure is "LLM error"
		}
		ran++
		var want map[string]any
		_ = json.Unmarshal(r.Out, &want)
		got := map[string]any{}
		raw, _ := json.Marshal(parseSelection(*r.Reply.Content, cands))
		_ = json.Unmarshal(raw, &got)
		if strings.HasPrefix(want["reason"].(string), "Exception: ") {
			want["reason"], got["reason"] = nil, nil
		}
		w, _ := json.Marshal(want)
		gg, _ := json.Marshal(got)
		if string(w) != string(gg) {
			t.Errorf("%s:\n got %s\nwant %s", r.Name, gg, w)
		}
	}
	if ran < 9 {
		t.Fatalf("only %d rows ran", ran)
	}
}

func TestMealPlanRoutes(t *testing.T) {
	e := setup(t)
	e.hh.set(1, "A")
	e.hh.set(2, "A")
	h, member := tok(1, "A"), tok(2, "A")
	e.expectDetail(t, http.StatusNotFound, "Job not found", http.MethodGet, "/meal-plans/generate/jobs/nope", nil, h...)
	e.expectValidation(t, "body.days", http.MethodPost, "/meal-plans/generate/jobs", map[string]any{"days": []any{}}, h...)
	e.expectValidation(t, "body.days", http.MethodPost, "/meal-plans/generate/jobs", map[string]any{}, h...)
	day := func(meals any) map[string]any {
		return map[string]any{"days": []any{map[string]any{"date": "2026-10-09", "meals": meals}}}
	}
	e.expectValidation(t, "body.days.0.meals.dinner.servings", http.MethodPost, "/meal-plans/generate/jobs",
		day(map[string]any{"dinner": map[string]any{"servings": 0}}), h...)
	e.expectValidation(t, "body.days.0.meals", http.MethodPost, "/meal-plans/generate/jobs", day(map[string]any{}), h...)
	e.expectValidation(t, "body.days.0.meals.brunch.[key]", http.MethodPost, "/meal-plans/generate/jobs",
		day(map[string]any{"brunch": map[string]any{"servings": 2}}), h...)
	e.expectValidation(t, "body.days.0.date", http.MethodPost, "/meal-plans/generate/jobs",
		map[string]any{"days": []any{map[string]any{"date": "soon", "meals": map[string]any{"dinner": map[string]any{"servings": 2}}}}}, h...)

	// No recipes, no LLM: the job completes with one unfilled slot.
	body := day(map[string]any{"dinner": map[string]any{"servings": 2}})
	body["pinned_recipe_id"], body["allow_external_recipes"] = nil, false
	o := e.obj(t, http.StatusAccepted, http.MethodPost, "/meal-plans/generate/jobs", body, h...)
	if len(o["job_id"].(string)) != 36 || len(o["request_id"].(string)) != 36 {
		t.Fatalf("submit: %v", o)
	}
	path := "/meal-plans/generate/jobs/" + o["job_id"].(string)
	e.expectDetail(t, http.StatusNotFound, "Job not found", http.MethodGet, path, nil, member...)
	e.waitJob(t, o["job_id"].(string), h)
	j := e.obj(t, http.StatusOK, http.MethodGet, path, nil, h...)
	res := j["result"].(map[string]any)
	if j["status"] != "COMPLETE" || res["slot_failures_count"] != float64(1) || len(j) != 5 {
		t.Fatalf("job: %v", j)
	}
	dinner := res["result"].(map[string]any)["days"].([]any)[0].(map[string]any)["meals"].(map[string]any)["dinner"].(map[string]any)
	if dinner["selection"] != nil || dinner["servings"] != float64(2) || dinner["is_meal_prep"] != false {
		t.Fatalf("slot: %v", dinner)
	}
	// RD10: meal-plan jobs are not in the import list.
	if l := e.obj(t, http.StatusOK, http.MethodGet, "/recipes/parse-url/jobs?status=", nil, h...)["jobs"].([]any); len(l) != 0 {
		t.Fatalf("list: %v", l)
	}
}

func TestMealPlanGeneration(t *testing.T) {
	e := setup(t)
	e.hh.set(1, "A")
	e.hh.set(2, "A", "B")
	e.hh.set(3, "B")
	h := tok(2, "A")
	// RD7: the planner (2) sees household A's and household B's recipes.
	pasta := e.create(t, tok(1, "A"), recipeBody("Weeknight Pasta", map[string]any{"tags": []string{"Dinner"}}))
	stew := e.create(t, tok(3, "B"), recipeBody("Beef Stew", map[string]any{"tags": []string{"dinner"}}))
	e.create(t, tok(1, "A"), recipeBody("Pancakes", map[string]any{"tags": []string{"breakfast"}}))
	pid, sid := jsonString(idOf(pasta)), jsonString(idOf(stew))

	var mu sync.Mutex
	var calls []string
	f := &fakeLLM{reply: func(req llm.ChatRequest) (string, error) {
		u := msgText(req.Messages[1])
		mu.Lock()
		calls = append(calls, u)
		mu.Unlock()
		if *req.Temperature != 0.2 || *req.MaxTokens != 300 {
			return "", errors.New("bad params")
		}
		switch {
		case strings.Contains(u, `"date": "2026-10-09"`):
			// rank the stew first with the pasta as the alternative
			return `{"ranked_recipes": [{"recipe_id": "` + sid + `", "confidence": 0.9, "reason": "hearty"}, ` +
				`{"recipe_id": "` + pid + `", "confidence": 0.6, "reason": "quick"}], "warnings": []}`, nil
		case strings.Contains(u, `"date": "2026-10-10"`):
			return "", errors.New("model down") // B19: falls back to the first candidate
		}
		return `{"ranked_recipes": [], "warnings": ["nothing fits"]}`, nil
	}}
	e.m.LLM = f
	dinner := map[string]any{"dinner": map[string]any{"servings": 2, "tags": []string{"dinner"}}}
	o := e.obj(t, http.StatusAccepted, http.MethodPost, "/meal-plans/generate/jobs", map[string]any{"days": []any{
		map[string]any{"date": "2026-10-11", "meals": dinner},
		map[string]any{"date": "2026-10-09", "meals": dinner},
		map[string]any{"date": "2026-10-10", "meals": dinner},
		map[string]any{"date": "2026-10-12", "meals": map[string]any{"breakfast": map[string]any{"servings": 1, "note": "Pancake"}}},
	}}, h...)
	job := e.waitJob(t, o["job_id"].(string), h)
	if job["status"] != "COMPLETE" {
		t.Fatalf("job: %v", job)
	}
	res := job["result"].(map[string]any)
	days := res["result"].(map[string]any)["days"].([]any)
	sel := func(i int, meal string) map[string]any {
		s, _ := days[i].(map[string]any)["meals"].(map[string]any)[meal].(map[string]any)["selection"].(map[string]any)
		return s
	}
	if days[0].(map[string]any)["date"] != "2026-10-09" {
		t.Fatalf("days are sorted: %v", days)
	}
	s0 := sel(0, "dinner")
	if s0["source"] != "user" || s0["recipe_id"] != sid || s0["confidence"] != 0.9 {
		t.Fatalf("ranked pick: %v", s0)
	}
	alts := s0["alternatives"].([]any)
	if len(alts) != 1 || alts[0].(map[string]any)["recipe_id"] != pid || alts[0].(map[string]any)["title"] != "Weeknight Pasta" {
		t.Fatalf("alternatives: %v", alts)
	}
	// Day 2: the model failed; the only unused dinner (the pasta) is taken deterministically.
	s1 := sel(1, "dinner")
	if s1["recipe_id"] != pid || s1["confidence"] != nil ||
		!strings.Contains(strings.Join(toStrings(s1["warnings"]), "|"), "LLM unavailable, using deterministic selection") {
		t.Fatalf("fallback: %v", s1)
	}
	// Day 3: both dinners are used: already_used, no LLM call.
	s2 := sel(2, "dinner")
	if s2["recipe_id"] != nil || !strings.HasPrefix(toStrings(s2["warnings"])[0], "already_used") {
		t.Fatalf("already used: %v", s2)
	}
	// Day 4: the note narrows to the pancakes; the model declines → no selection.
	if s3 := sel(3, "breakfast"); s3 != nil {
		t.Fatalf("declined: %v", s3)
	}
	if res["slot_failures_count"] != float64(2) {
		t.Fatalf("failures: %v", res["slot_failures_count"])
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 3 || !strings.Contains(calls[2], `"summary": null`) || !strings.Contains(calls[2], "Pancakes") ||
		strings.Contains(calls[2], "Stew") {
		t.Fatalf("calls: %d %q", len(calls), calls)
	}
	// RD2: nothing was staged.
	if e.count(t, `SELECT COUNT(*) FROM recipes_stage_recipes`) != 0 {
		t.Fatal("stage rows")
	}
}

func toStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}
