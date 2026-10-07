package cc

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// Provider OAuth (oauth.py): CC is the redirect authority. It builds the authorize URL
// (state + PKCE), exchanges the code, holds the tokens encrypted for the minutes until the
// node pulls them once, and tells the node over jarvis/auth/{provider}/ready. Three redirect
// modes (§3.6): native app redirect, the cloud relay bounce (external providers), and CC's
// own /oauth/callback over LAN HTTP (local providers like Home Assistant; no Caddy, D1).
//
// D4/D5: session create checks the target node is in one of the caller's households, and
// exchange/status check the session's node the same way. SSRF (D4): the client still names
// the provider's endpoints (they come from the node's command metadata, which only mobile
// can decrypt), so the exchange is fenced instead: an absolute exchange_url must be https on
// a public address, and a provider_base_url (LAN) exchange may not reach loopback,
// link-local or multicast. The connected address is checked at dial time, redirects are not
// followed, and the URL is fixed on the session at create time (exchange takes only a code).

const (
	authSessionTTL  = 10 * time.Minute
	tokenExchangeTO = 15 * time.Second
	authCompleteURI = "jarvis://auth-complete"
)

// --- token encryption (AES-256-GCM, stored as b64url(nonce‖ct)) ---

// tokenCipher holds the dedicated at-rest key (D40, Q9): 32 random bytes generated on first
// use into <home>/cc-oauth.key (0600), no longer derived from SECRET_KEY. Without a home
// directory (tests) the key lives in memory.
type tokenCipher struct {
	path string
	once sync.Once
	aead cipher.AEAD
	err  error
}

func newTokenCipher(home string) *tokenCipher {
	c := &tokenCipher{}
	if home != "" {
		c.path = filepath.Join(home, "cc-oauth.key")
	}
	return c
}

func (c *tokenCipher) load() (cipher.AEAD, error) {
	c.once.Do(func() {
		key, err := c.key()
		if err != nil {
			c.err = err
			return
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			c.err = err
			return
		}
		c.aead, c.err = cipher.NewGCM(block)
	})
	return c.aead, c.err
}

func (c *tokenCipher) key() ([]byte, error) {
	if c.path == "" {
		k := make([]byte, 32)
		_, err := rand.Read(k)
		return k, err
	}
	if raw, err := os.ReadFile(c.path); err == nil {
		k, err := hex.DecodeString(strings.TrimSpace(string(raw)))
		if err != nil || len(k) != 32 {
			return nil, fmt.Errorf("cc: %s is not a 32-byte hex key", c.path)
		}
		return k, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(c.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return c.key() // lost a race with another writer: use theirs
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.WriteString(hex.EncodeToString(k) + "\n"); err != nil {
		return nil, err
	}
	return k, f.Sync()
}

func (c *tokenCipher) encrypt(plain string) (string, error) {
	aead, err := c.load()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(aead.Seal(nonce, nonce, []byte(plain), nil)), nil
}

func (c *tokenCipher) decrypt(enc string) (string, error) {
	aead, err := c.load()
	if err != nil {
		return "", err
	}
	raw, err := base64.URLEncoding.DecodeString(enc)
	if err != nil || len(raw) < aead.NonceSize() {
		return "", errors.New("cc: malformed encrypted value")
	}
	plain, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], nil)
	return string(plain), err
}

// --- PKCE and relay state ---

// pkce is _generate_pkce: token_urlsafe(64)[:128] and its S256 challenge.
func pkce() (verifier, challenge string) {
	verifier = tokenURLSafe(64)
	if len(verifier) > 128 {
		verifier = verifier[:128]
	}
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

// relayState is _encode_relay_state: unpadded base64url of Python's json.dumps({"t", "r"})
// (", " and ": " separators). jarvis-relay parses it, so it is a cross-repo contract (§7.13).
func relayState(csrf, redirect string) string {
	enc := func(s string) string {
		var b bytes.Buffer
		e := json.NewEncoder(&b)
		e.SetEscapeHTML(false)
		_ = e.Encode(s)
		return strings.TrimSuffix(b.String(), "\n")
	}
	return base64.RawURLEncoding.EncodeToString([]byte(`{"t": ` + enc(csrf) + `, "r": ` + enc(redirect) + `}`))
}

// kv is one ordered query parameter.
type kv struct{ k, v string }

// urlencode is Python's urlencode (quote_plus) over ordered pairs; a later pair with an
// existing key replaces its value in place, like dict.update.
func urlencode(pairs []kv) string {
	var order []string
	vals := map[string]string{}
	for _, p := range pairs {
		if _, seen := vals[p.k]; !seen {
			order = append(order, p.k)
		}
		vals[p.k] = p.v
	}
	parts := make([]string, len(order))
	for i, k := range order {
		parts[i] = url.QueryEscape(k) + "=" + url.QueryEscape(vals[k])
	}
	return strings.Join(parts, "&")
}

// --- exchange egress guard ---

// oauthBlocked is the default exchange guard: an external endpoint must be a public address
// (the quick_search SSRF table); a local (provider_base_url) one may be on the LAN but never
// loopback (jarvisd's own engines), link-local (cloud metadata), multicast or unspecified.
func oauthBlocked(ip netip.Addr, local bool) bool {
	ip = ip.Unmap()
	if !local {
		return servertools.IPBlocked(ip)
	}
	return !ip.IsValid() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() || ip.IsInterfaceLocalMulticast()
}

// exchangeIsLocal: the exchange endpoint was derived from the client's provider_base_url
// (the LAN provider mode).
func exchangeIsLocal(exchangeURL string, baseURL sql.NullString) bool {
	if !baseURL.Valid || baseURL.String == "" {
		return false
	}
	return strings.HasPrefix(exchangeURL, strings.TrimRight(baseURL.String, "/")+"/")
}

// checkExchangeURL validates the endpoint at session create (literal addresses; names are
// checked at dial time).
func (m *Module) checkExchangeURL(raw string, local bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return fail(http.StatusBadRequest, "Invalid exchange URL")
	}
	if local {
		if u.Scheme != "http" && u.Scheme != "https" {
			return fail(http.StatusBadRequest, "Invalid exchange URL: must be http(s)")
		}
	} else if u.Scheme != "https" {
		return fail(http.StatusBadRequest, "Invalid exchange URL: an external provider's exchange_url must be https")
	}
	host := strings.ToLower(u.Hostname())
	// Same-machine providers (Home Assistant next to jarvisd) must be addressed by the machine's
	// network address: nodes reach the provider at the same URL, and to a node "localhost" is
	// itself.
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return fail(http.StatusBadRequest, localhostHint)
	}
	if ip, err := netip.ParseAddr(host); err == nil && m.smart.guard(ip, local) {
		if ip.Unmap().IsLoopback() {
			return fail(http.StatusBadRequest, localhostHint)
		}
		return fail(http.StatusBadRequest, "Invalid exchange URL: host not allowed")
	}
	return nil
}

const localhostHint = "Invalid exchange URL: use the provider's network address (for example " +
	"http://192.168.1.20:8123 or http://homeassistant.local:8123), not localhost. Nodes and phones " +
	"reach it at the same address, even when it runs on the same computer as Jarvis."

var errExchangeBlocked = errors.New("destination address not allowed")

// exchangeClient refuses blocked connected addresses and never follows redirects.
func (m *Module) exchangeClient(local bool) *http.Client {
	dialer := &net.Dialer{
		Timeout: tokenExchangeTO,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip, err := netip.ParseAddr(host)
			if err != nil || m.smart.guard(ip, local) {
				return errExchangeBlocked
			}
			return nil
		},
	}
	return &http.Client{
		Timeout: tokenExchangeTO,
		Transport: &http.Transport{
			Proxy: nil, DialContext: dialer.DialContext, TLSHandshakeTimeout: tokenExchangeTO, TLSClientConfig: m.smart.tls,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// --- sessions ---

type authSession struct {
	id, provider, nodeID, status, state, clientID, createdAt, expiresAt string
	exchangeURL, codeVerifier, baseURL, redirectURI, clientSecretEnc    sql.NullString
	accessEnc, refreshEnc, tokenDataEnc, completedAt                    sql.NullString
	userID                                                              sql.NullInt64
}

const authSessionCols = `id, provider, node_id, status, state, client_id, created_at, expires_at, exchange_url,
	code_verifier, provider_base_url, redirect_uri, client_secret_enc, access_token_enc, refresh_token_enc,
	token_data_enc, completed_at, user_id`

func scanAuthSession(s scanner) (*authSession, error) {
	var x authSession
	err := s.Scan(&x.id, &x.provider, &x.nodeID, &x.status, &x.state, &x.clientID, &x.createdAt, &x.expiresAt,
		&x.exchangeURL, &x.codeVerifier, &x.baseURL, &x.redirectURI, &x.clientSecretEnc, &x.accessEnc, &x.refreshEnc,
		&x.tokenDataEnc, &x.completedAt, &x.userID)
	if err != nil {
		return nil, err
	}
	return &x, nil
}

func (m *Module) authSessionWhere(ctx context.Context, where string, args ...any) (*authSession, error) {
	return scanAuthSession(m.deps.DB.Read.QueryRowContext(ctx, `SELECT `+authSessionCols+` FROM cc_auth_sessions WHERE `+where, args...))
}

// externalBase is _get_external_url: the oauth.external_url setting, else the request's
// X-Forwarded-Proto / X-Forwarded-Host / Host.
func (m *Module) externalBase(ctx context.Context, r *http.Request) string {
	if u := m.settings.String(ctx, settingOAuthExternalURL, settings.Scope{}); u != "" {
		return strings.TrimRight(u, "/")
	}
	scheme := r.Header.Get("X-Forwarded-Proto")
	if scheme == "" {
		scheme = "http"
		if r.TLS != nil {
			scheme = "https"
		}
	}
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	if host == "" {
		host = "localhost"
	}
	return scheme + "://" + host
}

// authConfig is AuthConfigPayload (the fields CC uses; the rest are validated and ignored,
// as legacy ignored extra_exchange_params and send_redirect_uri_in_exchange).
type authConfig struct {
	clientID, authorizeURL, exchangeURL, authorizePath, exchangePath string
	clientSecret, nativeRedirect                                     string
	scopes                                                           []string
	extraAuthorize                                                   []kv
	supportsPKCE                                                     bool
}

// orderedStrMap reads a dict[str, str] keeping the client's key order.
func orderedStrMap(b *body, raw any, name string) []kv {
	if raw == nil {
		return nil
	}
	obj, ok := raw.(*pyjson.Object)
	if !ok {
		b.fail(name, "Input should be a valid dictionary")
		return nil
	}
	var out []kv
	for _, k := range obj.Keys() {
		v, _ := obj.Get(k)
		s, isStr := v.(string)
		if !isStr {
			b.fail(name+" -> "+k, "Input should be a valid string")
			continue
		}
		out = append(out, kv{k, s})
	}
	return out
}

func (m *Module) handleCreateAuthSession(w http.ResponseWriter, r *http.Request, a provAuth) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, httpx.MaxBody))
	if err != nil {
		detail(w, http.StatusRequestEntityTooLarge, "Request body too large")
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	provider, _ := b.str("provider", true)
	nodeID, _ := b.str("node_id", true)
	baseURL, _ := b.optStrPtr("provider_base_url")
	acm, _ := b.object("auth_config", true)
	var cfg authConfig
	if acm != nil {
		ab := &body{m: acm, loc: "body -> auth_config", errs: b.errs}
		ab.str("provider", true)
		cfg.clientID, _ = ab.str("client_id", true)
		if _, present := acm["keys"]; !present {
			ab.fail("keys", "Field required")
		} else {
			ab.strList("keys")
		}
		cfg.authorizeURL, _ = ab.str("authorize_url", false)
		cfg.exchangeURL, _ = ab.str("exchange_url", false)
		cfg.authorizePath, _ = ab.str("authorize_path", false)
		cfg.exchangePath, _ = ab.str("exchange_path", false)
		cfg.scopes, _ = ab.strList("scopes")
		cfg.clientSecret, _ = ab.str("client_secret", false)
		cfg.nativeRedirect, _ = ab.str("native_redirect_uri", false)
		cfg.supportsPKCE, _ = ab.boolean("supports_pkce")
		ab.boolean("send_redirect_uri_in_exchange")
		// Python keeps the client's key order for the authorize query.
		if v, err := pyjson.Loads(string(raw)); err == nil {
			if top, ok := v.(*pyjson.Object); ok {
				if ac, ok := top.Get("auth_config"); ok {
					if aco, ok := ac.(*pyjson.Object); ok {
						eap, _ := aco.Get("extra_authorize_params")
						cfg.extraAuthorize = orderedStrMap(ab, eap, "extra_authorize_params")
						eep, _ := aco.Get("extra_exchange_params")
						orderedStrMap(ab, eep, "extra_exchange_params")
					}
				}
			}
		}
	}
	if !b.done(w) {
		return
	}
	if strings.ContainsAny(provider, "/+#") || provider == "" {
		// The provider names an MQTT topic level (jarvis/auth/{provider}/ready).
		detail(w, http.StatusBadRequest, "Invalid provider")
		return
	}
	ctx := r.Context()
	// D4/D5: the target node must be in one of the caller's households.
	if _, err := m.nodeAccess(ctx, a, nodeID); err != nil {
		m.writeErr(w, err)
		return
	}
	base := ""
	if baseURL != nil {
		base = *baseURL
	}
	var authURL, exchangeURL string
	switch {
	case cfg.authorizeURL != "":
		authURL = cfg.authorizeURL
	case base != "" && cfg.authorizePath != "":
		authURL = strings.TrimRight(base, "/") + cfg.authorizePath
	default:
		detail(w, http.StatusBadRequest, "Cannot build authorize URL: need authorize_url or provider_base_url + authorize_path")
		return
	}
	switch {
	case cfg.exchangeURL != "":
		exchangeURL = cfg.exchangeURL
	case base != "" && cfg.exchangePath != "":
		exchangeURL = strings.TrimRight(base, "/") + cfg.exchangePath
	default:
		detail(w, http.StatusBadRequest, "Cannot build exchange URL: need exchange_url or provider_base_url + exchange_path")
		return
	}
	baseNS := sql.NullString{String: base, Valid: baseURL != nil}
	if err := m.checkExchangeURL(exchangeURL, exchangeIsLocal(exchangeURL, baseNS)); err != nil {
		m.writeErr(w, err)
		return
	}

	state := tokenURLSafe(32)
	var verifier, challenge string
	if cfg.supportsPKCE {
		verifier, challenge = pkce()
	}
	relay := m.settings.String(ctx, settingOAuthRelayURL, settings.Scope{})
	useRelay := relay != "" && cfg.authorizeURL != ""
	var redirectURI, encodedState, clientID string
	switch {
	case cfg.nativeRedirect != "":
		// Native app redirect: the app catches the code and POSTs it to /exchange.
		redirectURI, encodedState, clientID = cfg.nativeRedirect, state, cfg.clientID
		useRelay = true
	case useRelay:
		redirectURI = strings.TrimRight(relay, "/") + "/oauth/bounce"
		encodedState, clientID = relayState(state, authCompleteURI), cfg.clientID
	default:
		// Local provider: CC's own callback. HA requires client_id to match the redirect
		// origin, so with authorize_path the external base is the client_id.
		ext := m.externalBase(ctx, r)
		redirectURI, encodedState, clientID = ext+"/api/v0/oauth/callback", state, cfg.clientID
		if cfg.authorizePath != "" {
			clientID = ext
		}
	}
	params := []kv{{"response_type", "code"}, {"client_id", clientID}, {"redirect_uri", redirectURI}, {"state", encodedState}}
	if len(cfg.scopes) > 0 {
		params = append(params, kv{"scope", strings.Join(cfg.scopes, " ")})
	}
	if challenge != "" {
		params = append(params, kv{"code_challenge", challenge}, kv{"code_challenge_method", "S256"})
	}
	params = append(params, cfg.extraAuthorize...) // extra params override (§7.13)
	fullAuthorize := authURL + "?" + urlencode(params)

	var secretEnc any
	if cfg.clientSecret != "" {
		enc, err := m.smart.cipher.encrypt(cfg.clientSecret)
		if err != nil {
			m.internalError(w, err)
			return
		}
		secretEnc = enc
	}
	var userID, verifierV any
	if !a.admin {
		userID = a.user.ID
	}
	if verifier != "" {
		verifierV = verifier
	}
	now := m.now()
	id := uuid4()
	if _, err := m.deps.DB.Write.ExecContext(ctx, `INSERT INTO cc_auth_sessions (id, provider, node_id, user_id, status, state,
		code_verifier, provider_base_url, authorize_url, exchange_url, redirect_uri, client_id, client_secret_enc, created_at,
		expires_at) VALUES (?, ?, ?, ?, 'pending', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, provider, nodeID, userID, state, verifierV, nullStr(baseURL), fullAuthorize, exchangeURL, redirectURI, clientID,
		secretEnc, dbTime(now), dbTime(now.Add(authSessionTTL))); err != nil {
		m.internalError(w, err)
		return
	}
	m.deps.Log.Info("cc: auth session created", "session", id[:8], "provider", provider, "node", nodeID)
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{
		"session_id": id, "authorize_url": fullAuthorize, "requires_code_exchange": useRelay,
	})
}

// completeSession exchanges code at the session's token endpoint, stores the tokens
// encrypted, marks the session active and tells the node.
func (m *Module) completeSession(ctx context.Context, s *authSession, code, redirectURI string) error {
	if s.status != "pending" {
		return fail(http.StatusBadRequest, "Session already "+s.status)
	}
	if m.now().After(parseTS(s.expiresAt)) {
		if _, err := m.deps.DB.Write.ExecContext(ctx, `UPDATE cc_auth_sessions SET status = 'expired' WHERE id = ?`, s.id); err != nil {
			return err
		}
		return fail(http.StatusGone, "Auth session expired")
	}
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("client_id", s.clientID)
	if s.clientSecretEnc.Valid {
		secret, err := m.smart.cipher.decrypt(s.clientSecretEnc.String)
		if err != nil {
			return err
		}
		form.Set("client_secret", secret)
	}
	if redirectURI != "" {
		form.Set("redirect_uri", redirectURI)
	}
	if s.codeVerifier.Valid {
		form.Set("code_verifier", s.codeVerifier.String)
	}
	local := exchangeIsLocal(s.exchangeURL.String, s.baseURL)
	if err := m.checkExchangeURL(s.exchangeURL.String, local); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.exchangeURL.String, strings.NewReader(form.Encode()))
	if err != nil {
		return fail(http.StatusBadGateway, "Token exchange failed: "+err.Error())
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := m.exchangeClient(local)
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		m.deps.Log.Error("cc: token exchange failed", "session", s.id[:8], "err", err)
		return fail(http.StatusBadGateway, "Token exchange failed: "+err.Error())
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		m.deps.Log.Error("cc: token exchange rejected", "session", s.id[:8], "status", resp.StatusCode)
		return fail(http.StatusBadGateway, fmt.Sprintf("Token exchange failed with status %d", resp.StatusCode))
	}
	var tokens map[string]any
	if err := json.Unmarshal(body, &tokens); err != nil || tokens == nil {
		return fail(http.StatusBadGateway, "Token exchange returned an invalid response")
	}
	enc := func(v any) (any, error) {
		s, ok := v.(string)
		if !ok || s == "" {
			return nil, nil
		}
		return m.smart.cipher.encrypt(s)
	}
	access, err := enc(tokens["access_token"])
	if err != nil {
		return err
	}
	refresh, err := enc(tokens["refresh_token"])
	if err != nil {
		return err
	}
	// The full token response, byte for byte (key order kept for the node).
	all, err := m.smart.cipher.encrypt(string(bytes.TrimSpace(body)))
	if err != nil {
		return err
	}
	if _, err := m.deps.DB.Write.ExecContext(ctx, `UPDATE cc_auth_sessions SET access_token_enc = ?, refresh_token_enc = ?,
		token_data_enc = ?, status = 'active', completed_at = ? WHERE id = ?`, access, refresh, all, dbTime(m.now()), s.id); err != nil {
		return err
	}
	m.deps.Log.Info("cc: auth session completed", "session", s.id[:8], "provider", s.provider, "node", s.nodeID)
	ready, _ := json.Marshal(map[string]any{"provider": s.provider, "node_id": s.nodeID, "session_id": s.id, "user_id": nullInt(s.userID)})
	if err := m.bus.PublishTopic("jarvis/auth/"+s.provider+"/ready", ready); err != nil {
		m.deps.Log.Warn("cc: auth ready not published", "provider", s.provider, "err", err)
	}
	return nil
}

func nullInt(n sql.NullInt64) any {
	if !n.Valid {
		return nil
	}
	return n.Int64
}

// handleOAuthCallback is the direct-mode redirect target; the state is the capability.
func (m *Module) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var errs []string
	for _, k := range []string{"code", "state"} {
		if _, ok := q[k]; !ok {
			errs = append(errs, "query -> "+k+": Field required")
		}
	}
	if len(errs) > 0 {
		validationError(w, errs...)
		return
	}
	ctx := r.Context()
	s, err := m.authSessionWhere(ctx, `state = ?`, q.Get("state"))
	if errors.Is(err, sql.ErrNoRows) {
		detail(w, http.StatusBadRequest, "Invalid state parameter")
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	redirect := s.redirectURI.String
	if redirect == "" {
		redirect = m.externalBase(ctx, r) + "/api/v0/oauth/callback"
	}
	if err := m.completeSession(ctx, s, q.Get("code"), redirect); err != nil {
		m.writeErr(w, err)
		return
	}
	// Absolute custom-scheme redirect so the mobile WebView sees completion.
	w.Header().Set("Location", authCompleteURI+"?session_id="+s.id)
	w.WriteHeader(http.StatusFound)
}

// ownedSession loads a session and checks the caller against its node's household (D4/D5).
func (m *Module) ownedSession(ctx context.Context, a provAuth, id string) (*authSession, error) {
	s, err := m.authSessionWhere(ctx, `id = ?`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fail(http.StatusNotFound, "Auth session not found")
	}
	if err != nil {
		return nil, err
	}
	if !a.admin {
		n, err := m.nodeByID(ctx, s.nodeID)
		if err != nil {
			return nil, err
		}
		if err := m.householdAccess(ctx, a, n.householdID.String); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// handleExchangeCode is the relay/native path: mobile posts the code it caught. Only the
// code comes from the body; every URL was fixed at create time (D4).
func (m *Module) handleExchangeCode(w http.ResponseWriter, r *http.Request, a provAuth) {
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	code, _ := b.str("code", true)
	if !b.done(w) {
		return
	}
	ctx := r.Context()
	id := r.PathValue("session_id")
	s, err := m.ownedSession(ctx, a, id)
	if err != nil {
		m.writeErr(w, err)
		return
	}
	if err := m.completeSession(ctx, s, code, s.redirectURI.String); err != nil {
		m.writeErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok", "session_id": id})
}

func (m *Module) handleAuthSessionStatus(w http.ResponseWriter, r *http.Request, a provAuth) {
	ctx := r.Context()
	s, err := m.ownedSession(ctx, a, r.PathValue("session_id"))
	if err != nil {
		m.writeErr(w, err)
		return
	}
	if s.status == "pending" && m.now().After(parseTS(s.expiresAt)) {
		if _, err := m.deps.DB.Write.ExecContext(ctx, `UPDATE cc_auth_sessions SET status = 'expired' WHERE id = ?`, s.id); err != nil {
			m.internalError(w, err)
			return
		}
		s.status = "expired"
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"session_id": s.id, "status": s.status, "provider": s.provider})
}

// handleProviderCredentials is the node's one-time pull after auth/{provider}/ready: the
// newest active session for (provider, this node), decrypted, then consumed. The encrypted
// tokens are wiped from the row on consumption (legacy kept them indefinitely, §8).
func (m *Module) handleProviderCredentials(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	ctx := r.Context()
	provider := r.PathValue("provider")
	s, err := m.authSessionWhere(ctx, `provider = ? AND node_id = ? AND status = 'active' ORDER BY completed_at DESC, rowid DESC LIMIT 1`,
		provider, n.ID)
	if errors.Is(err, sql.ErrNoRows) {
		detail(w, http.StatusNotFound, "No active auth session found for this provider/node")
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	dec := func(v sql.NullString) (any, error) {
		if !v.Valid || v.String == "" {
			return nil, nil
		}
		return m.smart.cipher.decrypt(v.String)
	}
	access, err := dec(s.accessEnc)
	if err != nil {
		m.internalError(w, err)
		return
	}
	refresh, err := dec(s.refreshEnc)
	if err != nil {
		m.internalError(w, err)
		return
	}
	var tokenData any
	if td, err := dec(s.tokenDataEnc); err != nil {
		m.internalError(w, err)
		return
	} else if str, ok := td.(string); ok {
		tokenData = json.RawMessage(str)
	}
	res, err := m.deps.DB.Write.ExecContext(ctx, `UPDATE cc_auth_sessions SET status = 'consumed', access_token_enc = NULL,
		refresh_token_enc = NULL, token_data_enc = NULL, client_secret_enc = NULL WHERE id = ? AND status = 'active'`, s.id)
	if err != nil {
		m.internalError(w, err)
		return
	}
	if k, _ := res.RowsAffected(); k == 0 {
		// A concurrent pull won: single use.
		detail(w, http.StatusNotFound, "No active auth session found for this provider/node")
		return
	}
	m.deps.Log.Info("cc: credentials consumed", "session", s.id[:8], "provider", provider, "node", n.ID)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"access_token": access, "refresh_token": refresh, "token_data": tokenData,
		"base_url": nullable(s.baseURL), "user_id": nullInt(s.userID),
	})
}
