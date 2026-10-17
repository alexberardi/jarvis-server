package firstrun

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// testServer makes a Server on a real loopback listener (never served: requests go straight
// to ServeHTTP).
func testServer(t *testing.T, d Deps) *Server {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if d.ConfigPort == "" {
		d.ConfigPort = deadPort(t) // never this computer's real config service
	}
	s, err := newServer(d.withDefaults(), ln)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// deadPort is a port of 127.0.0.1 nothing listens on.
func deadPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, p, _ := net.SplitHostPort(ln.Addr().String())
	_ = ln.Close()
	return p
}

// req builds a request the way the page's own fetch would send it.
func req(s *Server, method, sub string, body any) *http.Request {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	r := httptest.NewRequest(method, "/s/"+s.secret+"/"+sub, rd)
	r.Host = s.host
	if method == http.MethodPost {
		r.Header.Set("Origin", s.Origin())
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Sec-Fetch-Site", "same-origin")
	}
	return r
}

func send(s *Server, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	return v
}

func TestNewListensOnLoopbackOnly(t *testing.T) {
	s, err := New(Deps{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	addr := s.ln.Addr().(*net.TCPAddr)
	if !addr.IP.Equal(net.IPv4(127, 0, 0, 1)) || addr.Port == 0 {
		t.Fatalf("listening on %v", addr)
	}
	if !strings.HasPrefix(s.URL(), "http://127.0.0.1:") || !strings.Contains(s.URL(), "/s/"+s.secret+"/") || len(s.secret) < 43 {
		t.Fatalf("URL %q", s.URL())
	}
	s2, err := New(Deps{})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if s2.secret == s.secret {
		t.Fatal("two launches share a secret")
	}
}

func TestNewServerRefusesANonLoopbackListener(t *testing.T) {
	ln, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Skip(err)
	}
	if _, err := newServer(Deps{}.withDefaults(), ln); err == nil {
		t.Fatal("served on every interface")
	}
}

// apiRoutes is every route; each must refuse a request without the secret.
var apiRoutes = []struct{ method, path string }{
	{"GET", ""}, {"GET", "app.js"}, {"GET", "app.css"}, {"GET", "icon.png"},
	{"GET", "api/state"}, {"GET", "api/servers"}, {"POST", "api/servers/search"}, {"POST", "api/server"},
	{"POST", "api/server/forget"}, {"POST", "api/pair"}, {"POST", "api/pair/cancel"}, {"POST", "api/pair/confirm"}, {"POST", "api/signin"},
	{"POST", "api/signin/household"}, {"POST", "api/voice"}, {"GET", "api/mic"}, {"POST", "api/permission"},
	{"POST", "api/portal"}, {"POST", "api/done"}, {"POST", "api/remove"},
}

func TestEveryRouteNeedsTheSecret(t *testing.T) {
	removed := false
	s := testServer(t, Deps{Remove: func(context.Context) error { removed = true; return nil }, Icon: []byte("png")})
	for _, rt := range apiRoutes {
		for _, prefix := range []string{"/", "/s/", "/s/wrong/", "/s/" + s.secret[:len(s.secret)-1] + "/", "/s/" + s.secret + "x/", "/S/" + s.secret + "/"} {
			r := httptest.NewRequest(rt.method, prefix+rt.path, strings.NewReader(`{"confirm":"remove"}`))
			r.Host = s.host
			r.Header.Set("Origin", s.Origin())
			r.Header.Set("Content-Type", "application/json")
			if w := send(s, r); w.Code != http.StatusNotFound {
				t.Errorf("%s %s%s: %d, want 404", rt.method, prefix, rt.path, w.Code)
			}
		}
		// With the secret, the route exists (not 404 from the router).
		if w := send(s, req(s, rt.method, rt.path, map[string]any{})); w.Code == http.StatusNotFound && rt.path != "api/permission" &&
			rt.path != "api/portal" && rt.path != "api/voice" && rt.path != "api/mic" {
			t.Errorf("%s %s with the secret: 404 %s", rt.method, rt.path, w.Body)
		}
	}
	if removed {
		t.Fatal("a request without the secret removed the agent")
	}
}

func TestHostMustBeTheLoopbackAddress(t *testing.T) {
	s := testServer(t, Deps{})
	port := s.host[strings.LastIndex(s.host, ":")+1:]
	for _, host := range []string{"localhost:" + port, "evil.example:" + port, "127.0.0.1", "127.0.0.1:1", "[::1]:" + port, ""} {
		r := req(s, "GET", "api/state", nil)
		r.Host = host
		if w := send(s, r); w.Code != http.StatusMisdirectedRequest {
			t.Errorf("Host %q: %d", host, w.Code)
		}
	}
	if w := send(s, req(s, "GET", "api/state", nil)); w.Code != http.StatusOK {
		t.Fatalf("the right host: %d %s", w.Code, w.Body)
	}
}

func TestPostsMustComeFromThePage(t *testing.T) {
	removed := 0
	s := testServer(t, Deps{Remove: func(context.Context) error { removed++; return nil }})
	cases := map[string]func(r *http.Request){
		"no Origin":      func(r *http.Request) { r.Header.Del("Origin") },
		"another origin": func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") },
		"localhost origin": func(r *http.Request) {
			r.Header.Set("Origin", strings.Replace(s.Origin(), "127.0.0.1", "localhost", 1))
		},
		"null origin":         func(r *http.Request) { r.Header.Set("Origin", "null") },
		"a form post":         func(r *http.Request) { r.Header.Set("Content-Type", "application/x-www-form-urlencoded") },
		"text/plain":          func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") },
		"no content type":     func(r *http.Request) { r.Header.Del("Content-Type") },
		"cross-site fetch":    func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
		"same-site, not ours": func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-site") },
	}
	for name, mutate := range cases {
		r := req(s, "POST", "api/remove", map[string]string{"confirm": "remove"})
		mutate(r)
		if w := send(s, r); w.Code != http.StatusForbidden {
			t.Errorf("%s: %d", name, w.Code)
		}
	}
	if removed != 0 {
		t.Fatalf("removed %d times by a forged request", removed)
	}
	// Without Sec-Fetch-Site (an older browser) the origin check still holds the line.
	r := req(s, "POST", "api/remove", map[string]string{"confirm": "remove"})
	r.Header.Del("Sec-Fetch-Site")
	if w := send(s, r); w.Code != http.StatusOK || removed != 1 {
		t.Fatalf("the page's own post: %d %s", w.Code, w.Body)
	}
}

func TestCrossSiteReadsAndOtherMethodsAreRefused(t *testing.T) {
	s := testServer(t, Deps{})
	r := req(s, "GET", "api/state", nil)
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	if w := send(s, r); w.Code != http.StatusForbidden {
		t.Fatalf("cross-site read: %d", w.Code)
	}
	// Opening the URL from another program is "none": the page itself loads.
	r = req(s, "GET", "", nil)
	r.Header.Set("Sec-Fetch-Site", "none")
	if w := send(s, r); w.Code != http.StatusOK {
		t.Fatalf("opening the page: %d", w.Code)
	}
	for _, m := range []string{"OPTIONS", "PUT", "DELETE", "PATCH"} {
		r := req(s, m, "api/state", nil)
		r.Header.Set("Origin", "http://evil.example")
		r.Header.Set("Access-Control-Request-Method", "POST")
		w := send(s, r)
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: %d", m, w.Code)
		}
	}
}

func TestNoCORSAndStrictHeadersEverywhere(t *testing.T) {
	s := testServer(t, Deps{Icon: []byte{0x89, 'P', 'N', 'G'}})
	reqs := []*http.Request{req(s, "GET", "", nil), req(s, "GET", "app.js", nil), req(s, "GET", "api/state", nil),
		req(s, "POST", "api/done", map[string]any{}), httptest.NewRequest("GET", "/nope", nil)}
	for _, r := range reqs {
		r.Header.Set("Origin", "http://evil.example")
		if r.Method == "POST" {
			r.Header.Set("Origin", s.Origin())
		}
		w := send(s, r)
		for k := range w.Header() {
			if strings.HasPrefix(strings.ToLower(k), "access-control-") {
				t.Errorf("%s %s answered %s", r.Method, r.URL.Path, k)
			}
		}
		for k, want := range map[string]string{"Referrer-Policy": "no-referrer", "X-Frame-Options": "DENY", "Cache-Control": "no-store"} {
			if got := w.Header().Get(k); got != want {
				t.Errorf("%s %s: %s = %q", r.Method, r.URL.Path, k, got)
			}
		}
		if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "unsafe") {
			t.Errorf("CSP %q", csp)
		}
	}
}

func TestThePageHasNoInlineScriptAndEveryAssetLoads(t *testing.T) {
	s := testServer(t, Deps{Icon: []byte{0x89, 'P', 'N', 'G'}})
	w := send(s, req(s, "GET", "", nil))
	html := w.Body.String()
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("index: %d %s", w.Code, w.Header().Get("Content-Type"))
	}
	for _, bad := range []string{"<script>", "onclick=", "onload=", "javascript:", "http://", "https://", "//cdn"} {
		if strings.Contains(html, bad) {
			t.Errorf("index.html contains %q", bad)
		}
	}
	for _, a := range []struct{ path, ct string }{{"app.js", "text/javascript"}, {"app.css", "text/css"}, {"icon.png", "image/png"}} {
		w := send(s, req(s, "GET", a.path, nil))
		if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), a.ct) || w.Body.Len() == 0 {
			t.Errorf("%s: %d %q", a.path, w.Code, w.Header().Get("Content-Type"))
		}
	}
	js := send(s, req(s, "GET", "app.js", nil)).Body.String()
	for _, bad := range []string{"innerHTML", "eval(", "localStorage", "http://", "https://"} {
		if strings.Contains(js, bad) {
			t.Errorf("app.js uses %q", bad)
		}
	}
	// The bare secret path redirects to the page (relative URLs need the slash).
	r := httptest.NewRequest("GET", "/s/"+s.secret, nil)
	r.Host = s.host
	if w := send(s, r); w.Code != http.StatusFound || w.Header().Get("Location") != s.URL() {
		t.Fatalf("bare secret: %d %q", w.Code, w.Header().Get("Location"))
	}
}

func TestUnknownFieldsAndHugeBodiesAreRefused(t *testing.T) {
	s := testServer(t, Deps{})
	if w := send(s, req(s, "POST", "api/server", map[string]any{"url": "x", "evil": 1})); w.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d", w.Code)
	}
	r := req(s, "POST", "api/signin", nil)
	r.Body = io.NopCloser(strings.NewReader(`{"email":"` + strings.Repeat("a", maxBody) + `"}`))
	if w := send(s, r); w.Code != http.StatusBadRequest {
		t.Fatalf("huge body: %d", w.Code)
	}
}

func TestDoneAndRemoveStopTheServer(t *testing.T) {
	for _, tc := range []struct {
		path, why string
		body      map[string]any
	}{{"api/done", "done", map[string]any{}}, {"api/remove", "removed", map[string]any{"confirm": "remove"}}} {
		s, err := New(Deps{Remove: func(context.Context) error { return nil }, RemoveNote: "bye"})
		if err != nil {
			t.Fatal(err)
		}
		got := make(chan string, 1)
		go func() { got <- s.Serve(context.Background()) }()
		r := req(s, "POST", tc.path, tc.body)
		if w := send(s, r); w.Code != 200 {
			t.Fatalf("%s: %d %s", tc.path, w.Code, w.Body)
		}
		if why := <-got; why != tc.why {
			t.Fatalf("%s: stopped with %q", tc.path, why)
		}
	}
}

func TestRemoveNeedsConfirmation(t *testing.T) {
	n := 0
	s := testServer(t, Deps{Remove: func(context.Context) error { n++; return errors.New("partly") }})
	if w := send(s, req(s, "POST", "api/remove", map[string]any{})); w.Code != http.StatusBadRequest || n != 0 {
		t.Fatalf("unconfirmed: %d n=%d", w.Code, n)
	}
	w := send(s, req(s, "POST", "api/remove", map[string]any{"confirm": "remove"}))
	if w.Code != http.StatusInternalServerError || n != 1 || s.finishedWhy() != "" {
		t.Fatalf("failed remove: %d n=%d finished=%q", w.Code, n, s.finishedWhy())
	}
}

func TestIdle(t *testing.T) {
	now := time.Unix(1000, 0)
	s := testServer(t, Deps{Now: func() time.Time { return now }})
	if s.idle(now.Add(IdleTimeout - time.Second)) {
		t.Fatal("idle too early")
	}
	now = now.Add(IdleTimeout - time.Second)
	send(s, req(s, "GET", "api/state", nil)) // the page asks: the clock restarts
	if s.idle(now.Add(IdleTimeout - time.Second)) {
		t.Fatal("a request didn't count")
	}
	if !s.idle(now.Add(IdleTimeout)) {
		t.Fatal("never idle")
	}
}

func TestNormalizeAddress(t *testing.T) {
	ok := map[string]string{
		"192.168.1.10":            "http://192.168.1.10:7700",
		" jarvis.local ":          "http://jarvis.local:7700",
		"jarvis.local:7700":       "http://jarvis.local:7700",
		"http://10.0.0.5:7700/":   "http://10.0.0.5:7700",
		"https://jarvis.example":  "https://jarvis.example",
		"http://[fe80::1]:7700":   "http://[fe80::1]:7700",
		"https://j.example:8443/": "https://j.example:8443",
		// A page of the web app: the path goes, and the server is found from its host.
		"http://host:7710/addons?x=1": "http://host:7710",
	}
	for in, want := range ok {
		if got, err := NormalizeAddress(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	for _, in := range []string{"", "   ", "ftp://x", "http://u:p@host", "file:///etc/passwd", "javascript:alert(1)", "http://"} {
		if got, err := NormalizeAddress(in); err == nil {
			t.Errorf("%q accepted as %q", in, got)
		}
	}
}

// --- a fake jarvisd ---

type fakeJarvisd struct {
	t *testing.T
	*httptest.Server

	mu          sync.Mutex
	households  []node.Household
	mustChange  bool
	password    string
	polls       int
	approveAt   int    // the poll that is approved
	pollStatus  string // what polls answer before approveAt ("pending" or "denied")
	registered  []map[string]any
	minted      []map[string]any
	loginBodies []string
}

func newFakeJarvisd(t *testing.T) *fakeJarvisd {
	f := &fakeJarvisd{t: t, password: "right", approveAt: 2, pollStatus: "pending",
		households: []node.Household{{ID: "h1", Name: "Home", Role: "member"}}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /info", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"service":"jarvis-config-service"}`)
	})
	mux.HandleFunc("GET /services", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"services": []map[string]string{
			{"name": node.SvcCommandCenter, "url": f.URL}, {"name": node.SvcAuth, "url": f.URL}}})
	})
	mux.HandleFunc("POST /auth/login", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var in struct{ Email, Password string }
		_ = json.Unmarshal(b, &in)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.loginBodies = append(f.loginBodies, string(b))
		if in.Password != f.password {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"detail":"Invalid credentials"}`)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "jwt-1", "must_change_password": f.mustChange})
	})
	mux.HandleFunc("GET /households", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer jwt-1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(f.households)
	})
	mux.HandleFunc("POST /api/v0/provisioning/token", func(w http.ResponseWriter, r *http.Request) {
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		if r.Header.Get("Authorization") != "Bearer jwt-1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		f.minted = append(f.minted, in)
		f.mu.Unlock()
		_, _ = io.WriteString(w, `{"token":"prov_1","node_id":"node-1"}`)
	})
	mux.HandleFunc("POST /api/v0/nodes/pairings", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"pairing_id":"p1","poll_secret":"sec","code":"ABCD-EFGH","expires_in":600,"interval":3}`)
	})
	mux.HandleFunc("POST /api/v0/nodes/pairings/p1/poll", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.polls++
		if f.polls < f.approveAt {
			_, _ = io.WriteString(w, `{"status":"`+f.pollStatus+`"}`)
			return
		}
		_, _ = io.WriteString(w, `{"status":"approved","node_id":"node-2","provisioning_token":"prov_2","household_id":"h1","household_name":"The Smiths","approved_by":"sam@example.com","room":"office","name":"Office PC"}`)
	})
	mux.HandleFunc("POST /api/v0/nodes/register", func(w http.ResponseWriter, r *http.Request) {
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		f.registered = append(f.registered, in)
		f.mu.Unlock()
		room, _ := in["room"].(string)
		if room == "" {
			room = "office"
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"node_id": in["node_id"].(string), "room": room, "node_key": "key-" + in["node_id"].(string)})
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

// joinDeps are Deps for a computer that isn't set up, against f.
type joinDeps struct {
	mu      sync.Mutex
	saved   []node.Creds
	after   int
	waits   int
	afterOK error
}

func (j *joinDeps) deps(f *fakeJarvisd) Deps {
	return Deps{
		ComputerName: "test-pc", GOOS: "linux", HTTP: f.Client(), Loopback: f.URL,
		Identity: node.Registration{DeviceID: "dev-1", NodeKind: "desktop", Room: "ignored"},
		Creds: func() (node.Creds, bool) {
			j.mu.Lock()
			defer j.mu.Unlock()
			if len(j.saved) == 0 {
				return node.Creds{}, false
			}
			return j.saved[len(j.saved)-1], true
		},
		SaveCreds: func(c node.Creds) error { j.mu.Lock(); j.saved = append(j.saved, c); j.mu.Unlock(); return nil },
		AfterJoin: func(context.Context) error { j.mu.Lock(); j.after++; j.mu.Unlock(); return j.afterOK },
		WaitPairing: func(ctx context.Context, _ time.Duration) error {
			j.mu.Lock()
			j.waits++
			j.mu.Unlock()
			return ctx.Err()
		},
	}
}

func pickServer(t *testing.T, s *Server, url string) stateView {
	t.Helper()
	w := send(s, req(s, "POST", "api/server", map[string]string{"url": url}))
	if w.Code != 200 {
		t.Fatalf("choose server: %d %s", w.Code, w.Body)
	}
	return decode[stateView](t, w)
}

func TestPairingHappyPath(t *testing.T) {
	f := newFakeJarvisd(t)
	j := &joinDeps{}
	s := testServer(t, j.deps(f))
	if w := send(s, req(s, "POST", "api/pair", map[string]any{})); w.Code != http.StatusConflict {
		t.Fatalf("pairing before a server is chosen: %d", w.Code)
	}
	st := pickServer(t, s, f.URL)
	if st.Server == nil || st.Server.URL != f.URL || st.Joined {
		t.Fatalf("after choosing: %+v", st)
	}
	w := send(s, req(s, "POST", "api/pair", map[string]any{}))
	st = decode[stateView](t, w)
	if w.Code != 200 || st.Join.Mode != ModePair || st.Join.Code != "ABCD-EFGH" {
		t.Fatalf("pair: %d %s", w.Code, w.Body)
	}
	s.app.wg.Wait() // the poll loop: pending, then approved
	// Approved, but nothing joins until this computer's user sees whose household it is.
	st = decode[stateView](t, send(s, req(s, "GET", "api/state", nil)))
	if st.Joined || st.Join.State != JoinConfirm || st.Join.Household != "The Smiths" || st.Join.ApprovedBy != "sam@example.com" ||
		st.Join.Room != "office" || st.Join.Code != "" || len(j.saved) != 0 || len(f.registered) != 0 {
		t.Fatalf("after approval: %+v saved=%v", st.Join, j.saved)
	}
	if w := send(s, req(s, "POST", "api/server/forget", map[string]any{})); w.Code != http.StatusConflict {
		t.Fatalf("forget while confirming: %d", w.Code)
	}
	w = send(s, req(s, "POST", "api/pair/confirm", map[string]any{}))
	st = decode[stateView](t, w)
	if w.Code != 200 || !st.Joined || st.Join.State != JoinJoined || st.Join.Code != "" {
		t.Fatalf("after confirming: %d %+v", w.Code, st.Join)
	}
	if len(j.saved) != 1 || j.saved[0].NodeID != "node-2" || j.saved[0].NodeKey != "key-node-2" || j.saved[0].Name != "Office PC" ||
		j.saved[0].ConfigURL != f.URL || j.after != 1 || j.waits != 1 {
		t.Fatalf("saved %+v after=%d waits=%d", j.saved, j.after, j.waits)
	}
	if _, hasRoom := f.registered[0]["room"]; hasRoom {
		t.Fatalf("registered with a room over the approver's: %v", f.registered[0])
	}
	// The computer's identity goes with it, so jarvisd can retire its previous node.
	if f.registered[0]["device_id"] != "dev-1" || f.registered[0]["node_kind"] != "desktop" {
		t.Fatalf("registered without the identity: %v", f.registered[0])
	}
	if st.Computer != "Office PC" {
		t.Fatalf("computer shown as %q", st.Computer)
	}
	// Joined: no second join, no other server.
	if w := send(s, req(s, "POST", "api/pair", map[string]any{})); w.Code != http.StatusConflict {
		t.Fatalf("pair again: %d", w.Code)
	}
	if w := send(s, req(s, "POST", "api/server", map[string]string{"url": f.URL})); w.Code != http.StatusConflict {
		t.Fatalf("server again: %d", w.Code)
	}
}

func TestPairingDeniedSaysSo(t *testing.T) {
	f := newFakeJarvisd(t)
	f.pollStatus, f.approveAt = "denied", 100
	j := &joinDeps{}
	s := testServer(t, j.deps(f))
	pickServer(t, s, f.URL)
	send(s, req(s, "POST", "api/pair", map[string]any{}))
	s.app.wg.Wait()
	st := decode[stateView](t, send(s, req(s, "GET", "api/state", nil)))
	if st.Joined || st.Join.State != JoinFailed || !strings.Contains(st.Join.Error, "said no") || len(j.saved) != 0 {
		t.Fatalf("%+v saved=%v", st.Join, j.saved)
	}
}

func TestPairingCancel(t *testing.T) {
	f := newFakeJarvisd(t)
	j := &joinDeps{}
	d := j.deps(f)
	t0 := time.Unix(5000, 0)
	d.Now = func() time.Time { return t0 }
	block := make(chan struct{})
	d.WaitPairing = func(ctx context.Context, _ time.Duration) error {
		close(block)
		<-ctx.Done()
		return ctx.Err()
	}
	s := testServer(t, d)
	pickServer(t, s, f.URL)
	send(s, req(s, "POST", "api/pair", map[string]any{}))
	<-block
	st := decode[stateView](t, send(s, req(s, "GET", "api/state", nil)))
	if st.Join.State != JoinWaiting || st.Join.ExpiresIn != 600 {
		t.Fatalf("waiting: %+v", st.Join)
	}
	if w := send(s, req(s, "POST", "api/server/forget", map[string]any{})); w.Code != http.StatusConflict {
		t.Fatalf("forget while a code is shown: %d", w.Code)
	}
	st = decode[stateView](t, send(s, req(s, "POST", "api/pair/cancel", map[string]any{})))
	s.app.wg.Wait()
	if st.Join.State != JoinIdle || len(j.saved) != 0 {
		t.Fatalf("after cancel: %+v", st.Join)
	}
}

// Declining the household the approver named: nothing registers or is saved, and the page is
// back where it started.
func TestPairingDeclineHousehold(t *testing.T) {
	f := newFakeJarvisd(t)
	j := &joinDeps{}
	s := testServer(t, j.deps(f))
	pickServer(t, s, f.URL)
	if w := send(s, req(s, "POST", "api/pair/confirm", map[string]any{})); w.Code != http.StatusConflict {
		t.Fatalf("confirm with nothing approved: %d", w.Code)
	}
	send(s, req(s, "POST", "api/pair", map[string]any{}))
	s.app.wg.Wait()
	st := decode[stateView](t, send(s, req(s, "GET", "api/state", nil)))
	if st.Join.State != JoinConfirm {
		t.Fatalf("not asking: %+v", st.Join)
	}
	st = decode[stateView](t, send(s, req(s, "POST", "api/pair/cancel", map[string]any{})))
	if st.Joined || st.Join.State != JoinIdle || len(j.saved) != 0 || len(f.registered) != 0 || j.after != 0 {
		t.Fatalf("after declining: %+v saved=%v registered=%v", st.Join, j.saved, f.registered)
	}
	if w := send(s, req(s, "POST", "api/pair/confirm", map[string]any{})); w.Code != http.StatusConflict {
		t.Fatalf("confirm after declining: %d", w.Code)
	}
}

func TestSignInOneHousehold(t *testing.T) {
	f := newFakeJarvisd(t)
	j := &joinDeps{}
	s := testServer(t, j.deps(f))
	pickServer(t, s, f.URL)
	w := send(s, req(s, "POST", "api/signin", map[string]string{"email": "a@example.com", "password": "right"}))
	st := decode[stateView](t, w)
	if w.Code != 200 || !st.Joined || st.Join.Mode != ModeSignIn {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if len(j.saved) != 1 || j.saved[0].NodeID != "node-1" || j.saved[0].Name != "test-pc" || j.after != 1 {
		t.Fatalf("saved %+v", j.saved)
	}
	if f.minted[0]["household_id"] != "h1" || f.minted[0]["room"] != signInRoom || f.minted[0]["name"] != "test-pc" {
		t.Fatalf("minted %v", f.minted)
	}
	if r := f.registered[0]; r["device_id"] != "dev-1" || r["node_kind"] != "desktop" || r["room"] != signInRoom {
		t.Fatalf("registered %v", r)
	}
	// The password went to jarvisd's login only, and is nowhere in what the page keeps or shows.
	b, _ := json.Marshal(j.saved)
	if strings.Contains(string(b), "right") || strings.Contains(w.Body.String(), "right") || s.app.jwt != "" {
		t.Fatalf("password or token kept: %s / %s / jwt=%q", b, w.Body, s.app.jwt)
	}
	if !strings.Contains(f.loginBodies[0], `"password":"right"`) {
		t.Fatalf("login body %s", f.loginBodies[0])
	}
}

func TestSignInPicksAHousehold(t *testing.T) {
	f := newFakeJarvisd(t)
	f.households = []node.Household{{ID: "h1", Name: "Home"}, {ID: "h2", Name: "Cabin"}}
	j := &joinDeps{}
	s := testServer(t, j.deps(f))
	pickServer(t, s, f.URL)
	st := decode[stateView](t, send(s, req(s, "POST", "api/signin", map[string]string{"email": "a@example.com", "password": "right"})))
	if st.Join.State != JoinChooseHousehold || len(st.Join.Households) != 2 || st.Join.Households[1].Name != "Cabin" || st.Joined {
		t.Fatalf("%+v", st.Join)
	}
	if w := send(s, req(s, "POST", "api/signin/household", map[string]string{"household_id": "someone-elses"})); w.Code != http.StatusBadRequest {
		t.Fatalf("a household not offered: %d", w.Code)
	}
	st = decode[stateView](t, send(s, req(s, "POST", "api/signin/household", map[string]string{"household_id": "h2"})))
	if !st.Joined || f.minted[0]["household_id"] != "h2" || s.app.jwt != "" {
		t.Fatalf("%+v minted=%v", st.Join, f.minted)
	}
	if w := send(s, req(s, "POST", "api/signin/household", map[string]string{"household_id": "h2"})); w.Code != http.StatusConflict {
		t.Fatalf("a second pick: %d", w.Code)
	}
}

func TestSignInRefusals(t *testing.T) {
	f := newFakeJarvisd(t)
	j := &joinDeps{}
	s := testServer(t, j.deps(f))
	pickServer(t, s, f.URL)
	w := send(s, req(s, "POST", "api/signin", map[string]string{"email": "a@example.com", "password": "wrong"}))
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "don't match") {
		t.Fatalf("wrong password: %d %s", w.Code, w.Body)
	}
	f.mustChange = true
	w = send(s, req(s, "POST", "api/signin", map[string]string{"email": "a@example.com", "password": "right"}))
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "temporary password") ||
		!strings.Contains(w.Body.String(), "Change your password first") {
		t.Fatalf("temporary password: %d %s", w.Code, w.Body)
	}
	f.mustChange = false
	f.households = nil
	w = send(s, req(s, "POST", "api/signin", map[string]string{"email": "a@example.com", "password": "right"}))
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "isn't in a household") {
		t.Fatalf("no household: %d %s", w.Code, w.Body)
	}
	if w := send(s, req(s, "POST", "api/signin", map[string]string{"email": "a@example.com"})); w.Code != http.StatusBadRequest {
		t.Fatalf("no password: %d", w.Code)
	}
	if len(j.saved) != 0 || len(f.minted) != 0 {
		t.Fatalf("joined anyway: %v %v", j.saved, f.minted)
	}
}

func TestJoinedButTheAgentDidntStart(t *testing.T) {
	f := newFakeJarvisd(t)
	j := &joinDeps{afterOK: errors.New("no display")}
	s := testServer(t, j.deps(f))
	pickServer(t, s, f.URL)
	st := decode[stateView](t, send(s, req(s, "POST", "api/signin", map[string]string{"email": "a@example.com", "password": "right"})))
	if !st.Joined || !strings.Contains(st.Join.Warning, "no display") {
		t.Fatalf("%+v", st.Join)
	}
}

func TestChooseServerChecksIt(t *testing.T) {
	s := testServer(t, Deps{HTTP: &http.Client{Timeout: time.Second}})
	notJarvis := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{"service":"other"}`) }))
	defer notJarvis.Close()
	w := send(s, req(s, "POST", "api/server", map[string]string{"url": notJarvis.URL}))
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "doesn't look like a Jarvis Automation server") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	// The web app's address (any page of it) finds the config service on the same host.
	f := newFakeJarvisd(t)
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "<!doctype html>") }))
	defer web.Close()
	fu, _ := url.Parse(f.URL)
	s2 := testServer(t, Deps{HTTP: &http.Client{Timeout: time.Second}, ConfigPort: fu.Port()})
	w = send(s2, req(s2, "POST", "api/server", map[string]string{"url": web.URL + "/addons"}))
	if st := decode[stateView](t, w); st.Server == nil || st.Server.URL != f.URL {
		t.Fatalf("web app address: %d %+v", w.Code, st.Server)
	}
	if w := send(s, req(s, "POST", "api/server", map[string]string{"url": "http://u:p@x"})); w.Code != http.StatusBadRequest {
		t.Fatalf("bad address: %d", w.Code)
	}
}

func TestSearchFindsThisComputerFirstThenTheNetwork(t *testing.T) {
	f := newFakeJarvisd(t)
	other := newFakeJarvisd(t)
	gone := httptest.NewServer(http.NotFoundHandler())
	goneURL := gone.URL
	gone.Close()
	browse := make(chan struct{})
	d := Deps{ComputerName: "test-pc", HTTP: f.Client(), Loopback: f.URL,
		Browse: func(ctx context.Context, found func(url, name string)) {
			found(f.URL, "Jarvis (test-pc)")     // this computer again: loopback
			found(other.URL, "Jarvis (kitchen)") // another server
			found(other.URL, "Jarvis (kitchen)") // its second announcement
			found(goneURL, "broken")             // not answering: never listed
			close(browse)
		}}
	s := testServer(t, d)
	s.app.find.isLocal = func(u string) bool { return u == f.URL }
	send(s, req(s, "POST", "api/servers/search", map[string]any{}))
	<-browse
	s.app.find.wg.Wait()
	r := decode[struct {
		Servers   []Found `json:"servers"`
		Searching bool    `json:"searching"`
	}](t, send(s, req(s, "GET", "api/servers", nil)))
	if r.Searching || len(r.Servers) != 2 || r.Servers[0].Where != WhereHere || r.Servers[0].Name != "test-pc" ||
		r.Servers[1].URL != other.URL || r.Servers[1].Where != WhereNetwork {
		t.Fatalf("%+v", r)
	}
	// Picking a found server keeps its name.
	st := pickServer(t, s, other.URL)
	if st.Server.Name != "Jarvis (kitchen)" || st.Server.Where != WhereNetwork {
		t.Fatalf("%+v", st.Server)
	}
}

func TestVoiceSwitch(t *testing.T) {
	on := true
	var sets []bool
	s := testServer(t, Deps{Voice: &Voice{On: func() bool { return on }, Set: func(v bool) error { sets = append(sets, v); on = v; return nil }}})
	st := decode[stateView](t, send(s, req(s, "POST", "api/voice", map[string]bool{"on": false})))
	if st.Voice == nil || st.Voice.On || len(sets) != 1 {
		t.Fatalf("%+v %v", st.Voice, sets)
	}
	s2 := testServer(t, Deps{})
	if w := send(s2, req(s2, "POST", "api/voice", map[string]bool{"on": true})); w.Code != http.StatusNotFound {
		t.Fatalf("no voice here: %d", w.Code)
	}
	if st := decode[stateView](t, send(s2, req(s2, "GET", "api/state", nil))); st.Voice != nil || st.Mic {
		t.Fatalf("voice shown where it isn't: %+v", st)
	}
}

func TestPermissionsAskThenOpenSettings(t *testing.T) {
	status := map[string]string{"accessibility": NotAsked, "screen": Denied}
	asked := make(chan string, 2)
	var opened []string
	perm := func(id string) Permission {
		return Permission{ID: id, Label: id, Pane: "x-apple.systempreferences:" + id,
			Status: func() string { return status[id] }, Ask: func() { asked <- id }}
	}
	s := testServer(t, Deps{Permissions: []Permission{perm("accessibility"), perm("screen")},
		OpenURL: func(_ context.Context, u string) error { opened = append(opened, u); return nil }})
	st := decode[stateView](t, send(s, req(s, "GET", "api/state", nil)))
	if len(st.Permissions) != 2 || st.Permissions[0].Status != NotAsked || st.Permissions[1].Status != Denied {
		t.Fatalf("%+v", st.Permissions)
	}
	send(s, req(s, "POST", "api/permission", map[string]string{"id": "accessibility"}))
	if got := <-asked; got != "accessibility" || len(opened) != 0 {
		t.Fatalf("asked %q opened %v", got, opened)
	}
	send(s, req(s, "POST", "api/permission", map[string]string{"id": "screen"}))
	if len(opened) != 1 || opened[0] != "x-apple.systempreferences:screen" {
		t.Fatalf("opened %v", opened)
	}
	if w := send(s, req(s, "POST", "api/permission", map[string]string{"id": "x-apple.systempreferences:anything"})); w.Code != http.StatusNotFound {
		t.Fatalf("an unknown id: %d", w.Code)
	}
	s.app.wg.Wait()
}

func TestPortalStep(t *testing.T) {
	allowed := false
	s := testServer(t, Deps{Portal: &Portal{
		Status: func(context.Context) (bool, bool, string) { return allowed, true, "" },
		Allow:  func(context.Context) error { allowed = true; return nil },
	}})
	st := decode[stateView](t, send(s, req(s, "GET", "api/state", nil)))
	if st.Portal == nil || st.Portal.Allowed || !st.Portal.Available {
		t.Fatalf("%+v", st.Portal)
	}
	st = decode[stateView](t, send(s, req(s, "POST", "api/portal", map[string]any{})))
	if !st.Portal.Allowed {
		t.Fatalf("%+v", st.Portal)
	}
}

func TestAlreadyJoinedShowsTheComputer(t *testing.T) {
	s := testServer(t, Deps{ComputerName: "host", Creds: func() (node.Creds, bool) {
		return node.Creds{NodeID: "n", NodeKey: "secret-key", Name: "Office PC", ConfigURL: "http://192.0.2.7:7700"}, true
	}})
	w := send(s, req(s, "GET", "api/state", nil))
	st := decode[stateView](t, w)
	if !st.Joined || st.Computer != "Office PC" || st.Server.Name != "192.0.2.7" || st.Join.State != JoinJoined {
		t.Fatalf("%+v", st)
	}
	if strings.Contains(w.Body.String(), "secret-key") {
		t.Fatal("the node key reached the page")
	}
}

func TestMDNSNamesAndURLs(t *testing.T) {
	for in, want := range map[string]string{"Jarvis (kitchen-pc)": "kitchen-pc", `Jarvis (Alex\'s Mac)`: "Alex's Mac", "Other": "Other"} {
		if got := ServerName(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
	got := entryURLs(7700, []net.IP{net.ParseIP("192.168.1.5")}, []net.IP{net.ParseIP("fe80::1"), net.ParseIP("fd00::5")})
	if strings.Join(got, " ") != "http://192.168.1.5:7700 http://[fd00::5]:7700" {
		t.Fatalf("%v", got)
	}
	if entryURLs(0, []net.IP{net.ParseIP("192.168.1.5")}, nil) != nil {
		t.Fatal("no port")
	}
}
