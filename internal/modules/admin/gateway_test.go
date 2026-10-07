package admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	pconfig "github.com/alexberardi/jarvis-server/internal/platform/config"
	"github.com/alexberardi/jarvis-server/internal/platform/module"
)

// fakeVerify knows two tokens: "root" (a superuser) and "member" (not).
func fakeVerify(_ context.Context, tok string) (authn.User, error) {
	switch tok {
	case "root":
		return authn.User{ID: 1, IsSuperuser: true}, nil
	case "member":
		return authn.User{ID: 2}, nil
	}
	return authn.User{}, errors.New("bad token")
}

// echo records what a target listener received.
type echo struct {
	listener string
	got      []string // "METHOD path?query body"
}

func (e *echo) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	e.got = append(e.got, r.Method+" "+r.URL.RequestURI()+" "+string(b))
	w.Header().Set("X-Target", e.listener)
	w.WriteHeader(http.StatusTeapot)
}

func gatewayMux(t *testing.T) (*http.ServeMux, map[string]*echo) {
	t.Helper()
	targets := map[string]*echo{}
	for _, l := range []string{pconfig.ListenerAuth, pconfig.ListenerLLM, pconfig.ListenerConfig, pconfig.ListenerCC} {
		targets[l] = &echo{listener: l}
	}
	mux := http.NewServeMux()
	(&Module{UI: builtUI(), Verify: fakeVerify}).Register(mux, module.Deps{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Handler: func(l string) http.Handler {
			if e := targets[l]; e != nil {
				return e
			}
			return nil
		},
	})
	return mux, targets
}

func send(h http.Handler, method, target, body string, hdr ...string) *httptest.ResponseRecorder {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, target, rd)
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func detailOf(w *httptest.ResponseRecorder) string {
	var b struct{ Detail string }
	json.Unmarshal(w.Body.Bytes(), &b)
	return b.Detail
}

func TestGateRejectsAnonymousAndNonSuperusers(t *testing.T) {
	mux, targets := gatewayMux(t)
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/admin/users"}, {"GET", "/api/auth/me"}, {"GET", "/api/llm/v1/hardware"},
		{"GET", "/api/config/services"}, {"GET", "/api/cc/api/v0/admin/nodes"}, {"GET", "/api/nope"},
		{"POST", "/api/auth/change-password"},
	} {
		if w := send(mux, c.method, c.path, ""); w.Code != 401 || detailOf(w) != "Missing or invalid Authorization header" {
			t.Errorf("anonymous %s %s: %d %q", c.method, c.path, w.Code, w.Body.String())
		}
		if w := send(mux, c.method, c.path, "", "Authorization", "Bearer forged"); w.Code != 401 || detailOf(w) != "Invalid or expired token" {
			t.Errorf("bad token %s %s: %d %q", c.method, c.path, w.Code, w.Body.String())
		}
		if w := send(mux, c.method, c.path, "", "Authorization", "Bearer member"); w.Code != 403 || detailOf(w) != "Superuser access required" {
			t.Errorf("member %s %s: %d %q", c.method, c.path, w.Code, w.Body.String())
		}
	}
	for l, e := range targets {
		if len(e.got) != 0 {
			t.Errorf("%s reached without a superuser: %v", l, e.got)
		}
	}
	if w := send(mux, "GET", "/api/admin/users", "", "Authorization", "Bearer root"); w.Code != http.StatusTeapot {
		t.Errorf("superuser: %d %q", w.Code, w.Body.String())
	}
}

func TestBootstrapAllowList(t *testing.T) {
	mux, targets := gatewayMux(t)
	for _, c := range []struct{ method, path string }{
		{"POST", "/api/auth/login"}, {"POST", "/api/auth/refresh"}, {"POST", "/api/auth/logout"},
		{"GET", "/api/auth/setup-status"}, {"POST", "/api/auth/setup"},
	} {
		w := send(mux, c.method, c.path, `{"x":1}`, "Content-Type", "application/json")
		if w.Code != http.StatusTeapot || w.Header().Get("X-Target") != pconfig.ListenerAuth {
			t.Errorf("%s %s: %d %q", c.method, c.path, w.Code, w.Body.String())
		}
		if w.Header().Get("Content-Security-Policy") == "" {
			t.Errorf("%s %s: no security headers", c.method, c.path)
		}
	}
	if n := len(targets[pconfig.ListenerAuth].got); n != 5 {
		t.Fatalf("auth got %d calls", n)
	}
}

func TestPassThroughRewritesPaths(t *testing.T) {
	mux, targets := gatewayMux(t)
	su := []string{"Authorization", "Bearer root"}
	cases := []struct{ method, path, listener, want string }{
		{"POST", "/api/auth/login", pconfig.ListenerAuth, "POST /auth/login"},
		{"GET", "/api/auth/me", pconfig.ListenerAuth, "GET /auth/me"},
		{"GET", "/api/admin/households", pconfig.ListenerAuth, "GET /superuser/households"},
		{"GET", "/api/admin/users", pconfig.ListenerAuth, "GET /superuser/users"},
		{"GET", "/api/admin/nodes", pconfig.ListenerAuth, "GET /superuser/nodes"},
		{"POST", "/api/admin/users/7/temp-password", pconfig.ListenerAuth, "POST /superuser/users/7/temp-password"},
		{"GET", "/api/llm/v1/hardware", pconfig.ListenerLLM, "GET /v1/hardware"},
		{"GET", "/api/llm/v1/models/catalog?kind=llm", pconfig.ListenerLLM, "GET /v1/models/catalog?kind=llm"},
		{"GET", "/api/llm/v1/models/hf/Qwen/Qwen3-4B-GGUF", pconfig.ListenerLLM, "GET /v1/models/hf/Qwen/Qwen3-4B-GGUF"},
		{"GET", "/api/llm/v1/models/installs/abc", pconfig.ListenerLLM, "GET /v1/models/installs/abc"},
		{"DELETE", "/api/llm/v1/models/installed/m1?force=true", pconfig.ListenerLLM, "DELETE /v1/models/installed/m1?force=true"},
		{"PUT", "/api/llm/v1/models/labels", pconfig.ListenerLLM, "PUT /v1/models/labels"},
		{"GET", "/api/config/services", pconfig.ListenerConfig, "GET /services"},
		{"GET", "/api/config/services/health", pconfig.ListenerConfig, "GET /services/health"},
		{"GET", "/api/cc/api/v0/admin/nodes", pconfig.ListenerCC, "GET /api/v0/admin/nodes"},
	}
	for _, c := range cases {
		e := targets[c.listener]
		e.got = nil
		w := send(mux, c.method, c.path, "", su...)
		if w.Code != http.StatusTeapot || len(e.got) != 1 || strings.TrimSpace(e.got[0]) != c.want {
			t.Errorf("%s %s: %d, %s got %v, want %q", c.method, c.path, w.Code, c.listener, e.got, c.want)
		}
	}
}

func TestPassThroughIsAnAllowList(t *testing.T) {
	mux, targets := gatewayMux(t)
	su := []string{"Authorization", "Bearer root"}
	for _, c := range []struct{ method, path string }{
		{"POST", "/api/config/services"},           // registry writes go through the BFF
		{"DELETE", "/api/config/services/x"},       // ditto
		{"GET", "/api/auth/admin/app-clients"},     // admin-token routes are not exposed
		{"GET", "/api/admin/users/1"},              // only the listed superuser views
		{"POST", "/api/llm/v1/chat/completions"},   // not the inference surface
		{"GET", "/api/cc/api/v0/admin/traces"},     // traces come from the BFF
		{"GET", "/api/llm/v1/models/catalog/../x"}, // cleaned by the mux, then not listed
	} {
		if w := send(mux, c.method, c.path, "", su...); w.Code != 404 && w.Code != 301 {
			t.Errorf("%s %s: %d %q", c.method, c.path, w.Code, w.Body.String())
		}
	}
	for l, e := range targets {
		if len(e.got) != 0 {
			t.Errorf("%s reached outside the allow-list: %v", l, e.got)
		}
	}
}

func TestPassThroughTargetNotServed(t *testing.T) {
	mux := http.NewServeMux()
	(&Module{UI: builtUI(), Verify: fakeVerify}).Register(mux, module.Deps{
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Handler: func(string) http.Handler { return nil },
	})
	w := send(mux, "GET", "/api/llm/v1/hardware", "", "Authorization", "Bearer root")
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(detailOf(w), "llm") {
		t.Fatalf("%d %q", w.Code, w.Body.String())
	}
}

func TestEmptyJSONBodyIsAnEmptyObject(t *testing.T) {
	mux, targets := gatewayMux(t)
	e := targets[pconfig.ListenerAuth]
	su := []string{"Authorization", "Bearer root"}
	send(mux, "POST", "/api/admin/users/7/temp-password", "", append(su, "Content-Type", "application/json; charset=utf-8")...)
	send(mux, "POST", "/api/admin/users/7/temp-password", `{"ttl_hours":2}`, append(su, "Content-Type", "application/json")...)
	send(mux, "POST", "/api/admin/users/7/temp-password", "", su...) // not JSON: left alone

	// Unknown length (chunked) and empty.
	r := httptest.NewRequest("POST", "/api/auth/logout", io.NopCloser(strings.NewReader("")))
	r.ContentLength = -1
	r.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(httptest.NewRecorder(), r)
	// Unknown length with a body: kept intact.
	r = httptest.NewRequest("POST", "/api/auth/logout", io.NopCloser(strings.NewReader(`{"refresh_token":"x"}`)))
	r.ContentLength = -1
	r.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(httptest.NewRecorder(), r)

	want := []string{
		"POST /superuser/users/7/temp-password {}",
		`POST /superuser/users/7/temp-password {"ttl_hours":2}`,
		"POST /superuser/users/7/temp-password ",
		"POST /auth/logout {}",
		`POST /auth/logout {"refresh_token":"x"}`,
	}
	if strings.Join(e.got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(e.got, "\n"), strings.Join(want, "\n"))
	}
}
