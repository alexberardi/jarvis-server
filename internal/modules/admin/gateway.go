package admin

import (
	"bufio"
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	pconfig "github.com/alexberardi/jarvis-server/internal/platform/config"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
	"github.com/alexberardi/jarvis-server/internal/platform/module"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

var errNoVerifier = errors.New("admin: no user verifier")

// passRoute maps one admin path onto another listener's route, dispatched in process
// (inventory §3.2, AD1). The target module's own guard still runs.
type passRoute struct {
	pattern  string // admin mux pattern, "METHOD /api/..."
	listener string // target listener
	strip    string // admin path prefix removed...
	prefix   string // ...and replaced by this one
	open     bool   // bootstrap allow-list: reachable without a superuser token
}

func authRoute(pattern string, open bool) passRoute {
	return passRoute{pattern: pattern, listener: pconfig.ListenerAuth, strip: "/api", open: open}
}

func superuserRoute(pattern string) passRoute {
	return passRoute{pattern: pattern, listener: pconfig.ListenerAuth, strip: "/api/admin", prefix: "/superuser"}
}

func llmRoute(pattern string) passRoute {
	return passRoute{pattern: pattern, listener: pconfig.ListenerLLM, strip: "/api/llm"}
}

// passRoutes is the pass-through allow-list (I8): exact routes, never a wildcard over a
// module's surface.
var passRoutes = []passRoute{
	// Bootstrap: logging in, refreshing, logging out (it only revokes the refresh token it is
	// given) and first-superuser setup (which needs the setup token, AD2).
	authRoute("POST /api/auth/login", true),
	authRoute("POST /api/auth/refresh", true),
	authRoute("POST /api/auth/logout", true),
	authRoute("GET /api/auth/setup-status", true),
	authRoute("POST /api/auth/setup", true),
	authRoute("GET /api/auth/me", false),
	authRoute("POST /api/auth/change-password", false),

	// Today's SPA paths for the cross-household views.
	superuserRoute("GET /api/admin/households"),
	superuserRoute("GET /api/admin/users"),
	superuserRoute("GET /api/admin/nodes"),
	superuserRoute("POST /api/admin/users/{user_id}/temp-password"),

	// The model manager (docs/llm/06 §5).
	llmRoute("GET /api/llm/v1/hardware"),
	llmRoute("POST /api/llm/v1/hardware/engines"),
	llmRoute("GET /api/llm/v1/models/catalog"),
	llmRoute("GET /api/llm/v1/models/hf/{repo...}"),
	llmRoute("POST /api/llm/v1/models/install"),
	llmRoute("GET /api/llm/v1/models/installs"),
	llmRoute("GET /api/llm/v1/models/installs/{id}"),
	llmRoute("POST /api/llm/v1/models/installs/{id}/cancel"),
	llmRoute("GET /api/llm/v1/models/installed"),
	llmRoute("POST /api/llm/v1/models/installed"),
	llmRoute("DELETE /api/llm/v1/models/installed/{id}"),
	llmRoute("GET /api/llm/v1/models/labels"),
	llmRoute("PUT /api/llm/v1/models/labels"),

	// Registry reads; writes go through the BFF.
	{pattern: "GET /api/config/services", listener: pconfig.ListenerConfig, strip: "/api/config"},
	{pattern: "GET /api/config/services/health", listener: pconfig.ListenerConfig, strip: "/api/config"},

	// Node liveness for the Nodes page (user JWT).
	{pattern: "GET /api/cc/api/v0/admin/nodes", listener: pconfig.ListenerCC, strip: "/api/cc"},
}

// mountGateway registers the pass-through routes and the gated /api 404 behind them.
func (m *Module) mountGateway(mux *http.ServeMux, deps module.Deps) {
	verify := m.Verify
	if verify == nil {
		// Not wired (tests of the static side): no token verifies.
		verify = func(context.Context, string) (authn.User, error) { return authn.User{}, errNoVerifier }
	}
	m.verify = verify
	gate := settings.SuperuserGuard(verify)
	m.gate = gate
	gated := func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if gate(w, r) {
				h.ServeHTTP(w, r)
			}
		})
	}
	m.mountBFF(mux, deps)
	for _, rt := range passRoutes {
		h := pass(deps, rt)
		if !rt.open {
			h = gated(h)
		}
		mux.Handle(rt.pattern, secure(emptyJSONBody(h)))
	}
	// Anything else under /api is gated first, so an anonymous caller can't probe the
	// surface, then a JSON 404: /api must never fall through to the SPA (I6).
	notFound := secure(gated(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		httpx.Error(w, http.StatusNotFound, "Not Found")
	})))
	mux.Handle("/api", notFound)
	mux.Handle("/api/", notFound)
}

// pass dispatches to the target listener's mux with the path rewritten. The lookup happens
// per request: the target may register after the admin module.
func pass(deps module.Deps, rt passRoute) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var h http.Handler
		if deps.Handler != nil {
			h = deps.Handler(rt.listener)
		}
		if h == nil {
			httpx.Error(w, http.StatusServiceUnavailable, "The "+rt.listener+" module is not running")
			return
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = rt.prefix + strings.TrimPrefix(r.URL.Path, rt.strip)
		if r.URL.RawPath != "" {
			r2.URL.RawPath = rt.prefix + strings.TrimPrefix(r.URL.RawPath, rt.strip)
		}
		r2.RequestURI = r2.URL.RequestURI()
		h.ServeHTTP(w, r2)
	})
}

// emptyJSONBody treats a body-less POST/PUT/PATCH sent as application/json as `{}` (I9):
// the SPA sends arg-less POSTs that way, and FastAPI-style handlers would 422 "Field required".
func emptyJSONBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch:
		default:
			next.ServeHTTP(w, r)
			return
		}
		if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
			next.ServeHTTP(w, r)
			return
		}
		empty := r.Body == nil || r.Body == http.NoBody || r.ContentLength == 0
		if !empty && r.ContentLength < 0 {
			// Unknown length (chunked): peek one byte, keeping it for the handler.
			br := bufio.NewReader(r.Body)
			if _, err := br.Peek(1); err == io.EOF {
				empty = true
			} else {
				r.Body = readCloser{br, r.Body}
			}
		}
		if empty {
			r.Body = io.NopCloser(strings.NewReader("{}"))
			r.ContentLength = 2
		}
		next.ServeHTTP(w, r)
	})
}

type readCloser struct {
	io.Reader
	io.Closer
}
