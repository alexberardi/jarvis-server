// Package admin is the jarvisd admin module (jarvis-admin, legacy port 7710): it serves the
// embedded admin SPA (web/admin) with client-side-route fallback, and GET /health.
//
// This is the skeleton (docs/admin/00-inventory.md §11.5, A1). /api/* answers a JSON 404
// until the same-origin gateway and BFF endpoints land (§3.2, pending AQ1).
package admin

import (
	"io/fs"
	"net/http"
	"os"
	"time"

	pconfig "github.com/alexberardi/jarvis-server/internal/platform/config"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
	"github.com/alexberardi/jarvis-server/internal/platform/module"
	adminui "github.com/alexberardi/jarvis-server/web/admin"
)

// ServiceName is the legacy service name.
const ServiceName = "jarvis-admin"

// Module is the admin module.
type Module struct {
	// UIDir serves the SPA from this directory instead of the embedded build
	// (JARVIS_ADMIN_UI_DIR, e.g. web/admin/dist/ui), so a rebuilt UI is testable without
	// rebuilding jarvisd.
	UIDir string
	// UI overrides the SPA files (tests). Nil: UIDir when set, else the embedded build.
	UI fs.FS
	// Version is reported by /health.
	Version string
}

func (m *Module) Name() string      { return "admin" }
func (m *Module) Listener() string  { return pconfig.ListenerAdmin }
func (m *Module) Migrations() fs.FS { return nil }

func (m *Module) Register(mux *http.ServeMux, deps module.Deps) {
	ui, source := m.resolveUI()
	if ui == nil {
		deps.Log.Warn("admin UI not built; serving the placeholder page", "source", source)
	} else {
		deps.Log.Info("admin UI", "source", source)
	}
	static := NewStatic(ui, adminui.Placeholder())
	built := ui != nil

	mux.Handle("GET /health", secure(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"status":    "ok",
			"timestamp": time.Now().UTC().Format(time.RFC3339),
			"version":   m.Version,
			"ui_built":  built,
		})
	})))
	// The gateway and BFF endpoints are not built yet; /api must never fall through to the SPA.
	api := secure(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		httpx.Error(w, http.StatusNotFound, "Not Found")
	}))
	mux.Handle("/api", api)
	mux.Handle("/api/", api)
	mux.Handle("/", secure(static))
}

// resolveUI picks the SPA files: the override, then UIDir, then the embedded build. It
// returns nil when only the placeholder is available, and a description of the source.
func (m *Module) resolveUI() (fs.FS, string) {
	if m.UI != nil {
		return m.UI, "override"
	}
	if m.UIDir != "" {
		dir := os.DirFS(m.UIDir)
		if _, err := fs.Stat(dir, "index.html"); err != nil {
			return nil, "JARVIS_ADMIN_UI_DIR=" + m.UIDir + " has no index.html"
		}
		return dir, "JARVIS_ADMIN_UI_DIR=" + m.UIDir
	}
	if ui, ok := adminui.UI(); ok {
		return ui, "embedded"
	}
	return nil, "embedded placeholder"
}

// csp matches the Fastify admin's policy (inventory I7). The built SPA is one external module
// script with no inline scripts; inline styles are allowed for the React runtime.
const csp = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; font-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; " +
	"base-uri 'self'; form-action 'self'; object-src 'none'"

// secure sets the admin's security headers on every response.
func secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}
