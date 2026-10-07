package cc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
)

// fakeTokenServer is a provider token endpoint recording each exchange form.
type fakeTokenServer struct {
	*httptest.Server
	mu     sync.Mutex
	forms  []url.Values
	status int
}

func newTokenServer(t *testing.T, tlsOn bool) *fakeTokenServer {
	f := &fakeTokenServer{status: 200}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		f.forms = append(f.forms, r.PostForm)
		status := f.status
		f.mu.Unlock()
		w.WriteHeader(status)
		io.WriteString(w, `{"refresh_token": "rt", "access_token": "at", "expires_in": 3600}`)
	})
	if tlsOn {
		f.Server = httptest.NewTLSServer(h)
	} else {
		f.Server = httptest.NewServer(h)
	}
	t.Cleanup(f.Close)
	return f
}

func (f *fakeTokenServer) last() url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.forms[len(f.forms)-1]
}

// oauthEnv lets the exchange reach loopback test servers and trusts their certificates.
func oauthEnv(t *testing.T, tlsSrv *fakeTokenServer) *env {
	return newEnv(t, envOpts{configure: func(m *Module) {
		m.smart = &smartHome{guard: func(netip.Addr, bool) bool { return false }}
		if tlsSrv != nil {
			m.smart.tls = tlsSrv.Client().Transport.(*http.Transport).TLSClientConfig
		}
	}})
}

func query(t *testing.T, raw string) (string, url.Values) {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Scheme + "://" + u.Host + u.Path, u.Query()
}

func TestOAuthLocalProvider(t *testing.T) {
	ha := newTokenServer(t, false)
	e := oauthEnv(t, nil)
	tok := e.auth.addUser(1, "hh1", authn.RoleMember)
	n := e.createNode("n1", "hh1")
	c := e.dialNode(n)
	c.subscribe("jarvis/auth/+/ready")

	body := map[string]any{"provider": "home_assistant", "node_id": "n1", "provider_base_url": ha.URL + "/",
		"auth_config": map[string]any{"provider": "home_assistant", "client_id": "ignored", "keys": []string{"HA_TOKEN"},
			"authorize_path": "/auth/authorize", "exchange_path": "/auth/token"}}
	h := bearer(tok)
	h["X-Forwarded-Proto"], h["X-Forwarded-Host"] = "https", "jarvis.lan:7703"
	r := e.do("POST", "/api/v0/oauth/sessions", body, h).want(201).json()
	if r["requires_code_exchange"] != false {
		t.Fatal(r)
	}
	authURL := r["authorize_url"].(string)
	base, q := query(t, authURL)
	// HA: client_id is CC's own origin, the redirect is CC's callback, query order as urlencode.
	if base != ha.URL+"/auth/authorize" || !strings.HasPrefix(authURL[len(base)+1:],
		"response_type=code&client_id=https%3A%2F%2Fjarvis.lan%3A7703&redirect_uri=https%3A%2F%2Fjarvis.lan%3A7703%2Fapi%2Fv0%2Foauth%2Fcallback&state=") {
		t.Fatal(authURL)
	}
	sid := r["session_id"].(string)
	if s := e.do("GET", "/api/v0/oauth/sessions/"+sid, nil, bearer(tok)).want(200).json(); s["status"] != "pending" || s["provider"] != "home_assistant" {
		t.Fatal(s)
	}

	e.do("GET", "/api/v0/oauth/callback?code=abc", nil, nil).want(400)
	e.do("GET", "/api/v0/oauth/callback?code=abc&state=nope", nil, nil).detail(400, "Invalid state parameter")
	cb := e.doNoRedirect("/api/v0/oauth/callback?code=abc&state=" + url.QueryEscape(q.Get("state")))
	if cb.StatusCode != 302 || cb.Header.Get("Location") != "jarvis://auth-complete?session_id="+sid {
		t.Fatal(cb.StatusCode, cb.Header)
	}
	f := ha.last()
	if f.Get("grant_type") != "authorization_code" || f.Get("code") != "abc" || f.Get("client_id") != "https://jarvis.lan:7703" ||
		f.Get("redirect_uri") != "https://jarvis.lan:7703/api/v0/oauth/callback" || f.Has("client_secret") || f.Has("code_verifier") {
		t.Fatal(f)
	}
	pk := c.next()
	if p := payload(t, pk); pk.TopicName != "jarvis/auth/home_assistant/ready" || p["node_id"] != "n1" || p["session_id"] != sid || p["user_id"] != 1.0 {
		t.Fatal(pk.TopicName, p)
	}
	e.do("GET", "/api/v0/oauth/callback?code=abc&state="+url.QueryEscape(q.Get("state")), nil, nil).detail(400, "Session already active")

	// The node pulls once; the session is consumed and its tokens wiped.
	cr := e.do("GET", "/api/v0/oauth/provider/home_assistant/credentials", nil, n.h()).want(200)
	if !strings.Contains(string(cr.body), `"token_data":{"refresh_token":"rt","access_token":"at","expires_in":3600}`) {
		t.Fatal(string(cr.body)) // the provider's response, verbatim
	}
	creds := cr.json()
	if creds["access_token"] != "at" || creds["refresh_token"] != "rt" || creds["base_url"] != ha.URL+"/" || creds["user_id"] != 1.0 {
		t.Fatal(creds)
	}
	e.do("GET", "/api/v0/oauth/provider/home_assistant/credentials", nil, n.h()).
		detail(404, "No active auth session found for this provider/node")
	var enc any
	e.d.Read.QueryRow(`SELECT token_data_enc FROM cc_auth_sessions WHERE id = ?`, sid).Scan(&enc)
	if s := e.do("GET", "/api/v0/oauth/sessions/"+sid, nil, bearer(tok)).json(); s["status"] != "consumed" || enc != nil {
		t.Fatal(s, enc)
	}
}

// doNoRedirect issues a GET without following redirects.
func (e *env) doNoRedirect(path string) *http.Response {
	e.t.Helper()
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := c.Get(e.srv.URL + path)
	if err != nil {
		e.t.Fatal(err)
	}
	res.Body.Close()
	return res
}

func TestOAuthRelayAndNative(t *testing.T) {
	google := newTokenServer(t, true)
	e := oauthEnv(t, google)
	tok := e.auth.addUser(1, "hh1", authn.RoleMember)
	e.createNode("n1", "hh1")
	ctx := context.Background()
	if err := e.m.settings.Set(ctx, settingOAuthRelayURL, "https://relay.example/", scopeHN("", "")); err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{"provider": "nest", "client_id": "cid", "keys": []string{"NEST"}, "scopes": []string{"a", "b c"},
		"authorize_url": "https://accounts.example/o/auth", "exchange_url": google.URL + "/token", "supports_pkce": true,
		"client_secret": "sec"}
	// Raw JSON: the extra params' key order is the client's.
	cfgJSON := strings.TrimSuffix(mustJSON(t, cfg), "}") +
		`, "extra_authorize_params": {"prompt": "consent", "client_id": "override", "access_type": "offline"}}`
	raw := `{"provider": "nest", "node_id": "n1", "auth_config": ` + cfgJSON + `}`
	cfg["extra_authorize_params"] = map[string]any{"client_id": "override"}
	r := e.do("POST", "/api/v0/oauth/sessions", raw, bearer(tok)).want(201).json()
	if r["requires_code_exchange"] != true {
		t.Fatal(r)
	}
	authURL := r["authorize_url"].(string)
	_, q := query(t, authURL)
	keys := []string{}
	for _, kv := range strings.Split(strings.SplitN(authURL, "?", 2)[1], "&") {
		keys = append(keys, strings.SplitN(kv, "=", 2)[0])
	}
	// Extra params override in place (dict.update) and append in the client's order.
	if strings.Join(keys, ",") != "response_type,client_id,redirect_uri,state,scope,code_challenge,code_challenge_method,prompt,access_type" ||
		q.Get("client_id") != "override" || q.Get("redirect_uri") != "https://relay.example/oauth/bounce" || q.Get("scope") != "a b c" ||
		q.Get("code_challenge_method") != "S256" || !strings.Contains(authURL, "scope=a+b+c") {
		t.Fatal(authURL)
	}
	st, err := base64.RawURLEncoding.DecodeString(q.Get("state"))
	if err != nil || !strings.HasPrefix(string(st), `{"t": "`) || !strings.HasSuffix(string(st), `", "r": "jarvis://auth-complete"}`) {
		t.Fatal(string(st), err)
	}
	sid := r["session_id"].(string)

	stranger := e.auth.addUser(9, "hh9", authn.RoleOwner)
	e.do("POST", "/api/v0/oauth/sessions/"+sid+"/exchange", map[string]any{"code": "c"}, bearer(stranger)).want(403)
	e.do("GET", "/api/v0/oauth/sessions/"+sid, nil, bearer(stranger)).want(403)
	e.do("POST", "/api/v0/oauth/sessions/nope/exchange", map[string]any{"code": "c"}, bearer(tok)).detail(404, "Auth session not found")
	e.do("POST", "/api/v0/oauth/sessions/"+sid+"/exchange", map[string]any{}, bearer(tok)).want(400)

	google.mu.Lock()
	google.status = 400
	google.mu.Unlock()
	e.do("POST", "/api/v0/oauth/sessions/"+sid+"/exchange", map[string]any{"code": "c"}, bearer(tok)).
		detail(502, "Token exchange failed with status 400")
	google.mu.Lock()
	google.status = 200
	google.mu.Unlock()
	if r := e.do("POST", "/api/v0/oauth/sessions/"+sid+"/exchange", map[string]any{"code": "c"}, bearer(tok)).want(200).json(); r["status"] != "ok" || r["session_id"] != sid {
		t.Fatal(r)
	}
	f := google.last()
	if f.Get("client_secret") != "sec" || len(f.Get("code_verifier")) != 86 || f.Get("redirect_uri") != "https://relay.example/oauth/bounce" ||
		f.Get("client_id") != "cid" {
		t.Fatal(f) // the effective client_id, not the extra-param override (legacy)
	}

	// Native redirect: plain state, still a code exchange; admin key callers have no user.
	cfg["native_redirect_uri"] = "com.example:/oauth2redirect"
	r = e.do("POST", "/api/v0/oauth/sessions", map[string]any{"provider": "nest", "node_id": "n1", "auth_config": cfg}, adminH()).want(201).json()
	_, q = query(t, r["authorize_url"].(string))
	if r["requires_code_exchange"] != true || q.Get("redirect_uri") != "com.example:/oauth2redirect" || len(q.Get("state")) != 43 {
		t.Fatal(r)
	}
	var uid any
	e.d.Read.QueryRow(`SELECT user_id FROM cc_auth_sessions WHERE id = ?`, r["session_id"]).Scan(&uid)
	if uid != nil {
		t.Fatal(uid)
	}

	// Expiry: 410 on exchange, "expired" on status.
	e.advance(authSessionTTL + time.Second)
	nid := r["session_id"].(string)
	e.do("POST", "/api/v0/oauth/sessions/"+nid+"/exchange", map[string]any{"code": "c"}, adminH()).detail(410, "Auth session expired")
	if s := e.do("GET", "/api/v0/oauth/sessions/"+nid, nil, adminH()).json(); s["status"] != "expired" {
		t.Fatal(s)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestOAuthSessionGuards(t *testing.T) {
	e := newEnv(t) // the real exchange guard
	tok := e.auth.addUser(1, "hh1", authn.RoleMember)
	stranger := e.auth.addUser(9, "hh9", authn.RoleOwner)
	e.createNode("n1", "hh1")
	create := func(h hdr, cfg map[string]any, base any) *resp {
		c := map[string]any{"provider": "p", "client_id": "cid", "keys": []string{"K"}}
		for k, v := range cfg {
			c[k] = v
		}
		return e.do("POST", "/api/v0/oauth/sessions", map[string]any{"provider": "p", "node_id": "n1", "provider_base_url": base, "auth_config": c}, h)
	}
	ext := map[string]any{"authorize_url": "https://a.example/auth", "exchange_url": "https://a.example/token"}
	// D4: the target node must be in the caller's household.
	create(bearer(stranger), ext, nil).detail(403, "User is not a member of this household")
	create(bearer(tok), ext, nil).want(201)
	e.do("POST", "/api/v0/oauth/sessions", map[string]any{"provider": "p", "node_id": "nope",
		"auth_config": map[string]any{"provider": "p", "client_id": "c", "keys": []string{}}}, bearer(tok)).detail(404, "Node not found")
	create(bearer(tok), map[string]any{"exchange_url": "https://a.example/token"}, nil).
		detail(400, "Cannot build authorize URL: need authorize_url or provider_base_url + authorize_path")
	create(bearer(tok), map[string]any{"authorize_url": "https://a.example/auth"}, nil).
		detail(400, "Cannot build exchange URL: need exchange_url or provider_base_url + exchange_path")
	e.do("POST", "/api/v0/oauth/sessions", map[string]any{"provider": "p", "node_id": "n1", "auth_config": map[string]any{"provider": "p"}}, bearer(tok)).want(400)
	e.do("POST", "/api/v0/oauth/sessions", map[string]any{"provider": "a/#", "node_id": "n1",
		"auth_config": map[string]any{"provider": "p", "client_id": "c", "keys": []string{}, "authorize_url": "https://x/a", "exchange_url": "https://x/t"}}, bearer(tok)).
		detail(400, "Invalid provider")

	// SSRF (D4): external endpoints are https on public addresses; LAN providers never loopback.
	for _, bad := range []struct {
		cfg  map[string]any
		base any
	}{
		{map[string]any{"authorize_url": "https://a/auth", "exchange_url": "http://a.example/token"}, nil},
		{map[string]any{"authorize_url": "https://a/auth", "exchange_url": "https://127.0.0.1/token"}, nil},
		{map[string]any{"authorize_url": "https://a/auth", "exchange_url": "https://169.254.169.254/latest"}, nil},
		{map[string]any{"authorize_url": "https://a/auth", "exchange_url": "https://10.0.0.5/token"}, nil},
		{map[string]any{"authorize_url": "https://a/auth", "exchange_url": "https://localhost/token"}, nil},
		{map[string]any{"authorize_path": "/a", "exchange_path": "/t"}, "http://127.0.0.1:8123"},
		{map[string]any{"authorize_path": "/a", "exchange_path": "/t"}, "http://[::1]:8123"},
		{map[string]any{"authorize_path": "/a", "exchange_path": "/t"}, "file:///etc/passwd"},
	} {
		if r := create(bearer(tok), bad.cfg, bad.base); r.status != 400 || !strings.HasPrefix(r.json()["detail"].(string), "Invalid exchange URL") {
			t.Errorf("%v %v: %d %s", bad.cfg, bad.base, r.status, r.body)
		}
	}
	// A LAN provider (Home Assistant on 192.168.x) is allowed.
	create(bearer(tok), map[string]any{"authorize_path": "/a", "exchange_path": "/t"}, "http://192.168.1.50:8123").want(201)
	// Home Assistant on jarvisd's own computer: its network address works; localhost gets a hint.
	create(bearer(tok), map[string]any{"authorize_path": "/a", "exchange_path": "/t"}, "http://10.0.0.122:8123").want(201)
	for _, base := range []string{"http://localhost:8123", "http://127.0.0.1:8123", "http://[::1]:8123"} {
		create(bearer(tok), map[string]any{"authorize_path": "/a", "exchange_path": "/t"}, base).detail(400, localhostHint)
	}

	// Exchange re-checks the stored URL, and the dial guard refuses the connected address
	// whatever name led there (DNS rebinding).
	srv := newTokenServer(t, false)
	e.d.Write.Exec(`INSERT INTO cc_auth_sessions (id, provider, node_id, status, state, client_id, exchange_url, created_at, expires_at)
		VALUES ('s1', 'p', 'n1', 'pending', 'st', 'cid', ?, ?, ?)`, "https://127.0.0.1/t", dbTime(e.now), dbTime(e.now.Add(time.Hour)))
	e.do("POST", "/api/v0/oauth/sessions/s1/exchange", map[string]any{"code": "c"}, bearer(tok)).want(400)
	for _, local := range []bool{false, true} {
		if _, err := e.m.exchangeClient(local).Get(srv.URL); err == nil || !strings.Contains(err.Error(), "destination address not allowed") {
			t.Fatal(local, err)
		}
	}
}

func TestOAuthBlocked(t *testing.T) {
	for _, c := range []struct {
		ip           string
		local, block bool
	}{
		{"8.8.8.8", false, false},
		{"10.0.0.5", false, true},
		{"127.0.0.1", false, true},
		{"169.254.169.254", false, true},
		{"10.0.0.5", true, false},
		{"192.168.1.50", true, false},
		{"fd00::1", true, false},
		{"127.0.0.1", true, true},
		{"::1", true, true},
		{"169.254.169.254", true, true},
		{"::ffff:127.0.0.1", true, true},
		{"0.0.0.0", true, true},
		{"224.0.0.1", true, true},
	} {
		if got := oauthBlocked(netip.MustParseAddr(c.ip), c.local); got != c.block {
			t.Errorf("%s local=%v: %v", c.ip, c.local, got)
		}
	}
}

func TestOAuthHelpers(t *testing.T) {
	// Relay state is Python's json.dumps spacing, unpadded base64url (§7.13).
	if got := relayState("abc", "jarvis://auth-complete"); got != base64.RawURLEncoding.EncodeToString([]byte(`{"t": "abc", "r": "jarvis://auth-complete"}`)) {
		t.Fatal(got)
	}
	if got := urlencode([]kv{{"a", "x y"}, {"b", "/:?"}, {"a", "z"}}); got != "a=z&b=%2F%3A%3F" {
		t.Fatal(got)
	}
	v, ch := pkce()
	if len(v) != 86 || len(ch) != 43 {
		t.Fatal(v, ch)
	}

	// The dedicated key persists in <home>/cc-oauth.key, 0600, shared across restarts.
	home := t.TempDir()
	a := newTokenCipher(home)
	enc, err := a.encrypt("secret")
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := newTokenCipher(home).decrypt(enc); err != nil || plain != "secret" {
		t.Fatal(plain, err)
	}
	if fi, err := os.Stat(filepath.Join(home, "cc-oauth.key")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatal(fi, err)
	}
	if _, err := newTokenCipher(t.TempDir()).decrypt(enc); err == nil {
		t.Fatal("decrypted with another key")
	}
	if _, err := base64.URLEncoding.DecodeString(enc); err != nil {
		t.Fatal("not padded base64url", enc)
	}
}
