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
// (inventory §3.2, AD1). The admin checks the route's tier first; the target module's own
// guard still runs after it (the household checks live there).
type passRoute struct {
	pattern  string // admin mux pattern, "METHOD /api/..."
	listener string // target listener
	strip    string // admin path prefix removed...
	prefix   string // ...and replaced by this one
	tier     tier   // the zero value is tierSuperuser
}

func authRoute(pattern string, t tier) passRoute {
	return passRoute{pattern: pattern, listener: pconfig.ListenerAuth, strip: "/api", tier: t}
}

func superuserRoute(pattern string) passRoute {
	return passRoute{pattern: pattern, listener: pconfig.ListenerAuth, strip: "/api/admin", prefix: "/superuser"}
}

func llmRoute(pattern string) passRoute {
	return passRoute{pattern: pattern, listener: pconfig.ListenerLLM, strip: "/api/llm"}
}

// ccUserRoute is a cc route a household member uses (webapp 01 §2.2). cc checks the
// membership and that the node belongs to the household.
func ccUserRoute(pattern string) passRoute {
	return passRoute{pattern: pattern, listener: pconfig.ListenerCC, strip: "/api/cc", tier: tierUser}
}

// inboxRoute is a notifications inbox route (webapp 01 §2.2, Q2): the rows are the caller's own.
func inboxRoute(pattern string) passRoute {
	return passRoute{pattern: pattern, listener: pconfig.ListenerNotifications, strip: "/api/notifications", tier: tierUser}
}

// passRoutes is the pass-through allow-list (I8): exact routes, never a wildcard over a
// module's surface. Each carries its tier (docs/webapp/01 §2.2); a route without one is
// superuser.
var passRoutes = []passRoute{
	// Bootstrap: logging in, refreshing, logging out (it only revokes the refresh token it is
	// given) and first-superuser setup (which needs the setup token, AD2).
	authRoute("POST /api/auth/login", tierOpen),
	authRoute("POST /api/auth/refresh", tierOpen),
	authRoute("POST /api/auth/logout", tierOpen),
	authRoute("GET /api/auth/setup-status", tierOpen),
	authRoute("POST /api/auth/setup", tierOpen),
	// The caller's own account (G10). change-password is open to every user so a
	// must-change-password session can always finish (G8).
	authRoute("GET /api/auth/me", tierUser),
	authRoute("PATCH /api/auth/me", tierUser), // display name
	authRoute("POST /api/auth/change-password", tierUser),
	// The caller's own households: auth lists only their memberships and switches only into one.
	authRoute("GET /api/households", tierUser),
	authRoute("POST /api/auth/switch-household", tierUser),

	// Today's SPA paths for the cross-household views (G7: every user and email, and resetting
	// anyone's password). auth's own superuser guard backs these up.
	superuserRoute("GET /api/admin/households"),
	superuserRoute("GET /api/admin/users"),
	superuserRoute("GET /api/admin/nodes"),
	superuserRoute("POST /api/admin/users/{user_id}/temp-password"),

	// The model manager (docs/llm/06 §5; G6: disk, GPU and the live model for everyone).
	llmRoute("GET /api/llm/v1/hardware"),
	llmRoute("POST /api/llm/v1/hardware/engines"),
	llmRoute("POST /api/llm/v1/hardware/engines/{name}/retry"),
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

	// Registry reads (internal URLs; no member need); writes go through the BFF.
	{pattern: "GET /api/config/services", listener: pconfig.ListenerConfig, strip: "/api/config"},
	{pattern: "GET /api/config/services/health", listener: pconfig.ListenerConfig, strip: "/api/config"},

	// Node liveness: the Nodes page and the chat node picker. cc lists a non-superuser only the
	// nodes of their own households, and checks ?household_id= membership (G9: the picker
	// passes the active household, which also scopes a superuser's list).
	{pattern: "GET /api/cc/api/v0/display/config/{scope}", listener: pconfig.ListenerCC, strip: "/api/cc"},
	{pattern: "PUT /api/cc/api/v0/display/config/{scope}", listener: pconfig.ListenerCC, strip: "/api/cc"},
	{pattern: "GET /api/cc/api/v0/display/assets", listener: pconfig.ListenerCC, strip: "/api/cc"},
	{pattern: "POST /api/cc/api/v0/display/assets", listener: pconfig.ListenerCC, strip: "/api/cc"},
	{pattern: "GET /api/cc/api/v0/display/assets/{asset_id}", listener: pconfig.ListenerCC, strip: "/api/cc"},
	{pattern: "DELETE /api/cc/api/v0/display/assets/{asset_id}", listener: pconfig.ListenerCC, strip: "/api/cc"},
	ccUserRoute("GET /api/cc/api/v0/admin/nodes"),
	// Chat (webapp 01 §3.1): cc's mobile chat, unchanged. A node of another household is 404.
	ccUserRoute("POST /api/cc/api/v0/mobile/chat"),
	ccUserRoute("POST /api/cc/api/v0/mobile/chat/warmup"),
	ccUserRoute("GET /api/cc/api/v0/mobile/chat/capabilities"),
	ccUserRoute("GET /api/cc/api/v0/mobile/nodes/{node_id}/tools"),
	ccUserRoute("POST /api/cc/api/v0/nodes/{node_id}/actions"),
	// The household's primary node for the picker's default (§3.3).
	ccUserRoute("GET /api/cc/api/v0/households/{household_id}/smart-home/config"),
	// The household's prompt (Models page editor): cc requires the household admin role.
	ccUserRoute("GET /api/cc/api/v0/households/{household_id}/prompt"),
	ccUserRoute("PUT /api/cc/api/v0/households/{household_id}/prompt"),
	ccUserRoute("POST /api/cc/api/v0/households/{household_id}/prompt/reset"),
	ccUserRoute("PUT /api/cc/api/v0/households/{household_id}/prompt/base"),
	ccUserRoute("POST /api/cc/api/v0/households/{household_id}/prompt/import"),
	ccUserRoute("POST /api/cc/api/v0/households/{household_id}/prompt/preview"),
	// Household settings (Account page): cc lets any member read, only a household admin write.
	ccUserRoute("GET /api/cc/api/v0/mobile/household/{household_id}/settings"),
	ccUserRoute("PUT /api/cc/api/v0/mobile/household/{household_id}/settings/{key...}"),
	// Device-code pairing approval (install plan 1.1; webapp 01 §2.2 Add-ons row): cc counts what
	// waits (never listing codes) plus the caller's own households, and approves a typed code
	// only into one of those.
	ccUserRoute("GET /api/cc/api/v0/nodes/pairings"),
	ccUserRoute("POST /api/cc/api/v0/nodes/pairings/approve"),
	ccUserRoute("POST /api/cc/api/v0/nodes/pairings/deny"),
	// Add-ons: sign a device out (decision 2026-10-14). cc lets only the member who paired it,
	// or an admin of its household, do it; a desktop agent gets no factory reset.
	ccUserRoute("POST /api/cc/api/v0/nodes/{node_id}/sign-out"),
	// Add-ons and chat: which of the caller's own agents runs on the computer asking (install
	// plan §3.2 Q2 (a)). A hint that grants nothing; cc matches the TCP peer address, which pass
	// keeps (G11), never a forwarded header.
	{pattern: "GET /api/addons/this-device", listener: pconfig.ListenerCC, strip: "/api/addons", prefix: "/api/v0/nodes", tier: tierUser},
	// Taught workflows and recordings (Workflows and Recordings pages, plan P3 piece 26): cc shows a
	// workflow to its owner, and to the household when shared; only the owner or a household
	// admin changes it. A recording is its owner's alone.
	ccUserRoute("GET /api/cc/api/v0/workflows"),
	ccUserRoute("GET /api/cc/api/v0/workflows/{id}"),
	ccUserRoute("PUT /api/cc/api/v0/workflows/{id}"),
	ccUserRoute("DELETE /api/cc/api/v0/workflows/{id}"),
	ccUserRoute("POST /api/cc/api/v0/workflows/{id}/run"),
	ccUserRoute("GET /api/cc/api/v0/recordings"),
	ccUserRoute("GET /api/cc/api/v0/recordings/{id}"),
	ccUserRoute("DELETE /api/cc/api/v0/recordings/{id}"),
	ccUserRoute("GET /api/cc/api/v0/recordings/{id}/keyframes/{seq}"),
	ccUserRoute("GET /api/cc/api/v0/recordings/{id}/keyframes/{seq}/crop"),

	// Inbox (webapp 01 P5): the caller's own rows. Creating and updating items is app-to-app
	// only and not exposed.
	inboxRoute("GET /api/notifications/api/v0/inbox"),
	inboxRoute("GET /api/notifications/api/v0/inbox/unread-count"),
	inboxRoute("POST /api/notifications/api/v0/inbox/bulk/read"),
	inboxRoute("POST /api/notifications/api/v0/inbox/bulk/delete"),
	inboxRoute("GET /api/notifications/api/v0/inbox/{item_id}"),
	inboxRoute("GET /api/notifications/api/v0/inbox/{item_id}/attachments/{attachment_id}"),
	inboxRoute("PATCH /api/notifications/api/v0/inbox/{item_id}/read"),
	inboxRoute("DELETE /api/notifications/api/v0/inbox/{item_id}"),
}

// forwardedCredentials are headers a pass route never forwards to the target module. The
// gateway authorises the bearer token; the module behind must see that same principal, not a
// node key, app credentials or an admin key riding along (cc's provisioning routes would
// otherwise prefer an X-API-Key over the JWT the tier checked).
var forwardedCredentials = []string{"X-API-Key", "X-Jarvis-App-Id", "X-Jarvis-App-Key", "X-Jarvis-Admin-Token"}

// mountGateway registers the BFF and pass-through routes, each behind its tier, and the
// superuser-gated /api 404 behind them.
func (m *Module) mountGateway(mux *http.ServeMux, deps module.Deps) {
	verify := m.Verify
	if verify == nil {
		// Not wired (tests of the static side): no token verifies.
		verify = func(context.Context, string) (authn.User, error) { return authn.User{}, errNoVerifier }
	}
	m.verify = verify
	m.gate = settings.SuperuserGuard(verify)
	m.mountBFF(mux, deps)
	for _, rt := range passRoutes {
		m.handle(mux, rt.pattern, rt.tier, pass(deps, rt))
	}
	// Anything else under /api is superuser-gated first, so an anonymous caller or a member
	// can't probe the surface, then a JSON 404: /api must never fall through to the SPA (I6).
	notFound := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		httpx.Error(w, http.StatusNotFound, "Not Found")
	})
	m.handle(mux, "/api", tierSuperuser, notFound)
	m.handle(mux, "/api/", tierSuperuser, notFound)
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
		// The clone keeps r.RemoteAddr, the TCP peer. G11: "this computer" detection
		// (GET /api/addons/this-device) must use that, never X-Forwarded-For, except from a
		// loopback peer (the tunnel); TestPassKeepsThePeerAddress pins it.
		r2 := r.Clone(r.Context())
		if rt.tier != tierOpen {
			for _, h := range forwardedCredentials {
				r2.Header.Del(h)
			}
		}
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
