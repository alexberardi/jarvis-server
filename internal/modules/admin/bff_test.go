package admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/doctor"
	authmod "github.com/alexberardi/jarvis-server/internal/modules/auth"
	ccmod "github.com/alexberardi/jarvis-server/internal/modules/cc"
	llmmod "github.com/alexberardi/jarvis-server/internal/modules/llm"
	pconfig "github.com/alexberardi/jarvis-server/internal/platform/config"
	"github.com/alexberardi/jarvis-server/internal/platform/db"
	"github.com/alexberardi/jarvis-server/internal/platform/module"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// --- fakes for the module interfaces ---

type fakeSettings struct {
	name string
	svc  *settings.Service
}

func (f fakeSettings) Name() string                { return f.name }
func (f fakeSettings) Settings() *settings.Service { return f.svc }

type fakeTraces struct{ last ccmod.TraceFilter }

func (f *fakeTraces) ListTraces(_ context.Context, flt ccmod.TraceFilter) ([]map[string]any, int, error) {
	f.last = flt
	return []map[string]any{{"id": "t1", "span_count": 2}}, 7, nil
}

func (f *fakeTraces) GetTrace(_ context.Context, id string) (map[string]any, bool, error) {
	if id != "t1" {
		return nil, false, nil
	}
	return map[string]any{"id": "t1", "spans": []any{map[string]any{"name": "stt"}}, "error_message": nil}, true, nil
}

type fakeAccounts struct{ c authmod.SetupCounts }

func (f *fakeAccounts) SetupCounts(context.Context) (authmod.SetupCounts, error) { return f.c, nil }

type fakeModels struct{ states map[string]string }

func (f fakeModels) LabelStates(context.Context) map[string]string { return f.states }
func (f fakeModels) HardwareSummary(context.Context) (llmmod.SetupHardware, bool) {
	return llmmod.SetupHardware{Flavours: map[string][]string{"llama-server": {"cpu", "cuda"}}}, true
}

type fakePrompts struct{ value string }

func (f *fakePrompts) PromptProvider(context.Context) ccmod.PromptProviderStatus {
	st := ccmod.PromptProviderStatus{Value: f.value, Derived: "Qwen3_8B_Compressed", Options: []string{"ChatGPTOpenAI", "Qwen3_8B_Compressed"}}
	st.Effective, st.Source = st.Derived, ccmod.PromptSourceModel
	if f.value != "" {
		st.Effective, st.Source = f.value, ccmod.PromptSourceSetting
	}
	st.Valid = true
	return st
}

func (f *fakePrompts) SetPromptProvider(_ context.Context, name string) error {
	if name != "" && name != "ChatGPTOpenAI" && name != "Qwen3_8B_Compressed" {
		return ccmod.ErrUnknownPromptProvider
	}
	f.value = name
	return nil
}

type bffEnv struct {
	mux      *http.ServeMux
	m        *Module
	accounts *fakeAccounts
	traces   *fakeTraces
	prompts  *fakePrompts
	doctors  *atomic.Int32
	llm, cc  *settings.Service
}

func newSettings(t *testing.T, d *db.DB, name string, defs []settings.Definition) *settings.Service {
	t.Helper()
	svc, err := settings.New(d, name, defs, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return svc
}

func newBFF(t *testing.T) *bffEnv {
	t.Helper()
	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "jarvis.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	e := &bffEnv{
		accounts: &fakeAccounts{c: authmod.SetupCounts{Superusers: 1, Households: 2, Nodes: 3}},
		traces:   &fakeTraces{}, prompts: &fakePrompts{}, doctors: &atomic.Int32{},
	}
	e.llm = newSettings(t, d, "llm", []settings.Definition{
		{Key: "llm.request_timeout_seconds", Category: "llm", Type: settings.Int, Default: int64(240)},
		{Key: "llm.hf_token", Category: "llm", Type: settings.String, Default: "", IsSecret: true},
		{Key: "llm.restart_me", Category: "llm", Type: settings.Bool, Default: false, RequiresReload: true},
	})
	e.cc = newSettings(t, d, "cc", []settings.Definition{
		{Key: "llm.prompt_provider", Category: "llm", Type: settings.String, Default: ""},
		{Key: "smarthome.manager", Category: "smarthome", Type: settings.String, Default: "jarvis_direct",
			Options: []any{"jarvis_direct", "home_assistant"}},
	})
	e.m = &Module{
		UI: builtUI(), Verify: fakeVerify, Version: "1.2.3",
		SettingsSources: []SettingsSource{fakeSettings{"llm", e.llm}, fakeSettings{"cc", e.cc}, fakeSettings{"notready", nil}},
		Traces:          e.traces, Accounts: e.accounts, Prompts: e.prompts,
		Models:   fakeModels{states: map[string]string{"live": "ready", "background": "not_configured"}},
		Exposure: doctor.Exposure{Listeners: []string{pconfig.ListenerAdmin}},
		RunDoctor: func(_ context.Context, o doctor.Options) []doctor.Check {
			e.doctors.Add(1)
			return []doctor.Check{
				{Name: "listening", Status: doctor.OK, Detail: "fine"},
				{Name: "firewall 10.0.0.0/24", Status: doctor.Fail, Detail: "drops", Fix: "ufw allow"},
			}
		},
	}
	home := t.TempDir()
	e.mux = http.NewServeMux()
	e.m.Register(e.mux, module.Deps{
		Config: pconfig.Config{Home: home, Ports: map[string]int{pconfig.ListenerAdmin: 7710, pconfig.ListenerAuth: 7701}},
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Handler: func(l string) http.Handler {
			if l == pconfig.ListenerAdmin {
				return http.NotFoundHandler()
			}
			return nil
		},
	})
	return e
}

var root = []string{"Authorization", "Bearer root"}

func decode(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("not JSON (%d): %s", w.Code, w.Body.String())
	}
	return v
}

func TestBFFRoutesAreGated(t *testing.T) {
	e := newBFF(t)
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/settings", ""}, {"GET", "/api/settings/", ""},
		{"PUT", "/api/settings/llm/llm.request_timeout_seconds", `{"value":5}`},
		{"GET", "/api/system/info", ""}, {"GET", "/api/traces", ""}, {"GET", "/api/traces/t1", ""},
		{"GET", "/api/prompt-provider", ""}, {"PUT", "/api/prompt-provider", `{"value":""}`},
		{"GET", "/api/doctor", ""}, // a superuser exists, so the doctor is gated too
	} {
		if w := send(e.mux, c.method, c.path, c.body); w.Code != 401 {
			t.Errorf("anonymous %s %s: %d", c.method, c.path, w.Code)
		}
		if w := send(e.mux, c.method, c.path, c.body, "Authorization", "Bearer member"); w.Code != 403 {
			t.Errorf("member %s %s: %d", c.method, c.path, w.Code)
		}
		if w := send(e.mux, c.method, c.path, c.body, root...); w.Code != 200 {
			t.Errorf("superuser %s %s: %d %s", c.method, c.path, w.Code, w.Body.String())
		} else if w.Header().Get("Content-Security-Policy") == "" {
			t.Errorf("%s %s: no security headers", c.method, c.path)
		}
	}
	if v, _ := e.llm.Get(context.Background(), "llm.request_timeout_seconds", settings.Scope{}); v.Value != int64(5) {
		t.Fatalf("anonymous or member write landed, or the superuser's didn't: %v", v.Value)
	}
}

func TestSettingsAggregator(t *testing.T) {
	e := newBFF(t)
	ctx := context.Background()
	e.llm.Set(ctx, "llm.hf_token", "hf_secret", settings.Scope{})

	for _, path := range []string{"/api/settings", "/api/settings/"} {
		out := decode(t, send(e.mux, "GET", path, "", root...))
		svcs := out["services"].([]any)
		if out["total_services"] != 2.0 || out["successful_services"] != 2.0 || out["failed_services"] != 0.0 || len(svcs) != 2 {
			t.Fatalf("%s: %v", path, out)
		}
		// Sorted by module name; "notready" (no settings service yet) is skipped.
		first := svcs[0].(map[string]any)
		if first["service_name"] != "cc" || first["display_name"] != "Command center" || first["success"] != true ||
			first["error"] != nil || first["latency_ms"] == nil {
			t.Fatalf("cc result: %v", first)
		}
		var token map[string]any
		for _, s := range svcs[1].(map[string]any)["settings"].([]any) {
			if s.(map[string]any)["key"] == "llm.hf_token" {
				token = s.(map[string]any)
			}
		}
		if token == nil || token["value"] != "********" || token["is_secret"] != true || token["from_db"] != true {
			t.Fatalf("secret not masked: %v", token)
		}
	}
	out := decode(t, send(e.mux, "GET", "/api/settings/?service=llm", "", root...))
	if out["total_services"] != 1.0 || out["services"].([]any)[0].(map[string]any)["service_name"] != "llm" {
		t.Fatalf("filtered: %v", out)
	}
	if w := send(e.mux, "GET", "/api/settings?service=nope", "", root...); w.Code != 404 || detailOf(w) != "Service 'nope' not found" {
		t.Fatalf("unknown service: %d %s", w.Code, w.Body.String())
	}
}

func TestSettingsPut(t *testing.T) {
	e := newBFF(t)
	ctx := context.Background()
	put := func(path, body string) *httptest.ResponseRecorder {
		return send(e.mux, "PUT", path, body, append([]string{"Content-Type", "application/json"}, root...)...)
	}
	w := put("/api/settings/llm/llm.request_timeout_seconds", `{"value":90}`)
	out := decode(t, w)
	if w.Code != 200 || out["service_name"] != "llm" || out["success"] != true || out["key"] != "llm.request_timeout_seconds" ||
		out["requires_reload"] != false || out["message"] != nil || out["error"] != nil {
		t.Fatalf("put: %d %v", w.Code, out)
	}
	if v, _ := e.llm.Get(ctx, "llm.request_timeout_seconds", settings.Scope{}); v.Value != int64(90) {
		t.Fatalf("stored %v", v.Value)
	}
	if out := decode(t, put("/api/settings/llm/llm.restart_me", `{"value":true}`)); out["requires_reload"] != true ||
		!strings.Contains(out["message"].(string), "restart") {
		t.Fatalf("reload message: %v", out)
	}
	for _, c := range []struct {
		path, body string
		code       int
	}{
		{"/api/settings/nope/x", `{"value":1}`, 404},
		{"/api/settings/llm/nope", `{"value":1}`, 404},
		{"/api/settings/llm/llm.request_timeout_seconds", `{}`, 422},
		{"/api/settings/llm/llm.request_timeout_seconds", `{"value":"ninety"}`, 422},
		{"/api/settings/llm/llm.request_timeout_seconds", `{"value":1.5}`, 422},
		{"/api/settings/llm/llm.restart_me", `{"value":"yes"}`, 422},
		{"/api/settings/cc/smarthome.manager", `{"value":"hubitat"}`, 422},
		{"/api/settings/cc/llm.prompt_provider", `{"value":"Llama2"}`, 422},
	} {
		if w := put(c.path, c.body); w.Code != c.code {
			t.Errorf("%s %s: %d %s", c.path, c.body, w.Code, w.Body.String())
		}
	}
	// null clears to the default; options are accepted.
	if w := put("/api/settings/llm/llm.request_timeout_seconds", `{"value":null}`); w.Code != 200 {
		t.Fatalf("null: %d", w.Code)
	}
	if w := put("/api/settings/cc/smarthome.manager", `{"value":"home_assistant"}`); w.Code != 200 {
		t.Fatalf("option: %d", w.Code)
	}
	// The prompt provider goes through cc's validated setter.
	if w := put("/api/settings/cc/llm.prompt_provider", `{"value":"ChatGPTOpenAI"}`); w.Code != 200 || e.prompts.value != "ChatGPTOpenAI" {
		t.Fatalf("prompt provider via settings: %d %q", w.Code, e.prompts.value)
	}
}

func TestSystemInfo(t *testing.T) {
	e := newBFF(t)
	out := decode(t, send(e.mux, "GET", "/api/system/info", "", root...))
	for _, k := range []string{"hostname", "platform", "release", "cpuCount", "totalMemoryMb", "version", "uptime",
		"arch", "go_version", "started_at", "home", "db_bytes", "disk_free_bytes", "listeners"} {
		if _, ok := out[k]; !ok {
			t.Errorf("no %s: %v", k, out)
		}
	}
	if out["version"] != "1.2.3" || out["cpuCount"].(float64) < 1 || out["uptime"].(float64) < 0 {
		t.Fatalf("values: %v", out)
	}
	ls := out["listeners"].([]any)
	if len(ls) != 2 || ls[0].(map[string]any)["name"] != "auth" || ls[0].(map[string]any)["served"] != false ||
		ls[1].(map[string]any)["name"] != "admin" || ls[1].(map[string]any)["served"] != true {
		t.Fatalf("listeners: %v", ls)
	}
}

func TestTracesBFF(t *testing.T) {
	e := newBFF(t)
	out := decode(t, send(e.mux, "GET", "/api/traces?limit=10&offset=20&status=error&source=node&household_id=h&node_id=n", "", root...))
	if out["total"] != 7.0 || len(out["traces"].([]any)) != 1 {
		t.Fatalf("list: %v", out)
	}
	want := ccmod.TraceFilter{Limit: 10, Offset: 20, Status: "error", Source: "node", HouseholdID: "h", NodeID: "n"}
	if e.traces.last != want {
		t.Fatalf("filter %+v", e.traces.last)
	}
	if w := send(e.mux, "GET", "/api/traces?limit=500", "", root...); w.Code != 400 || decode(t, w)["error"] != "validation_error" {
		t.Fatalf("bad limit: %d %s", w.Code, w.Body.String())
	}
	if d := decode(t, send(e.mux, "GET", "/api/traces/t1", "", root...)); d["id"] != "t1" || len(d["spans"].([]any)) != 1 {
		t.Fatalf("detail: %v", d)
	}
	if w := send(e.mux, "GET", "/api/traces/nope", "", root...); w.Code != 404 || detailOf(w) != "Trace not found" {
		t.Fatalf("missing: %d %s", w.Code, w.Body.String())
	}
	e.m.Traces = nil
	if w := send(e.mux, "GET", "/api/traces", "", root...); w.Code != 503 {
		t.Fatalf("no cc: %d", w.Code)
	}
}

func TestPromptProviderBFF(t *testing.T) {
	e := newBFF(t)
	if out := decode(t, send(e.mux, "GET", "/api/prompt-provider", "", root...)); out["source"] != "model" ||
		out["effective"] != "Qwen3_8B_Compressed" || len(out["options"].([]any)) != 2 {
		t.Fatalf("derived: %v", out)
	}
	hdr := append([]string{"Content-Type", "application/json"}, root...)
	if out := decode(t, send(e.mux, "PUT", "/api/prompt-provider", `{"value":"ChatGPTOpenAI"}`, hdr...)); out["source"] != "setting" ||
		out["effective"] != "ChatGPTOpenAI" {
		t.Fatalf("override: %v", out)
	}
	for _, body := range []string{`{"value":"Llama2"}`, `{"value":5}`, `{}`} {
		if w := send(e.mux, "PUT", "/api/prompt-provider", body, hdr...); w.Code != 422 {
			t.Errorf("%s: %d", body, w.Code)
		}
	}
	if out := decode(t, send(e.mux, "PUT", "/api/prompt-provider", `{"value":null}`, hdr...)); out["source"] != "model" {
		t.Fatalf("cleared: %v", out)
	}
}

func TestDoctorBFF(t *testing.T) {
	e := newBFF(t)
	out := decode(t, send(e.mux, "GET", "/api/doctor", "", root...))
	checks := out["checks"].([]any)
	if out["status"] != "fail" || len(checks) != 2 || out["ran_at"] == "" ||
		checks[1].(map[string]any)["fix"] != "ufw allow" {
		t.Fatalf("doctor: %v", out)
	}
	send(e.mux, "GET", "/api/doctor", "", root...)
	if n := e.doctors.Load(); n != 1 {
		t.Fatalf("cached run re-ran: %d runs", n)
	}
	send(e.mux, "GET", "/api/doctor?refresh=true", "", root...)
	if n := e.doctors.Load(); n != 2 {
		t.Fatalf("refresh didn't re-run: %d runs", n)
	}
	// Before the first superuser exists, the wizard's Check step reads it anonymously.
	e.accounts.c = authmod.SetupCounts{}
	if w := send(e.mux, "GET", "/api/doctor", ""); w.Code != 200 {
		t.Fatalf("pre-setup doctor: %d", w.Code)
	}
}

func TestSetupState(t *testing.T) {
	e := newBFF(t)
	// Fresh install: the reduced view, open, with where to find the token.
	e.accounts.c = authmod.SetupCounts{}
	out := decode(t, send(e.mux, "GET", "/api/setup/state", ""))
	if out["needs_superuser"] != true || out["setup_token_required"] != true || out["version"] != "1.2.3" ||
		out["superuser"] != false || !strings.HasSuffix(out["setup_token_file"].(string), authmod.SetupTokenFile) {
		t.Fatalf("fresh: %v", out)
	}
	if _, leaked := out["labels"]; leaked {
		t.Fatalf("anonymous view has labels: %v", out)
	}

	e.accounts.c = authmod.SetupCounts{Superusers: 1, Households: 2, Nodes: 3}
	for _, hdr := range [][]string{nil, {"Authorization", "Bearer member"}, {"Authorization", "Bearer forged"}} {
		w := send(e.mux, "GET", "/api/setup/state", "", hdr...)
		out := decode(t, w)
		if w.Code != 200 || out["needs_superuser"] != false || out["superuser"] != false || out["setup_token_file"] != nil || out["labels"] != nil {
			t.Fatalf("reduced view %v: %d %v", hdr, w.Code, out)
		}
	}
	out = decode(t, send(e.mux, "GET", "/api/setup/state", "", root...))
	labels := out["labels"].(map[string]any)
	if out["superuser"] != true || labels["live"] != "ready" || out["live_ready"] != true || out["models_configured"] != true ||
		out["households"] != 2.0 || out["nodes"] != 3.0 || out["hardware_url"] != "/api/llm/v1/hardware" {
		t.Fatalf("full view: %v", out)
	}
	if hw := out["hardware"].(map[string]any); hw["flavours"].(map[string]any)["llama-server"] == nil {
		t.Fatalf("hardware: %v", hw)
	}
	if pp := out["prompt_provider"].(map[string]any); pp["source"] != "model" {
		t.Fatalf("prompt provider: %v", pp)
	}
	doc := out["doctor"].(map[string]any)
	if doc["status"] != "fail" || len(doc["failing"].([]any)) != 1 {
		t.Fatalf("doctor summary: %v", doc)
	}

	// No live model yet: not ready and not configured.
	e.m.Models = fakeModels{states: map[string]string{"live": llmmod.StateNotConfigured}}
	out = decode(t, send(e.mux, "GET", "/api/setup/state", "", root...))
	if out["live_ready"] != false || out["models_configured"] != false {
		t.Fatalf("unconfigured: %v", out)
	}
	// A loading model is configured but not ready.
	e.m.Models = fakeModels{states: map[string]string{"live": "starting"}}
	out = decode(t, send(e.mux, "GET", "/api/setup/state", "", root...))
	if out["live_ready"] != false || out["models_configured"] != true {
		t.Fatalf("loading: %v", out)
	}
}

func TestNeedsSuperuserFailsClosed(t *testing.T) {
	e := newBFF(t)
	e.m.Accounts = errAccounts{}
	if w := send(e.mux, "GET", "/api/doctor", ""); w.Code != 401 {
		t.Fatalf("doctor open on an accounts error: %d", w.Code)
	}
	if out := decode(t, send(e.mux, "GET", "/api/setup/state", "")); out["needs_superuser"] != false {
		t.Fatalf("setup state on an accounts error: %v", out)
	}
}

type errAccounts struct{}

func (errAccounts) SetupCounts(context.Context) (authmod.SetupCounts, error) {
	return authmod.SetupCounts{}, errors.New("db down")
}
