// Package config is the jarvisd config module: the service registry that every Jarvis client
// reads first (jarvis-config-service, legacy port 7700). Mobile finds it over mDNS, checks
// GET /info, then reads GET /services for every other service's URL.
//
// jarvisd registers its own modules here at startup (same host, legacy ports), so discovery
// stays correct with no setup. Rows for things outside the process (jarvis-web, the
// osx-api, Python services during the strangler migration, GPU satellites) stay editable
// through the admin routes as before.
package config

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	pconfig "github.com/alexberardi/jarvis-server/internal/platform/config"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
	"github.com/alexberardi/jarvis-server/internal/platform/mdns"
	"github.com/alexberardi/jarvis-server/internal/platform/module"
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

// ServiceNames maps each jarvisd listener to the name clients look it up by.
var ServiceNames = map[string]string{
	pconfig.ListenerConfig:        "jarvis-config-service",
	pconfig.ListenerAuth:          "jarvis-auth",
	pconfig.ListenerLogs:          "jarvis-logs",
	pconfig.ListenerCC:            "jarvis-command-center",
	pconfig.ListenerLLM:           "jarvis-llm-proxy-api",
	pconfig.ListenerSTT:           "jarvis-whisper-api",
	pconfig.ListenerTTS:           "jarvis-tts",
	pconfig.ListenerNotifications: "jarvis-notifications",
	pconfig.ListenerRecipes:       "jarvis-recipes-server",
	pconfig.ListenerOCR:           "jarvis-ocr-service",
	pconfig.ListenerAdmin:         "jarvis-admin",
}

// Definitions are config-service's settings. Health probes use health_check.timeout.
var Definitions = []settings.Definition{
	{Key: "health_check.timeout", Category: "health_check", Type: settings.Float, Default: 5.0,
		Description: "Timeout in seconds for health check requests"},
	// Kept for /settings parity (admin lists it); nothing reads it, as in the legacy service.
	{Key: "health_check.enabled", Category: "health_check", Type: settings.Bool, Default: true,
		Description: "Whether health checks are enabled"},
}

// Module is the config module.
type Module struct {
	// Served lists the listeners jarvisd itself serves; their registry rows are kept in
	// sync at startup. Set by main from the module list.
	Served []string
	// AdminToken guards the registry write routes (legacy JARVIS_CONFIG_ADMIN_TOKEN).
	AdminToken string
	// HealthTimeout bounds each health probe (legacy health_check.timeout, default 5 s).
	HealthTimeout time.Duration
	// Advertise announces _jarvis-config._tcp over mDNS so the mobile app finds this server.
	Advertise bool
	// SettingsGuard protects /settings (legacy: superuser JWT for reads and writes). Nil
	// leaves /settings unmounted.
	SettingsGuard settings.Guard
	// MQTTPort is the embedded broker's TCP port (cc module); when set, it is registered as
	// jarvis-mqtt-broker (scheme mqtt), which nodes resolve the broker URL from.
	MQTTPort int
	// External lists registry rows for services jarvisd doesn't serve but clients resolve
	// through /services, by name → base URL (e.g. jarvis-pantry from cc's pantry.base_url,
	// D48). Synced at startup.
	External func(ctx context.Context) map[string]string

	settings *settings.Service

	deps   module.Deps
	client *http.Client
}

func (m *Module) Name() string      { return "config" }
func (m *Module) Listener() string  { return pconfig.ListenerConfig }
func (m *Module) Migrations() fs.FS { return Migrations() }

// Settings is the module's settings service (valid after Register), for the admin aggregator.
func (m *Module) Settings() *settings.Service { return m.settings }

func (m *Module) Register(mux *http.ServeMux, deps module.Deps) {
	m.deps = deps
	if m.HealthTimeout <= 0 {
		m.HealthTimeout = 5 * time.Second
	}
	m.client = &http.Client{Timeout: m.HealthTimeout}
	svc, err := settings.New(deps.DB, "config", Definitions, deps.Log)
	if err != nil {
		panic(err) // static definitions
	}
	m.settings = svc
	if m.SettingsGuard != nil {
		svc.Mount(mux, m.SettingsGuard, m.SettingsGuard)
	}

	mux.HandleFunc("GET /info", func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"service": "jarvis-config-service"})
	})
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	})
	mux.HandleFunc("GET /services", m.list)
	mux.HandleFunc("GET /services/health", m.healthAll)
	mux.HandleFunc("GET /services/{name}", m.get)
	mux.HandleFunc("GET /services/{name}/health", m.healthOne)
	mux.HandleFunc("POST /services", m.admin(m.create))
	mux.HandleFunc("PUT /services/{name}", m.admin(m.update))
	mux.HandleFunc("DELETE /services/{name}", m.admin(m.delete))
}

// Start registers jarvisd's own listeners in the registry and starts the mDNS advertisement.
func (m *Module) Start(ctx context.Context) error {
	if err := m.settings.Migrate(ctx); err != nil {
		return err
	}
	if err := m.syncSelf(ctx); err != nil {
		return err
	}
	if port := m.deps.Config.Ports[pconfig.ListenerConfig]; m.Advertise && port != 0 {
		// Best effort: a host without multicast still serves; the app can be pointed at it.
		if _, err := mdns.Advertise(ctx, mdns.Options{Port: port, Logger: m.deps.Log}); err != nil {
			m.deps.Log.Warn("mdns advertisement failed; the app will need the server address", "err", err)
		}
	}
	return nil
}

// syncSelf upserts a row per served listener: host "localhost" (the URL-style rewrite turns it
// into host.docker.internal or the caller's remote_host) and the listener's port. ids and
// created_at survive restarts; external_* are left for the operator.
func (m *Module) syncSelf(ctx context.Context) error {
	for _, l := range m.Served {
		name, ok := ServiceNames[l]
		if !ok {
			continue
		}
		port := m.deps.Config.Ports[l]
		if port == 0 {
			continue // ephemeral (tests); nothing stable to advertise
		}
		_, err := m.deps.DB.Write.ExecContext(ctx, `
			INSERT INTO config_services (name, host, port, scheme, health_path, description)
			VALUES (?, 'localhost', ?, 'http', '/health', 'served by jarvisd')
			ON CONFLICT (name) DO UPDATE SET host = 'localhost', port = excluded.port, scheme = 'http',
				health_path = '/health', description = 'served by jarvisd',
				updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
			WHERE config_services.host != 'localhost' OR config_services.port != excluded.port
				OR config_services.scheme != 'http'`,
			name, port)
		if err != nil {
			return fmt.Errorf("config: register %s: %w", name, err)
		}
	}
	if m.External != nil {
		for name, raw := range m.External(ctx) {
			if err := m.syncExternal(ctx, name, raw); err != nil {
				return err
			}
		}
	}
	if m.MQTTPort > 0 {
		_, err := m.deps.DB.Write.ExecContext(ctx, `
			INSERT INTO config_services (name, host, port, scheme, health_path, description)
			VALUES ('jarvis-mqtt-broker', 'localhost', ?, 'mqtt', NULL, 'embedded MQTT broker (jarvisd)')
			ON CONFLICT (name) DO UPDATE SET host = 'localhost', port = excluded.port, scheme = 'mqtt',
				health_path = NULL, description = excluded.description,
				updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
			WHERE config_services.host != 'localhost' OR config_services.port != excluded.port
				OR config_services.scheme != 'mqtt'`, m.MQTTPort)
		if err != nil {
			return fmt.Errorf("config: register jarvis-mqtt-broker: %w", err)
		}
	}
	return nil
}

// syncExternal upserts an external service row from its base URL. A malformed URL is logged
// and skipped: it must not stop startup.
func (m *Module) syncExternal(ctx context.Context, name, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		m.deps.Log.Warn("config: skipping external service with a bad URL", "name", name, "url", raw)
		return nil
	}
	port, _ := strconv.Atoi(u.Port())
	if port == 0 {
		port = map[string]int{"http": 80, "https": 443}[u.Scheme]
	}
	_, err = m.deps.DB.Write.ExecContext(ctx, `
		INSERT INTO config_services (name, host, port, scheme, health_path, description)
		VALUES (?, ?, ?, ?, '/health', 'external (jarvisd setting)')
		ON CONFLICT (name) DO UPDATE SET host = excluded.host, port = excluded.port, scheme = excluded.scheme,
			description = excluded.description, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		WHERE config_services.host != excluded.host OR config_services.port != excluded.port
			OR config_services.scheme != excluded.scheme`,
		name, u.Hostname(), port, u.Scheme)
	if err != nil {
		return fmt.Errorf("config: register %s: %w", name, err)
	}
	return nil
}

// --- service rows ---

type service struct {
	ID           int64
	Name         string
	Host         string
	Port         int
	Scheme       string
	HealthPath   sql.NullString
	Description  sql.NullString
	ExternalHost sql.NullString
	ExternalPort sql.NullInt64
	CreatedAt    sql.NullString
	UpdatedAt    sql.NullString
}

const serviceCols = `id, name, host, port, scheme, health_path, description, external_host, external_port, created_at, updated_at`

func scanService(sc interface{ Scan(...any) error }) (service, error) {
	var s service
	err := sc.Scan(&s.ID, &s.Name, &s.Host, &s.Port, &s.Scheme, &s.HealthPath, &s.Description,
		&s.ExternalHost, &s.ExternalPort, &s.CreatedAt, &s.UpdatedAt)
	return s, err
}

// urlStyle mirrors the legacy UrlStyle enum.
type urlStyle struct {
	dockerized bool
	external   bool
	remoteHost string
}

func isLocal(h string) bool { return h == "localhost" || h == "127.0.0.1" }

func (s service) url(st urlStyle) string {
	host, port := s.Host, s.Port
	if st.external {
		if s.ExternalHost.Valid && s.ExternalHost.String != "" {
			host = s.ExternalHost.String
		}
		if s.ExternalPort.Valid && s.ExternalPort.Int64 != 0 {
			port = int(s.ExternalPort.Int64)
		}
	}
	switch {
	case st.remoteHost != "" && isLocal(host):
		host = st.remoteHost
	case st.dockerized && isLocal(host):
		host = "host.docker.internal"
	}
	return fmt.Sprintf("%s://%s:%d", s.Scheme, host, port)
}

func (s service) healthURL() string {
	p := "/health"
	if s.HealthPath.Valid {
		p = s.HealthPath.String
	}
	return s.url(urlStyle{}) + p
}

func nullable[T any](v T, ok bool) any {
	if !ok {
		return nil
	}
	return v
}

// pyTime renders a stored ISO-8601 UTC timestamp the way the legacy service did: a naive
// datetime with microseconds ("2026-10-06T22:12:31.921459"). Clients parse it as-is.
func pyTime(ns sql.NullString) any {
	if !ns.Valid || ns.String == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, ns.String); err == nil {
			t = t.UTC()
			if t.Nanosecond() == 0 {
				return t.Format("2006-01-02T15:04:05")
			}
			return t.Format("2006-01-02T15:04:05.000000")
		}
	}
	return ns.String
}

func (s service) response(st urlStyle) map[string]any {
	return map[string]any{
		"id":            s.ID,
		"name":          s.Name,
		"host":          s.Host,
		"port":          s.Port,
		"scheme":        s.Scheme,
		"health_path":   nullable(s.HealthPath.String, s.HealthPath.Valid),
		"description":   nullable(s.Description.String, s.Description.Valid),
		"external_host": nullable(s.ExternalHost.String, s.ExternalHost.Valid),
		"external_port": nullable(s.ExternalPort.Int64, s.ExternalPort.Valid),
		"url":           s.url(st),
		"created_at":    pyTime(s.CreatedAt),
		"updated_at":    pyTime(s.UpdatedAt),
	}
}

// parseStyle reads ?style=&remote_host=. An unknown style is FastAPI's enum 422.
func parseStyle(w http.ResponseWriter, r *http.Request) (urlStyle, bool) {
	q := r.URL.Query()
	remote := q.Get("remote_host")
	switch style := q.Get("style"); style {
	case "", "default":
		return urlStyle{}, true
	case "dockerized":
		return urlStyle{dockerized: true}, true
	case "external", "remote":
		// Both use the published coordinates and swap a localhost host for the caller's.
		return urlStyle{external: true, remoteHost: remote}, true
	default:
		httpx.ValidationError(w, httpx.FieldError{
			Type:  "enum",
			Loc:   []any{"query", "style"},
			Msg:   "Input should be 'default', 'dockerized', 'remote' or 'external'",
			Input: style,
		})
		return urlStyle{}, false
	}
}

func (m *Module) all(ctx context.Context) ([]service, error) {
	rows, err := m.deps.DB.Read.QueryContext(ctx, `SELECT `+serviceCols+` FROM config_services ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []service
	for rows.Next() {
		s, err := scanService(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (m *Module) byName(ctx context.Context, name string) (service, error) {
	return scanService(m.deps.DB.Read.QueryRowContext(ctx,
		`SELECT `+serviceCols+` FROM config_services WHERE name = ?`, name))
}

func notFound(w http.ResponseWriter, name string) {
	httpx.Error(w, http.StatusNotFound, fmt.Sprintf("Service '%s' not found", name))
}

func (m *Module) internalError(w http.ResponseWriter, err error) {
	m.deps.Log.Error("config: database error", "err", err)
	httpx.Error(w, http.StatusInternalServerError, "Internal Server Error")
}

func (m *Module) list(w http.ResponseWriter, r *http.Request) {
	st, ok := parseStyle(w, r)
	if !ok {
		return
	}
	svcs, err := m.all(r.Context())
	if err != nil {
		m.internalError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(svcs))
	for _, s := range svcs {
		out = append(out, s.response(st))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"services": out})
}

func (m *Module) get(w http.ResponseWriter, r *http.Request) {
	st, ok := parseStyle(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	s, err := m.byName(r.Context(), name)
	if errors.Is(err, sql.ErrNoRows) {
		notFound(w, name)
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, s.response(st))
}

// --- health probes ---

type healthStatus struct {
	Healthy   bool     `json:"healthy"`
	LatencyMS *float64 `json:"latency_ms"`
	Error     *string  `json:"error"`
}

func (m *Module) probe(ctx context.Context, s service) healthStatus {
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.healthURL(), nil)
	if err != nil {
		e := err.Error()
		return healthStatus{Error: &e}
	}
	client := m.client
	if secs := m.settings.Float(ctx, "health_check.timeout", settings.Scope{}); secs > 0 {
		client = &http.Client{Timeout: time.Duration(secs * float64(time.Second))}
	}
	res, err := client.Do(req)
	if err != nil {
		var e string
		switch {
		case isTimeout(err):
			e = "Timeout"
		case strings.Contains(err.Error(), "connection refused"):
			e = "Connection refused"
		default:
			e = err.Error()
		}
		return healthStatus{Error: &e}
	}
	res.Body.Close()
	lat := float64(time.Since(start).Microseconds()) / 1000
	lat = float64(int64(lat*100+0.5)) / 100
	if res.StatusCode == http.StatusOK {
		return healthStatus{Healthy: true, LatencyMS: &lat}
	}
	e := fmt.Sprintf("HTTP %d", res.StatusCode)
	return healthStatus{LatencyMS: &lat, Error: &e}
}

func isTimeout(err error) bool {
	var t interface{ Timeout() bool }
	return errors.As(err, &t) && t.Timeout()
}

func (m *Module) healthAll(w http.ResponseWriter, r *http.Request) {
	svcs, err := m.all(r.Context())
	if err != nil {
		m.internalError(w, err)
		return
	}
	results := make(map[string]healthStatus, len(svcs))
	healthy := 0
	type res struct {
		name string
		h    healthStatus
	}
	ch := make(chan res, len(svcs))
	for _, s := range svcs {
		go func() { ch <- res{s.Name, m.probe(r.Context(), s)} }()
	}
	for range svcs {
		x := <-ch
		results[x.name] = x.h
		if x.h.Healthy {
			healthy++
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"services": results, "healthy_count": healthy, "total_count": len(svcs),
	})
}

func (m *Module) healthOne(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	s, err := m.byName(r.Context(), name)
	if errors.Is(err, sql.ErrNoRows) {
		notFound(w, name)
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, m.probe(r.Context(), s))
}

// --- admin writes ---

// placeholderTokens are shipped-template values that must never authenticate.
var placeholderTokens = map[string]bool{
	"": true, "change-me": true, "changeme": true, "change_me": true, "__set_me__": true,
	"change-me-to-something-secure": true, "change_me_config_admin_token": true,
}

func (m *Module) admin(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok, present := r.Header["X-Admin-Token"]
		if !present {
			httpx.ValidationError(w, httpx.FieldError{Type: "missing", Loc: []any{"header", "x-admin-token"}, Msg: "Field required"})
			return
		}
		if placeholderTokens[strings.ToLower(strings.TrimSpace(m.AdminToken))] {
			httpx.Error(w, http.StatusInternalServerError, "Admin token not configured on server")
			return
		}
		got := tok[0]
		if placeholderTokens[strings.ToLower(strings.TrimSpace(got))] || !authn.Equal(got, m.AdminToken) {
			httpx.Error(w, http.StatusUnauthorized, "Invalid admin token")
			return
		}
		h(w, r)
	}
}

var (
	bareHost   = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	ipv6Lit    = regexp.MustCompile(`^\[[0-9A-Fa-f:.]+\]$`)
	schemeRule = regexp.MustCompile(`^(https?|mqtts?|wss?)$`)
)

type serviceInput struct {
	Name        *string `json:"name"`
	Host        *string `json:"host"`
	Port        *int    `json:"port"`
	Scheme      *string `json:"scheme"`
	HealthPath  *string `json:"health_path"`
	Description *string `json:"description"`
}

// validate applies ServiceBase/ServiceUpdate's pydantic rules. full=true is a create.
func (in *serviceInput) validate(full bool) []httpx.FieldError {
	var errs []httpx.FieldError
	add := func(field, typ, msg string, input any) {
		errs = append(errs, httpx.FieldError{Type: typ, Loc: []any{"body", field}, Msg: msg, Input: input})
	}
	if full {
		if in.Name == nil {
			add("name", "missing", "Field required", nil)
		}
		if in.Host == nil {
			add("host", "missing", "Field required", nil)
		}
		if in.Port == nil {
			add("port", "missing", "Field required", nil)
		}
	}
	if in.Name != nil && (len(*in.Name) < 1 || len(*in.Name) > 64) {
		add("name", "string_too_short", "String should have between 1 and 64 characters", *in.Name)
	}
	if in.Host != nil {
		h := strings.TrimSpace(*in.Host)
		switch {
		case len(*in.Host) < 1 || len(*in.Host) > 255:
			add("host", "string_too_long", "String should have between 1 and 255 characters", *in.Host)
		case strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]"):
			if !ipv6Lit.MatchString(h) {
				add("host", "value_error", "Value error, invalid IPv6 host literal", *in.Host)
			}
		case !bareHost.MatchString(h):
			add("host", "value_error", "Value error, host must be a bare hostname or IP (letters, digits, '.', '-', '_') with no scheme, path, port, '@', or whitespace", *in.Host)
		default:
			*in.Host = h
		}
	}
	if in.Port != nil && (*in.Port < 1 || *in.Port > 65535) {
		add("port", "greater_than_equal", "Input should be between 1 and 65535", *in.Port)
	}
	if in.Scheme != nil && (!schemeRule.MatchString(*in.Scheme) || len(*in.Scheme) > 10) {
		add("scheme", "string_pattern_mismatch", "String should match pattern '^(https?|mqtts?|wss?)$'", *in.Scheme)
	}
	if in.HealthPath != nil && len(*in.HealthPath) > 255 {
		add("health_path", "string_too_long", "String should have at most 255 characters", *in.HealthPath)
	}
	if in.Description != nil && len(*in.Description) > 500 {
		add("description", "string_too_long", "String should have at most 500 characters", *in.Description)
	}
	return errs
}

func (m *Module) create(w http.ResponseWriter, r *http.Request) {
	var in serviceInput
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	if errs := in.validate(true); len(errs) > 0 {
		httpx.ValidationError(w, errs...)
		return
	}
	scheme, health := "http", "/health"
	if in.Scheme != nil {
		scheme = *in.Scheme
	}
	if in.HealthPath != nil {
		health = *in.HealthPath
	}
	_, err := m.deps.DB.Write.ExecContext(r.Context(), `
		INSERT INTO config_services (name, host, port, scheme, health_path, description)
		VALUES (?, ?, ?, ?, ?, ?)`, *in.Name, *in.Host, *in.Port, scheme, health, in.Description)
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		httpx.Error(w, http.StatusConflict, fmt.Sprintf("Service '%s' already exists", *in.Name))
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	s, err := m.byName(r.Context(), *in.Name)
	if err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, s.response(urlStyle{}))
}

func (m *Module) update(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var in serviceInput
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	in.Name = nil // not updatable
	if errs := in.validate(false); len(errs) > 0 {
		httpx.ValidationError(w, errs...)
		return
	}
	var sets []string
	var args []any
	set := func(col string, v any) {
		sets = append(sets, col+" = ?")
		args = append(args, v)
	}
	if in.Host != nil {
		set("host", *in.Host)
	}
	if in.Port != nil {
		set("port", *in.Port)
	}
	if in.Scheme != nil {
		set("scheme", *in.Scheme)
	}
	if in.HealthPath != nil {
		set("health_path", *in.HealthPath)
	}
	if in.Description != nil {
		set("description", *in.Description)
	}
	sets = append(sets, "updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')")
	res, err := m.deps.DB.Write.ExecContext(r.Context(),
		`UPDATE config_services SET `+strings.Join(sets, ", ")+` WHERE name = ?`, append(args, name)...)
	if err != nil {
		m.internalError(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		notFound(w, name)
		return
	}
	s, err := m.byName(r.Context(), name)
	if err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, s.response(urlStyle{}))
}

func (m *Module) delete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	res, err := m.deps.DB.Write.ExecContext(r.Context(), `DELETE FROM config_services WHERE name = ?`, name)
	if err != nil {
		m.internalError(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		notFound(w, name)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
