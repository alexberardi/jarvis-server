package recipes

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
	"github.com/alexberardi/jarvis-server/internal/platform/ssrf"
)

const soupLD = `{"@context":"https://schema.org","@type":"Recipe","name":"Contract Soup","description":"A soup.",` +
	`"recipeYield":"4 servings","totalTime":"PT45M","recipeIngredient":["2 cups water","1 tsp salt","1 onion, diced"],` +
	`"recipeInstructions":[{"@type":"HowToStep","text":"Boil the water."},{"@type":"HowToStep","text":"Season."}],` +
	`"keywords":"soup, easy","image":"https://example.com/soup.jpg"}`

func loopbackFetcher() *ssrf.Fetcher {
	return &ssrf.Fetcher{Blocked: func(ip netip.Addr) bool { return !ip.Unmap().IsLoopback() && ssrf.IPBlocked(ip) }}
}

func TestParseURLAsync(t *testing.T) {
	e := setup(t)
	h := tok(1, "")
	for _, u := range []string{"http://127.0.0.1/", "http://localhost:7030/", "http://10.0.0.1/recipe", "http://[::1]/"} {
		o := e.obj(t, http.StatusBadRequest, http.MethodPost, "/recipes/parse-url/async", map[string]any{"url": u}, h...)
		d := o["detail"].(map[string]any)
		if d["error_code"] != "invalid_url" || d["message"] != "Host is blocked (localhost/private)." || d["status_code"] != nil || len(d["job_id"].(string)) != 36 {
			t.Fatalf("%s: %v", u, o)
		}
	}
	e.expectValidation(t, "body.url", http.MethodPost, "/recipes/parse-url/async", map[string]any{"url": "ftp://example.com/x"}, h...)
	e.expectValidation(t, "body.url", http.MethodPost, "/recipes/parse-url/async", map[string]any{}, h...)
	e.expectValidation(t, "body.url", http.MethodPost, "/recipes/parse-url/async", map[string]any{"url": 5}, h...)

	var gets, heads int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" || strings.Contains(r.Header.Get("User-Agent"), "Go-http-client") {
			t.Errorf("no scraper UA: %q", r.Header.Get("User-Agent"))
		}
		switch r.URL.Path {
		case "/ok":
			if r.Method == http.MethodHead {
				heads++
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			gets++
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte("<html><body><h1>Soup</h1>" + strings.Repeat("<p>Some recipe text here.</p>", 20) + "</body></html>"))
		case "/forbidden":
			w.WriteHeader(http.StatusForbidden)
		case "/missing":
			w.WriteHeader(http.StatusNotFound)
		case "/pdf":
			w.Header().Set("Content-Type", "application/pdf")
		case "/binary":
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(strings.Repeat("\x01\x02\x03\x04abc", 100)))
		case "/to-private":
			http.Redirect(w, r, "http://10.0.0.1/", http.StatusFound)
		}
	}))
	defer srv.Close()
	e.m.Fetch = loopbackFetcher()

	o := e.obj(t, http.StatusOK, http.MethodPost, "/recipes/parse-url/async", map[string]any{"url": srv.URL + "/ok", "use_llm_fallback": false}, h...)
	want := map[string]any{"id": o["id"], "status": "PENDING", "result": nil, "error_code": nil, "error_message": nil,
		"next_action": "webview_extract", "next_action_reason": "webview_required"}
	if !reflect.DeepEqual(o, want) || heads != 1 || gets != 2 {
		t.Fatalf("ok: %v heads=%d gets=%d", o, heads, gets)
	}
	e.expectDetail(t, http.StatusNotFound, "Job not found", http.MethodGet, "/recipes/jobs/"+o["id"].(string), nil, h...)

	detail := func(path string) map[string]any {
		return e.obj(t, http.StatusBadRequest, http.MethodPost, "/recipes/parse-url/async", map[string]any{"url": srv.URL + path}, h...)["detail"].(map[string]any)
	}
	if d := detail("/forbidden"); d["error_code"] != "fetch_failed" || d["status_code"] != float64(403) ||
		d["next_action"] != "webview_extract" || d["next_action_reason"] != "blocked_by_site" || d["message"] != "Site returned status 403." {
		t.Fatalf("403: %v", d)
	}
	if d := detail("/missing"); d["error_code"] != "fetch_failed" || d["next_action"] != nil {
		t.Fatalf("404: %v", d)
	}
	if _, has := detail("/missing")["next_action"]; has {
		t.Fatal("next_action only on 401/403")
	}
	if d := detail("/pdf"); d["error_code"] != "unsupported_content_type" || d["message"] != "Unsupported content type: application/pdf" {
		t.Fatalf("pdf: %v", d)
	}
	if d := detail("/binary"); d["error_code"] != "encoding_error" || d["next_action_reason"] != "encoding_error" {
		t.Fatalf("binary: %v", d)
	}
	if d := detail("/to-private"); d["error_code"] != "invalid_url" {
		t.Fatalf("redirect to private: %v", d)
	}
}

func TestWebviewImportJSONLD(t *testing.T) {
	e := setup(t)
	e.hh.set(1, "A")
	e.hh.set(2, "A")
	h, member := tok(1, "A"), tok(2, "A")
	o := e.obj(t, http.StatusOK, http.MethodPost, "/recipes/parse-payload/async", map[string]any{"input": map[string]any{
		"source_type": "client_webview", "source_url": "https://example.com/contract-soup", "jsonld_blocks": []string{soupLD},
		"html_snippet": "<main><h1>Contract Soup</h1></main>", "client": "test",
	}}, h...)
	if o["status"] != "PENDING" || len(o) != 2 {
		t.Fatalf("submit: %v", o)
	}
	id := o["id"].(string)
	if e.count(t, `SELECT COUNT(*) FROM recipes_recipe_parse_jobs WHERE id = ? AND household_id = 'A' AND job_type = 'ingestion'`, id) != 1 {
		t.Fatal("job row not stamped")
	}
	job := e.waitJob(t, id, h)
	if job["status"] != "COMPLETE" {
		t.Fatalf("job: %v", job)
	}
	draft := job["result"].(map[string]any)["recipe_draft"].(map[string]any)
	if draft["title"] != "Contract Soup" || draft["servings"] != float64(4) || !reflect.DeepEqual(draft["tags"], []any{"easy"}) ||
		draft["cook_time_minutes"] != float64(45) || len(draft["ingredients"].([]any)) != 3 {
		t.Fatalf("draft: %v", draft)
	}
	pipe := job["result"].(map[string]any)["pipeline"].(map[string]any)
	if pipe["parser_strategy"] != "client_json_ld" || pipe["used_llm"] != false {
		t.Fatalf("pipeline: %v", pipe)
	}
	e.expectDetail(t, http.StatusNotFound, "Job not found", http.MethodGet, "/recipes/jobs/"+id, nil, member...)
	l := e.obj(t, http.StatusOK, http.MethodGet, "/recipes/parse-url/jobs", nil, h...)["jobs"].([]any)
	if len(l) != 1 || !reflect.DeepEqual(l[0].(map[string]any)["preview"], map[string]any{"title": "Contract Soup", "source_host": "example.com"}) {
		t.Fatalf("list: %v", l)
	}

	e.expectValidation(t, "body.input", http.MethodPost, "/recipes/parse-payload/async", map[string]any{}, h...)
	e.expectValidation(t, "body.input.source_type", http.MethodPost, "/recipes/parse-payload/async",
		map[string]any{"input": map[string]any{"source_type": "email"}}, h...)
	e.expectValidation(t, "body.input.source_type", http.MethodPost, "/recipes/parse-payload/async",
		map[string]any{"input": map[string]any{}}, h...)
	e.expectValidation(t, "body.input.jsonld_blocks.0", http.MethodPost, "/recipes/parse-payload/async",
		map[string]any{"input": map[string]any{"source_type": "client_webview", "jsonld_blocks": []any{1}}}, h...)
}

func TestWebviewImportErrors(t *testing.T) {
	e := setup(t)
	h := tok(1, "")
	submit := func(in map[string]any) string {
		return e.obj(t, http.StatusOK, http.MethodPost, "/recipes/parse-payload/async", map[string]any{"input": in}, h...)["id"].(string)
	}
	for _, c := range []struct {
		in        map[string]any
		code, msg string
	}{
		{map[string]any{"source_type": "client_webview", "source_url": "https://example.com/x"}, "invalid_payload", "no content to parse"},
		{map[string]any{"source_type": "image_upload", "images": []any{map[string]any{"filename": "a.jpg", "data_base64": "/9j/4AAQ"}}},
			"not_implemented", "image_ingestion_not_implemented"},
		{map[string]any{"source_type": "server_fetch"}, "invalid_payload", "source_url required"},
	} {
		job := e.waitJob(t, submit(c.in), h)
		if job["status"] != "ERROR" || job["error_code"] != c.code || job["error_message"] != c.msg || job["result"] != nil {
			t.Fatalf("%v: %v", c.in, job)
		}
	}
}

func TestWebviewImportLLM(t *testing.T) {
	e := setup(t)
	h := tok(1, "")
	f := &fakeLLM{reply: func(req llm.ChatRequest) (string, error) {
		return "Sure! ```json\n{\"title\": \"Toast\", \"servings\": 2, \"ingredients\": [{\"text\": \"bread\", \"quantity_display\": 2, \"unit\": \"slices\"}], \"steps\": [\"Toast\", \"Butter\"]}\n```", nil
	}}
	e.m.LLM = f
	id := e.obj(t, http.StatusOK, http.MethodPost, "/recipes/parse-payload/async", map[string]any{"input": map[string]any{
		"source_type": "client_webview", "source_url": "https://example.com/toast",
		"html_snippet": "<main><h1>Toast</h1><p>Toast bread. Butter it.</p></main>",
	}}, h...)["id"].(string)
	job := e.waitJob(t, id, h)
	if job["status"] != "COMPLETE" {
		t.Fatalf("job: %v", job)
	}
	res := job["result"].(map[string]any)
	draft := res["recipe_draft"].(map[string]any)
	ing := draft["ingredients"].([]any)[0].(map[string]any)
	// B5 widened: a numeric quantity_display is kept as its string.
	if draft["title"] != "Toast" || ing["quantity"] != "2" || ing["unit"] != "slices" || draft["servings"] != float64(2) {
		t.Fatalf("draft: %v", draft)
	}
	if p := res["pipeline"].(map[string]any); p["parser_strategy"] != "llm_fallback" || p["used_llm"] != true {
		t.Fatalf("pipeline: %v", p)
	}
	reqs := f.requests()
	if len(reqs) != 1 {
		t.Fatalf("%d LLM calls", len(reqs))
	}
	r := reqs[0]
	if r.Label != llm.LabelLive || *r.Temperature != 0 || *r.MaxTokens != 800 || r.ResponseFormat.Type != "json_object" ||
		!strings.HasPrefix(msgText(r.Messages[1]), "URL: https://example.com/toast\nTitle: Toast\nContent:\n") {
		t.Fatalf("request: %+v %q", r, msgText(r.Messages[1]))
	}
}

func TestWebviewImportLLMRetries(t *testing.T) {
	e := setup(t)
	h := tok(1, "")
	if err := e.m.settings.Set(e.ctx, SettingMaxRetries, int64(2), settings.Scope{}); err != nil {
		t.Fatal(err)
	}
	f := &fakeLLM{reply: func(llm.ChatRequest) (string, error) { return `{"error": "invalid"}`, nil }}
	e.m.LLM = f
	id := e.obj(t, http.StatusOK, http.MethodPost, "/recipes/parse-payload/async", map[string]any{"input": map[string]any{
		"source_type": "client_webview", "html_snippet": "<main><p>Just a blog post about my day.</p></main>",
	}}, h...)["id"].(string)
	job := e.waitJob(t, id, h)
	if job["status"] != "ERROR" || job["error_code"] != "llm_failed" {
		t.Fatalf("job: %v", job)
	}
	if n := len(f.requests()); n != 2 {
		t.Fatalf("llm_failed retries up to queue.max_retries: %d calls", n)
	}
	if e.count(t, `SELECT attempts FROM recipes_recipe_parse_jobs WHERE id = ?`, id) != 2 {
		t.Fatal("attempts")
	}
}
