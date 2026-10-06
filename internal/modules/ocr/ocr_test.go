package ocr

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/blob"
	"github.com/alexberardi/jarvis-server/internal/platform/db"
	"github.com/alexberardi/jarvis-server/internal/platform/module"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// --- fakes ---

type fakeAuth struct{ fail bool }

func (f fakeAuth) ValidateApp(_ context.Context, id, key string) (authn.App, bool, error) {
	if f.fail {
		return authn.App{}, false, errors.New("auth down")
	}
	return authn.App{ID: id}, id == "app" && key == "secret", nil
}
func (fakeAuth) ValidateNode(context.Context, string, string, string) (authn.NodeValidation, error) {
	return authn.NodeValidation{}, nil
}
func (fakeAuth) HouseholdRole(context.Context, int64, string) (authn.Role, bool, error) {
	return "", false, nil
}

// fakeEngine returns text (or err) and counts calls.
type fakeEngine struct {
	name  string
	text  string
	err   error
	down  bool
	calls atomic.Int32
}

func (f *fakeEngine) Name() string                   { return f.name }
func (f *fakeEngine) Available(context.Context) bool { return !f.down }
func (f *fakeEngine) Recognize(_ context.Context, img Image, o Options) (Result, error) {
	f.calls.Add(1)
	if f.err != nil {
		return Result{}, f.err
	}
	return Result{Text: f.text, Blocks: []Block{{Text: f.text, BBox: []float64{1, 2, 3, 4}, Confidence: 0.9}}}, nil
}

// rejectValidator marks any text containing "garbled" invalid.
type rejectValidator struct{}

func (rejectValidator) Validate(_ context.Context, text string) (bool, float64, string) {
	if strings.Contains(text, "garbled") {
		return false, 0.1, "looks garbled"
	}
	return true, 0.9, "reads fine"
}

type env struct {
	m   *Module
	h   http.Handler
	q   *queue.Queue
	ctx context.Context
}

func setup(t *testing.T, m *Module) *env {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "jarvis.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := db.Migrate(ctx, d, queue.MigrationModule, queue.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, d, "ocr", Migrations()); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	blobs, err := blob.NewFS(t.TempDir(), log)
	if err != nil {
		t.Fatal(err)
	}
	q := queue.New(d, log)
	q.PollInterval = 10 * time.Millisecond
	if m.Auth == nil {
		m.Auth = fakeAuth{}
	}
	if m.Engines == nil {
		m.Engines = []Engine{}
	}
	if m.Validator == nil {
		m.Validator = rejectValidator{}
	}
	mux := http.NewServeMux()
	m.Register(mux, module.Deps{DB: d, Log: log, Queue: q, Blobs: blobs})
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	q.Start(ctx)
	return &env{m: m, h: mux, q: q, ctx: ctx}
}

var appH = map[string]string{"X-Jarvis-App-Id": "app", "X-Jarvis-App-Key": "secret"}

func (e *env) do(t *testing.T, method, path string, body any, hdr map[string]string) (int, map[string]any) {
	t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	default:
		raw, _ := json.Marshal(b)
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func (e *env) setSetting(t *testing.T, key string, v any) {
	t.Helper()
	if err := e.m.settings.Set(e.ctx, key, v, settings.Scope{}); err != nil {
		t.Fatal(err)
	}
}

func pngB64() string { return base64.StdEncoding.EncodeToString(testPNG("HELLO JARVIS")) }

func img(b64 string) map[string]any {
	return map[string]any{"content_type": "image/png", "base64": b64}
}

func detailLoc(out map[string]any) []string {
	var locs []string
	list, _ := out["detail"].([]any)
	for _, e := range list {
		b, _ := json.Marshal(e.(map[string]any)["loc"])
		locs = append(locs, string(b))
	}
	return locs
}

// --- tests ---

func TestAppAuth(t *testing.T) {
	e := setup(t, &Module{})
	for _, path := range []string{"/v1/providers", "/v1/queue/status", "/v1/ocr/jobs/x"} {
		code, out := e.do(t, "GET", path, nil, nil)
		want := map[string]any{"error_code": "unauthorized", "error_message": "Missing or invalid app credentials"}
		if code != 401 || !jsonEq(out["detail"], want) {
			t.Fatalf("%s no creds: %d %v", path, code, out)
		}
		code, _ = e.do(t, "GET", path, nil, map[string]string{"X-Jarvis-App-Id": "app", "X-Jarvis-App-Key": "bad"})
		if code != 401 {
			t.Fatalf("%s bad key: %d (the legacy 503 is fixed)", path, code)
		}
	}
	// Auth before body validation.
	if code, _ := e.do(t, "POST", "/v1/ocr/batch", "{}", nil); code != 401 {
		t.Fatalf("batch without creds: %d", code)
	}
	down := setup(t, &Module{Auth: fakeAuth{fail: true}})
	code, out := down.do(t, "GET", "/v1/providers", nil, appH)
	if code != 503 || out["detail"].(map[string]any)["error_code"] != "auth_unavailable" {
		t.Fatalf("auth down: %d %v", code, out)
	}
}

func jsonEq(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

func TestProvidersFollowSettings(t *testing.T) {
	tess := &fakeEngine{name: EngineTesseract, text: "x"}
	av := &fakeEngine{name: EngineAppleVision, text: "x", down: true}
	llm := &fakeEngine{name: EngineLLMVision, text: "x"}
	e := setup(t, &Module{Engines: []Engine{tess, av, llm}})
	code, out := e.do(t, "GET", "/v1/providers", nil, appH)
	p := out["providers"].(map[string]any)
	if code != 200 || p["tesseract"] != true || p["apple_vision"] != false || p["llm_proxy_vision"] != false {
		t.Fatalf("defaults: %d %v", code, out)
	}
	if d := out["diagnostics"].(map[string]any); len(d) != 1 || d["tesseract"].(map[string]any)["reason"] != "ok" {
		t.Fatalf("diagnostics list only enabled engines: %v", d)
	}
	e.setSetting(t, "ocr.enable_apple_vision", true)
	e.setSetting(t, "ocr.enable_llm_proxy_vision", true)
	_, out = e.do(t, "GET", "/v1/providers", nil, appH)
	p = out["providers"].(map[string]any)
	d := out["diagnostics"].(map[string]any)
	if p["apple_vision"] != false || d["apple_vision"].(map[string]any)["reason"] != "unavailable" || p["llm_proxy_vision"] != true {
		t.Fatalf("enabled: %v", out)
	}
}

func TestBatchAuto(t *testing.T) {
	tess := &fakeEngine{name: EngineTesseract, text: "garbled ~~"}
	llm := &fakeEngine{name: EngineLLMVision, text: "Chocolate cake"}
	e := setup(t, &Module{Engines: []Engine{tess, llm}})
	e.setSetting(t, "ocr.enable_llm_proxy_vision", true)
	body := map[string]any{"images": []any{img(pngB64()), img(pngB64())}, "options": map[string]any{"return_boxes": false}}
	code, out := e.do(t, "POST", "/v1/ocr/batch", body, appH)
	if code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	meta := out["meta"].(map[string]any)
	if meta["provider_used"] != "llm_proxy_vision" || meta["total_images"] != float64(2) {
		t.Fatalf("garbled tesseract output falls through to the next engine: %v", out)
	}
	res := out["results"].([]any)[0].(map[string]any)
	if res["text"] != "Chocolate cake" || len(res["blocks"].([]any)) != 0 {
		t.Fatalf("result: %v", res)
	}
	if _, ok := res["meta"].(map[string]any)["duration_ms"]; !ok {
		t.Fatalf("meta.duration_ms: %v", res)
	}

	// Every engine fails validation: tesseract is the fallback, unvalidated.
	llm.err = errors.New("LLM down")
	code, out = e.do(t, "POST", "/v1/ocr/batch", body, appH)
	if code != 200 || out["meta"].(map[string]any)["provider_used"] != "tesseract" {
		t.Fatalf("fallback: %d %v", code, out)
	}
}

func TestBatchErrors(t *testing.T) {
	none := setup(t, &Module{})
	body := map[string]any{"images": []any{img(pngB64())}}
	code, out := none.do(t, "POST", "/v1/ocr/batch", body, appH)
	if code != 500 || out["detail"] != "Internal server error: No OCR providers available" {
		t.Fatalf("no engines: %d %v", code, out)
	}

	tess := &fakeEngine{name: EngineTesseract, text: "x"}
	e := setup(t, &Module{Engines: []Engine{tess}})
	cases := []struct {
		body   any
		code   int
		detail string
	}{
		{map[string]any{"images": []any{img(pngB64())}, "provider": "easyocr"}, 400,
			"Provider 'easyocr' is not enabled or available. Available providers: tesseract"},
		{map[string]any{"images": []any{img(pngB64()), img("abc")}}, 400, "Invalid base64 image data at index 1: Incorrect padding"},
	}
	for _, c := range cases {
		code, out := e.do(t, "POST", "/v1/ocr/batch", c.body, appH)
		if code != c.code || out["detail"] != c.detail {
			t.Fatalf("%v: %d %v", c.body, code, out)
		}
	}
	tess.down = true
	code, out = e.do(t, "POST", "/v1/ocr/batch", map[string]any{"images": []any{img(pngB64())}, "provider": "tesseract"}, appH)
	if code != 400 || out["detail"] != "Provider 'tesseract' is not available" {
		t.Fatalf("down: %d %v", code, out)
	}
	tess.down = false
	tess.err = errors.New("cannot decode image")
	code, out = e.do(t, "POST", "/v1/ocr/batch", map[string]any{"images": []any{img(pngB64())}, "provider": "tesseract"}, appH)
	if code != 422 || !strings.HasPrefix(out["detail"].(string), "Failed to process image 0 in batch: ") {
		t.Fatalf("image error: %d %v", code, out)
	}
	tess.err = errors.New("engine exploded")
	code, out = e.do(t, "POST", "/v1/ocr/batch", map[string]any{"images": []any{img(pngB64())}, "provider": "tesseract"}, appH)
	if code != 500 || out["detail"] != "Internal server error: engine exploded" {
		t.Fatalf("engine error: %d %v", code, out)
	}
}

func TestValidation(t *testing.T) {
	e := setup(t, &Module{})
	many := make([]any, 101)
	for i := range many {
		many[i] = img("x")
	}
	cases := []struct {
		path string
		body any
		loc  string
	}{
		{"/v1/ocr", map[string]any{}, `["body","image"]`},
		{"/v1/ocr", map[string]any{"image": map[string]any{"content_type": "image/png"}}, `["body","image","base64"]`},
		{"/v1/ocr", map[string]any{"image": img("x"), "provider": "bogus"}, `["body","provider"]`},
		{"/v1/ocr", map[string]any{"image": img("x"), "options": map[string]any{"mode": "page"}}, `["body","options","mode"]`},
		{"/v1/ocr", map[string]any{"image": img("x"), "options": map[string]any{"return_boxes": "maybe"}}, `["body","options","return_boxes"]`},
		{"/v1/ocr", "not json", `["body",0]`},
		{"/v1/ocr/batch", map[string]any{}, `["body","images"]`},
		{"/v1/ocr/batch", map[string]any{"images": []any{}}, `["body","images"]`},
		{"/v1/ocr/batch", map[string]any{"images": many}, `["body","images"]`},
		{"/v1/ocr/batch", map[string]any{"images": []any{map[string]any{"content_type": "x"}}}, `["body","images",0,"base64"]`},
		{"/v1/ocr/jobs", map[string]any{"images": []any{img(pngB64())}, "callback_url": "ftp://x"}, `["body","callback_url"]`},
		{"/v1/ocr/jobs", map[string]any{"images": make([]any, 9)}, `["body","images"]`},
	}
	for _, c := range cases {
		code, out := e.do(t, "POST", c.path, c.body, appH)
		if code != 422 || !strings.Contains(strings.Join(detailLoc(out), " "), c.loc) {
			t.Fatalf("%s %v: %d %v", c.path, c.body, code, out)
		}
	}
	// The 422 provider message lists the legacy literal.
	_, out := e.do(t, "POST", "/v1/ocr", map[string]any{"image": img("x"), "provider": "bogus"}, appH)
	msg := out["detail"].([]any)[0].(map[string]any)["msg"]
	if msg != "Input should be 'auto', 'tesseract', 'easyocr', 'paddleocr', 'rapidocr', 'apple_vision', 'llm_proxy_vision' or 'llm_proxy_cloud'" {
		t.Fatalf("msg: %v", msg)
	}
	if code, out := e.do(t, "GET", "/v1/ocr/jobs/nope", nil, appH); code != 404 || out["detail"] != "Job not found" {
		t.Fatalf("unknown job: %d %v", code, out)
	}
}

func (e *env) waitJob(t *testing.T, id string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, out := e.do(t, "GET", "/v1/ocr/jobs/"+id, nil, appH)
		if s := out["status"]; s == "completed" || s == "failed" {
			return out
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s stuck: %v", id, out)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSubmitJob(t *testing.T) {
	tess := &fakeEngine{name: EngineTesseract, text: "  Two   cups\r\n\n\n\nflour  "}
	e := setup(t, &Module{Engines: []Engine{tess}})
	e.setSetting(t, "ocr.max_text_bytes", int64(8))
	code, out := e.do(t, "POST", "/v1/ocr", map[string]any{"image": img(pngB64())}, appH)
	if code != 200 || out["status"] != "pending" || !strings.HasSuffix(out["created_at"].(string), "Z") {
		t.Fatalf("submit: %d %v", code, out)
	}
	id := out["job_id"].(string)
	final := e.waitJob(t, id)
	res := final["result"].(map[string]any)
	if final["status"] != "completed" || final["error"] != nil || final["updated_at"] == nil || final["created_at"] != out["created_at"] {
		t.Fatalf("final: %v", final)
	}
	// Normalized ("Two cups\n\nflour") then cut to 8 bytes.
	if res["provider_used"] != "tesseract" || res["text"] != "Two cups" || res["meta"].(map[string]any)["truncated"] != true {
		t.Fatalf("result: %v", res)
	}
	if len(res["blocks"].([]any)) != 1 {
		t.Fatalf("return_boxes defaults to true: %v", res)
	}
	// The image is gone once the job finished.
	infos, _ := e.m.deps.Blobs.List(e.ctx, jobPrefix+id+"/")
	if len(infos) != 1 || !strings.HasSuffix(infos[0].Key, "job.json") {
		t.Fatalf("blobs left: %v", infos)
	}

	// No tier yields valid text: failed, with the reason.
	tess.text = "garbled"
	_, out = e.do(t, "POST", "/v1/ocr", map[string]any{"image": img(pngB64())}, appH)
	final = e.waitJob(t, out["job_id"].(string))
	if final["status"] != "failed" || final["error"] != "looks garbled" || final["result"] != nil {
		t.Fatalf("failed job: %v", final)
	}

	// Tiers follow ocr.enabled_tiers.
	tess.text = "fine"
	e.setSetting(t, "ocr.enabled_tiers", "llm_local")
	_, out = e.do(t, "POST", "/v1/ocr", map[string]any{"image": img(pngB64())}, appH)
	if final = e.waitJob(t, out["job_id"].(string)); final["error"] != "All tiers failed validation" {
		t.Fatalf("tiers: %v", final)
	}

	code, out = e.do(t, "GET", "/v1/queue/status", nil, appH)
	if code != 200 || out["redis_connected"] != true || out["queue_name"] != "jarvis.ocr.jobs" || out["queue_length"] != float64(0) {
		t.Fatalf("queue status: %v", out)
	}
}

// callbackSink records callback POSTs, failing the first n.
type callbackSink struct {
	mu      sync.Mutex
	failN   int
	bodies  []map[string]any
	headers []http.Header
	got     chan struct{}
}

func (c *callbackSink) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failN > 0 {
		c.failN--
		w.WriteHeader(500)
		return
	}
	var b map[string]any
	json.NewDecoder(r.Body).Decode(&b)
	c.bodies = append(c.bodies, b)
	c.headers = append(c.headers, r.Header.Clone())
	w.WriteHeader(204)
	c.got <- struct{}{}
}

func TestFlowJobs(t *testing.T) {
	sink := &callbackSink{failN: 1, got: make(chan struct{}, 4)}
	srv := httptest.NewServer(sink)
	defer srv.Close()
	tess := &fakeEngine{name: EngineTesseract, text: "Pancakes\n1 cup flour"}
	e := setup(t, &Module{Engines: []Engine{tess}, AppID: "jarvisd", AppKey: "k"})

	// JSON, with a PDF that is rejected per image.
	code, out := e.do(t, "POST", "/v1/ocr/jobs", map[string]any{
		"images":       []any{img(pngB64()), map[string]any{"content_type": "application/pdf", "base64": "JVBERi0="}},
		"options":      map[string]any{"language": "en"},
		"callback_url": srv.URL + "/ocr/callback", "workflow_id": "wf-1", "parent_job_id": "recipe-job-1",
		"request_id": "req-1", "source": "jarvis-recipes-server",
	}, appH)
	if code != 202 || out["status"] != "pending" {
		t.Fatalf("submit: %d %v", code, out)
	}
	id := out["job_id"].(string)
	final := e.waitJob(t, id)
	payload := final["result"].(map[string]any)
	results := payload["results"].([]any)
	if final["status"] != "completed" || payload["status"] != "success" || len(results) != 2 {
		t.Fatalf("final: %v", final)
	}
	r0, r1 := results[0].(map[string]any), results[1].(map[string]any)
	if r0["ocr_text"] != "Pancakes\n1 cup flour" || r0["error"] != nil || r0["meta"].(map[string]any)["tier"] != "tesseract" {
		t.Fatalf("image 0: %v", r0)
	}
	if r1["error"].(map[string]any)["code"] != "unsupported_media" || r1["meta"].(map[string]any)["is_valid"] != false {
		t.Fatalf("image 1: %v", r1)
	}
	if !jsonEq(payload["error"], map[string]any{"code": nil, "message": nil}) {
		t.Fatalf("payload error on success: %v", payload["error"])
	}

	select {
	case <-sink.got:
	case <-time.After(30 * time.Second):
		t.Fatal("no callback after a retry")
	}
	sink.mu.Lock()
	msg, hdr := sink.bodies[0], sink.headers[0]
	sink.mu.Unlock()
	if hdr.Get("X-Jarvis-App-Id") != "jarvisd" || hdr.Get("X-Jarvis-App-Key") != "k" {
		t.Fatalf("callback creds: %v", hdr)
	}
	if msg["job_type"] != "ocr.completed" || msg["schema_version"] != float64(1) || msg["workflow_id"] != "wf-1" ||
		msg["target"] != "jarvis-recipes-server" || msg["ocr_job_id"] != id ||
		!jsonEq(msg["trace"], map[string]any{"parent_job_id": "recipe-job-1", "request_id": "req-1"}) ||
		!jsonEq(msg["payload"], payload) {
		t.Fatalf("callback message: %v", msg)
	}

	// Multipart, all images unreadable: completed job, payload failed.
	tess.text = "garbled"
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("images", "page1.png")
	fw.Write(testPNG("HI"))
	mw.WriteField("options", `{"language":"en"}`)
	mw.WriteField("workflow_id", "wf-2")
	mw.Close()
	req := httptest.NewRequest("POST", "/v1/ocr/jobs", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	for k, v := range appH {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != 202 {
		t.Fatalf("multipart: %d %s", rec.Code, rec.Body)
	}
	json.Unmarshal(rec.Body.Bytes(), &out)
	final = e.waitJob(t, out["job_id"].(string))
	payload = final["result"].(map[string]any)
	if payload["status"] != "failed" || payload["error"].(map[string]any)["code"] != "ocr_no_valid_output" {
		t.Fatalf("multipart failed payload: %v", final)
	}
	if r := payload["results"].([]any)[0].(map[string]any); r["error"].(map[string]any)["code"] != "ocr_no_valid_output" {
		t.Fatalf("multipart image: %v", r)
	}
}

func TestPurge(t *testing.T) {
	e := setup(t, &Module{Engines: []Engine{&fakeEngine{name: EngineTesseract, text: "ok"}}})
	_, out := e.do(t, "POST", "/v1/ocr", map[string]any{"image": img(pngB64())}, appH)
	id := out["job_id"].(string)
	e.waitJob(t, id)
	e.m.now = func() time.Time { return time.Now().Add(23 * time.Hour) }
	e.m.runPurge(e.ctx, queue.Job{})
	if code, _ := e.do(t, "GET", "/v1/ocr/jobs/"+id, nil, appH); code != 200 {
		t.Fatal("purged too early")
	}
	e.m.now = func() time.Time { return time.Now().Add(25 * time.Hour) }
	e.m.runPurge(e.ctx, queue.Job{})
	if code, _ := e.do(t, "GET", "/v1/ocr/jobs/"+id, nil, appH); code != 404 {
		t.Fatal("not purged after 24 h")
	}
}

func TestHelpers(t *testing.T) {
	for in, want := range map[string]string{"aGk=": "hi", "aG\nk=": "hi", "a!G?k=": "hi"} {
		if got, err := pyB64Decode(in); err != nil || string(got) != want {
			t.Fatalf("pyB64Decode(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := pyB64Decode("abc"); err == nil || err.Error() != "Incorrect padding" {
		t.Fatalf("padding: %v", err)
	}
	if got := normalizeText("\x00 a   b \r\n\r\n\r\n\nc  "); got != "a b\n\nc" {
		t.Fatalf("normalize: %q", got)
	}
	if s, cut := truncateBytes("héllo", 2); s != "h" || !cut {
		t.Fatalf("truncate mid-rune: %q %v", s, cut)
	}
	if s, cut := truncateBytes("abc", 5); s != "abc" || cut {
		t.Fatalf("truncate short: %q %v", s, cut)
	}
	if tessLang(nil) != "eng" || tessLang([]string{"EN", "fr", "xx", "de"}) != "eng+fra+xx" {
		t.Fatalf("tessLang: %s", tessLang([]string{"EN", "fr", "xx", "de"}))
	}
	tsv := "level\tpage_num\tblock_num\tpar_num\tline_num\tword_num\tleft\ttop\twidth\theight\tconf\ttext\n" +
		"1\t1\t0\t0\t0\t0\t0\t0\t100\t50\t-1\t\n" +
		"5\t1\t1\t1\t1\t1\t10\t20\t30\t40\t96.5\tHELLO\n"
	b := parseTSV([]byte(tsv))
	if len(b) != 1 || b[0].Text != "HELLO" || b[0].Confidence != 0.965 || !jsonEq(b[0].BBox, []float64{10, 20, 30, 40}) {
		t.Fatalf("tsv: %+v", b)
	}
}

func TestAppleVisionEngine(t *testing.T) {
	var status atomic.Int32
	status.Store(200)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer key" {
			w.WriteHeader(401)
			return
		}
		if s := int(status.Load()); s != 200 {
			w.WriteHeader(s)
			return
		}
		if r.URL.Path == "/health" {
			io.WriteString(w, `{"status":"ok"}`)
			return
		}
		var in map[string]any
		json.NewDecoder(r.Body).Decode(&in)
		if in["image_base64"] == "" || !jsonEq(in["language_hints"], []string{"en"}) {
			w.WriteHeader(400)
			return
		}
		io.WriteString(w, `{"text":"Hi\nthere","bbox_format":"normalized_xywh_topleft","blocks":[{"text":"Hi","bbox":[0.5,0.5,0.25,0.25],"confidence":0.8}]}`)
	}))
	defer srv.Close()
	a := &AppleVision{URL: srv.URL, Key: "key"}
	ctx := context.Background()
	if !a.Available(ctx) {
		t.Fatalf("probe: %+v", a.Diagnose(ctx))
	}
	data := testPNG("HI")
	w, h, _ := imageSize(data)
	r, err := a.Recognize(ctx, Image{Data: data, ContentType: "image/png"}, Options{ReturnBoxes: true})
	if err != nil || r.Text != "Hi\nthere" || !jsonEq(r.Blocks[0].BBox, []float64{float64(w) / 2, float64(h) / 2, float64(w) / 4, float64(h) / 4}) {
		t.Fatalf("recognize: %+v %v", r, err)
	}
	bad := &AppleVision{URL: srv.URL, Key: "wrong"}
	if d := bad.Diagnose(ctx); d.Available || d.Reason != "auth_failed" {
		t.Fatalf("auth: %+v", d)
	}
	gone := &AppleVision{URL: "http://127.0.0.1:1", Key: "key"}
	if d := gone.Diagnose(ctx); d.Reason != "unreachable" {
		t.Fatalf("unreachable: %+v", d)
	}
	if d := (&AppleVision{}).Diagnose(ctx); d.Reason != "not_configured" {
		t.Fatalf("unconfigured: %+v", d)
	}
}

func TestLLMVisionEngine(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Model    string `json:"model"`
			Messages []struct {
				Content []map[string]any `json:"content"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		url := in.Messages[0].Content[1]["image_url"].(map[string]any)["url"].(string)
		if r.Header.Get("X-Jarvis-App-Id") != "id" || in.Model != "background" || !strings.HasPrefix(url, "data:image/jpeg;base64,") {
			w.WriteHeader(400)
			return
		}
		io.WriteString(w, `{"choices":[{"message":{"content":" {\"page1\": {\"text\": \"Soup\"}} "}}]}`)
	}))
	defer srv.Close()
	l := &LLMVision{URL: srv.URL, AppID: "id", AppKey: "k"}
	r, err := l.Recognize(context.Background(), Image{Data: testPNG("HI"), ContentType: "image/jpeg"}, Options{ReturnBoxes: true})
	if err != nil || r.Text != "Soup" || len(r.Blocks) != 1 || r.Blocks[0].Confidence != 0.95 {
		t.Fatalf("%+v %v", r, err)
	}
}

// TestTesseractEngine runs the real binary when it is installed.
func TestTesseractEngine(t *testing.T) {
	path, err := exec.LookPath("tesseract")
	if err != nil {
		t.Skip("tesseract not on PATH")
	}
	r, err := (&Tesseract{Path: path}).Recognize(context.Background(), Image{Data: testPNG("HELLO JARVIS")}, Options{ReturnBoxes: true})
	if err != nil || !strings.Contains(r.Text, "HELLO") || len(r.Blocks) < 2 {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := (&Tesseract{Path: path}).Recognize(context.Background(), Image{Data: []byte("not an image")}, Options{}); err == nil {
		t.Fatal("garbage input should fail")
	}
}

// --- test image: a 5x7 bitmap font with just the letters used ---

var glyphs = map[rune][7]string{
	'H': {"10001", "10001", "10001", "11111", "10001", "10001", "10001"},
	'E': {"11111", "10000", "10000", "11110", "10000", "10000", "11111"},
	'L': {"10000", "10000", "10000", "10000", "10000", "10000", "11111"},
	'O': {"01110", "10001", "10001", "10001", "10001", "10001", "01110"},
	'J': {"00111", "00010", "00010", "00010", "00010", "10010", "01100"},
	'A': {"01110", "10001", "10001", "11111", "10001", "10001", "10001"},
	'R': {"11110", "10001", "10001", "11110", "10100", "10010", "10001"},
	'V': {"10001", "10001", "10001", "01010", "01010", "00100", "00100"},
	'I': {"01110", "00100", "00100", "00100", "00100", "00100", "01110"},
	'S': {"01111", "10000", "10000", "01110", "00001", "00001", "11110"},
	' ': {"00000", "00000", "00000", "00000", "00000", "00000", "00000"},
}

func testPNG(text string) []byte {
	const scale, margin = 6, 24
	w := margin*2 + len(text)*6*scale
	h := margin*2 + 7*scale
	im := image.NewGray(image.Rect(0, 0, w, h))
	for i := range im.Pix {
		im.Pix[i] = 0xff
	}
	for ci, ch := range text {
		g := glyphs[ch]
		for row := 0; row < 7; row++ {
			for col := 0; col < 5; col++ {
				if g[row][col] != '1' {
					continue
				}
				x0, y0 := margin+(ci*6+col)*scale, margin+row*scale
				for y := y0; y < y0+scale; y++ {
					for x := x0; x < x0+scale; x++ {
						im.SetGray(x, y, color.Gray{})
					}
				}
			}
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, im)
	return buf.Bytes()
}
