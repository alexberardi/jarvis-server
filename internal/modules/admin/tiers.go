package admin

import (
	"net/http"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// tier is who may call an admin /api route (docs/webapp/01 §2, G1). Every BFF route, every
// pass route and the /api catch-all carries one. The zero value is tierSuperuser, so a route
// nobody classified stays superuser-only; TestEveryAPIRouteHasATier fails on it anyway.
//
// tierUser grants nothing new: it is "any valid user token", the same JWT that already reaches
// the modules directly on their own ports. The household check stays in the module the route
// reaches (cc's requireRole, auth's membership queries, notifications' per-user rows). The
// security core is the other direction: no install-wide route (settings across households,
// traces, logs, connections, update/restart/stop, the model manager, the cross-household
// account views) may be tierUser.
type tier int

const (
	tierSuperuser tier = iota // the install operator (auth_users.is_superuser)
	tierUser                  // any valid user token; the module behind checks the household
	tierOpen                  // no token: bootstrap only (login, setup, the reduced setup state)
)

func (t tier) String() string {
	switch t {
	case tierOpen:
		return "open"
	case tierUser:
		return "user"
	}
	return "superuser"
}

// apiRoute is one registered /api pattern and its tier, recorded for the tier test.
type apiRoute struct {
	pattern string
	tier    tier
}

// guard is the check a tier runs before the handler; false means it already answered.
func (m *Module) guard(t tier) func(http.ResponseWriter, *http.Request) bool {
	switch t {
	case tierOpen:
		return func(http.ResponseWriter, *http.Request) bool { return true }
	case tierUser:
		return m.userGate
	}
	return m.gate
}

// userGate admits any valid user token (tierUser): 401 without one, never 403.
func (m *Module) userGate(w http.ResponseWriter, r *http.Request) bool {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "Missing or invalid Authorization header")
		return false
	}
	if _, err := m.verify(r.Context(), tok); err != nil {
		httpx.Error(w, http.StatusUnauthorized, "Invalid or expired token")
		return false
	}
	return true
}

// handle registers an /api route behind its tier's gate and records it. Every /api route on
// the admin mux goes through here (TestAPIRoutesRegisterThroughHandle); the security headers
// and the empty-JSON-body rule (I9) apply to all of them.
func (m *Module) handle(mux *http.ServeMux, pattern string, t tier, h http.Handler) {
	if m.innerHook != nil {
		h = m.innerHook(pattern, h)
	}
	g := m.guard(t)
	inner := h
	mux.Handle(pattern, secure(emptyJSONBody(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if g(w, r) {
			inner.ServeHTTP(w, r)
		}
	}))))
	m.routes = append(m.routes, apiRoute{pattern: pattern, tier: t})
}
