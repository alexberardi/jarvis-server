package admin

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/alexberardi/jarvis-server/internal/platform/module"
	adminui "github.com/alexberardi/jarvis-server/web/admin"
)

const indexHTML = `<!doctype html><html><head><script type="module" src="/assets/index-abc123.js"></script></head><body><div id="root"></div></body></html>`

func builtUI() fstest.MapFS {
	return fstest.MapFS{
		"index.html":              {Data: []byte(indexHTML)},
		"favicon.png":             {Data: []byte("\x89PNG")},
		"assets/index-abc123.js":  {Data: []byte("console.log(1)")},
		"assets/index-abc123.css": {Data: []byte("body{}")},
		"assets/font-x.woff2":     {Data: []byte("wOF2")},
		"assets/logo-x.svg":       {Data: []byte("<svg/>")},
	}
}

func newMux(t *testing.T, m *Module) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	m.Register(mux, module.Deps{Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	return mux
}

func get(t *testing.T, h http.Handler, method, target string, hdr ...string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, target, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestPlaceholderWhenNotBuilt(t *testing.T) {
	// Point UIDir at an empty directory so the test doesn't depend on whether this checkout
	// has a built UI embedded.
	mux := newMux(t, &Module{UIDir: t.TempDir()})
	for _, p := range []string{"/", "/settings", "/assets/index-abc123.js", "/index.html"} {
		w := get(t, mux, "GET", p)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "admin UI not built") {
			t.Errorf("%s: %d %q", p, w.Code, w.Body.String())
		}
		if ct := w.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
			t.Errorf("%s: content-type %q", p, ct)
		}
		if cc := w.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("%s: cache-control %q", p, cc)
		}
	}
	if !strings.Contains(get(t, mux, "GET", "/health").Body.String(), `"ui_built":false`) {
		t.Error("health should report the UI as not built")
	}
}

func TestServesBuiltUI(t *testing.T) {
	mux := newMux(t, &Module{UI: builtUI()})
	cases := []struct {
		path, ct, cache, body string
	}{
		{"/", "text/html; charset=utf-8", "no-cache", indexHTML},
		{"/index.html", "text/html; charset=utf-8", "no-cache", indexHTML},
		{"/favicon.png", "image/png", "no-cache", "\x89PNG"},
		{"/assets/index-abc123.js", "text/javascript; charset=utf-8", "public, max-age=31536000, immutable", "console.log(1)"},
		{"/assets/index-abc123.css", "text/css; charset=utf-8", "public, max-age=31536000, immutable", "body{}"},
		{"/assets/font-x.woff2", "font/woff2", "public, max-age=31536000, immutable", "wOF2"},
		{"/assets/logo-x.svg", "image/svg+xml", "public, max-age=31536000, immutable", "<svg/>"},
	}
	for _, c := range cases {
		w := get(t, mux, "GET", c.path)
		if w.Code != http.StatusOK || w.Body.String() != c.body {
			t.Errorf("%s: %d %q", c.path, w.Code, w.Body.String())
		}
		if got := w.Header().Get("Content-Type"); got != c.ct {
			t.Errorf("%s: content-type %q, want %q", c.path, got, c.ct)
		}
		if got := w.Header().Get("Cache-Control"); got != c.cache {
			t.Errorf("%s: cache-control %q, want %q", c.path, got, c.cache)
		}
	}
}

func TestSPAFallback(t *testing.T) {
	mux := newMux(t, &Module{UI: builtUI()})
	for _, p := range []string{"/settings", "/traces/abc-123", "/setup", "/assets", "/no/such/page.html"} {
		w := get(t, mux, "GET", p)
		if w.Code != http.StatusOK || w.Body.String() != indexHTML {
			t.Errorf("%s: %d %q", p, w.Code, w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "no-cache" || w.Header().Get("Content-Type") != "text/html; charset=utf-8" {
			t.Errorf("%s: headers %v", p, w.Header())
		}
	}
	// A missing hashed chunk is a 404, never index.html cached as immutable JavaScript.
	w := get(t, mux, "GET", "/assets/index-old999.js")
	if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "<html") {
		t.Fatalf("missing asset: %d %q", w.Code, w.Body.String())
	}
	if strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
		t.Fatal("missing asset must not be cached as immutable")
	}
}

func TestAPIIsJSON404(t *testing.T) {
	mux := newMux(t, &Module{UI: builtUI()})
	for _, c := range []struct{ method, path string }{
		{"GET", "/api"}, {"GET", "/api/"}, {"GET", "/api/settings"}, {"POST", "/api/auth/login"},
		{"DELETE", "/api/admin/users/1"},
	} {
		w := get(t, mux, c.method, c.path)
		if w.Code != http.StatusNotFound || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
			t.Errorf("%s %s: %d %s", c.method, c.path, w.Code, w.Header().Get("Content-Type"))
		}
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body["detail"] != "Not Found" {
			t.Errorf("%s %s: body %q", c.method, c.path, w.Body.String())
		}
	}
}

func TestHealth(t *testing.T) {
	mux := newMux(t, &Module{UI: builtUI(), Version: "v1.2.3"})
	w := get(t, mux, "GET", "/health")
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("%d %q", w.Code, w.Body.String())
	}
	if w.Code != 200 || body["status"] != "ok" || body["version"] != "v1.2.3" || body["ui_built"] != true {
		t.Fatalf("%d %v", w.Code, body)
	}
}

func TestSecurityHeaders(t *testing.T) {
	mux := newMux(t, &Module{UI: builtUI()})
	for _, p := range []string{"/", "/settings", "/assets/index-abc123.js", "/health", "/api/x"} {
		h := get(t, mux, "GET", p).Header()
		if !strings.Contains(h.Get("Content-Security-Policy"), "script-src 'self'") ||
			!strings.Contains(h.Get("Content-Security-Policy"), "frame-ancestors 'none'") ||
			h.Get("X-Frame-Options") != "DENY" || h.Get("X-Content-Type-Options") != "nosniff" ||
			h.Get("Referrer-Policy") == "" {
			t.Errorf("%s: headers %v", p, h)
		}
	}
}

func TestMethodsAndConditional(t *testing.T) {
	mux := newMux(t, &Module{UI: builtUI()})
	if w := get(t, mux, "POST", "/settings"); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /settings: %d", w.Code)
	}
	if w := get(t, mux, "HEAD", "/"); w.Code != http.StatusOK || w.Body.Len() != 0 {
		t.Errorf("HEAD /: %d %d bytes", w.Code, w.Body.Len())
	}
	etag := get(t, mux, "GET", "/").Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag on index.html")
	}
	if w := get(t, mux, "GET", "/", "If-None-Match", etag); w.Code != http.StatusNotModified {
		t.Errorf("If-None-Match: %d", w.Code)
	}
}

// TestPathTraversal serves from a real directory with a secret beside it: no spelling of a
// parent path may reach it, on any OS.
func TestPathTraversal(t *testing.T) {
	root := t.TempDir()
	ui := filepath.Join(root, "ui")
	if err := os.MkdirAll(filepath.Join(ui, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		filepath.Join(ui, "index.html"):       indexHTML,
		filepath.Join(ui, "assets", "a-1.js"): "ok",
		filepath.Join(root, "secret.txt"):     "SECRET",
	} {
		if err := os.WriteFile(name, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mux := newMux(t, &Module{UIDir: ui})
	if w := get(t, mux, "GET", "/assets/a-1.js"); w.Body.String() != "ok" {
		t.Fatalf("UIDir not served: %d %q", w.Code, w.Body.String())
	}
	// The mux redirects most unclean paths before the handler runs, so also hit the static
	// handler directly with the raw paths.
	static := NewStatic(os.DirFS(ui), nil)
	for _, p := range []string{
		"/../secret.txt",
		"/assets/../../secret.txt",
		"/%2e%2e/secret.txt",
		"/assets/%2e%2e/%2e%2e/secret.txt",
		"/..%2fsecret.txt",
		"/..%5csecret.txt",
		"/assets/..%5c..%5csecret.txt",
		"/C:%5csecret.txt",
		"//secret.txt",
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.URL.RawPath = ""
		r.URL.Path = mustUnescape(t, p)
		for _, h := range []http.Handler{mux, static} {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if strings.Contains(w.Body.String(), "SECRET") {
				t.Errorf("%s leaked the secret", p)
			}
		}
		// Raw, un-cleaned names never reach the FS either.
		if _, err := static.read(strings.TrimPrefix(r.URL.Path, "/")); err == nil {
			t.Errorf("%s: read accepted an unclean path", p)
		}
	}
}

func mustUnescape(t *testing.T, p string) string {
	t.Helper()
	r := httptest.NewRequest("GET", "http://x"+p, nil)
	return r.URL.Path
}

func TestContentTypeTable(t *testing.T) {
	for name, want := range map[string]string{
		"a.js": "text/javascript; charset=utf-8", "a.MJS": "text/javascript; charset=utf-8",
		"a.css": "text/css; charset=utf-8", "a.html": "text/html; charset=utf-8",
		"a.json": "application/json", "a.ico": "image/x-icon", "a.png": "image/png",
		"a.woff2": "font/woff2", "a.svg": "image/svg+xml", "a.unknown": "application/octet-stream",
	} {
		if got := ContentType(name); got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
}

func TestEmbeddedFallsBackToPlaceholderOrUI(t *testing.T) {
	// Whatever this checkout embeds, / must serve it: the built index or the placeholder.
	mux := newMux(t, &Module{})
	w := get(t, mux, "GET", "/")
	if w.Code != 200 {
		t.Fatalf("%d", w.Code)
	}
	if _, built := adminui.UI(); !built && !strings.Contains(w.Body.String(), "admin UI not built") {
		t.Fatalf("placeholder not served: %q", w.Body.String())
	}
}
