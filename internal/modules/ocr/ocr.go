// Package ocr is the jarvisd ocr module (jarvis-ocr-service, legacy port 7031).
//
// HTTP, same paths and shapes as the legacy service, app-to-app auth on everything but
// /health and /settings:
//   - POST /v1/ocr/batch: synchronous OCR of 1-100 images (the legacy live path).
//   - POST /v1/ocr + GET /v1/ocr/jobs/{id}: one image, queued and polled. The legacy endpoint
//     enqueued a message its own worker dropped, so jobs sat "pending" forever; here they run.
//   - POST /v1/ocr/jobs: 1-8 images through the tier chain as one queued job, with an optional
//     callback. It replaces the Redis queue-flow handoff with jarvis-recipes-server
//     (docs/schema/ocr.md, "recipes handoff").
//   - GET /v1/providers, GET /v1/queue/status, /settings.
//
// Engines (PLAN §3.3): tesseract via exec when the binary is present, Apple Vision (in process
// through purego on macOS, ID13; through the jarvis-osx-api helper when JARVIS_OSX_API_URL is
// set, as the fallback there and the only route elsewhere), and LLM vision through an OpenAI-compatible endpoint. EasyOCR,
// PaddleOCR and RapidOCR are cut. Images and job state live in the blob store; the work runs
// as durable queue jobs.
package ocr

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	pconfig "github.com/alexberardi/jarvis-server/internal/platform/config"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
	"github.com/alexberardi/jarvis-server/internal/platform/module"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
	"github.com/alexberardi/jarvis-server/internal/platform/scheduler"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Migrations returns the module's goose migrations, rooted at the migrations directory.
func Migrations() fs.FS {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic(err) // unreachable: the directory is embedded at build time
	}
	return sub
}

// ServiceName is the legacy service name.
const ServiceName = "jarvis-ocr-service"

// Definitions are the settings the module reads. Dropped (docs/cc D11, PLAN §7): the cut
// engines' flags (easyocr, paddleocr, rapidocr), ocr.enable_llm_proxy_cloud (a second LLM
// tier on the same model), server.log_level (jarvisd logs) and auth.cache_ttl_seconds (auth is
// in-process). No env fallbacks (M3). The two engine flags are read live, so no restart.
// ocr.enable_apple_vision defaults on where Vision is built in (macOS, ID13);
// ocr.llm_vision_timeout_seconds is new (A10e M4: the legacy 60 s was too short).
var Definitions = []settings.Definition{
	{Key: "ocr.enable_apple_vision", Category: "ocr.providers", Type: settings.Bool, Default: defaultAppleVision,
		Description: "Enable Apple Vision OCR (built in on macOS; elsewhere through a jarvis-osx-api helper on a Mac)"},
	{Key: "ocr.enable_llm_proxy_vision", Category: "ocr.providers", Type: settings.Bool, Default: false,
		Description: "Enable LLM vision mode for OCR"},
	{Key: "ocr.llm_vision_timeout_seconds", Category: "ocr.processing", Type: settings.Int, Default: int64(180),
		Description: "Per-image timeout for LLM vision OCR, in seconds (a dense page on a 9B model can need minutes)"},
	{Key: "ocr.max_text_bytes", Category: "ocr.processing", Type: settings.Int, Default: int64(51200),
		Description: "Maximum output text size in bytes (truncates if exceeded)"},
	{Key: "ocr.min_valid_chars", Category: "ocr.processing", Type: settings.Int, Default: int64(3),
		Description: "Minimum characters for valid OCR output"},
	{Key: "ocr.language_default", Category: "ocr.processing", Type: settings.String, Default: "en",
		Description: "Default language hint for OCR"},
	{Key: "ocr.max_attempts", Category: "ocr.processing", Type: settings.Int, Default: int64(3),
		Description: "Maximum retry attempts for failed OCR jobs"},
	{Key: "ocr.enabled_tiers", Category: "ocr.processing", Type: settings.String, Default: "tesseract,apple_vision,llm_local",
		Description: "Comma-separated list of enabled provider tiers for fallback"},
	{Key: "ocr.validation_model", Category: "ocr.processing", Type: settings.String, Default: "live",
		Description: "LLM model used for output validation"},
}

// Module is the ocr module.
type Module struct {
	// Auth validates app-to-app callers (ValidateApp).
	Auth authn.Authority
	// Superuser guards settings writes; app credentials or a superuser JWT may read.
	SettingsRead, SettingsWrite settings.Guard
	// Version is what /health reports.
	Version string

	// LLMURL is an OpenAI-compatible endpoint (during the strangler phase the legacy
	// llm-proxy, later jarvisd's own) used for LLM vision and for text validation, with
	// LLMAppID/LLMAppKey as app credentials. Empty: no LLM vision, validation fails open.
	LLMURL, LLMAppID, LLMAppKey string
	// AppleVisionURL/AppleVisionKey reach jarvis-osx-api (POST /v1/ocr, an ocr:read key). On
	// macOS jarvisd reads with Vision in process (ID13) and this is only the fallback.
	AppleVisionURL, AppleVisionKey string
	// TesseractPath is the tesseract binary. Empty: looked up on PATH; "-": disabled.
	TesseractPath string
	// AppID/AppKey are jarvisd's own app credentials, sent on job completion callbacks.
	AppID, AppKey string
	// AppCreds supplies them when AppID is empty (jarvisd's self-issued app client, auth
	// SelfAppCreds). Both empty: callbacks go out unsigned.
	AppCreds func(ctx context.Context) (id, key string, err error)
	// Concurrency caps OCR jobs running at once (legacy: one worker). Default 1.
	Concurrency int
	// CallbackAttempts bounds callback retries (default 12, exponential backoff up to 5 min).
	CallbackAttempts int
	HTTPClient       *http.Client
	// LLMClient carries the LLM vision and validation calls (jarvisd routes them in memory to
	// its llm module). Nil uses HTTPClient.
	LLMClient *http.Client

	// Engines, when set, replaces the engines built from the fields above (tests).
	Engines []Engine
	// Validator, when set, replaces the LLM validation call (tests).
	Validator Validator

	deps     module.Deps
	settings *settings.Service
	engines  []Engine
	now      func() time.Time
}

func (m *Module) Name() string      { return "ocr" }
func (m *Module) Listener() string  { return pconfig.ListenerOCR }
func (m *Module) Migrations() fs.FS { return Migrations() }

// Settings is the module's settings service (valid after Register), for the admin aggregator.
func (m *Module) Settings() *settings.Service { return m.settings }

func (m *Module) httpClient() *http.Client {
	if m.HTTPClient != nil {
		return m.HTTPClient
	}
	return http.DefaultClient
}

func (m *Module) buildEngines() []Engine {
	if m.Engines != nil {
		return m.Engines
	}
	var out []Engine
	path := m.TesseractPath
	if path == "" {
		path = findTesseract(exec.LookPath, tesseractDirs())
	}
	if path != "" && path != "-" {
		out = append(out, &Tesseract{Path: path})
	}
	native, err := newNativeAppleVision()
	if err != nil && runtime.GOOS == "darwin" && m.deps.Log != nil {
		m.deps.Log.Warn("ocr: native Apple Vision unavailable", "err", err)
	}
	var remote Engine
	if m.AppleVisionURL != "" {
		remote = &AppleVision{URL: m.AppleVisionURL, Key: m.AppleVisionKey, Client: m.HTTPClient}
	}
	if av := appleVisionEngine(native, remote); av != nil {
		out = append(out, av)
	}
	if m.LLMURL != "" {
		out = append(out, &LLMVision{URL: m.LLMURL, AppID: m.LLMAppID, AppKey: m.LLMAppKey, Client: m.llmClient(),
			TimeoutFn: m.llmVisionTimeout})
	}
	return out
}

// llmVisionTimeout is ocr.llm_vision_timeout_seconds, read live (at least 1 s).
func (m *Module) llmVisionTimeout(ctx context.Context) time.Duration {
	if m.settings == nil {
		return defaultLLMVisionTimeout
	}
	return time.Duration(max(1, m.settings.Int(ctx, "ocr.llm_vision_timeout_seconds", settings.Scope{}))) * time.Second
}

// tesseractDirs are where package managers install tesseract, searched after PATH: launchd
// starts a LaunchDaemon with PATH=/usr/bin:/bin:/usr/sbin:/sbin, so Homebrew's
// (/opt/homebrew/bin on Apple silicon, /usr/local/bin on Intel) and MacPorts' copies would
// otherwise never be found by the installed service.
func tesseractDirs() []string {
	if runtime.GOOS == "windows" {
		return nil
	}
	return []string{"/opt/homebrew/bin", "/usr/local/bin", "/opt/local/bin"}
}

// findTesseract returns tesseract on PATH, else the first executable regular file named
// tesseract in dirs (symlinks followed), else "".
func findTesseract(lookPath func(string) (string, error), dirs []string) string {
	if p, err := lookPath("tesseract"); err == nil {
		return p
	}
	for _, d := range dirs {
		p := filepath.Join(d, "tesseract")
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

// noTextEngineHint is the start-up warning for a jarvisd with neither tesseract nor Apple
// Vision: recipe photo import then fails with ocr_unavailable unless LLM vision is on.
func noTextEngineHint(engines []Engine) string {
	for _, e := range engines {
		if n := e.Name(); n == EngineTesseract || n == EngineAppleVision {
			return ""
		}
	}
	return "ocr: no tesseract found and Apple Vision is not configured, so recipe photo import fails " +
		"(ocr_unavailable) unless ocr.enable_llm_proxy_vision is on with a vision model; install tesseract " +
		"(brew install tesseract / apt install tesseract-ocr) and restart jarvisd"
}

func (m *Module) Register(mux *http.ServeMux, deps module.Deps) {
	m.deps = deps
	if m.now == nil {
		m.now = time.Now
	}
	if m.Version == "" {
		m.Version = "dev"
	}
	svc, err := settings.New(deps.DB, "ocr", Definitions, deps.Log)
	if err != nil {
		panic(err) // static definitions
	}
	m.settings = svc
	m.engines = m.buildEngines()
	if hint := noTextEngineHint(m.engines); hint != "" && deps.Log != nil {
		deps.Log.Warn(hint)
	}
	if m.SettingsRead != nil && m.SettingsWrite != nil {
		svc.Mount(mux, m.SettingsRead, m.SettingsWrite)
	}

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok", "version": m.Version})
	})
	mux.HandleFunc("GET /v1/providers", m.app(m.handleProviders))
	mux.HandleFunc("GET /v1/queue/status", m.app(m.handleQueueStatus))
	mux.HandleFunc("POST /v1/ocr", m.app(m.handleSubmit))
	mux.HandleFunc("GET /v1/ocr/jobs/{job_id}", m.app(m.handleJobStatus))
	mux.HandleFunc("POST /v1/ocr/jobs", m.app(m.handleSubmitJob))
	mux.HandleFunc("POST /v1/ocr/batch", m.app(m.handleBatch))

	if deps.Queue != nil {
		conc := m.Concurrency
		if conc <= 0 {
			conc = 1
		}
		deps.Queue.Register(jobType, queue.Handler{Run: m.runJob, Concurrency: conc, Lease: 15 * time.Minute,
			Backoff: func(int) time.Duration { return 5 * time.Second }})
		attempts := m.CallbackAttempts
		if attempts <= 0 {
			attempts = 12
		}
		deps.Queue.Register(callbackType, queue.Handler{Run: m.runCallback, Concurrency: 2, MaxAttempts: attempts, Lease: time.Minute})
		deps.Queue.Register(purgeType, queue.Handler{Run: m.runPurge})
	}
}

// Start migrates the settings table and schedules the hourly job purge.
func (m *Module) Start(ctx context.Context) error {
	if err := m.settings.Migrate(ctx); err != nil {
		return err
	}
	if m.deps.Scheduler == nil || m.deps.Blobs == nil {
		return nil
	}
	return m.deps.Scheduler.Ensure(ctx, scheduler.Trigger{
		Name: purgeType, Kind: scheduler.KindInterval, JobType: purgeType,
		Spec: scheduler.Spec{Every: time.Hour, StartNow: true},
	})
}

// --- auth ---

func appError(w http.ResponseWriter, status int, code, msg string) {
	httpx.Error(w, status, map[string]any{"error_code": code, "error_message": msg})
}

// app is the legacy verify_app_auth: both headers, checked by the auth module. Unlike the
// legacy one it answers 401 for bad credentials on the first try (LEGACY-BUG: that was a 503
// until the failure was cached).
func (m *Module) app(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, key := r.Header.Get("X-Jarvis-App-Id"), r.Header.Get("X-Jarvis-App-Key")
		if id == "" || key == "" {
			appError(w, http.StatusUnauthorized, "unauthorized", "Missing or invalid app credentials")
			return
		}
		app, ok, err := m.Auth.ValidateApp(r.Context(), id, key)
		if err != nil {
			m.deps.Log.Error("ocr: app auth unavailable", "err", err)
			appError(w, http.StatusServiceUnavailable, "auth_unavailable", "Auth service unavailable")
			return
		}
		if !ok {
			appError(w, http.StatusUnauthorized, "unauthorized", "Missing or invalid app credentials")
			return
		}
		h(w, r.WithContext(authn.WithApp(r.Context(), app)))
	}
}

// --- handlers ---

func (m *Module) handleProviders(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	reg := m.registry(ctx)
	providers := map[string]bool{}
	diags := map[string]Diagnostic{}
	for _, name := range providerOrder {
		e, ok := reg[name]
		if !ok {
			providers[name] = false
			continue
		}
		d := diagnose(ctx, e)
		d.Provider = name
		providers[name] = d.Available
		diags[name] = d
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"providers": providers, "diagnostics": diags})
}

func (m *Module) handleQueueStatus(w http.ResponseWriter, r *http.Request) {
	pending, processing, err := m.queueCounts(r.Context())
	if err != nil {
		httpx.Error(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	// The legacy shape names Redis; jarvisd's queue is embedded and always connected.
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"redis_connected": true, "queue_length": pending, "workers_active": processing,
		"queue_name": "jarvis.ocr.jobs",
		"redis_info": map[string]any{"host": "jarvisd", "port": 0, "version": "embedded"},
	})
}

func decodeImage(img imageInput) (Image, error) {
	data, err := pyB64Decode(img.Base64)
	if err != nil {
		return Image{}, err
	}
	return Image{Data: data, ContentType: img.ContentType}, nil
}

func (m *Module) handleSubmit(w http.ResponseWriter, r *http.Request) {
	raw, ok := readBody(w, r)
	if !ok {
		return
	}
	req, ok := parseOCRRequest(w, raw)
	if !ok {
		return
	}
	img, err := decodeImage(req.Image)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "Invalid base64 image data: "+err.Error())
		return
	}
	lang := ""
	if len(req.Options.LanguageHints) > 0 {
		lang = req.Options.LanguageHints[0]
	} else {
		lang = m.settings.String(r.Context(), "ocr.language_default", settings.Scope{})
	}
	j := &jobRecord{Kind: kindSingle, Provider: req.Provider, Options: req.Options, Language: lang,
		DocumentID: req.DocumentID, CorrelationID: correlationID(r)}
	if err := m.createJob(r.Context(), j, []Image{img}); err != nil {
		m.deps.Log.Error("ocr: enqueue", "err", err, "correlation_id", j.CorrelationID)
		httpx.Error(w, http.StatusServiceUnavailable, "Queue service unavailable: "+err.Error())
		return
	}
	m.deps.Log.Info("ocr: job queued", "job_id", j.JobID, "provider", j.Provider, "correlation_id", j.CorrelationID)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"job_id": j.JobID, "status": j.Status, "created_at": j.CreatedAt})
}

func correlationID(r *http.Request) string {
	if c := r.Header.Get("X-Correlation-ID"); c != "" {
		return c
	}
	return "unknown"
}

func (m *Module) handleJobStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("job_id")
	if m.deps.Blobs == nil || !validJobID(id) {
		httpx.Error(w, http.StatusNotFound, "Job not found")
		return
	}
	j, err := m.loadJob(r.Context(), id)
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "Job not found")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, j.statusView())
}

func (m *Module) handleBatch(w http.ResponseWriter, r *http.Request) {
	raw, ok := readBody(w, r)
	if !ok {
		return
	}
	req, ok := parseBatchRequest(w, raw)
	if !ok {
		return
	}
	imgs := make([]Image, len(req.Images))
	for i, in := range req.Images {
		img, err := decodeImage(in)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, fmt.Sprintf("Invalid base64 image data at index %d: %v", i, err))
			return
		}
		imgs[i] = img
	}
	results, used, err := m.batch(r.Context(), imgs, req.Provider, req.Options)
	if err != nil {
		var ue unavailableError
		var pe processingError
		var be badRequestError
		switch {
		case errors.As(err, &ue), errors.As(err, &be):
			httpx.Error(w, http.StatusBadRequest, err.Error())
		case errors.As(err, &pe):
			httpx.Error(w, http.StatusUnprocessableEntity, err.Error())
		default:
			m.deps.Log.Error("ocr: batch failed", "err", err, "correlation_id", correlationID(r))
			httpx.Error(w, http.StatusInternalServerError, "Internal server error: "+err.Error())
		}
		return
	}
	out := make([]ocrResponse, len(results))
	var total float64
	for i, res := range results {
		d := ms(res.Duration)
		total += d
		out[i] = ocrResponse{ProviderUsed: used, Text: res.Text, Blocks: res.Blocks, Meta: map[string]any{"duration_ms": round2(d)}}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"results": out,
		"meta":    map[string]any{"total_images": len(results), "total_duration_ms": round2(total), "provider_used": used},
	})
}

// --- POST /v1/ocr/jobs (the queue-flow replacement) ---

// flowRequest is POST /v1/ocr/jobs: 1-8 images plus what the legacy queue-flow envelope
// carried (workflow_id, the caller's job id as parent_job_id, request_id, source) and an
// optional callback_url.
type flowRequest struct {
	Images      []Image
	Language    string
	CallbackURL string
	WorkflowID  string
	ParentJobID string
	RequestID   string
	Source      string
}

const maxFlowImages = 8

func (m *Module) handleSubmitJob(w http.ResponseWriter, r *http.Request) {
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	var req flowRequest
	var ok bool
	if ct == "multipart/form-data" {
		req, ok = parseFlowMultipart(w, r)
	} else {
		var raw []byte
		if raw, ok = readBody(w, r); ok {
			req, ok = parseFlowJSON(w, raw)
		}
	}
	if !ok {
		return
	}
	if req.Language == "" {
		req.Language = m.settings.String(r.Context(), "ocr.language_default", settings.Scope{})
	}
	j := &jobRecord{Kind: kindFlow, Provider: "auto", Language: req.Language, CorrelationID: correlationID(r),
		CallbackURL: req.CallbackURL, WorkflowID: req.WorkflowID, ParentJobID: req.ParentJobID,
		RequestID: req.RequestID, Source: req.Source}
	if err := m.createJob(r.Context(), j, req.Images); err != nil {
		m.deps.Log.Error("ocr: enqueue", "err", err)
		httpx.Error(w, http.StatusServiceUnavailable, "Queue service unavailable: "+err.Error())
		return
	}
	m.deps.Log.Info("ocr: flow job queued", "job_id", j.JobID, "images", len(req.Images), "workflow_id", j.WorkflowID)
	httpx.WriteJSON(w, http.StatusAccepted, map[string]any{"job_id": j.JobID, "status": j.Status, "created_at": j.CreatedAt})
}

func validCallback(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func parseFlowJSON(w http.ResponseWriter, raw []byte) (flowRequest, bool) {
	o, ok := readObject(w, raw)
	if !ok {
		return flowRequest{}, false
	}
	var req flowRequest
	inputs := o.imageList("images", 1, maxFlowImages)
	if v, present := o.m["options"]; present && v != nil {
		if c, ok := o.child("options", v); ok {
			req.Language, _ = c.str("language", false)
		}
	}
	req.CallbackURL, _ = o.str("callback_url", false)
	if req.CallbackURL != "" && !validCallback(req.CallbackURL) {
		o.fail("url_parsing", "Input should be a valid http(s) URL", req.CallbackURL, "callback_url")
	}
	req.WorkflowID, _ = o.str("workflow_id", false)
	req.ParentJobID, _ = o.str("parent_job_id", false)
	req.RequestID, _ = o.str("request_id", false)
	req.Source, _ = o.str("source", false)
	if !o.done(w) {
		return flowRequest{}, false
	}
	for i, in := range inputs {
		img, err := decodeImage(in)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, fmt.Sprintf("Invalid base64 image data at index %d: %v", i, err))
			return flowRequest{}, false
		}
		req.Images = append(req.Images, img)
	}
	return req, true
}

func parseFlowMultipart(w http.ResponseWriter, r *http.Request) (flowRequest, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		httpx.Error(w, http.StatusBadRequest, "Invalid multipart body: "+err.Error())
		return flowRequest{}, false
	}
	var errs []httpx.FieldError
	files := r.MultipartForm.File["images"]
	switch {
	case len(files) == 0:
		errs = append(errs, httpx.FieldError{Type: "missing", Loc: []any{"body", "images"}, Msg: "Field required"})
	case len(files) > maxFlowImages:
		errs = append(errs, httpx.FieldError{Type: "too_long", Loc: []any{"body", "images"},
			Msg: fmt.Sprintf("List should have at most %d items after validation, not %d", maxFlowImages, len(files))})
	}
	req := flowRequest{
		CallbackURL: r.FormValue("callback_url"), WorkflowID: r.FormValue("workflow_id"),
		ParentJobID: r.FormValue("parent_job_id"), RequestID: r.FormValue("request_id"),
		Source: r.FormValue("source"), Language: r.FormValue("language"),
	}
	if opts := r.FormValue("options"); opts != "" {
		var o struct {
			Language string `json:"language"`
		}
		if err := json.Unmarshal([]byte(opts), &o); err != nil {
			errs = append(errs, httpx.FieldError{Type: "json_invalid", Loc: []any{"body", "options"}, Msg: "Invalid JSON", Input: opts})
		} else if req.Language == "" {
			req.Language = o.Language
		}
	}
	if req.CallbackURL != "" && !validCallback(req.CallbackURL) {
		errs = append(errs, httpx.FieldError{Type: "url_parsing", Loc: []any{"body", "callback_url"},
			Msg: "Input should be a valid http(s) URL", Input: req.CallbackURL})
	}
	if len(errs) > 0 {
		httpx.ValidationError(w, errs...)
		return flowRequest{}, false
	}
	for _, fh := range files {
		img, err := readPart(fh)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "Could not read uploaded image: "+err.Error())
			return flowRequest{}, false
		}
		req.Images = append(req.Images, img)
	}
	return req, true
}

func readPart(fh *multipart.FileHeader) (Image, error) {
	f, err := fh.Open()
	if err != nil {
		return Image{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return Image{}, err
	}
	ct := fh.Header.Get("Content-Type")
	if ct == "" || ct == "application/octet-stream" {
		ct = http.DetectContentType(data)
	}
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	return Image{Data: data, ContentType: ct}, nil
}

func (m *Module) llmClient() *http.Client {
	if m.LLMClient != nil {
		return m.LLMClient
	}
	return m.HTTPClient
}
