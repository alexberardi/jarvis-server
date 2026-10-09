package admin

import (
	"context"
	"errors"
	"net/http"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/doctor"
	authmod "github.com/alexberardi/jarvis-server/internal/modules/auth"
	ccmod "github.com/alexberardi/jarvis-server/internal/modules/cc"
	llmmod "github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
	"github.com/alexberardi/jarvis-server/internal/platform/module"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
	"github.com/alexberardi/jarvis-server/internal/platform/sysinfo"
)

// The BFF endpoints (inventory §6.2, A3): what the SPA needs that no module exposes to a user
// JWT. They call the modules through these interfaces, in process, never over HTTP.

// SettingsSource is a module with a settings service (Name is the module name, e.g. "llm").
type SettingsSource interface {
	Name() string
	Settings() *settings.Service
}

// TraceStore reads request traces (the cc module).
type TraceStore interface {
	ListTraces(ctx context.Context, f ccmod.TraceFilter) ([]map[string]any, int, error)
	GetTrace(ctx context.Context, id string) (map[string]any, bool, error)
}

// Accounts counts superusers, households and nodes (the auth module).
type Accounts interface {
	SetupCounts(ctx context.Context) (authmod.SetupCounts, error)
}

// Households names every household (the auth module): the settings page lists each
// household's own value of a household-scoped setting by name.
type Households interface {
	HouseholdNames(ctx context.Context) (map[string]string, error)
}

// Models reports label states and the hardware detection (the llm module).
type Models interface {
	LabelStates(ctx context.Context) map[string]string
	HardwareSummary(ctx context.Context) (llmmod.SetupHardware, bool)
	// RecentInstalls lists the latest installs, newest first (the per-job summary, AD3b).
	RecentInstalls(ctx context.Context) []llmmod.SetupInstall
}

// PromptProviders reads and overrides the prompt provider (the cc module, AD4).
type PromptProviders interface {
	PromptProvider(ctx context.Context) ccmod.PromptProviderStatus
	SetPromptProvider(ctx context.Context, name string) error
}

// processStart approximates jarvisd's start for /api/system/info's uptime.
var processStart = time.Now()

// displayNames label each module's card on the Settings page.
var displayNames = map[string]string{
	"admin":         "Admin & updates",
	"auth":          "Accounts & sign-in",
	"cc":            "Command center",
	"config":        "Service registry",
	"llm":           "Language models",
	"logs":          "Logs",
	"notifications": "Notifications",
	"ocr":           "OCR",
	"recipes":       "Recipes",
	"stt":           "Speech to text",
	"tts":           "Text to speech",
}

// mountBFF registers the BFF routes; mountGateway calls it after setting the gate.
func (m *Module) mountBFF(mux *http.ServeMux, deps module.Deps) {
	gated := func(h http.HandlerFunc) http.Handler {
		return secure(emptyJSONBody(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if m.gate(w, r) {
				h(w, r)
			}
		})))
	}
	open := func(h http.HandlerFunc) http.Handler { return secure(emptyJSONBody(h)) }

	mux.Handle("GET /api/settings", gated(m.handleSettings))
	mux.Handle("GET /api/settings/{$}", gated(m.handleSettings))
	mux.Handle("PUT /api/settings/{service}/{key...}", gated(m.handlePutSetting))
	mux.Handle("DELETE /api/settings/{service}/{key...}", gated(m.handleDeleteSetting))
	mux.Handle("GET /api/system/info", gated(m.handleSystemInfo))
	mux.Handle("GET /api/traces", gated(m.handleTraces))
	mux.Handle("GET /api/traces/{id}", gated(m.handleTrace))
	mux.Handle("GET /api/prompt-provider", gated(m.handlePromptProvider))
	mux.Handle("PUT /api/prompt-provider", gated(m.handlePutPromptProvider))
	// A4: logs (AD9), connections (AD7), the update check (AD5).
	mux.Handle("GET /api/logs", gated(m.handleLogs))
	mux.Handle("GET /api/logs/sources", gated(m.handleLogSources))
	mux.Handle("GET /api/logs/stream", gated(m.handleLogStream))
	mux.Handle("GET /api/connections", gated(m.handleConnections))
	mux.Handle("POST /api/connections/services", gated(m.handleAddService))
	mux.Handle("DELETE /api/connections/services/{name}", gated(m.handleRemoveService))
	mux.Handle("PUT /api/connections/services/{name}/public_url", gated(m.handleSetPublicURL))
	mux.Handle("POST /api/connections/apps", gated(m.handleCreateApp))
	mux.Handle("POST /api/connections/apps/{app_id}/rotate", gated(m.handleRotateApp))
	mux.Handle("POST /api/connections/apps/{app_id}/revoke", gated(m.handleRevokeApp))
	mux.Handle("GET /api/update", gated(m.handleUpdate))
	mux.Handle("POST /api/update/check", gated(m.handleUpdateCheck))
	mux.Handle("PUT /api/update/settings", gated(m.handleUpdateSettings))
	// AD5 one-click signed update, AD8 restart button.
	mux.Handle("POST /api/update/apply", gated(m.handleApply))
	mux.Handle("GET /api/update/apply", gated(m.handleApplyStatus))
	mux.Handle("POST /api/system/restart", gated(m.handleRestart))
	// AD8b: the stop button.
	mux.Handle("POST /api/system/stop", gated(m.handleStop))
	// AD3b: the wizard's Voice step.
	mux.Handle("GET /api/tts/voices", gated(m.handleVoices))
	mux.Handle("POST /api/tts/sample", gated(m.handleVoiceSample))
	// Open while no superuser exists (the wizard's Check step runs before Account), gated after.
	mux.Handle("GET /api/doctor", open(m.handleDoctor))
	// Always reachable: the reduced view anonymously, the full one with a superuser token.
	mux.Handle("GET /api/setup/state", open(m.handleSetupState))
}

func unavailable(w http.ResponseWriter, what string) {
	httpx.Error(w, http.StatusServiceUnavailable, "The "+what+" module is not running")
}

// --- settings aggregator (S6, §6.2 #2) ---

type serviceSettings struct {
	ServiceName string         `json:"service_name"`
	DisplayName string         `json:"display_name"`
	Success     bool           `json:"success"`
	Settings    []adminSetting `json:"settings"`
	Error       *string        `json:"error"`
	LatencyMS   float64        `json:"latency_ms"`
}

// adminSetting is one setting as the settings page gets it: the system-scope SettingResponse
// (its value is the default for every household), plus, for a key households may set
// (Definition.Household), each household's own value and how many households use the default.
type adminSetting struct {
	settings.SettingResponse
	HouseholdScoped        bool              `json:"household_scoped"`
	HouseholdValues        *[]householdValue `json:"household_values,omitempty"`
	HouseholdsUsingDefault *int              `json:"households_using_default,omitempty"`
}

// householdValue is one household's own value of a setting.
type householdValue struct {
	HouseholdID   string `json:"household_id"`
	HouseholdName string `json:"household_name"`
	Value         any    `json:"value"`
	UpdatedAt     string `json:"updated_at,omitempty"`
}

// settingsSources lists the ready settings services by module name.
func (m *Module) settingsSources() []SettingsSource {
	var out []SettingsSource
	seen := map[string]bool{}
	for _, s := range append([]SettingsSource{m}, m.SettingsSources...) {
		if s != nil && s.Settings() != nil && !seen[s.Name()] {
			seen[s.Name()] = true
			out = append(out, s)
		}
	}
	slices.SortFunc(out, func(a, b SettingsSource) int { return strings.Compare(a.Name(), b.Name()) })
	return out
}

func (m *Module) settingsSource(name string) SettingsSource {
	for _, s := range m.settingsSources() {
		if s.Name() == name {
			return s
		}
	}
	return nil
}

func displayName(module string) string {
	if d, ok := displayNames[module]; ok {
		return d
	}
	return module
}

// handleSettings is GET /api/settings[?service=]: every module's settings at system scope, in
// the legacy config-service gateway's AggregatedSettingsResponse shape. A setting households
// may change also carries household_values (each household with its own value, by name) and
// households_using_default.
func (m *Module) handleSettings(w http.ResponseWriter, r *http.Request) {
	want := r.URL.Query().Get("service")
	results := []serviceSettings{}
	ok := 0
	var households map[string]string // nil: no household listing (no auth module, or it failed)
	if m.Households != nil {
		h, err := m.Households.HouseholdNames(r.Context())
		if err != nil {
			if r.Context().Err() != nil {
				return
			}
			m.deps.Log.Error("admin: listing households failed", "err", err)
		} else {
			households = h
		}
	}
	for _, src := range m.settingsSources() {
		if want != "" && src.Name() != want {
			continue
		}
		start := time.Now()
		list, err := m.adminSettings(r.Context(), src.Settings(), households)
		if err != nil && r.Context().Err() != nil {
			return // the client went away (navigated off the page): nothing failed, nobody reads the reply
		}
		res := serviceSettings{ServiceName: src.Name(), DisplayName: displayName(src.Name()), Success: err == nil,
			Settings: list, LatencyMS: float64(time.Since(start).Microseconds()) / 1000}
		if err != nil {
			msg := err.Error()
			res.Error, res.Settings = &msg, []adminSetting{}
			m.deps.Log.Error("admin: listing settings failed", "service", src.Name(), "err", err)
		} else {
			ok++
		}
		results = append(results, res)
	}
	if want != "" && len(results) == 0 {
		httpx.Error(w, http.StatusNotFound, "Service '"+want+"' not found")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"services": results, "total_services": len(results),
		"successful_services": ok, "failed_services": len(results) - ok,
	})
}

// adminSettings renders svc's settings at system scope, with each household's own value of the
// household-scoped ones when households (id → name) is known. A row for a household that no
// longer exists is left out (deleting a household purges its rows).
func (m *Module) adminSettings(ctx context.Context, svc *settings.Service, households map[string]string) ([]adminSetting, error) {
	list, err := svc.List(ctx, settings.Scope{}, "")
	if err != nil {
		return nil, err
	}
	out := make([]adminSetting, len(list))
	for i, s := range list {
		out[i] = adminSetting{SettingResponse: s}
		def, _ := svc.Definition(s.Key)
		if !def.Household {
			continue
		}
		out[i].HouseholdScoped = true
		if households == nil {
			continue
		}
		rows, err := svc.HouseholdValues(ctx, s.Key)
		if err != nil {
			return nil, err
		}
		values := []householdValue{}
		for _, hv := range rows {
			name, ok := households[hv.HouseholdID]
			if !ok {
				continue
			}
			values = append(values, householdValue{HouseholdID: hv.HouseholdID, HouseholdName: name,
				Value: hv.Value, UpdatedAt: hv.UpdatedAt})
		}
		slices.SortFunc(values, func(a, b householdValue) int {
			if c := strings.Compare(strings.ToLower(a.HouseholdName), strings.ToLower(b.HouseholdName)); c != 0 {
				return c
			}
			return strings.Compare(a.HouseholdID, b.HouseholdID)
		})
		usingDefault := len(households) - len(values)
		out[i].HouseholdValues, out[i].HouseholdsUsingDefault = &values, &usingDefault
	}
	return out, nil
}

// settingTarget resolves {service}/{key...} and the optional ?household_id= of a settings
// write, writing the error itself. hh is "" for the system scope; with a household_id the key
// must be one households may change and the household must exist.
func (m *Module) settingTarget(w http.ResponseWriter, r *http.Request) (src SettingsSource, def settings.Definition, hh string, ok bool) {
	service, key := r.PathValue("service"), r.PathValue("key")
	src = m.settingsSource(service)
	if src == nil {
		httpx.Error(w, http.StatusNotFound, "Service '"+service+"' not found")
		return nil, def, "", false
	}
	def, ok = src.Settings().Definition(key)
	if !ok {
		httpx.Error(w, http.StatusNotFound, "Setting not found: "+key)
		return nil, def, "", false
	}
	q := r.URL.Query()
	if !q.Has("household_id") {
		return src, def, "", true
	}
	hh = strings.TrimSpace(q.Get("household_id"))
	if hh == "" {
		httpx.ValidationError(w, httpx.FieldError{Type: "missing", Loc: []any{"query", "household_id"},
			Msg: "household_id must not be empty", Input: q.Get("household_id")})
		return nil, def, "", false
	}
	if !def.Household {
		httpx.Error(w, http.StatusNotFound, "Setting is not household-controllable: "+key)
		return nil, def, "", false
	}
	if m.Households == nil {
		unavailable(w, "auth")
		return nil, def, "", false
	}
	names, err := m.Households.HouseholdNames(r.Context())
	if err != nil {
		m.deps.Log.Error("admin: listing households failed", "err", err)
		httpx.Error(w, http.StatusInternalServerError, "Internal server error")
		return nil, def, "", false
	}
	if _, found := names[hh]; !found {
		httpx.Error(w, http.StatusNotFound, "Household not found")
		return nil, def, "", false
	}
	return src, def, hh, true
}

// handlePutSetting is PUT /api/settings/{service}/{key...}[?household_id=] with {"value": …}:
// a write to that module's settings, answering ServiceUpdateResponse. Without household_id it
// sets the system value (the default for every household); with one, that household's own
// value of a household-scoped key, where null removes it (as DELETE does). The key is
// audit-logged, never the value.
func (m *Module) handlePutSetting(w http.ResponseWriter, r *http.Request) {
	src, def, hh, ok := m.settingTarget(w, r)
	if !ok {
		return
	}
	service, key := src.Name(), def.Key
	var body map[string]any
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	value, present := body["value"]
	if !present {
		httpx.ValidationError(w, httpx.FieldError{Type: "missing", Loc: []any{"body", "value"}, Msg: "Field required", Input: body})
		return
	}
	if msg := checkValue(def, value); msg != "" {
		httpx.ValidationError(w, httpx.FieldError{Type: "value_error", Loc: []any{"body", "value"}, Msg: msg, Input: value})
		return
	}
	// The setting's own validator (Definition.Validate), with its message.
	if err := src.Settings().Validate(key, value); err != nil {
		httpx.ValidationError(w, httpx.FieldError{Type: "value_error", Loc: []any{"body", "value"},
			Msg: settings.InvalidValueMessage(err), Input: value})
		return
	}
	var err error
	switch {
	case hh != "" && value == nil:
		// A NULL household row would hide the system value (the cascade stops at it), so null
		// means "use the default": remove the household's row.
		_, err = src.Settings().Delete(r.Context(), key, settings.Scope{HouseholdID: hh})
	case hh != "":
		err = src.Settings().Set(r.Context(), key, value, settings.Scope{HouseholdID: hh})
	case service == "cc" && key == "llm.prompt_provider" && m.Prompts != nil:
		// The one setting with a closed set of values a voice turn depends on (AD4).
		name, _ := value.(string)
		err = m.Prompts.SetPromptProvider(r.Context(), name)
		if errors.Is(err, ccmod.ErrUnknownPromptProvider) {
			httpx.ValidationError(w, httpx.FieldError{Type: "value_error", Loc: []any{"body", "value"},
				Msg: "Unknown prompt provider; one of " + strings.Join(m.Prompts.PromptProvider(r.Context()).Options, ", "), Input: value})
			return
		}
	default:
		err = src.Settings().Set(r.Context(), key, value, settings.Scope{})
	}
	if errors.Is(err, settings.ErrInvalidValue) {
		httpx.ValidationError(w, httpx.FieldError{Type: "value_error", Loc: []any{"body", "value"},
			Msg: settings.InvalidValueMessage(err), Input: value})
		return
	}
	m.settingWritten(w, service, def, hh, err)
}

// handleDeleteSetting is DELETE /api/settings/{service}/{key...}?household_id=: remove that
// household's own value so it uses the default again. Removing a value that isn't there
// succeeds.
func (m *Module) handleDeleteSetting(w http.ResponseWriter, r *http.Request) {
	if !r.URL.Query().Has("household_id") {
		httpx.ValidationError(w, httpx.FieldError{Type: "missing", Loc: []any{"query", "household_id"},
			Msg: "Field required", Input: nil})
		return
	}
	src, def, hh, ok := m.settingTarget(w, r)
	if !ok {
		return
	}
	_, err := src.Settings().Delete(r.Context(), def.Key, settings.Scope{HouseholdID: hh})
	m.settingWritten(w, src.Name(), def, hh, err)
}

// settingWritten answers a settings write with ServiceUpdateResponse (plus household_id for a
// household's value) and audit-logs the key, never the value.
func (m *Module) settingWritten(w http.ResponseWriter, service string, def settings.Definition, hh string, err error) {
	key := def.Key
	if err != nil {
		m.deps.Log.Error("admin: setting update failed", "service", service, "key", key, "household", hh, "err", err)
		out := map[string]any{
			"service_name": service, "success": false, "key": key, "requires_reload": def.RequiresReload,
			"message": nil, "error": "Failed to update setting: " + key,
		}
		if hh != "" {
			out["household_id"] = hh
		}
		httpx.WriteJSON(w, http.StatusInternalServerError, out)
		return
	}
	if hh != "" {
		m.deps.Log.Info("admin: setting changed", "service", service, "key", key, "household", hh)
	} else {
		m.deps.Log.Info("admin: setting changed", "service", service, "key", key)
	}
	var msg any
	if def.RequiresReload {
		// AQ8 (a): there is no container to restart; say so and how.
		msg = "Applies after jarvisd restarts"
	}
	out := map[string]any{
		"service_name": service, "success": true, "key": key, "requires_reload": def.RequiresReload,
		"message": msg, "error": nil,
	}
	if hh != "" {
		out["household_id"] = hh
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// checkValue rejects a value its setting's type can't hold (the module router stores anything
// and falls back to the default on read; the admin says so up front). Null clears to the
// default. Options, when declared, are the allowed values.
func checkValue(def settings.Definition, v any) string {
	if v == nil {
		return ""
	}
	switch def.Type {
	case settings.Int:
		f, ok := v.(float64)
		if !ok || f != float64(int64(f)) {
			return "Input should be a valid integer"
		}
	case settings.Float:
		if _, ok := v.(float64); !ok {
			return "Input should be a valid number"
		}
	case settings.Bool:
		if _, ok := v.(bool); !ok {
			return "Input should be a valid boolean"
		}
	case settings.String:
		if _, ok := v.(string); !ok {
			return "Input should be a valid string"
		}
	}
	if len(def.Options) > 0 && !slices.ContainsFunc(def.Options, func(o any) bool { return o == v }) {
		return "Input should be one of the setting's options"
	}
	return ""
}

// --- system info (S18, §6.2 #5) ---

type listenerInfo struct {
	Name   string `json:"name"`
	Port   int    `json:"port"`
	Served bool   `json:"served"`
}

// handleSystemInfo is GET /api/system/info: the Fastify shape (hostname, platform, release,
// cpuCount, totalMemoryMb, version, uptime in seconds) plus jarvisd's own facts.
func (m *Module) handleSystemInfo(w http.ResponseWriter, r *http.Request) {
	host, _ := os.Hostname()
	cfg := m.deps.Config
	var listeners []listenerInfo
	for name, port := range cfg.Ports {
		served := m.deps.Handler != nil && m.deps.Handler(name) != nil
		listeners = append(listeners, listenerInfo{Name: name, Port: port, Served: served})
	}
	slices.SortFunc(listeners, func(a, b listenerInfo) int {
		if a.Port != b.Port {
			return a.Port - b.Port
		}
		return strings.Compare(a.Name, b.Name)
	})
	var dbBytes int64
	if cfg.Home != "" {
		for _, suffix := range []string{"", "-wal", "-shm"} {
			if st, err := os.Stat(cfg.DBPath() + suffix); err == nil {
				dbBytes += st.Size()
			}
		}
	}
	var diskFree uint64
	if cfg.Home != "" {
		diskFree = sysinfo.DiskFree(cfg.Home)
	}
	version := m.Version
	if version == "" {
		version = "dev"
	}
	blocked, _ := m.applyBlocker(r.Context())
	selfUpdate := blocked == ""
	stop := m.stopInfo()
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"hostname":        host,
		"platform":        runtime.GOOS,
		"release":         sysinfo.Release(),
		"cpuCount":        runtime.NumCPU(),
		"totalMemoryMb":   sysinfo.TotalMemory() / (1 << 20),
		"version":         version,
		"uptime":          time.Since(processStart).Seconds(),
		"arch":            runtime.GOARCH,
		"go_version":      runtime.Version(),
		"started_at":      processStart.UTC().Format(time.RFC3339Nano),
		"home":            cfg.Home,
		"db_bytes":        dbBytes,
		"disk_free_bytes": diskFree,
		"listeners":       listeners,
		// AD8: who restarts jarvisd, and whether the restart button can.
		"supervisor":        m.supervisor(),
		"restart_supported": m.supervisor().Supervised(),
		// What the SPA may offer without probing: restart (supervised), stop (the installed
		// service definition keeps jarvisd stopped) and the one-click update (POST
		// /api/update/apply would be accepted now).
		"capabilities": map[string]bool{
			"restart":     m.supervisor().Supervised(),
			"stop":        stop.Supported,
			"self_update": selfUpdate,
		},
		// AD8b: why Stop is unavailable and the fix, and how to start jarvisd again.
		"stop": stop,
	})
}

// --- traces (S12, §6.2 #3) ---

// handleTraces is GET /api/traces: cc's trace list, unchanged in shape ({traces, total}), for a
// superuser instead of the cc admin key.
func (m *Module) handleTraces(w http.ResponseWriter, r *http.Request) {
	if m.Traces == nil {
		unavailable(w, "cc")
		return
	}
	f, problem := ccmod.ParseTraceFilter(r.URL.Query())
	if problem != "" {
		// cc's own validation shape, which the Fastify proxy passed through.
		httpx.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"error":   "validation_error",
			"message": "Request validation failed. Please correct the highlighted fields.",
			"details": []string{problem},
		})
		return
	}
	traces, total, err := m.Traces.ListTraces(r.Context(), f)
	if err != nil {
		m.deps.Log.Error("admin: listing traces failed", "err", err)
		httpx.Error(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"traces": traces, "total": total})
}

// handleTrace is GET /api/traces/{id}: one trace with its spans.
func (m *Module) handleTrace(w http.ResponseWriter, r *http.Request) {
	if m.Traces == nil {
		unavailable(w, "cc")
		return
	}
	t, found, err := m.Traces.GetTrace(r.Context(), r.PathValue("id"))
	if err != nil {
		m.deps.Log.Error("admin: reading a trace failed", "err", err)
		httpx.Error(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	if !found {
		httpx.Error(w, http.StatusNotFound, "Trace not found")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, t)
}

// --- prompt provider (AD4) ---

// handlePromptProvider is GET /api/prompt-provider: the effective provider and its source.
func (m *Module) handlePromptProvider(w http.ResponseWriter, r *http.Request) {
	if m.Prompts == nil {
		unavailable(w, "cc")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, m.Prompts.PromptProvider(r.Context()))
}

// handlePutPromptProvider is PUT /api/prompt-provider with {"value": name}: override the
// provider, or clear the override with "" or null so the live model's applies again.
func (m *Module) handlePutPromptProvider(w http.ResponseWriter, r *http.Request) {
	if m.Prompts == nil {
		unavailable(w, "cc")
		return
	}
	var body map[string]any
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	raw, present := body["value"]
	if !present {
		httpx.ValidationError(w, httpx.FieldError{Type: "missing", Loc: []any{"body", "value"}, Msg: "Field required", Input: body})
		return
	}
	name, isString := raw.(string)
	if raw != nil && !isString {
		httpx.ValidationError(w, httpx.FieldError{Type: "string_type", Loc: []any{"body", "value"}, Msg: "Input should be a valid string", Input: raw})
		return
	}
	if err := m.Prompts.SetPromptProvider(r.Context(), name); err != nil {
		if errors.Is(err, ccmod.ErrUnknownPromptProvider) {
			httpx.ValidationError(w, httpx.FieldError{Type: "value_error", Loc: []any{"body", "value"},
				Msg: "Unknown prompt provider; one of " + strings.Join(m.Prompts.PromptProvider(r.Context()).Options, ", "), Input: raw})
			return
		}
		m.deps.Log.Error("admin: setting the prompt provider failed", "err", err)
		httpx.Error(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	m.deps.Log.Info("admin: setting changed", "service", "cc", "key", "llm.prompt_provider")
	httpx.WriteJSON(w, http.StatusOK, m.Prompts.PromptProvider(r.Context()))
}

// --- doctor (§6.2 #4) ---

// doctorTTL is how long a doctor run is reused (it execs firewall tools).
const doctorTTL = 30 * time.Second

type doctorCache struct {
	mu     sync.Mutex
	checks []doctor.Check
	ranAt  time.Time
}

type doctorResult struct {
	Status doctor.Status  `json:"status"`
	Checks []doctor.Check `json:"checks"`
	RanAt  string         `json:"ran_at"`
}

// runDoctor returns the cached run, or a fresh one when it is older than doctorTTL or refresh
// is set. Concurrent callers share one run.
func (m *Module) runDoctor(ctx context.Context, refresh bool) doctorResult {
	m.doc.mu.Lock()
	defer m.doc.mu.Unlock()
	if refresh || m.doc.ranAt.IsZero() || time.Since(m.doc.ranAt) > doctorTTL {
		run := m.RunDoctor
		if run == nil {
			run = doctor.Run
		}
		m.doc.checks = run(ctx, doctor.Options{Ports: m.Exposure.Ports(m.deps.Config.Ports)})
		if m.doc.checks == nil {
			m.doc.checks = []doctor.Check{}
		}
		m.doc.ranAt = time.Now()
	}
	return doctorResult{Status: doctor.Worst(m.doc.checks), Checks: m.doc.checks, RanAt: m.doc.ranAt.UTC().Format(time.RFC3339)}
}

// needsSuperuser reports whether first-run setup is still open. Unknown (no Accounts, or a DB
// error) counts as false, which keeps the pre-setup exceptions closed.
func (m *Module) needsSuperuser(ctx context.Context) (authmod.SetupCounts, bool) {
	if m.Accounts == nil {
		return authmod.SetupCounts{}, false
	}
	c, err := m.Accounts.SetupCounts(ctx)
	if err != nil {
		m.deps.Log.Error("admin: counting accounts failed", "err", err)
		return authmod.SetupCounts{}, false
	}
	return c, c.Superusers == 0
}

// handleDoctor is GET /api/doctor[?refresh=true]: listener and host-firewall checks with the
// exact fix commands. Before the first superuser exists it is open, for the setup wizard's
// Check step; after that a superuser token is required.
func (m *Module) handleDoctor(w http.ResponseWriter, r *http.Request) {
	if _, open := m.needsSuperuser(r.Context()); !open && !m.gate(w, r) {
		return
	}
	refresh := r.URL.Query().Get("refresh")
	httpx.WriteJSON(w, http.StatusOK, m.runDoctor(r.Context(), refresh == "true" || refresh == "1"))
}

// --- setup state (S1/S3, §6.2 #6) ---

// liveStates are the live label states that answer a voice turn.
var liveStates = []string{"ready", "degraded", "remote"}

// isSuperuser reports whether the request carries a valid superuser token, without writing
// an error: /api/setup/state answers the reduced view instead.
func (m *Module) isSuperuser(r *http.Request) bool {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || m.verify == nil {
		return false
	}
	u, err := m.verify(r.Context(), tok)
	return err == nil && u.IsSuperuser
}

// handleSetupState is GET /api/setup/state, the SPA's boot gate and the setup wizard's driver
// (AD3/AD3b: Check → Account → Hardware → five model jobs → Privacy → Done). Anonymous callers get the reduced view
// (needs_superuser, setup_token_required, version); a superuser also gets label states, the
// hardware summary, the per-job model checklist (jobs), the prompt provider, a doctor summary
// and the household/node counts.
func (m *Module) handleSetupState(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	counts, needs := m.needsSuperuser(ctx)
	version := m.Version
	if version == "" {
		version = "dev"
	}
	out := map[string]any{
		"needs_superuser":      needs,
		"setup_token_required": needs, // AD2: first-run setup always needs the token
		"version":              version,
		"superuser":            false,
	}
	if needs && m.deps.Config.Home != "" {
		// Where the operator finds the token if the link's #token= was lost.
		out["setup_token_file"] = authmod.SetupTokenPath(m.deps.Config.Home)
	}
	if !m.isSuperuser(r) {
		httpx.WriteJSON(w, http.StatusOK, out)
		return
	}
	out["superuser"] = true
	out["households"], out["nodes"] = counts.Households, counts.Nodes

	labels := map[string]string{}
	var hardware any
	if m.Models != nil {
		if l := m.Models.LabelStates(ctx); l != nil {
			labels = l
		}
		if hw, ok := m.Models.HardwareSummary(ctx); ok {
			hardware = hw
		}
	}
	live := labels["live"]
	out["labels"] = labels
	out["live_ready"] = slices.Contains(liveStates, live)
	// Configured means a model is assigned, even while it is still loading.
	out["models_configured"] = live != "" && live != llmmod.StateNotConfigured
	out["hardware"] = hardware
	out["hardware_url"] = "/api/llm/v1/hardware"
	var installs []llmmod.SetupInstall
	if m.Models != nil {
		installs = m.Models.RecentInstalls(ctx)
	}
	jobs := summarizeJobs(labels, installs)
	out["jobs"] = jobs
	completed, saved := false, ""
	if m.settings != nil {
		completed = m.settings.Bool(ctx, SettingSetupCompleted, settings.Scope{})
		saved = m.settings.String(ctx, SettingSetupStep, settings.Scope{})
	}
	out["setup_completed"] = completed
	out["setup_step"] = setupStep(completed, saved, jobs)

	var prompt any
	if m.Prompts != nil {
		prompt = m.Prompts.PromptProvider(ctx)
	}
	out["prompt_provider"] = prompt

	doc := m.runDoctor(ctx, false)
	failing := []string{}
	for _, c := range doc.Checks {
		if c.Status != doctor.OK {
			failing = append(failing, c.Name)
		}
	}
	out["doctor"] = map[string]any{"status": doc.Status, "failing": failing, "ran_at": doc.RanAt}
	httpx.WriteJSON(w, http.StatusOK, out)
}
