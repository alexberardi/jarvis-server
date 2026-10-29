package admin

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	pconfig "github.com/alexberardi/jarvis-server/internal/platform/config"
	"github.com/alexberardi/jarvis-server/internal/platform/module"
)

// wantTiers is the tier of every /api pattern on the admin mux (docs/webapp/01 §2.2). It is
// written out here, apart from the code, on purpose: a new route fails
// TestEveryAPIRouteHasATier until someone decides its tier in this table.
var wantTiers = map[string]tier{
	"GET /api/cc/api/v0/display/config/{scope}":       tierSuperuser,
	"PUT /api/cc/api/v0/display/config/{scope}":       tierSuperuser,
	"GET /api/cc/api/v0/display/assets":               tierSuperuser,
	"POST /api/cc/api/v0/display/assets":              tierSuperuser,
	"GET /api/cc/api/v0/display/assets/{asset_id}":    tierSuperuser,
	"DELETE /api/cc/api/v0/display/assets/{asset_id}": tierSuperuser,
	// Bootstrap.
	"POST /api/auth/login":       tierOpen,
	"POST /api/auth/refresh":     tierOpen,
	"POST /api/auth/logout":      tierOpen,
	"GET /api/auth/setup-status": tierOpen,
	"POST /api/auth/setup":       tierOpen,
	"GET /api/setup/state":       tierOpen, // the reduced view; the full one needs a superuser
	"GET /api/doctor":            tierOpen, // the handler gates it once a superuser exists

	// The caller's own account and households (G10).
	"GET /api/auth/me":                tierUser,
	"PATCH /api/auth/me":              tierUser,
	"POST /api/auth/change-password":  tierUser,
	"GET /api/households":             tierUser,
	"POST /api/auth/switch-household": tierUser,

	// cc, scoped to the caller's households by cc (G9, §2.2).
	"GET /api/cc/api/v0/admin/nodes":                                 tierUser,
	"POST /api/cc/api/v0/mobile/chat":                                tierUser,
	"POST /api/cc/api/v0/mobile/chat/warmup":                         tierUser,
	"GET /api/cc/api/v0/mobile/chat/capabilities":                    tierUser,
	"GET /api/cc/api/v0/mobile/nodes/{node_id}/tools":                tierUser,
	"POST /api/cc/api/v0/nodes/{node_id}/actions":                    tierUser,
	"GET /api/cc/api/v0/households/{household_id}/smart-home/config": tierUser,
	// The household prompt: cc enforces the household admin role.
	"GET /api/cc/api/v0/households/{household_id}/prompt":          tierUser,
	"PUT /api/cc/api/v0/households/{household_id}/prompt":          tierUser,
	"POST /api/cc/api/v0/households/{household_id}/prompt/reset":   tierUser,
	"PUT /api/cc/api/v0/households/{household_id}/prompt/base":     tierUser,
	"POST /api/cc/api/v0/households/{household_id}/prompt/import":  tierUser,
	"POST /api/cc/api/v0/households/{household_id}/prompt/preview": tierUser,
	// Household settings (Account page): any member reads, cc requires the household admin
	// role to write.
	"GET /api/cc/api/v0/mobile/household/{household_id}/settings":          tierUser,
	"PUT /api/cc/api/v0/mobile/household/{household_id}/settings/{key...}": tierUser,
	"GET /api/cc/api/v0/nodes/pairings":                                    tierUser,
	"POST /api/cc/api/v0/nodes/pairings/approve":                           tierUser,
	"POST /api/cc/api/v0/nodes/pairings/deny":                              tierUser,
	// Add-ons: device sign-out (cc: the pairer or a household admin) and "this computer" (a hint).
	"POST /api/cc/api/v0/nodes/{node_id}/sign-out": tierUser,
	"GET /api/addons/this-device":                  tierUser,
	// Taught workflows and recordings: cc checks the owner, sharing and the household admin role.
	"GET /api/cc/api/v0/workflows":                            tierUser,
	"GET /api/cc/api/v0/workflows/{id}":                       tierUser,
	"PUT /api/cc/api/v0/workflows/{id}":                       tierUser,
	"DELETE /api/cc/api/v0/workflows/{id}":                    tierUser,
	"POST /api/cc/api/v0/workflows/{id}/run":                  tierUser,
	"GET /api/cc/api/v0/recordings":                           tierUser,
	"GET /api/cc/api/v0/recordings/{id}":                      tierUser,
	"DELETE /api/cc/api/v0/recordings/{id}":                   tierUser,
	"GET /api/cc/api/v0/recordings/{id}/keyframes/{seq}":      tierUser,
	"GET /api/cc/api/v0/recordings/{id}/keyframes/{seq}/crop": tierUser,
	// The desktop agent download (install plan 3.1): a public release asset, verified here.
	"GET /api/addons/agent/{os}/{arch}": tierUser,

	// The inbox: per-user rows.
	"GET /api/notifications/api/v0/inbox":                                       tierUser,
	"GET /api/notifications/api/v0/inbox/unread-count":                          tierUser,
	"POST /api/notifications/api/v0/inbox/bulk/read":                            tierUser,
	"POST /api/notifications/api/v0/inbox/bulk/delete":                          tierUser,
	"GET /api/notifications/api/v0/inbox/{item_id}":                             tierUser,
	"GET /api/notifications/api/v0/inbox/{item_id}/attachments/{attachment_id}": tierUser,
	"PATCH /api/notifications/api/v0/inbox/{item_id}/read":                      tierUser,
	"DELETE /api/notifications/api/v0/inbox/{item_id}":                          tierUser,

	// G7: every user and email; resetting anyone's password.
	"GET /api/admin/households":                     tierSuperuser,
	"GET /api/admin/users":                          tierSuperuser,
	"GET /api/admin/nodes":                          tierSuperuser,
	"POST /api/admin/users/{user_id}/temp-password": tierSuperuser,

	// G6: the model manager.
	"GET /api/llm/v1/hardware":                       tierSuperuser,
	"POST /api/llm/v1/hardware/engines":              tierSuperuser,
	"POST /api/llm/v1/hardware/engines/{name}/retry": tierSuperuser,
	"GET /api/llm/v1/models/catalog":                 tierSuperuser,
	"GET /api/llm/v1/models/hf/{repo...}":            tierSuperuser,
	"POST /api/llm/v1/models/install":                tierSuperuser,
	"GET /api/llm/v1/models/installs":                tierSuperuser,
	"GET /api/llm/v1/models/installs/{id}":           tierSuperuser,
	"POST /api/llm/v1/models/installs/{id}/cancel":   tierSuperuser,
	"GET /api/llm/v1/models/installed":               tierSuperuser,
	"POST /api/llm/v1/models/installed":              tierSuperuser,
	"DELETE /api/llm/v1/models/installed/{id}":       tierSuperuser,
	"GET /api/llm/v1/models/labels":                  tierSuperuser,
	"PUT /api/llm/v1/models/labels":                  tierSuperuser,

	// The registry lists internal URLs.
	"GET /api/config/services":        tierSuperuser,
	"GET /api/config/services/health": tierSuperuser,

	// G2: settings across households.
	"GET /api/settings":                       tierSuperuser,
	"GET /api/settings/{$}":                   tierSuperuser,
	"PUT /api/settings/{service}/{key...}":    tierSuperuser,
	"DELETE /api/settings/{service}/{key...}": tierSuperuser,
	"GET /api/prompt-provider":                tierSuperuser,
	"PUT /api/prompt-provider":                tierSuperuser,
	// G3: traces. G4: logs.
	"GET /api/traces":       tierSuperuser,
	"GET /api/traces/{id}":  tierSuperuser,
	"GET /api/logs":         tierSuperuser,
	"GET /api/logs/sources": tierSuperuser,
	"GET /api/logs/stream":  tierSuperuser,
	// G5: connections/credentials, update, restart, stop.
	"GET /api/connections":                            tierSuperuser,
	"POST /api/connections/services":                  tierSuperuser,
	"DELETE /api/connections/services/{name}":         tierSuperuser,
	"PUT /api/connections/services/{name}/public_url": tierSuperuser,
	"POST /api/connections/apps":                      tierSuperuser,
	"POST /api/connections/apps/{app_id}/rotate":      tierSuperuser,
	"POST /api/connections/apps/{app_id}/revoke":      tierSuperuser,
	"GET /api/update":                                 tierSuperuser,
	"POST /api/update/check":                          tierSuperuser,
	"PUT /api/update/settings":                        tierSuperuser,
	"POST /api/update/apply":                          tierSuperuser,
	"GET /api/update/apply":                           tierSuperuser,
	"GET /api/system/info":                            tierSuperuser,
	"POST /api/system/restart":                        tierSuperuser,
	"POST /api/system/stop":                           tierSuperuser,
	// The setup wizard's voice step.
	"GET /api/tts/voices":  tierSuperuser,
	"POST /api/tts/sample": tierSuperuser,
	// The Models page's Speech section: the remote speech provider presets (a static catalog)
	// and a test that transcribes a bundled clip through the configured stt engine.
	"GET /api/speech/presets":   tierSuperuser,
	"POST /api/speech/stt-test": tierSuperuser,
	// The Phone page (SIP support): every household's SIP accounts, self-tests that dial out
	// through the PBX, and the SIP trace (numbers and caller IDs of every household's calls).
	"GET /api/phone/sip":                      tierSuperuser,
	"POST /api/phone/sip/{port}/tests/{test}": tierSuperuser,
	"GET /api/phone/sip/trace":                tierSuperuser,

	// The catch-all: unlisted /api paths are gated, then 404.
	"/api":  tierSuperuser,
	"/api/": tierSuperuser,
}

// principals are the callers of the tier table (webapp 01 §5 P1). Household roles add nothing
// at the gateway: every valid non-superuser token is the same "user" there.
var principals = []struct {
	name, token string
	user        authn.User
}{
	{"anonymous", "", authn.User{}},
	{"member of H1", "member-h1", authn.User{ID: 11, HouseholdID: "h1"}},
	{"power_user of H1", "power-h1", authn.User{ID: 12, HouseholdID: "h1"}},
	{"household admin of H1", "admin-h1", authn.User{ID: 13, HouseholdID: "h1"}},
	{"superuser", "root", authn.User{ID: 1, IsSuperuser: true, HouseholdID: "h0"}},
	{"member of H2", "member-h2", authn.User{ID: 21, HouseholdID: "h2"}},
}

func tierVerify(_ context.Context, tok string) (authn.User, error) {
	if tok == "must-change" {
		return authn.User{}, authn.ErrPasswordChangeRequired
	}
	for _, p := range principals {
		if p.token != "" && p.token == tok {
			return p.user, nil
		}
	}
	return authn.User{}, errors.New("bad token")
}

// requestFor turns a mux pattern into a concrete request line.
func requestFor(pattern string) (method, path string) {
	method, path, ok := strings.Cut(pattern, " ")
	if !ok {
		method, path = "GET", pattern
	}
	path = strings.ReplaceAll(path, "{$}", "")
	path = regexp.MustCompile(`\{[a-z_]+\.\.\.\}`).ReplaceAllString(path, "a/b")
	path = regexp.MustCompile(`\{[a-z_]+\}`).ReplaceAllString(path, "x1")
	if path == "/api/" {
		path = "/api/not-a-route"
	}
	return method, path
}

// tierMux is the admin mux with every /api handler replaced, behind its gate, by a 200 stub,
// so the table sees exactly what the gate decides.
func tierMux(t *testing.T) (*http.ServeMux, *Module) {
	t.Helper()
	m := &Module{UI: builtUI(), Verify: tierVerify}
	m.innerHook = func(string, http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-Reached", "1")
			w.WriteHeader(http.StatusOK)
		})
	}
	mux := http.NewServeMux()
	m.Register(mux, module.Deps{Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	return mux, m
}

func TestEveryAPIRouteHasATier(t *testing.T) {
	mux, m := tierMux(t)
	seen := map[string]bool{}
	for _, rt := range m.routes {
		seen[rt.pattern] = true
		want, ok := wantTiers[rt.pattern]
		if !ok {
			t.Errorf("%q is registered but unclassified: add it to wantTiers (docs/webapp/01 §2.2)", rt.pattern)
			continue
		}
		if rt.tier != want {
			t.Errorf("%q is %s, want %s", rt.pattern, rt.tier, want)
		}
	}
	for p := range wantTiers {
		if !seen[p] {
			t.Errorf("%q is in wantTiers but not registered", p)
		}
	}

	// The table: every pattern × every principal.
	for _, rt := range m.routes {
		want := wantTiers[rt.pattern]
		method, path := requestFor(rt.pattern)
		for _, p := range principals {
			var hdr []string
			if p.token != "" {
				hdr = []string{"Authorization", "Bearer " + p.token}
			}
			w := send(mux, method, path, "", hdr...)
			var code int
			switch {
			case want == tierOpen:
				code = 200
			case p.token == "":
				code = 401
			case want == tierSuperuser && !p.user.IsSuperuser:
				code = 403
			default:
				code = 200
			}
			if w.Code != code || (code == 200) != (w.Header().Get("X-Reached") == "1") {
				t.Errorf("%s %s as %s: %d (reached=%q), want %d", method, path, p.name, w.Code, w.Header().Get("X-Reached"), code)
			}
			if w.Header().Get("Content-Security-Policy") == "" {
				t.Errorf("%s %s: no security headers", method, path)
			}
		}
		if want != tierOpen {
			if w := send(mux, method, path, "", "Authorization", "Bearer forged"); w.Code != 401 {
				t.Errorf("%s %s with a forged token: %d", method, path, w.Code)
			}
		}
	}
}

// TestNoMemberReachesInstallWideRoutes is the security core (webapp 01 §2.1, G2-G7), stated
// by path prefix so it holds however the routes are later split or renamed.
func TestNoMemberReachesInstallWideRoutes(t *testing.T) {
	_, m := tierMux(t)
	for _, rt := range m.routes {
		_, path := requestFor(rt.pattern)
		for _, prefix := range []string{
			"/api/settings", "/api/traces", "/api/logs", "/api/connections", "/api/update",
			"/api/system", "/api/llm/", "/api/admin/", "/api/config/", "/api/prompt-provider", "/api/tts/", "/api/speech/",
		} {
			if strings.HasPrefix(path, prefix) && rt.tier != tierSuperuser {
				t.Errorf("%q is %s; everything under %s must be superuser", rt.pattern, rt.tier, prefix)
			}
		}
	}
}

// TestAPIRoutesRegisterThroughHandle keeps the tier table complete: an /api route registered
// straight on the mux would bypass both the gate choice and m.routes.
func TestAPIRoutesRegisterThroughHandle(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "Handle" && sel.Sel.Name != "HandleFunc") {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok {
				// A computed pattern: only handle() may do that.
				if name != "tiers.go" {
					t.Errorf("%s: %s with a computed pattern; register /api routes with m.handle", fset.Position(call.Pos()), sel.Sel.Name)
				}
				return true
			}
			if p, _ := strconv.Unquote(lit.Value); strings.Contains(p, "/api") {
				t.Errorf("%s: %q registered on the mux directly; use m.handle with a tier", fset.Position(call.Pos()), p)
			}
			return true
		})
	}
}

// TestPassStripsOtherCredentials: the module behind a gated pass route sees the bearer
// principal the tier checked, never a key that rode along.
func TestPassStripsOtherCredentials(t *testing.T) {
	var got http.Header
	mux := http.NewServeMux()
	(&Module{UI: builtUI(), Verify: tierVerify}).Register(mux, module.Deps{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Handler: func(string) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = r.Header.Clone() })
		},
	})
	send(mux, "GET", "/api/cc/api/v0/households/h1/smart-home/config", "",
		"Authorization", "Bearer member-h1", "X-API-Key", "cc-admin-key", "X-Jarvis-App-Id", "a",
		"X-Jarvis-App-Key", "k", "X-Jarvis-Admin-Token", "t")
	if got == nil {
		t.Fatal("not dispatched")
	}
	for _, h := range forwardedCredentials {
		if got.Get(h) != "" {
			t.Errorf("%s forwarded", h)
		}
	}
	if got.Get("Authorization") != "Bearer member-h1" {
		t.Errorf("bearer not forwarded: %v", got)
	}
}

// TestPassKeepsThePeerAddress is G11's hook: the target sees the TCP peer, and an
// X-Forwarded-For header never replaces it. The this-device route will rely on this.
func TestPassKeepsThePeerAddress(t *testing.T) {
	var peer, xff string
	mux := http.NewServeMux()
	(&Module{UI: builtUI(), Verify: tierVerify}).Register(mux, module.Deps{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Handler: func(string) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				peer, xff = r.RemoteAddr, r.Header.Get("X-Forwarded-For")
			})
		},
	})
	send(mux, "GET", "/api/cc/api/v0/admin/nodes", "", "Authorization", "Bearer member-h1", "X-Forwarded-For", "10.9.9.9")
	if peer != "192.0.2.1:1234" { // httptest.NewRequest's peer
		t.Errorf("peer %q", peer)
	}
	if xff != "10.9.9.9" {
		t.Errorf("X-Forwarded-For should reach the module as a header only: %q", xff)
	}
}

func TestPassRoutesTargetKnownListeners(t *testing.T) {
	known := []string{pconfig.ListenerAuth, pconfig.ListenerLLM, pconfig.ListenerConfig, pconfig.ListenerCC, pconfig.ListenerNotifications}
	for _, rt := range passRoutes {
		if !slices.Contains(known, rt.listener) {
			t.Errorf("%q targets %q", rt.pattern, rt.listener)
		}
		if _, path, _ := strings.Cut(rt.pattern, " "); !strings.HasPrefix(path, rt.strip+"/") {
			t.Errorf("%q does not start with its strip prefix %q", rt.pattern, rt.strip)
		}
	}
}

// TestMustChangePasswordAtTheGateway is G8: a must-change-password token reaches only the
// password-change routes; every other gated route answers 403 password_change_required,
// whatever its tier.
func TestMustChangePasswordAtTheGateway(t *testing.T) {
	mux, m := tierMux(t)
	for _, rt := range m.routes {
		method, path := requestFor(rt.pattern)
		w := send(mux, method, path, "", "Authorization", "Bearer must-change")
		reached := w.Header().Get("X-Reached") == "1"
		switch {
		case rt.tier == tierOpen || passwordChangeRoutes[rt.pattern]:
			if !reached {
				t.Errorf("%s %s: %d, want it reached", method, path, w.Code)
			}
		case reached || w.Code != 403 || !strings.Contains(w.Body.String(), `"code":"password_change_required"`):
			t.Errorf("%s %s: %d %s (reached=%v)", method, path, w.Code, w.Body.String(), reached)
		}
	}
	for p := range passwordChangeRoutes {
		if wantTiers[p] != tierUser {
			t.Errorf("%s is a password-change route but not tierUser", p)
		}
	}
}
