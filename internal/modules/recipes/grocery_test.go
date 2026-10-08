package recipes

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// fakeLLM answers every chat with reply (or err) and records the requests.
type fakeLLM struct {
	mu    sync.Mutex
	reply func(req llm.ChatRequest) (string, error)
	reqs  []llm.ChatRequest
}

func (f *fakeLLM) Chat(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	f.mu.Unlock()
	out, err := f.reply(req)
	if err != nil {
		return nil, err
	}
	return &llm.ChatResponse{Content: out}, nil
}

func (f *fakeLLM) requests() []llm.ChatRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]llm.ChatRequest(nil), f.reqs...)
}

func msgText(m llm.Message) string {
	if m.Content == nil || m.Content.Text == nil {
		return ""
	}
	return *m.Content.Text
}

func TestSKUMapRoutes(t *testing.T) {
	e := setup(t)
	e.hh.set(1, "A")
	e.hh.set(2, "A")
	h, member, outsider := tok(1, "A"), tok(2, "A"), tok(3, "")
	if l := e.list(t, "/grocery/sku-map", h...); len(l) != 0 {
		t.Fatalf("empty: %v", l)
	}
	m := e.obj(t, http.StatusOK, http.MethodPut, "/grocery/sku-map", map[string]any{
		"ingredient_name": " Olive Oil ", "sku": "111", "product_name": "GV Olive Oil", "unit_size": "17 oz"}, h...)
	want := map[string]any{"id": m["id"], "retailer": "walmart", "ingredient_name": "olive oil", "sku": "111",
		"product_name": "GV Olive Oil", "unit_size": "17 oz", "source": "manual"}
	if !reflect.DeepEqual(m, want) {
		t.Fatalf("put: %v", m)
	}
	// Upsert by a member: same row, omitted optionals become null.
	m2 := e.obj(t, http.StatusOK, http.MethodPut, "/grocery/sku-map", map[string]any{
		"ingredient_name": "olive oil", "sku": "222", "retailer": "walmart"}, member...)
	if m2["id"] != m["id"] || m2["sku"] != "222" || m2["product_name"] != nil || m2["unit_size"] != nil {
		t.Fatalf("upsert: %v", m2)
	}
	b := e.obj(t, http.StatusOK, http.MethodPut, "/grocery/sku-map", map[string]any{
		"ingredient_name": "2 lb Ground Beef (93/7 or leaner)", "raw": true, "sku": "333"}, h...)
	if b["ingredient_name"] != "ground beef" {
		t.Fatalf("raw: %v", b)
	}
	l := e.list(t, "/grocery/sku-map?retailer=walmart", member...)
	if len(l) != 2 || l[0].(map[string]any)["ingredient_name"] != "ground beef" {
		t.Fatalf("list: %v", l)
	}
	if l := e.list(t, "/grocery/sku-map", outsider...); len(l) != 0 {
		t.Fatalf("outsider: %v", l)
	}
	e.expectValidation(t, "query.retailer", http.MethodGet, "/grocery/sku-map?retailer=target", nil, h...)
	e.expectValidation(t, "body.sku", http.MethodPut, "/grocery/sku-map", map[string]any{"ingredient_name": "x", "sku": ""}, h...)
	e.expectValidation(t, "body.sku", http.MethodPut, "/grocery/sku-map", map[string]any{"ingredient_name": "x", "sku": strings.Repeat("1", 65)}, h...)
	e.expectValidation(t, "body.ingredient_name", http.MethodPut, "/grocery/sku-map", map[string]any{"sku": "1"}, h...)
	e.expectValidation(t, "body.retailer", http.MethodPut, "/grocery/sku-map", map[string]any{"ingredient_name": "x", "sku": "1", "retailer": "target"}, h...)
	e.expectValidation(t, "body.raw", http.MethodPut, "/grocery/sku-map", map[string]any{"ingredient_name": "x", "sku": "1", "raw": "maybe"}, h...)

	p := path("/grocery/sku-map/%d", idOf(b))
	e.expectDetail(t, http.StatusNotFound, "Mapping not found", http.MethodDelete, p, nil, outsider...)
	e.json(t, http.StatusNoContent, http.MethodDelete, p, nil, member...)
	e.expectDetail(t, http.StatusNotFound, "Mapping not found", http.MethodDelete, p, nil, h...)
}

// seedCart gives household A a two-meal plan (chili, tacos) and salt as a staple.
func seedCart(t *testing.T, e *env, h []string) {
	t.Helper()
	r1 := e.create(t, h, recipeBody("chili", map[string]any{"ingredients": []map[string]any{
		{"text": "2 lb ground beef", "quantity_display": "2", "unit": "lb"},
		{"text": "1 cup Olive Oil, divided", "quantity_display": "1", "unit": "Cup "},
		{"text": "Salt to taste"},
		{"text": "2 cloves garlic, minced", "quantity_display": "2", "unit": "cloves"},
	}}))
	r2 := e.create(t, h, recipeBody("tacos", map[string]any{"ingredients": []map[string]any{
		{"text": "1 1/2 lb Ground Beef (93/7 or leaner)", "quantity_display": "1 1/2", "unit": "LB"},
		{"text": "2 tbsp olive oil", "quantity_display": "2", "unit": "tbsp"},
		{"text": "salt"},
	}}))
	e.obj(t, http.StatusOK, http.MethodPost, "/planner/commit", map[string]any{"start_date": day(1), "items": []any{
		planItem(day(1), "dinner", idOf(r1)), planItem(day(2), "dinner", idOf(r2)),
	}}, h...)
	e.obj(t, http.StatusCreated, http.MethodPost, "/staples", map[string]any{"name": "salt"}, h...)
}

func TestCart(t *testing.T) {
	e := setup(t)
	e.hh.set(1, "A")
	e.hh.set(2, "A")
	h, member := tok(1, "A"), tok(2, "A")
	seedCart(t, e, h)
	q := "?start_date=" + day(0) + "&end_date=" + day(7)

	// Empty map: nothing matched, no url, no match pass.
	cart := e.obj(t, http.StatusOK, http.MethodPost, "/grocery/cart"+q, nil, h...)
	raw, _ := json.Marshal(cart)
	want := `{"items":[],"match_job_id":null,"retailer":"walmart","unmatched":[` +
		`{"amount_display":"2 cloves","ingredient_name":"garlic","recipes":["chili"]},` +
		`{"amount_display":"3.5 lb","ingredient_name":"ground beef","recipes":["chili","tacos"]},` +
		`{"amount_display":"1 cup, 2 tbsp","ingredient_name":"olive oil","recipes":["chili","tacos"]}],"url":null}`
	if string(raw) != want {
		t.Fatalf("cart:\n got %s\nwant %s", raw, want)
	}

	// One mapping: the rest is unmatched, so a match pass is queued.
	e.obj(t, http.StatusOK, http.MethodPut, "/grocery/sku-map", map[string]any{"ingredient_name": "ground beef", "sku": "1001", "unit_size": "1 lb", "product_name": "Beef 1lb"}, h...)
	cart = e.obj(t, http.StatusOK, http.MethodPost, "/grocery/cart"+q, nil, member...)
	jobID, _ := cart["match_job_id"].(string)
	if len(jobID) != 36 || cart["url"] != "https://affil.walmart.com/cart/addToCart?items=1001_4" {
		t.Fatalf("cart with a mapping: %v", cart)
	}
	var data string
	if err := e.d.Read.QueryRow(`SELECT job_data FROM recipes_recipe_parse_jobs WHERE id = ? AND job_type = 'grocery_match'`, jobID).Scan(&data); err != nil {
		t.Fatal(err)
	}
	if data != `{"retailer":"walmart","unmatched":["garlic","olive oil"]}` {
		t.Fatalf("job_data %s", data)
	}
	// The job is the member's (author-only poll).
	e.obj(t, http.StatusOK, http.MethodGet, "/recipes/jobs/"+jobID, nil, member...)
	e.expectDetail(t, http.StatusNotFound, "Job not found", http.MethodGet, "/recipes/jobs/"+jobID, nil, h...)

	// Everything mapped: no job; pack quantity = ceil(qty/size) when the units match.
	e.obj(t, http.StatusOK, http.MethodPut, "/grocery/sku-map", map[string]any{"ingredient_name": "olive oil", "sku": "1002", "unit_size": "17 oz"}, h...)
	e.obj(t, http.StatusOK, http.MethodPut, "/grocery/sku-map", map[string]any{"ingredient_name": "garlic", "sku": "10 03"}, h...)
	cart = e.obj(t, http.StatusOK, http.MethodPost, "/grocery/cart"+q+"&retailer=walmart", nil, member...)
	if cart["url"] != "https://affil.walmart.com/cart/addToCart?items=10%2003_1,1001_4,1002_1" || cart["match_job_id"] != nil ||
		len(cart["unmatched"].([]any)) != 0 {
		t.Fatalf("full cart: %v", cart)
	}
	gb := cart["items"].([]any)[1].(map[string]any)
	if !reflect.DeepEqual(gb, map[string]any{"ingredient_name": "ground beef", "sku": "1001", "quantity": 4.0,
		"product_name": "Beef 1lb", "unit_size": "1 lb", "source": "manual"}) {
		t.Fatalf("item: %v", gb)
	}

	e.expectDetail(t, http.StatusUnprocessableEntity, "end_date must not be before start_date", http.MethodPost,
		"/grocery/cart?start_date="+day(7)+"&end_date="+day(0), nil, h...)
	e.expectValidation(t, "query.retailer", http.MethodPost, "/grocery/cart"+q+"&retailer=target", nil, h...)
	e.expectValidation(t, "query.end_date", http.MethodPost, "/grocery/cart?start_date="+day(0), nil, h...)
}

// waitJob polls a parse job until it leaves PENDING/RUNNING.
func (e *env) waitJob(t *testing.T, id string, h []string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		o := e.obj(t, http.StatusOK, http.MethodGet, "/recipes/jobs/"+id, nil, h...)
		if s := o["status"]; s != statusPending && s != statusRunning {
			return o
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s still %v", id, o["status"])
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestGroceryMatchJob(t *testing.T) {
	e := setup(t)
	e.hh.set(1, "A")
	e.hh.set(3, "C")
	h := tok(1, "A")
	f := &fakeLLM{}
	e.m.LLM = f

	// The outsider's mapping (id 1) must never be usable by A.
	e.obj(t, http.StatusOK, http.MethodPut, "/grocery/sku-map", map[string]any{"ingredient_name": "secret", "sku": "666"}, tok(3, "C")...)
	beef := e.obj(t, http.StatusOK, http.MethodPut, "/grocery/sku-map", map[string]any{"ingredient_name": "ground beef", "sku": "2001", "unit_size": "1 lb", "product_name": "Beef"}, h...)
	oil := e.obj(t, http.StatusOK, http.MethodPut, "/grocery/sku-map", map[string]any{"ingredient_name": "oil", "sku": "2002"}, h...)
	r := e.create(t, h, recipeBody("burgers", map[string]any{"ingredients": []map[string]any{
		{"text": "1 lb 90/10 ground beef", "quantity_display": "1", "unit": "lb"},
		{"text": "2 tbsp olive oil", "quantity_display": "2", "unit": "tbsp"},
		{"text": "4 burger buns", "quantity_display": "4"},
		{"text": "1 tsp ground beef"}, // already mapped: matched exactly
	}}))
	e.obj(t, http.StatusOK, http.MethodPost, "/planner/commit", map[string]any{"start_date": day(1),
		"items": []any{planItem(day(1), "dinner", idOf(r))}}, h...)

	f.reply = func(llm.ChatRequest) (string, error) {
		return `{"matches": [
			{"ingredient": "90/10 ground beef", "candidate_id": ` + jsonID(beef) + `},
			{"ingredient": "olive oil", "candidate_id": ` + jsonID(oil) + `.0},
			{"ingredient": "burger buns", "candidate_id": 1},
			{"ingredient": "invented thing", "candidate_id": ` + jsonID(oil) + `},
			{"ingredient": "burger buns", "candidate_id": "` + jsonID(oil) + `"},
			{"ingredient": "burger buns", "candidate_id": null},
			"junk"]}`, nil
	}
	cart := e.obj(t, http.StatusOK, http.MethodPost, "/grocery/cart?start_date="+day(0)+"&end_date="+day(2), nil, h...)
	id := cart["match_job_id"].(string)
	job := e.waitJob(t, id, h)
	res, _ := json.Marshal(job["result"])
	want := `{"attempted":["90/10 ground beef","burger buns","olive oil"],` +
		`"learned":[{"ingredient_name":"90/10 ground beef","product_name":"Beef","sku":"2001"},{"ingredient_name":"olive oil","product_name":null,"sku":"2002"}],` +
		`"unresolved":["burger buns"]}`
	if job["status"] != statusComplete || string(res) != want {
		t.Fatalf("job: %v\n got %s\nwant %s", job["status"], res, want)
	}

	// The request: P5 on the background label, temperature 0, JSON mode, 1500 tokens.
	req := f.requests()[0]
	if req.Label != llm.LabelBackground || *req.Temperature != 0 || *req.MaxTokens != 1500 || req.ResponseFormat.Type != "json_object" {
		t.Fatalf("request: %+v", req)
	}
	if !strings.HasPrefix(msgText(req.Messages[1]), "CANDIDATES:\n") || strings.Contains(msgText(req.Messages[1]), "secret") {
		t.Fatalf("candidates are the household's own: %q", msgText(req.Messages[1]))
	}

	// The next cart matches the learned aliases exactly, and their source is "llm".
	cart = e.obj(t, http.StatusOK, http.MethodPost, "/grocery/cart?start_date="+day(0)+"&end_date="+day(2), nil, h...)
	if cart["url"] != "https://affil.walmart.com/cart/addToCart?items=2001_1,2001_1,2002_1" {
		t.Fatalf("learned cart: %v", cart["url"])
	}
	for _, it := range cart["items"].([]any) {
		im := it.(map[string]any)
		if im["ingredient_name"] == "olive oil" && im["source"] != "llm" {
			t.Fatalf("alias source: %v", im)
		}
	}
	if cart["match_job_id"] == nil {
		t.Fatal("buns still unmatched: another pass")
	}
	e.waitJob(t, cart["match_job_id"].(string), h)

	// A manual mapping is never overwritten by the pass, even one made while the pass was
	// queued (the job still asks about the name).
	e.obj(t, http.StatusOK, http.MethodPut, "/grocery/sku-map", map[string]any{"ingredient_name": "burger buns", "sku": "3000"}, h...)
	f.reply = func(llm.ChatRequest) (string, error) {
		return `{"matches": [{"ingredient": "Burger Buns ", "candidate_id": ` + jsonID(oil) + `}]}`, nil
	}
	var id2 string
	if err := e.d.Tx(e.ctx, func(tx *sql.Tx) error {
		var err error
		id2, err = e.m.createJob(e.ctx, tx, caller{ID: 1, households: []string{"A"}, writeHousehold: "A"}, "grocery_match",
			groceryMatchJobType, map[string]any{"retailer": "walmart", "unmatched": []string{"burger buns"}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	e.q.Notify(groceryMatchJobType)
	if job := e.waitJob(t, id2, h); job["status"] != statusComplete {
		t.Fatalf("job: %v", job)
	}
	var sku, source string
	if err := e.d.Read.QueryRow(`SELECT sku, source FROM recipes_grocery_sku_map WHERE ingredient_name = 'burger buns'`).Scan(&sku, &source); err != nil {
		t.Fatal(err)
	}
	if sku != "3000" || source != "manual" {
		t.Fatalf("manual row overwritten: %s %s", sku, source)
	}
}

func jsonID(o map[string]any) string { b, _ := json.Marshal(o["id"]); return string(b) }

// TestGroceryMatchFailures: a model failure or garbage is "nothing learned", never an error;
// an empty job is ERROR empty_job; a purged or canceled row is skipped.
func TestGroceryMatchFailures(t *testing.T) {
	e := setup(t)
	e.hh.set(1, "A")
	h := tok(1, "A")
	f := &fakeLLM{}
	e.m.LLM = f
	e.obj(t, http.StatusOK, http.MethodPut, "/grocery/sku-map", map[string]any{"ingredient_name": "beef", "sku": "1"}, h...)
	r := e.create(t, h, recipeBody("r", map[string]any{"ingredients": []map[string]any{{"text": "rice"}}}))
	e.obj(t, http.StatusOK, http.MethodPost, "/planner/commit", map[string]any{"start_date": day(1),
		"items": []any{planItem(day(1), "dinner", idOf(r))}}, h...)
	cartJob := func() string {
		t.Helper()
		return e.obj(t, http.StatusOK, http.MethodPost, "/grocery/cart?start_date="+day(0)+"&end_date="+day(2), nil, h...)["match_job_id"].(string)
	}
	for name, reply := range map[string]func(llm.ChatRequest) (string, error){
		"error":    func(llm.ChatRequest) (string, error) { return "", errors.New("model not loaded") },
		"not json": func(llm.ChatRequest) (string, error) { return "sorry", nil },
		"list":     func(llm.ChatRequest) (string, error) { return `[1,2]`, nil },
		"no list":  func(llm.ChatRequest) (string, error) { return `{"matches": {"a": 1}}`, nil },
		"control":  func(llm.ChatRequest) (string, error) { return "{\"matches\": [\x01]}", nil },
	} {
		f.reply = reply
		job := e.waitJob(t, cartJob(), h)
		res, _ := json.Marshal(job["result"])
		if job["status"] != statusComplete || string(res) != `{"attempted":["rice"],"learned":[],"unresolved":["rice"]}` {
			t.Errorf("%s: %v %s", name, job["status"], res)
		}
	}

	// No LLM wired: the same.
	e.m.LLM = nil
	if job := e.waitJob(t, cartJob(), h); job["status"] != statusComplete {
		t.Fatalf("no llm: %v", job)
	}

	// An empty job.
	c := caller{ID: 1, households: []string{"A"}, writeHousehold: "A"}
	var id string
	if err := e.d.Tx(e.ctx, func(tx *sql.Tx) error {
		var err error
		id, err = e.m.createJob(e.ctx, tx, c, "grocery_match", groceryMatchJobType, map[string]any{"retailer": "walmart", "unmatched": []any{1}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	e.q.Notify(groceryMatchJobType)
	if job := e.waitJob(t, id, h); job["status"] != statusError || job["error_code"] != "empty_job" || job["error_message"] != "No ingredients to match" {
		t.Fatalf("empty job: %v", job)
	}
}

// TestMatchRequestLabel: the setting names the label; anything else is live.
func TestMatchRequestLabel(t *testing.T) {
	e := setup(t)
	if r := e.m.matchRequest(e.ctx, nil); r.Label != llm.LabelBackground {
		t.Fatalf("default: %s", r.Label)
	}
	for v, want := range map[string]string{"live": llm.LabelLive, "BACKGROUND": llm.LabelBackground, "qwen3-14b": llm.LabelLive} {
		if err := e.m.settings.Set(e.ctx, SettingBackgroundModel, v, settings.Scope{}); err != nil {
			t.Fatal(err)
		}
		if r := e.m.matchRequest(e.ctx, nil); r.Label != want {
			t.Errorf("%s: %s, want %s", v, r.Label, want)
		}
	}
}

// TestGoldenPromptP5: the match prompt and parameters, byte for byte.
func TestGoldenPromptP5(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "..", "fixtures", "golden", "recipes", "prompts", "P5_grocery_match.json"))
	if err != nil {
		t.Fatal(err)
	}
	var g struct {
		Inputs struct {
			Candidates []struct {
				ID             int64   `json:"id"`
				IngredientName string  `json:"ingredient_name"`
				ProductName    *string `json:"product_name"`
			} `json:"candidates"`
			Unmatched []string `json:"unmatched"`
		} `json:"inputs"`
		Requests []struct {
			Body struct {
				Model          string            `json:"model"`
				Temperature    float64           `json:"temperature"`
				ResponseFormat map[string]string `json:"response_format"`
				Messages       []struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"messages"`
				MaxTokens int  `json:"max_tokens"`
				Stream    bool `json:"stream"`
			} `json:"body"`
		} `json:"requests"`
	}
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	if len(g.Requests) != 1 || len(g.Inputs.Candidates) <= maxMatchCandidates || len(g.Inputs.Unmatched) <= maxUnmatchedPerPass {
		t.Fatal("golden does not exercise the caps")
	}
	var cands []skuMapping
	for _, c := range g.Inputs.Candidates {
		cands = append(cands, skuMapping{ID: c.ID, IngredientName: c.IngredientName, ProductName: c.ProductName})
	}
	msgs := buildMatchPrompt(cands, g.Inputs.Unmatched)
	want := g.Requests[0].Body
	if len(msgs) != len(want.Messages) {
		t.Fatalf("%d messages, want %d", len(msgs), len(want.Messages))
	}
	for i, m := range msgs {
		if m.Role != want.Messages[i].Role || msgText(m) != want.Messages[i].Content {
			t.Errorf("message %d differs:\n got %q\nwant %q", i, msgText(m), want.Messages[i].Content)
		}
	}
	e := setup(t)
	req := e.m.matchRequest(e.ctx, msgs)
	if req.Label != want.Model || *req.Temperature != want.Temperature || *req.MaxTokens != want.MaxTokens ||
		req.ResponseFormat.Type != want.ResponseFormat["type"] || want.Stream {
		t.Fatalf("parameters: %+v vs %+v", req, want)
	}
}
