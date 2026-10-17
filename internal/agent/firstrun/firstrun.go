// Package firstrun is the desktop agent's setup page (installers plan item 1.5, DESIGN.md §13):
// a small web page the agent serves to the user's own browser, so nobody needs a terminal to
// add a computer to Jarvis Automation. It finds the server (this computer first, then the
// network over mDNS, or a typed address), joins it with a pairing code or by signing in, links
// the browser extension's store pages, shows the macOS permissions or the GNOME/KDE typing
// permission, has a microphone meter and the "Listen for Hey Jarvis" switch, and can remove
// the agent.
//
// Security. The server listens on 127.0.0.1 only, on a port the OS picks, and every path is
// under /s/<secret>/, a per-launch 256-bit secret that only the opened URL carries. Anything
// else is a 404, including a wrong secret (compared in constant time). The Host header must be
// exactly 127.0.0.1:<port> (DNS rebinding), a POST must come from that same origin (Origin),
// be JSON (so another site can't send it without a CORS preflight, which is never answered)
// and not be cross-site (Sec-Fetch-Site). No response carries a CORS header. The page sends
// no Referer anywhere (the secret is in its URL) and runs under a strict CSP with no inline
// script. The password of "Sign in" only ever arrives in a POST body, goes on to jarvisd's
// login and is dropped; the session token stays in memory until the computer is added.
package firstrun

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed assets
var assets embed.FS

// IdleTimeout is how long the server waits without a request from the page before it stops.
// An open page asks every couple of seconds (a background tab at least once a minute).
const IdleTimeout = 15 * time.Minute

// maxBody bounds a POST body.
const maxBody = 64 << 10

// Server is the setup page's HTTP server. Make one with New.
type Server struct {
	d      Deps
	secret string
	host   string // "127.0.0.1:<port>", the only Host accepted
	ln     net.Listener
	mux    *http.ServeMux
	ctx    context.Context // ends when Serve returns
	cancel context.CancelFunc

	mu       sync.Mutex
	lastSeen time.Time
	finished string // "done" or "removed": Serve stops

	app *app
}

// New listens on 127.0.0.1 (a free port) and makes the per-launch secret. Nothing is served
// until Serve.
func New(d Deps) (*Server, error) {
	d = d.withDefaults()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("setup page: %w", err)
	}
	return newServer(d, ln)
}

func newServer(d Deps, ln net.Listener) (*Server, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		_ = ln.Close()
		return nil, err
	}
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok || !addr.IP.IsLoopback() {
		_ = ln.Close()
		return nil, fmt.Errorf("setup page: %s isn't a loopback address", ln.Addr())
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{d: d, secret: base64.RawURLEncoding.EncodeToString(b), ln: ln,
		host: "127.0.0.1:" + strconv.Itoa(addr.Port), ctx: ctx, cancel: cancel, lastSeen: d.Now()}
	s.app = newApp(s)
	s.mux = s.routes()
	return s, nil
}

// URL is the page's address, secret included. Open it in the user's browser; never log it.
func (s *Server) URL() string { return "http://" + s.host + "/s/" + s.secret + "/" }

// Origin is the page's origin, the only one a POST may come from.
func (s *Server) Origin() string { return "http://" + s.host }

// Serve answers requests until ctx ends, the page says it is done (Done, Remove), or nothing
// has asked for IdleTimeout. It returns why it stopped: "done", "removed", "idle" or "".
func (s *Server) Serve(ctx context.Context) string {
	defer s.cancel()
	hs := &http.Server{Handler: s, ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- hs.Serve(s.ln) }()
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	why := ""
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case <-s.ctx.Done():
			why = s.finishedWhy()
			break loop
		case <-tick.C:
			if s.idle(s.d.Now()) {
				why = "idle"
				break loop
			}
		case err := <-errc:
			s.d.Log.Warn("setup page: server stopped", "err", err)
			break loop
		}
	}
	sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = hs.Shutdown(sctx)
	s.app.close()
	return why
}

// Close stops listening without serving (when Serve is never called).
func (s *Server) Close() error {
	s.cancel()
	s.app.close()
	return s.ln.Close()
}

// idle: nothing has asked for IdleTimeout.
func (s *Server) idle(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return now.Sub(s.lastSeen) >= IdleTimeout
}

func (s *Server) touch() {
	s.mu.Lock()
	s.lastSeen = s.d.Now()
	s.mu.Unlock()
}

// finish stops Serve once the answer to this request is written (ServeHTTP; Serve's
// Shutdown lets an answer in flight finish).
func (s *Server) finish(why string) {
	s.mu.Lock()
	if s.finished == "" {
		s.finished = why
	}
	s.mu.Unlock()
}

func (s *Server) finishedWhy() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.finished
}

// securityHeaders go on every answer, a refusal included.
func securityHeaders(h http.Header) {
	h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; "+
		"img-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	h.Set("X-Frame-Options", "DENY")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-store")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
}

// ServeHTTP checks the host, the secret and (for a POST) the origin, then routes.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	securityHeaders(w.Header())
	if r.Host != s.host {
		// DNS rebinding: a name that resolves to 127.0.0.1 isn't this page.
		http.Error(w, "wrong host", http.StatusMisdirectedRequest)
		return
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/s/")
	secret, sub, _ := strings.Cut(rest, "/")
	if !ok || subtle.ConstantTimeCompare([]byte(secret), []byte(s.secret)) != 1 {
		http.NotFound(w, r)
		return
	}
	if !strings.Contains(rest, "/") {
		http.Redirect(w, r, s.URL(), http.StatusFound)
		return
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		if site := r.Header.Get("Sec-Fetch-Site"); strings.HasPrefix(sub, "api/") && site != "" && site != "same-origin" {
			http.Error(w, "cross-site request refused", http.StatusForbidden)
			return
		}
	case http.MethodPost:
		if err := s.checkPost(r); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	default:
		w.Header().Set("Allow", "GET, HEAD, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.touch()
	r2 := r.Clone(r.Context())
	r2.URL.Path = "/" + sub
	r2.URL.RawPath = ""
	s.mux.ServeHTTP(w, r2)
	if s.finishedWhy() != "" {
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		s.cancel()
	}
}

// checkPost refuses a POST that didn't come from the page itself.
func (s *Server) checkPost(r *http.Request) error {
	if r.Header.Get("Origin") != s.Origin() {
		return errors.New("this request didn't come from the setup page")
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
		return errors.New("cross-site request refused")
	}
	ct, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || ct != "application/json" {
		return errors.New("expected JSON")
	}
	return nil
}

// routes are the page's paths, below /s/<secret>.
func (s *Server) routes() *http.ServeMux {
	m := http.NewServeMux()
	m.HandleFunc("GET /{$}", s.static("index.html"))
	m.HandleFunc("GET /app.js", s.static("app.js"))
	m.HandleFunc("GET /app.css", s.static("app.css"))
	m.HandleFunc("GET /icon.png", func(w http.ResponseWriter, r *http.Request) {
		if len(s.d.Icon) == 0 {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(s.d.Icon)
	})
	a := s.app
	m.HandleFunc("GET /api/state", a.getState)
	m.HandleFunc("GET /api/servers", a.getServers)
	m.HandleFunc("POST /api/servers/search", a.postSearch)
	m.HandleFunc("POST /api/server", a.postServer)
	m.HandleFunc("POST /api/server/forget", a.postForget)
	m.HandleFunc("POST /api/pair", a.postPair)
	m.HandleFunc("POST /api/pair/cancel", a.postPairCancel)
	m.HandleFunc("POST /api/pair/confirm", a.postPairConfirm)
	m.HandleFunc("POST /api/signin", a.postSignIn)
	m.HandleFunc("POST /api/signin/household", a.postHousehold)
	m.HandleFunc("POST /api/voice", a.postVoice)
	m.HandleFunc("GET /api/mic", a.getMic)
	m.HandleFunc("POST /api/permission", a.postPermission)
	m.HandleFunc("POST /api/portal", a.postPortal)
	m.HandleFunc("POST /api/done", a.postDone)
	m.HandleFunc("POST /api/remove", a.postRemove)
	return m
}

func (s *Server) static(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, err := fs.ReadFile(assets, path.Join("assets", name))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		// Fixed types: mime.TypeByExtension reads the OS's table (Windows' registry may say
		// text/plain for .js, which nosniff would then refuse to run).
		w.Header().Set("Content-Type", assetTypes[path.Ext(name)])
		_, _ = w.Write(b)
	}
}

// assetTypes are the embedded assets' content types.
var assetTypes = map[string]string{
	".html": "text/html; charset=utf-8",
	".js":   "text/javascript; charset=utf-8",
	".css":  "text/css; charset=utf-8",
}

// writeJSON answers v with status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// fail answers a problem the page shows to the user as is: msg is plain, friendly text.
func fail(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// readJSON decodes a POST body into v; unknown fields are refused.
func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		fail(w, http.StatusBadRequest, "That request didn't make sense to the setup page. Reload it and try again.")
		return false
	}
	return true
}
