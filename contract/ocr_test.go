//go:build contract

package contract

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// jarvis-ocr-service (legacy port 7031). Every route except /health and /settings takes app
// credentials (X-Jarvis-App-Id/Key), checked by the service's own app/auth.py.
//
// Routes: GET /v1/providers, GET /v1/queue/status, POST /v1/ocr (enqueue one image, answers
// {job_id, status, created_at}), GET /v1/ocr/jobs/{job_id} (poll), POST /v1/ocr/batch
// (synchronous, what jarvis-recipes-server's ingest pipeline calls) and the shared /settings
// router.
//
// Engines: whichever the target has. The MBP has none available (no tesseract binary, Apple
// Vision and LLM vision disabled), so there the image tests take the "no engine" branch; a
// target with an engine (jarvisd with tesseract on PATH) takes the recognition branch, which
// reads text out of a generated PNG (ocrTestPNG).
//
// Leftovers: on Python, POST /v1/ocr writes a Redis key ocr_job:<uuid> with a 24 h TTL and
// pushes a message the worker drops (see TestOCRJobFlow). There is no delete route; the TTL
// removes it.

var ocrAuthError = Obj{"detail": Obj{
	"error_code":    Eq("unauthorized"),
	"error_message": Eq("Missing or invalid app credentials"),
}}

var ocrAuthUnavailable = Obj{"detail": Obj{
	"error_code":    Eq("auth_unavailable"),
	"error_message": Eq("Auth service unavailable"),
}}

var ocrBlockShape = Obj{"text": String, "bbox": ArrayOf(Num), "confidence": Num}

var ocrResponseShape = Obj{
	"provider_used": NonEmptyString,
	"text":          String,
	"blocks":        ArrayOf(ocrBlockShape),
	"meta":          Object,
}

var ocrJobCreatedShape = Obj{"job_id": UUID, "status": Eq("pending"), "created_at": TimestampUTC}

var ocrJobStatusShape = Obj{
	"job_id":     UUID,
	"status":     OneOf("pending", "processing", "completed", "failed"),
	"created_at": TimestampUTC,
	"updated_at": NullOr(TimestampUTC),
	"result":     NullOr(ocrResponseShape),
	"error":      NullOr(String),
}

// The engines jarvisd keeps (PLAN §3.3) are always listed; cut ones may be listed as false.
var ocrProvidersShape = Obj{
	"providers": All(MapOf(Bool), Open{"tesseract": Bool, "apple_vision": Bool, "llm_proxy_vision": Bool}),
	"diagnostics": MapOf(Open{
		"provider":  NonEmptyString,
		"available": Bool,
		"reason":    OneOf("ok", "unavailable", "unreachable", "auth_failed", "not_configured", "probe_error", "unhealthy", "unknown"),
		"detail":    NullOr(String),
	}),
}

func ocrImage(b64 string) map[string]any {
	return map[string]any{"content_type": "image/png", "base64": b64}
}

// ocrProviders returns the target's provider availability map.
func ocrProviders(t *testing.T, app *App) map[string]any {
	t.Helper()
	return T(t).Get(t, OCR, "/v1/providers", app.H()).Expect(http.StatusOK, ocrProvidersShape).Object()["providers"].(map[string]any)
}

// ocrTextEngine reports whether a provider that reads text from pixels (not an LLM, which may
// paraphrase) is available.
func ocrTextEngine(p map[string]any) bool {
	return p["tesseract"] == true || p["apple_vision"] == true
}

func ocrAnyEngine(p map[string]any) bool {
	for _, v := range p {
		if v == true {
			return true
		}
	}
	return false
}

func TestOCRAuth(t *testing.T) {
	tg := T(t)
	tg.Need(t, OCR)
	app := SharedApp(t)
	routes := []struct {
		method, path string
		body         any
	}{
		{"GET", "/v1/providers", nil},
		{"GET", "/v1/queue/status", nil},
		{"POST", "/v1/ocr", map[string]any{}},
		{"GET", "/v1/ocr/jobs/contract-no-such-job", nil},
		{"POST", "/v1/ocr/batch", map[string]any{}},
	}
	for _, rt := range routes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			// Auth runs before body validation: an empty body still answers 401.
			tg.Do(t, OCR, rt.method, rt.path, rt.body).Expect(http.StatusUnauthorized, ocrAuthError)
			tg.Do(t, OCR, rt.method, rt.path, rt.body, H{"X-Jarvis-App-Id": app.ID}).Expect(http.StatusUnauthorized, ocrAuthError)
			tg.Do(t, OCR, rt.method, rt.path, rt.body, H{"X-Jarvis-App-Key": app.Key}).Expect(http.StatusUnauthorized, ocrAuthError)
			tg.Do(t, OCR, rt.method, rt.path, rt.body, SharedUser(t).H()).Expect(http.StatusUnauthorized, ocrAuthError)

			// A fresh wrong key (the service caches results per id+key pair).
			bad := H{"X-Jarvis-App-Id": app.ID, "X-Jarvis-App-Key": "contract-wrong-" + randHex(6)}
			first := tg.Do(t, OCR, rt.method, rt.path, rt.body, bad)
			if Jarvisd() {
				first.Expect(http.StatusUnauthorized, ocrAuthError)
			} else {
				// LEGACY-BUG: verify_app_auth raises its 401 inside a try whose
				// `except Exception` turns it into 503 auth_unavailable. Only the cached
				// failure (10 s TTL), served outside the try, is the intended 401.
				first.Expect(http.StatusServiceUnavailable, ocrAuthUnavailable)
			}
			tg.Do(t, OCR, rt.method, rt.path, rt.body, bad).Expect(http.StatusUnauthorized, ocrAuthError)
		})
	}
}

func TestOCRProviders(t *testing.T) {
	tg := T(t)
	tg.Need(t, OCR)
	app := SharedApp(t)
	obj := tg.Get(t, OCR, "/v1/providers", app.H()).Expect(http.StatusOK, ocrProvidersShape).Object()
	for name, d := range obj["diagnostics"].(map[string]any) {
		dm := d.(map[string]any)
		if dm["provider"] != name {
			t.Fatalf("diagnostics[%s].provider = %v", name, dm["provider"])
		}
		if (dm["reason"] == "ok") != (dm["available"] == true) {
			t.Fatalf("diagnostics[%s]: reason %v disagrees with available %v", name, dm["reason"], dm["available"])
		}
	}
}

func TestOCRQueueStatus(t *testing.T) {
	tg := T(t)
	tg.Need(t, OCR)
	tg.Get(t, OCR, "/v1/queue/status", SharedApp(t).H()).Expect(http.StatusOK, Obj{
		"redis_connected": Eq(true),
		"queue_length":    Int,
		"workers_active":  Int,
		"queue_name":      Eq("jarvis.ocr.jobs"),
		"redis_info":      Obj{"host": String, "port": Int, "version": String},
	})
}

func TestOCRSubmitValidation(t *testing.T) {
	tg := T(t)
	tg.Need(t, OCR)
	h := SharedApp(t).H()
	cases := []struct {
		name string
		body any
		loc  []any
	}{
		{"no image", map[string]any{}, []any{"body", "image"}},
		{"no base64", map[string]any{"image": map[string]any{"content_type": "image/png"}}, []any{"body", "image", "base64"}},
		{"no content_type", map[string]any{"image": map[string]any{"base64": "x"}}, []any{"body", "image", "content_type"}},
		{"unknown provider", map[string]any{"image": ocrImage("x"), "provider": "contract-bogus"}, []any{"body", "provider"}},
		{"bad mode", map[string]any{"image": ocrImage("x"), "options": map[string]any{"mode": "page"}}, []any{"body", "options", "mode"}},
		{"not json", "not json", []any{"body", 0}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tg.Post(t, OCR, "/v1/ocr", c.body, h).ExpectStatus(http.StatusUnprocessableEntity).ExpectShape(ValidationError(c.loc...))
		})
	}
	tg.Get(t, OCR, "/v1/ocr/jobs/contract-no-such-job", h).ExpectError(http.StatusNotFound, "Job not found")
	tg.Get(t, OCR, "/v1/ocr/jobs/00000000-0000-4000-8000-000000000000", h).ExpectError(http.StatusNotFound, "Job not found")
}

// TestOCRJobFlow: POST /v1/ocr enqueues and answers at once; GET /v1/ocr/jobs/{id} polls.
func TestOCRJobFlow(t *testing.T) {
	tg := T(t)
	tg.Need(t, OCR)
	app := SharedApp(t)
	engine := ocrAnyEngine(ocrProviders(t, app))

	created := tg.Post(t, OCR, "/v1/ocr", map[string]any{
		"document_id": "contract-" + tg.RunID,
		"image":       ocrImage(ocrTestPNGBase64()),
		"options":     map[string]any{"language_hints": []string{"en"}, "return_boxes": true, "mode": "document"},
	}, app.H(), H{"X-Correlation-ID": "contract-" + tg.RunID}).Expect(http.StatusOK, ocrJobCreatedShape).Object()
	id := created["job_id"].(string)
	path := "/v1/ocr/jobs/" + id

	first := tg.Get(t, OCR, path, app.H()).Expect(http.StatusOK, ocrJobStatusShape).Object()
	if first["job_id"] != id || first["created_at"] != created["created_at"] {
		t.Fatalf("job status disagrees with the submit answer: %v vs %v", first, created)
	}

	if !Jarvisd() {
		// LEGACY-BUG: the endpoint enqueues {job_id, request}, but worker.py validates the
		// queue-flow v1 envelope (schema_version, workflow_id, payload.image_refs, reply_to)
		// and drops anything else as bad_request, silently (no reply_to). The job stays
		// "pending" until its 24 h TTL. PLAN §2: "Fix the broken POST /v1/ocr job path".
		time.Sleep(3 * time.Second)
		tg.Get(t, OCR, path, app.H()).Expect(http.StatusOK, Open{
			"status": Eq("pending"), "updated_at": Null, "result": Null, "error": Null,
		})
		return
	}

	// jarvisd runs the job: it ends "completed" with an OCRResponse, or "failed" with an error.
	deadline := time.Now().Add(slowTimeout())
	var final map[string]any
	for {
		final = tg.Get(t, OCR, path, app.H()).Expect(http.StatusOK, ocrJobStatusShape).Object()
		if s := final["status"]; s == "completed" || s == "failed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s still %v after %s", id, final["status"], slowTimeout())
		}
		time.Sleep(250 * time.Millisecond)
	}
	if final["updated_at"] == nil {
		t.Fatalf("a finished job has updated_at: %v", final)
	}
	switch {
	case final["status"] == "completed":
		if final["result"] == nil || final["error"] != nil {
			t.Fatalf("completed job: want result and no error, got %v", final)
		}
		res := final["result"].(map[string]any)
		if _, ok := res["meta"].(map[string]any)["duration_ms"]; !ok {
			t.Fatalf("result.meta has duration_ms: %v", res["meta"])
		}
	case engine:
		t.Fatalf("job failed although an engine is available: %v", final["error"])
	default:
		if final["result"] != nil || final["error"] == nil || final["error"] == "" {
			t.Fatalf("failed job: want an error and no result, got %v", final)
		}
	}
}

func TestOCRBatchValidation(t *testing.T) {
	tg := T(t)
	tg.Need(t, OCR)
	h := SharedApp(t).H()
	tooMany := make([]any, 101)
	for i := range tooMany {
		tooMany[i] = ocrImage("x")
	}
	cases := []struct {
		name string
		body any
		loc  []any
	}{
		{"no images", map[string]any{}, []any{"body", "images"}},
		{"empty images", map[string]any{"images": []any{}}, []any{"body", "images"}},
		{"101 images", map[string]any{"images": tooMany}, []any{"body", "images"}},
		{"image without base64", map[string]any{"images": []any{map[string]any{"content_type": "image/png"}}}, []any{"body", "images", 0, "base64"}},
		{"unknown provider", map[string]any{"images": []any{ocrImage("x")}, "provider": "contract-bogus"}, []any{"body", "provider"}},
		{"bad mode", map[string]any{"images": []any{ocrImage("x")}, "options": map[string]any{"mode": "page"}}, []any{"body", "options", "mode"}},
		{"not json", "not json", []any{"body", 0}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tg.Post(t, OCR, "/v1/ocr/batch", c.body, h).ExpectStatus(http.StatusUnprocessableEntity).ExpectShape(ValidationError(c.loc...))
		})
	}

	// Base64 is decoded before any provider runs: a bad one is a 400 naming the image.
	r := tg.Post(t, OCR, "/v1/ocr/batch", map[string]any{"images": []any{ocrImage(ocrTestPNGBase64()), ocrImage("abc")}}, h).
		ExpectStatus(http.StatusBadRequest)
	if d, _ := r.Object()["detail"].(string); !strings.HasPrefix(d, "Invalid base64 image data at index 1: ") {
		r.Fatalf("detail: want the 'Invalid base64 image data at index 1: ' prefix")
	}

	// A provider that is not enabled (EasyOCR is off everywhere; jarvisd cut it).
	r = tg.Post(t, OCR, "/v1/ocr/batch", map[string]any{"images": []any{ocrImage(ocrTestPNGBase64())}, "provider": "easyocr"}, h).
		ExpectStatus(http.StatusBadRequest)
	if d, _ := r.Object()["detail"].(string); !strings.HasPrefix(d, "Provider 'easyocr' is not enabled or available. Available providers: ") {
		r.Fatalf("detail: want the 'Provider 'easyocr' is not enabled or available. Available providers: ' prefix")
	}
}

func TestOCRBatch(t *testing.T) {
	tg := T(t)
	tg.Need(t, OCR)
	app := SharedApp(t)
	providers := ocrProviders(t, app)
	png := ocrTestPNGBase64()
	body := func(provider string, n int) map[string]any {
		imgs := make([]any, n)
		for i := range imgs {
			imgs[i] = ocrImage(png)
		}
		return map[string]any{
			"provider": provider, "images": imgs,
			"options": map[string]any{"language_hints": []string{"en"}, "return_boxes": false, "mode": "document"},
		}
	}

	if !ocrAnyEngine(providers) {
		// No engine: auto ends in an unhandled error, flattened to 500.
		r := tg.Post(t, OCR, "/v1/ocr/batch", body("auto", 1), app.H()).ExpectStatus(http.StatusInternalServerError)
		if d, _ := r.Object()["detail"].(string); !strings.HasPrefix(d, "Internal server error") {
			r.Fatalf("detail: want an 'Internal server error' prefix")
		}
		// A named provider that is not available is a 400.
		r = tg.Post(t, OCR, "/v1/ocr/batch", body("tesseract", 1), app.H()).ExpectStatus(http.StatusBadRequest)
		if d, _ := r.Object()["detail"].(string); !strings.HasPrefix(d, "Provider 'tesseract' is not") {
			r.Fatalf("detail: want a 'Provider 'tesseract' is not' prefix")
		}
		return
	}

	batchShape := Obj{
		"results": ArrayOf(ocrResponseShape),
		"meta":    Obj{"total_images": Int, "total_duration_ms": Num, "provider_used": NonEmptyString},
	}
	obj := tg.SlowJSON(t, OCR, "/v1/ocr/batch", body("auto", 2), app.H()).Expect(http.StatusOK, batchShape).Object()
	results := obj["results"].([]any)
	meta := obj["meta"].(map[string]any)
	if len(results) != 2 || mustFloat(meta["total_images"]) != 2 {
		t.Fatalf("one result per image, in order: %v", obj)
	}
	for i, res := range results {
		m := res.(map[string]any)
		if m["provider_used"] != meta["provider_used"] {
			t.Fatalf("results[%d].provider_used %v != meta.provider_used %v", i, m["provider_used"], meta["provider_used"])
		}
		if _, ok := m["meta"].(map[string]any)["duration_ms"]; !ok {
			t.Fatalf("results[%d].meta has duration_ms: %v", i, m["meta"])
		}
		if ocrTextEngine(providers) && !strings.Contains(strings.ToUpper(m["text"].(string)), "HELLO") {
			t.Fatalf("results[%d].text %q: want it to read HELLO", i, m["text"])
		}
	}

	if providers["tesseract"] == true {
		// A named provider, with boxes: the boxes carry the words.
		b := body("tesseract", 1)
		b["options"].(map[string]any)["return_boxes"] = true
		obj := tg.SlowJSON(t, OCR, "/v1/ocr/batch", b, app.H()).Expect(http.StatusOK, batchShape).Object()
		res := obj["results"].([]any)[0].(map[string]any)
		if res["provider_used"] != "tesseract" || len(res["blocks"].([]any)) == 0 {
			t.Fatalf("tesseract with return_boxes: want provider tesseract and blocks, got %v", res)
		}
		for _, bl := range res["blocks"].([]any) {
			if n := len(bl.(map[string]any)["bbox"].([]any)); n != 4 {
				t.Fatalf("bbox is [x, y, w, h]: %v", bl)
			}
		}
	}
}

// EnvOCRCallbackHost is an address the target can reach this test process at (jarvisd's
// POST /v1/ocr/jobs callback test). Without it the callback subtest skips.
const EnvOCRCallbackHost = "JARVIS_CONTRACT_CALLBACK_HOST"

var ocrImageResultShape = Obj{
	"index":     Int,
	"ocr_text":  String,
	"truncated": Bool,
	"meta": Obj{
		"language": String, "confidence": Num, "text_len": Int, "is_valid": Bool, "tier": String,
		"validation_reason": NullOr(String),
	},
	"error": NullOr(Obj{"code": NonEmptyString, "message": String}),
}

var ocrCompletionPayloadShape = Obj{
	"status":       OneOf("success", "failed"),
	"results":      ArrayOf(ocrImageResultShape),
	"artifact_ref": Null,
	"error":        Obj{"code": NullOr(String), "message": NullOr(String)},
}

// TestOCRFlowJobs covers POST /v1/ocr/jobs, jarvisd's replacement for the Redis queue-flow
// handoff (jarvis-recipes-server LPUSHed a v1 envelope to jarvis.ocr.jobs and got an RQ job
// back). The Python service has no such route, so this is jarvisd-only (docs/schema/ocr.md).
func TestOCRFlowJobs(t *testing.T) {
	if !Jarvisd() {
		t.Skip("jarvisd-only: POST /v1/ocr/jobs replaces the legacy Redis queue-flow handoff")
	}
	tg := T(t)
	tg.Need(t, OCR)
	app := SharedApp(t)
	text := ocrTextEngine(ocrProviders(t, app))
	png := ocrTestPNGBase64()

	tg.Post(t, OCR, "/v1/ocr/jobs", map[string]any{}).Expect(http.StatusUnauthorized, ocrAuthError)
	tg.Post(t, OCR, "/v1/ocr/jobs", map[string]any{}, app.H()).ExpectStatus(http.StatusUnprocessableEntity).ExpectShape(ValidationError("body", "images"))
	nine := make([]any, 9)
	for i := range nine {
		nine[i] = ocrImage(png)
	}
	tg.Post(t, OCR, "/v1/ocr/jobs", map[string]any{"images": nine}, app.H()).ExpectStatus(http.StatusUnprocessableEntity).ExpectShape(ValidationError("body", "images"))
	tg.Post(t, OCR, "/v1/ocr/jobs", map[string]any{"images": []any{ocrImage(png)}, "callback_url": "file:///etc/passwd"}, app.H()).
		ExpectStatus(http.StatusUnprocessableEntity).ExpectShape(ValidationError("body", "callback_url"))

	wait := func(t *testing.T, id string) map[string]any {
		deadline := time.Now().Add(slowTimeout())
		for {
			st := tg.Get(t, OCR, "/v1/ocr/jobs/"+id, app.H()).Expect(http.StatusOK, Obj{
				"job_id": Eq(id), "status": OneOf("pending", "processing", "completed", "failed"),
				"created_at": TimestampUTC, "updated_at": NullOr(TimestampUTC),
				"result": NullOr(ocrCompletionPayloadShape), "error": NullOr(String),
			}).Object()
			if s := st["status"]; s == "completed" || s == "failed" {
				return st
			}
			if time.Now().After(deadline) {
				t.Fatalf("job %s still %v", id, st["status"])
			}
			time.Sleep(250 * time.Millisecond)
		}
	}
	check := func(t *testing.T, st map[string]any, n int) {
		if st["status"] != "completed" {
			t.Fatalf("job: %v", st)
		}
		payload := st["result"].(map[string]any)
		results := payload["results"].([]any)
		if len(results) != n {
			t.Fatalf("one result per image: %v", payload)
		}
		if text {
			r0 := results[0].(map[string]any)
			if payload["status"] != "success" || !strings.Contains(strings.ToUpper(r0["ocr_text"].(string)), "HELLO") {
				t.Fatalf("want HELLO read: %v", payload)
			}
		}
	}

	t.Run("json", func(t *testing.T) {
		r := tg.Post(t, OCR, "/v1/ocr/jobs", map[string]any{
			"images": []any{ocrImage(png), ocrImage(png)}, "options": map[string]any{"language": "en"},
			"workflow_id": "contract-" + tg.RunID, "parent_job_id": "contract-" + tg.RunID, "source": "contract",
		}, app.H()).Expect(http.StatusAccepted, ocrJobCreatedShape).Object()
		check(t, wait(t, r["job_id"].(string)), 2)
	})

	t.Run("multipart", func(t *testing.T) {
		body, ct := MultipartBody(FormPart{Name: "images", Filename: "page1.png", ContentType: "image/png", Data: ocrTestPNG()}, FormField("options", `{"language":"en"}`),
			FormField("workflow_id", "contract-"+tg.RunID))
		r := tg.SlowDo(t, OCR, http.MethodPost, "/v1/ocr/jobs", ct, body, app.H()).Expect(http.StatusAccepted, ocrJobCreatedShape).Object()
		check(t, wait(t, r["job_id"].(string)), 1)
	})

	t.Run("callback", func(t *testing.T) {
		host := os.Getenv(EnvOCRCallbackHost)
		if host == "" {
			t.Skipf("%s is not set", EnvOCRCallbackHost)
		}
		ln, err := net.Listen("tcp", ":0")
		if err != nil {
			t.Fatal(err)
		}
		got := make(chan map[string]any, 1)
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var msg map[string]any
			dec := json.NewDecoder(r.Body)
			dec.UseNumber()
			dec.Decode(&msg)
			if r.Header.Get("X-Jarvis-App-Id") == "" || r.Header.Get("X-Jarvis-App-Key") == "" {
				msg = map[string]any{"error": "callback without app credentials"}
			}
			w.WriteHeader(http.StatusNoContent)
			select {
			case got <- msg:
			default:
			}
		})}
		go srv.Serve(ln)
		defer srv.Close()
		cb := fmt.Sprintf("http://%s:%d/ocr/callback", host, ln.Addr().(*net.TCPAddr).Port)
		r := tg.Post(t, OCR, "/v1/ocr/jobs", map[string]any{
			"images": []any{ocrImage(png)}, "callback_url": cb,
			"workflow_id": "wf-" + tg.RunID, "parent_job_id": "job-" + tg.RunID, "request_id": "req-" + tg.RunID,
			"source": "jarvis-recipes-server",
		}, app.H()).Expect(http.StatusAccepted, ocrJobCreatedShape).Object()
		select {
		case msg := <-got:
			if errs := (Obj{
				"schema_version": Eq(1), "job_id": UUID, "workflow_id": Eq("wf-" + tg.RunID),
				"job_type": Eq("ocr.completed"), "source": Eq("jarvis-ocr-service"), "target": Eq("jarvis-recipes-server"),
				"created_at": TimestampUTC, "attempt": Eq(1), "reply_to": Null, "payload": ocrCompletionPayloadShape,
				"trace":      Obj{"request_id": Eq("req-" + tg.RunID), "parent_job_id": Eq("job-" + tg.RunID)},
				"ocr_job_id": Eq(r["job_id"]),
			}).Match("$", msg); len(errs) > 0 {
				t.Fatalf("callback message: %v\n%v", errs, msg)
			}
		case <-time.After(slowTimeout()):
			t.Fatal("no callback")
		}
	})
}

// ocrSettingKeys are the OCR keys jarvisd keeps (the read ones), with their types.
var ocrSettingKeys = map[string]string{
	"ocr.enabled_tiers":           "string",
	"ocr.language_default":        "string",
	"ocr.max_attempts":            "int",
	"ocr.max_text_bytes":          "int",
	"ocr.min_valid_chars":         "int",
	"ocr.validation_model":        "string",
	"ocr.enable_apple_vision":     "bool",
	"ocr.enable_llm_proxy_vision": "bool",
}

func TestOCRSettings(t *testing.T) {
	tg := T(t)
	tg.Need(t, OCR)
	app := SharedApp(t)
	user := SharedUser(t)
	super := SharedSuperuser(t)

	list := tg.Get(t, OCR, "/settings/", app.H()).Expect(http.StatusOK, settingsList).Object()["settings"].([]any)
	byKey := map[string]map[string]any{}
	for _, s := range list {
		m := s.(map[string]any)
		byKey[m["key"].(string)] = m
	}
	for k, typ := range ocrSettingKeys {
		m, ok := byKey[k]
		if !ok {
			t.Fatalf("/settings/ lacks %s", k)
		}
		if m["value_type"] != typ {
			t.Fatalf("%s: value_type %v, want %s", k, m["value_type"], typ)
		}
	}
	tg.Get(t, OCR, "/settings/?category=ocr.processing", app.H()).Expect(http.StatusOK, settingsList)
	cats := tg.Get(t, OCR, "/settings/categories", app.H()).Expect(http.StatusOK, Obj{"categories": NonEmptyArrayOf(NonEmptyString)}).Object()
	for _, want := range []string{"ocr.processing", "ocr.providers"} {
		if !strings.Contains(fmt.Sprint(cats["categories"]), want) {
			t.Fatalf("categories lack %s: %v", want, cats)
		}
	}
	one := tg.Get(t, OCR, "/settings/ocr.max_attempts", app.H()).Expect(http.StatusOK, settingShape).Object()
	tg.Get(t, OCR, "/settings/ocr.max_attempts", super.H()).Expect(http.StatusOK, settingShape)
	tg.Get(t, OCR, "/settings/contract.no.such.key", app.H()).
		ExpectStatus(http.StatusNotFound).ExpectShape(settingNotFound("contract.no.such.key"))

	// Read auth: superuser JWT or app credentials.
	tg.Get(t, OCR, "/settings/").ExpectError(http.StatusUnauthorized, "Missing authentication. Provide either Bearer token or app credentials.")
	tg.Get(t, OCR, "/settings/", H{"X-Jarvis-App-Id": app.ID, "X-Jarvis-App-Key": "wrong"}).ExpectError(http.StatusUnauthorized, "Invalid app credentials")
	tg.Get(t, OCR, "/settings/", user.H()).ExpectError(http.StatusForbidden, "Superuser access required")
	tg.Get(t, OCR, "/settings/", Bearer("not-a-jwt")).ExpectError(http.StatusUnauthorized, "Invalid or expired token")

	// Write auth: superuser JWT only.
	writes := []struct {
		method, path string
		body         any
	}{
		{http.MethodPut, "/settings/ocr.max_attempts", map[string]any{"value": one["value"]}},
		{http.MethodPost, "/settings/sync-from-env", nil},
		{http.MethodPost, "/settings/invalidate-cache", nil},
	}
	for _, w := range writes {
		tg.Do(t, OCR, w.method, w.path, w.body).ExpectError(http.StatusUnauthorized, "Missing or invalid Authorization header")
		tg.Do(t, OCR, w.method, w.path, w.body, app.H()).ExpectError(http.StatusUnauthorized, "Missing or invalid Authorization header")
		tg.Do(t, OCR, w.method, w.path, w.body, Bearer("not-a-jwt")).ExpectError(http.StatusUnauthorized, "Invalid or expired token")
		tg.Do(t, OCR, w.method, w.path, w.body, user.H()).ExpectError(http.StatusForbidden, "Superuser access required")
	}

	// A superuser write of the current value (no behaviour change on the target), and the
	// cache invalidation. sync-from-env is not called: it would copy the target's env into
	// its settings DB.
	tg.Do(t, OCR, http.MethodPut, "/settings/ocr.max_attempts", map[string]any{"value": one["value"]}, super.H()).
		Expect(http.StatusOK, Obj{"success": Eq(true), "key": Eq("ocr.max_attempts"), "requires_reload": Eq(false), "message": Null})
	tg.Do(t, OCR, http.MethodPut, "/settings/contract.no.such.key", map[string]any{"value": 1}, super.H()).
		ExpectStatus(http.StatusNotFound).ExpectShape(settingNotFound("contract.no.such.key"))
	tg.Do(t, OCR, http.MethodPut, "/settings/ocr.max_attempts", map[string]any{}, super.H()).
		ExpectStatus(http.StatusUnprocessableEntity).ExpectShape(ValidationError("body", "value"))
	tg.Post(t, OCR, "/settings/invalidate-cache", nil, super.H()).Expect(http.StatusOK, Obj{"status": Eq("ok"), "invalidated": Eq("all")})
	if after := tg.Get(t, OCR, "/settings/ocr.max_attempts", app.H()).Object(); fmt.Sprint(after["value"]) != fmt.Sprint(one["value"]) {
		t.Fatalf("ocr.max_attempts changed: %v -> %v", one["value"], after["value"])
	}
}

// --- test image ---

// ocrGlyphs is a 5x7 bitmap font with just the letters of ocrTestText.
var ocrGlyphs = map[rune][7]string{
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

const ocrTestText = "HELLO JARVIS"

// ocrTestPNG renders ocrTestText black on white, 6 px per font dot, as a grayscale PNG.
func ocrTestPNG() []byte {
	const scale, margin = 6, 24
	w := margin*2 + len(ocrTestText)*6*scale
	h := margin*2 + 7*scale
	img := image.NewGray(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = 0xff
	}
	for ci, ch := range ocrTestText {
		g := ocrGlyphs[ch]
		for row := 0; row < 7; row++ {
			for col := 0; col < 5; col++ {
				if g[row][col] != '1' {
					continue
				}
				x0, y0 := margin+(ci*6+col)*scale, margin+row*scale
				for y := y0; y < y0+scale; y++ {
					for x := x0; x < x0+scale; x++ {
						img.SetGray(x, y, color.Gray{})
					}
				}
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func ocrTestPNGBase64() string { return base64.StdEncoding.EncodeToString(ocrTestPNG()) }
