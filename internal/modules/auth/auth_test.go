package auth

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	pconfig "github.com/alexberardi/jarvis-server/internal/platform/config"
	"github.com/alexberardi/jarvis-server/internal/platform/db"
	"github.com/alexberardi/jarvis-server/internal/platform/module"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

const adminToken = "test-admin-token"

func TestMain(m *testing.M) {
	bcryptCost = bcrypt.MinCost // passlib's 12 rounds would make the suite take minutes
	rsaBits = 1024
	os.Exit(m.Run())
}

type env struct {
	t    *testing.T
	m    *Module
	h    http.Handler
	db   *db.DB
	home string
	app  struct{ id, key string }
}

func newEnv(t *testing.T, opts ...func(*Module)) *env {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "jarvis.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := db.Migrate(ctx, d, "auth", Migrations()); err != nil {
		t.Fatal(err)
	}
	m := &Module{AdminToken: adminToken, RateLimit: RateLimit{Disabled: true}}
	for _, o := range opts {
		o(m)
	}
	home := t.TempDir()
	mux := http.NewServeMux()
	m.Register(mux, module.Deps{
		Config: pconfig.Config{Home: home, Host: "127.0.0.1", Ports: map[string]int{}},
		DB:     d,
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, m: m, h: mux, db: d, home: home}
	_, body := e.do("POST", "/admin/app-clients", map[string]any{"app_id": "test-app", "name": "Test"}, hAdmin)
	e.app.id, e.app.key = "test-app", body["key"].(string)
	return e
}

type hdrs map[string]string

var hAdmin = hdrs{"X-Jarvis-Admin-Token": adminToken}

func bearerH(tok string) hdrs { return hdrs{"Authorization": "Bearer " + tok} }

func (e *env) appH() hdrs { return hdrs{"X-Jarvis-App-Id": e.app.id, "X-Jarvis-App-Key": e.app.key} }

// raw sends a request and returns the recorder.
func (e *env) raw(method, path string, body any, h hdrs) *httptest.ResponseRecorder {
	e.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	default:
		buf, _ := json.Marshal(b)
		rd = bytes.NewReader(buf)
	}
	r := httptest.NewRequest(method, path, rd)
	r.RemoteAddr = "192.0.2.1:1234"
	for k, v := range h {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	return rec
}

// do returns status and the body as an object (nil for arrays; use list).
func (e *env) do(method, path string, body any, h hdrs) (int, map[string]any) {
	e.t.Helper()
	rec := e.raw(method, path, body, h)
	var m map[string]any
	json.Unmarshal(rec.Body.Bytes(), &m)
	return rec.Code, m
}

func (e *env) list(method, path string, h hdrs) []map[string]any {
	e.t.Helper()
	rec := e.raw(method, path, nil, h)
	if rec.Code != 200 {
		e.t.Fatalf("%s %s: %d %s", method, path, rec.Code, rec.Body)
	}
	var out []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		e.t.Fatal(err)
	}
	return out
}

// expect fails unless status matches and, when want is non-empty, detail equals it.
func (e *env) expect(status int, wantDetail string, method, path string, body any, h hdrs) map[string]any {
	e.t.Helper()
	c, m := e.do(method, path, body, h)
	if c != status {
		e.t.Fatalf("%s %s: status %d, want %d: %v", method, path, c, status, m)
	}
	if wantDetail != "" && m["detail"] != wantDetail {
		e.t.Fatalf("%s %s: detail %q, want %q", method, path, m["detail"], wantDetail)
	}
	return m
}

type testUser struct {
	id              int64
	email, password string
	access, refresh string
	household       string
}

var userN int

func (e *env) register(extra ...hdrs) *testUser {
	e.t.Helper()
	userN++
	u := &testUser{email: fmt.Sprintf("user%d@example.com", userN), password: "password-" + fmt.Sprint(userN)}
	var h hdrs
	if len(extra) > 0 {
		h = extra[0]
	}
	m := e.expect(201, "", "POST", "/auth/register", map[string]any{"email": u.email, "password": u.password}, h)
	u.id = int64(m["user"].(map[string]any)["id"].(float64))
	u.access, u.refresh, u.household = m["access_token"].(string), m["refresh_token"].(string), m["household_id"].(string)
	return u
}

func (e *env) superuser() *testUser {
	u := e.register()
	e.expect(200, "", "PUT", fmt.Sprintf("/admin/users/%d/superuser", u.id), map[string]any{"is_superuser": true}, hAdmin)
	m := e.expect(200, "", "POST", "/auth/login", map[string]any{"email": u.email, "password": u.password}, nil)
	u.access, u.refresh = m["access_token"].(string), m["refresh_token"].(string)
	return u
}

func claimsOf(t *testing.T, tok string) (map[string]any, map[string]any) {
	t.Helper()
	parts := strings.Split(tok, ".")
	var hdr, c map[string]any
	for i, v := range []*map[string]any{&hdr, &c} {
		raw, err := base64.RawURLEncoding.DecodeString(parts[i])
		if err != nil {
			t.Fatal(err)
		}
		json.Unmarshal(raw, v)
	}
	return hdr, c
}

func keysOf(m map[string]any) string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	return strings.Join(sortStrings(ks), ",")
}

func sortStrings(s []string) []string {
	for i := range s {
		for j := i + 1; j < len(s); j++ {
			if s[j] < s[i] {
				s[i], s[j] = s[j], s[i]
			}
		}
	}
	return s
}

// withClock pins the module clock for the test.
func withClock(t *testing.T, at time.Time) *time.Time {
	t.Helper()
	cur := at
	old := now
	now = func() time.Time { return cur }
	t.Cleanup(func() { now = old })
	return &cur
}

// --- tests ---

func TestHealthAndPublicKey(t *testing.T) {
	e := newEnv(t)
	e.expect(200, "", "GET", "/health", nil, nil)
	m := e.expect(200, "", "GET", "/auth/public-key", nil, nil)
	if keysOf(m) != "algorithm,kid,public_key" || m["algorithm"] != "RS256" {
		t.Fatalf("%v", m)
	}
	if _, err := jwt.ParseRSAPublicKeyFromPEM([]byte(m["public_key"].(string))); err != nil {
		t.Fatal(err)
	}
}

func TestRegisterLoginMe(t *testing.T) {
	e := newEnv(t)
	m := e.expect(201, "", "POST", "/auth/register", map[string]any{"email": "Alice@Example.COM", "password": "password1"}, nil)
	if keysOf(m) != "access_token,household_id,refresh_token,token_type,user" || m["token_type"] != "bearer" {
		t.Fatalf("register shape %v", m)
	}
	user := m["user"].(map[string]any)
	// EmailStr lowercases the domain; the username defaults to the local part.
	if user["email"] != "Alice@example.com" || user["username"] != "Alice" || keysOf(user) != "email,id,is_superuser,must_change_password,username" {
		t.Fatalf("user %v", user)
	}
	hdr, c := claimsOf(t, m["access_token"].(string))
	if hdr["alg"] != "RS256" || hdr["kid"] == nil || hdr["typ"] != "JWT" {
		t.Fatalf("header %v", hdr)
	}
	if keysOf(c) != "email,exp,household_id,iat,is_superuser,jti,sub" || c["sub"] != fmt.Sprint(user["id"]) ||
		c["household_id"] != m["household_id"] || c["is_superuser"] != false {
		t.Fatalf("claims %v", c)
	}
	if exp, iat := c["exp"].(float64), c["iat"].(float64); exp-iat != 30*60 {
		t.Fatalf("ttl %v", exp-iat)
	}
	hh := e.list("GET", "/households", bearerH(m["access_token"].(string)))
	if len(hh) != 1 || hh[0]["name"] != "My Home" || hh[0]["role"] != "admin" {
		t.Fatalf("households %v", hh)
	}

	e.expect(400, "Email already registered", "POST", "/auth/register", map[string]any{"email": "Alice@example.com", "password": "password1"}, nil)
	// Username collision (same local part) is a 400, not the legacy 500.
	e.expect(400, "Username already taken", "POST", "/auth/register", map[string]any{"email": "Alice@other.org", "password": "password1"}, nil)

	lm := e.expect(200, "", "POST", "/auth/login", map[string]any{"email": "Alice@EXAMPLE.com", "password": "password1"}, nil)
	if keysOf(lm) != "access_token,must_change_password,refresh_token,token_type,user" {
		t.Fatalf("login shape %v", lm)
	}
	e.expect(401, "Invalid email or password", "POST", "/auth/login", map[string]any{"email": "Alice@example.com", "password": "nope-nope"}, nil)
	e.expect(401, "Invalid email or password", "POST", "/auth/login", map[string]any{"email": "nobody@example.com", "password": "nope-nope"}, nil)

	me := e.expect(200, "", "GET", "/auth/me", nil, bearerH(lm["access_token"].(string)))
	if me["email"] != "Alice@example.com" {
		t.Fatal(me)
	}
	rec := e.raw("GET", "/auth/me", nil, nil)
	if rec.Code != 401 || rec.Header().Get("WWW-Authenticate") != "Bearer" || !strings.Contains(rec.Body.String(), "Not authenticated") {
		t.Fatalf("no token: %d %s", rec.Code, rec.Body)
	}
	e.expect(401, "Not authenticated", "GET", "/auth/me", nil, hdrs{"Authorization": "Basic abc"})
	e.expect(401, "Could not validate credentials", "GET", "/auth/me", nil, bearerH("not-a-jwt"))
}

func TestRegisterValidation(t *testing.T) {
	e := newEnv(t)
	cases := []struct {
		body any
		loc  string
	}{
		{map[string]any{"email": "a@example.com", "password": "short"}, "password"},
		{map[string]any{"email": "not-an-email", "password": "password1"}, "email"},
		{map[string]any{"email": "a@host.local", "password": "password1"}, "email"},
		{map[string]any{"password": "password1"}, "email"},
		{map[string]any{"email": "a@example.com", "password": 12345678}, "password"},
		{"", ""},
		{"[1]", ""},
		{"{bad", "0"},
	}
	for _, c := range cases {
		code, m := e.do("POST", "/auth/register", c.body, nil)
		if code != 422 {
			t.Fatalf("%v: %d %v", c.body, code, m)
		}
		loc := m["detail"].([]any)[0].(map[string]any)["loc"].([]any)
		if c.loc != "" && fmt.Sprint(loc[len(loc)-1]) != c.loc && !(c.loc == "0" && len(loc) == 2) {
			t.Fatalf("%v: loc %v", c.body, loc)
		}
	}
}

// Deviation (D5): X-Household-Id no longer lets an unauthenticated caller join a household.
func TestRegisterHouseholdHeader(t *testing.T) {
	e := newEnv(t)
	owner := e.register()
	e.expect(400, "Invalid or expired invite code", "POST", "/auth/register",
		map[string]any{"email": "intruder@example.com", "password": "password1"}, hdrs{"X-Household-Id": owner.household})
	e.expect(400, "Household not found", "POST", "/auth/register",
		map[string]any{"email": "intruder@example.com", "password": "password1"}, hdrs{"X-Household-Id": "00000000-0000-4000-8000-000000000000"})
	if n, _ := count(context.Background(), e.db.Read, `SELECT COUNT(*) FROM auth_users WHERE email = 'intruder@example.com'`); n != 0 {
		t.Fatal("user created")
	}
}

func TestRefreshRotationGraceAndReuse(t *testing.T) {
	clock := withClock(t, time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC))
	e := newEnv(t)
	u := e.register()
	r1 := e.expect(200, "", "POST", "/auth/refresh", map[string]any{"refresh_token": u.refresh}, nil)
	next := r1["refresh_token"].(string)
	if next == u.refresh {
		t.Fatal("refresh did not rotate")
	}
	// Grace window: replaying the parent returns the same successor.
	*clock = clock.Add(5 * time.Second)
	r2 := e.expect(200, "", "POST", "/auth/refresh", map[string]any{"refresh_token": u.refresh}, nil)
	if r2["refresh_token"] != next {
		t.Fatal("grace replay should return the cached successor")
	}
	// After the window: a plain 401, and the family survives (legacy default).
	*clock = clock.Add(10 * time.Second)
	e.expect(401, "Invalid refresh token", "POST", "/auth/refresh", map[string]any{"refresh_token": u.refresh}, nil)
	r3 := e.expect(200, "", "POST", "/auth/refresh", map[string]any{"refresh_token": next}, nil)

	// Grace is tunable via the settings DB.
	if err := e.m.settings.Set(context.Background(), settingGraceSeconds, float64(0), settings.Scope{}); err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(time.Second)
	e.expect(401, "Invalid refresh token", "POST", "/auth/refresh", map[string]any{"refresh_token": next}, nil)

	e.expect(401, "Invalid refresh token", "POST", "/auth/refresh", map[string]any{"refresh_token": "garbage"}, nil)
	// Expiry (14 days).
	*clock = clock.Add(15 * 24 * time.Hour)
	e.expect(401, "Refresh token expired", "POST", "/auth/refresh", map[string]any{"refresh_token": r3["refresh_token"]}, nil)
}

func TestRefreshRevokeFamilyOnReuse(t *testing.T) {
	clock := withClock(t, time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC))
	e := newEnv(t, func(m *Module) { m.RevokeFamilyOnReuse = true })
	u := e.register()
	r1 := e.expect(200, "", "POST", "/auth/refresh", map[string]any{"refresh_token": u.refresh}, nil)
	*clock = clock.Add(time.Minute)
	e.expect(401, "Invalid refresh token", "POST", "/auth/refresh", map[string]any{"refresh_token": u.refresh}, nil)
	// The live tail of the family was revoked too.
	e.expect(401, "Invalid refresh token", "POST", "/auth/refresh", map[string]any{"refresh_token": r1["refresh_token"]}, nil)
}

func TestRefreshGraceMissAfterRestart(t *testing.T) {
	withClock(t, time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC))
	e := newEnv(t)
	u := e.register()
	r1 := e.expect(200, "", "POST", "/auth/refresh", map[string]any{"refresh_token": u.refresh}, nil)
	e.m.grace = newGraceCache() // "restart": the in-process cache is gone
	e.expect(401, "Invalid refresh token", "POST", "/auth/refresh", map[string]any{"refresh_token": u.refresh}, nil)
	// By default a replay never signs the live session out.
	e.expect(200, "", "POST", "/auth/refresh", map[string]any{"refresh_token": r1["refresh_token"]}, nil)
}

func TestLogout(t *testing.T) {
	e := newEnv(t)
	u := e.register()
	other := e.expect(200, "", "POST", "/auth/login", map[string]any{"email": u.email, "password": u.password}, nil)

	e.expect(204, "", "POST", "/auth/logout", map[string]any{"refresh_token": u.refresh}, nil)
	e.expect(401, "Invalid refresh token", "POST", "/auth/refresh", map[string]any{"refresh_token": u.refresh}, nil)
	// Another device's family survives a single logout...
	r := e.expect(200, "", "POST", "/auth/refresh", map[string]any{"refresh_token": other["refresh_token"]}, nil)
	// ...but not all_devices.
	e.expect(204, "", "POST", "/auth/logout", map[string]any{"refresh_token": u.refresh, "all_devices": true}, nil)
	e.expect(401, "Invalid refresh token", "POST", "/auth/refresh", map[string]any{"refresh_token": r["refresh_token"]}, nil)
	// Unknown tokens are still 204.
	e.expect(204, "", "POST", "/auth/logout", map[string]any{"refresh_token": "nope"}, nil)
	code, m := e.do("POST", "/auth/logout", map[string]any{}, nil)
	if code != 422 || m["detail"].([]any)[0].(map[string]any)["loc"].([]any)[1] != "refresh_token" {
		t.Fatalf("%d %v", code, m)
	}
}

func TestChangePassword(t *testing.T) {
	e := newEnv(t)
	u := e.register()
	h := bearerH(u.access)
	e.expect(401, "Incorrect password", "POST", "/auth/change-password", map[string]any{"current_password": "wrong", "new_password": "newpassword1"}, h)
	e.expect(400, "New password must be different from the current password", "POST", "/auth/change-password",
		map[string]any{"current_password": u.password, "new_password": u.password}, h)
	m := e.expect(200, "", "POST", "/auth/change-password", map[string]any{"current_password": u.password, "new_password": "newpassword1"}, h)
	if m["must_change_password"] != false || m["refresh_token"] == "" {
		t.Fatal(m)
	}
	e.expect(401, "Invalid refresh token", "POST", "/auth/refresh", map[string]any{"refresh_token": u.refresh}, nil)
	e.expect(200, "", "POST", "/auth/refresh", map[string]any{"refresh_token": m["refresh_token"]}, nil)
	e.expect(200, "", "POST", "/auth/login", map[string]any{"email": u.email, "password": "newpassword1"}, nil)
}

func TestSetup(t *testing.T) {
	e := newEnv(t)
	if m := e.expect(200, "", "GET", "/auth/setup-status", nil, nil); m["needs_setup"] != true {
		t.Fatal(m)
	}
	m := e.expect(201, "", "POST", "/auth/setup", map[string]any{"email": "root@example.com", "password": "password1"},
		hdrs{SetupTokenHeader: e.setupToken()})
	if m["user"].(map[string]any)["is_superuser"] != true {
		t.Fatal(m)
	}
	if _, c := claimsOf(t, m["access_token"].(string)); c["is_superuser"] != true {
		t.Fatal(c)
	}
	if m := e.expect(200, "", "GET", "/auth/setup-status", nil, nil); m["needs_setup"] != false {
		t.Fatal(m)
	}
	e.expect(409, "Setup already completed", "POST", "/auth/setup", map[string]any{"email": "x@example.com", "password": "password1"}, nil)
}

// setupToken reads the token file the module wrote at Start.
func (e *env) setupToken() string {
	e.t.Helper()
	b, err := os.ReadFile(SetupTokenPath(e.home))
	if err != nil {
		e.t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

func TestSetupToken(t *testing.T) {
	var announced, announcedPath string
	e := newEnv(t, func(m *Module) { m.OnSetupToken = func(tok, path string) { announced, announcedPath = tok, path } })
	tok := e.setupToken()
	if raw, err := base64.RawURLEncoding.DecodeString(tok); err != nil || len(raw) != 32 {
		t.Fatalf("token %q: %v (%d bytes)", tok, err, len(raw))
	}
	if announced != tok || announcedPath != SetupTokenPath(e.home) {
		t.Fatalf("announced %q at %q", announced, announcedPath)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(SetupTokenPath(e.home))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("token file mode %v (%v)", fi.Mode(), err)
		}
	}

	body := map[string]any{"email": "root@example.com", "password": "password1"}
	e.expect(401, "Setup token required", "POST", "/auth/setup", body, nil)
	e.expect(403, "Invalid setup token", "POST", "/auth/setup", body, hdrs{SetupTokenHeader: "wrong"})
	e.expect(403, "Invalid setup token", "POST", "/auth/setup", body, hdrs{SetupTokenHeader: tok[:len(tok)-1]})
	e.expect(403, "Invalid setup token", "POST", "/auth/setup",
		map[string]any{"email": "root@example.com", "password": "password1", "setup_token": "nope"}, nil)
	// Validation still comes first, as before.
	e.expect(422, "", "POST", "/auth/setup", map[string]any{"email": "nope", "password": "password1"}, nil)
	if m := e.expect(200, "", "GET", "/auth/setup-status", nil, nil); m["needs_setup"] != true {
		t.Fatal("a rejected setup created a superuser")
	}

	// A restart before setup keeps the same token (the printed link still works).
	if err := e.m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.setupToken() != tok {
		t.Fatal("restart replaced the token")
	}

	// The body field works too; success deletes the file.
	e.expect(201, "", "POST", "/auth/setup",
		map[string]any{"email": "root@example.com", "password": "password1", "setup_token": tok}, nil)
	if _, err := os.Stat(SetupTokenPath(e.home)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("token file still there: %v", err)
	}
	// Afterwards the token is irrelevant: always the legacy 409.
	for _, h := range []hdrs{nil, {SetupTokenHeader: tok}, {SetupTokenHeader: "wrong"}} {
		e.expect(409, "Setup already completed", "POST", "/auth/setup", map[string]any{"email": "x@example.com", "password": "password1"}, h)
	}
	// A restart with a superuser writes no new token.
	announced = ""
	if err := e.m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(SetupTokenPath(e.home)); !errors.Is(err, os.ErrNotExist) || announced != "" {
		t.Fatalf("token re-created after setup: %v %q", err, announced)
	}
}

// Two setups racing with the right token: exactly one wins.
func TestSetupTokenRace(t *testing.T) {
	e := newEnv(t)
	tok := e.setupToken()
	var wg sync.WaitGroup
	codes := make([]int, 6)
	for i := range codes {
		wg.Go(func() {
			rec := e.raw("POST", "/auth/setup", map[string]any{"email": fmt.Sprintf("r%d@example.com", i), "password": "password1"},
				hdrs{SetupTokenHeader: tok})
			codes[i] = rec.Code
		})
	}
	wg.Wait()
	won := 0
	for _, c := range codes {
		switch c {
		case 201:
			won++
		case 409:
		default:
			t.Fatalf("codes %v", codes)
		}
	}
	if won != 1 {
		t.Fatalf("codes %v", codes)
	}
}

// A stale token file left by an install that already has a superuser is removed at start.
func TestSetupTokenStaleFileRemoved(t *testing.T) {
	e := newEnv(t)
	e.expect(201, "", "POST", "/auth/setup", map[string]any{"email": "root@example.com", "password": "password1"},
		hdrs{SetupTokenHeader: e.setupToken()})
	if err := os.WriteFile(SetupTokenPath(e.home), []byte("stale\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(SetupTokenPath(e.home)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale token file kept: %v", err)
	}
}

// A user in two households: checks use the target (path) household, never the token's.
func TestMultiHouseholdMembership(t *testing.T) {
	e := newEnv(t)
	owner := e.register()
	u := e.register()
	// u joins owner's household as a member via an invite.
	inv := e.expect(201, "", "POST", "/households/"+owner.household+"/invites", map[string]any{}, bearerH(owner.access))
	e.expect(200, "", "POST", "/households/join", map[string]any{"invite_code": strings.ToLower(inv["code"].(string))}, bearerH(u.access))

	h := bearerH(u.access) // token's household_id is u's own household (admin there)
	if hh := e.list("GET", "/households", h); len(hh) != 2 {
		t.Fatalf("%v", hh)
	}
	e.expect(200, "", "GET", "/households/"+owner.household, nil, h)
	// Admin of their own household (the token's), but only a member of the target.
	e.expect(403, "Requires admin role or higher", "PATCH", "/households/"+owner.household, map[string]any{"name": "x"}, h)
	e.expect(403, "Requires power_user role or higher", "POST", "/households/"+owner.household+"/invites", map[string]any{}, h)
	e.expect(200, "", "PATCH", "/households/"+u.household, map[string]any{"name": "Mine"}, h)

	// Switching households re-issues the token for the target, members only.
	m := e.expect(200, "", "POST", "/auth/switch-household", map[string]any{"household_id": owner.household}, h)
	if _, c := claimsOf(t, m["access_token"].(string)); c["household_id"] != owner.household || keysOf(m) != "access_token,household_id" {
		t.Fatalf("%v %v", m, c)
	}
	outsider := e.register()
	e.expect(403, "Not a member of this household", "POST", "/auth/switch-household", map[string]any{"household_id": owner.household}, bearerH(outsider.access))
	e.expect(403, "Not a member of this household", "GET", "/households/"+owner.household, nil, bearerH(outsider.access))
	e.expect(403, "Not a member of this household", "GET", "/households/"+owner.household+"/members", nil, bearerH(outsider.access))

	// Authority uses the target household too.
	r, ok, err := e.m.HouseholdRole(context.Background(), u.id, owner.household)
	if err != nil || !ok || r != authn.RoleMember {
		t.Fatal(r, ok, err)
	}
	if _, ok, _ := e.m.HouseholdRole(context.Background(), outsider.id, owner.household); ok {
		t.Fatal("outsider has a role")
	}

	// UserHouseholds (recipes' RD7 union): every membership, oldest first.
	hhs, err := e.m.UserHouseholds(context.Background(), u.id)
	if err != nil || len(hhs) != 2 || hhs[0] != u.household || hhs[1] != owner.household {
		t.Fatal(hhs, err)
	}
	if hhs, _ := e.m.UserHouseholds(context.Background(), 999999); len(hhs) != 0 {
		t.Fatal(hhs)
	}

	// HouseholdNames (the admin's per-household setting values): every household by id.
	names, err := e.m.HouseholdNames(context.Background())
	if err != nil || names[u.household] == "" || names[owner.household] == "" || len(names) < 3 {
		t.Fatal(names, err)
	}
}

func TestMembersAndLeave(t *testing.T) {
	e := newEnv(t)
	owner := e.register()
	u := e.register()
	oh := bearerH(owner.access)
	base := "/households/" + owner.household
	m := e.expect(201, "", "POST", base+"/members", map[string]any{"user_id": u.id}, oh)
	if keysOf(m) != "created_at,email,role,user_id,username" || m["role"] != "member" {
		t.Fatal(m)
	}
	e.expect(400, "User is already a member of this household", "POST", base+"/members", map[string]any{"user_id": u.id}, oh)
	e.expect(404, "User not found", "POST", base+"/members", map[string]any{"user_id": 9999}, oh)
	code, body := e.do("POST", base+"/members", map[string]any{"user_id": u.id, "role": "owner"}, oh)
	if code != 422 {
		t.Fatal(code, body)
	}
	if ms := e.list("GET", base+"/members", oh); len(ms) != 2 {
		t.Fatal(ms)
	}
	e.expect(400, "Cannot demote yourself", "PATCH", fmt.Sprintf("%s/members/%d", base, owner.id), map[string]any{"role": "member"}, oh)
	e.expect(404, "Member not found", "PATCH", base+"/members/9999", map[string]any{"role": "member"}, oh)
	code, _ = e.do("PATCH", base+"/members/abc", map[string]any{"role": "member"}, oh)
	if code != 422 {
		t.Fatal(code)
	}
	e.expect(200, "", "PATCH", fmt.Sprintf("%s/members/%d", base, u.id), map[string]any{"role": "power_user"}, oh)
	e.expect(400, "Cannot kick yourself. Use POST /households/{id}/leave instead.", "DELETE", fmt.Sprintf("%s/members/%d", base, owner.id), nil, oh)

	// Leave rules.
	solo := e.register()
	e.expect(400, "Cannot leave your only household. Join or create another household first.", "POST", "/households/"+solo.household+"/leave", nil, bearerH(solo.access))
	e.expect(404, "Not a member of this household", "POST", base+"/leave", nil, bearerH(solo.access))
	e.expect(201, "", "POST", "/households", map[string]any{"name": "Second"}, oh)
	e.expect(400, "You're the only admin. Promote another member to admin before leaving.", "POST", base+"/leave", nil, oh)
	m = e.expect(200, "", "POST", base+"/leave", nil, bearerH(u.access))
	if m["left"] != true || m["household_deleted"] != false {
		t.Fatal(m)
	}
	// The last member out deletes the household.
	m2 := e.expect(201, "", "POST", "/households", map[string]any{"name": "Cabin"}, oh)
	m = e.expect(200, "", "POST", "/households/"+m2["id"].(string)+"/leave", nil, oh)
	if m["household_deleted"] != true {
		t.Fatal(m)
	}
	e.expect(404, "Member not found", "DELETE", fmt.Sprintf("%s/members/%d", base, u.id), nil, oh) // u already left
}

func TestInvites(t *testing.T) {
	clock := withClock(t, time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC))
	e := newEnv(t)
	owner := e.register()
	oh := bearerH(owner.access)
	base := "/households/" + owner.household + "/invites"
	e.expect(400, "Invite codes cannot assign admin role", "POST", base, map[string]any{"default_role": "admin"}, oh)
	if c, _ := e.do("POST", base, map[string]any{"expires_in_days": 91}, oh); c != 422 {
		t.Fatal(c)
	}
	inv := e.expect(201, "", "POST", base, map[string]any{"default_role": "power_user", "max_uses": 1}, oh)
	if keysOf(inv) != "code,created_at,default_role,expires_at,household_id,id,max_uses,revoked,use_count" || len(inv["code"].(string)) != 8 {
		t.Fatal(inv)
	}
	code := inv["code"].(string)
	if v := e.expect(200, "", "GET", "/invites/"+strings.ToLower(code)+"/validate", nil, nil); v["valid"] != true || v["household_name"] != "My Home" {
		t.Fatal(v)
	}
	// Registering with the invite joins with its role and uses it up.
	m := e.expect(201, "", "POST", "/auth/register", map[string]any{"email": "inv@example.com", "password": "password1", "invite_code": code}, nil)
	if m["household_id"] != owner.household {
		t.Fatal(m)
	}
	if r, _, _ := e.m.HouseholdRole(context.Background(), int64(m["user"].(map[string]any)["id"].(float64)), owner.household); r != authn.RolePowerUser {
		t.Fatal(r)
	}
	if v := e.expect(200, "", "GET", "/invites/"+code+"/validate", nil, nil); v["valid"] != false || v["household_name"] != nil {
		t.Fatal(v)
	}
	e.expect(400, "Invalid or expired invite code", "POST", "/auth/register", map[string]any{"email": "inv2@example.com", "password": "password1", "invite_code": code}, nil)

	inv2 := e.expect(201, "", "POST", base, map[string]any{"expires_in_days": 1}, oh)
	if l := e.list("GET", base, oh); len(l) != 2 || l[0]["id"] != inv2["id"] {
		t.Fatal(l)
	}
	joiner := e.register()
	e.expect(400, "Already a member of this household", "POST", "/households/join", map[string]any{"invite_code": inv2["code"]}, oh)
	*clock = clock.Add(25 * time.Hour)
	e.expect(400, "Invalid or expired invite code", "POST", "/households/join", map[string]any{"invite_code": inv2["code"]}, bearerH(e.relogin(joiner)))
	oh = bearerH(e.relogin(owner))
	e.expect(204, "", "DELETE", fmt.Sprintf("%s/%v", base, inv2["id"]), nil, oh)
	e.expect(404, "Invite not found", "DELETE", fmt.Sprintf("%s/%v", base, inv2["id"].(float64)+100), nil, oh)
	if l := e.list("GET", base, oh); len(l) != 1 {
		t.Fatal(l)
	}
}

func createNode(e *env, household string, services ...string) (string, string) {
	userN++
	id := fmt.Sprintf("node-%d", userN)
	m := e.expect(201, "", "POST", "/admin/nodes", map[string]any{"node_id": id, "household_id": household, "name": "Kitchen", "services": services}, hAdmin)
	return id, m["node_key"].(string)
}

func TestNodesAndValidation(t *testing.T) {
	e := newEnv(t)
	u := e.register()
	id, key := createNode(e, u.household, "jarvis-logs")
	validate := func(nodeID, k, svc string) map[string]any {
		return e.expect(200, "", "POST", "/internal/validate-node", map[string]any{"node_id": nodeID, "node_key": k, "service_id": svc}, e.appH())
	}
	v := validate(id, key, "jarvis-logs")
	if v["valid"] != true || v["household_id"] != u.household || len(v["household_member_ids"].([]any)) != 1 || v["reason"] != nil {
		t.Fatal(v)
	}
	for _, c := range []struct{ id, key, svc, reason string }{
		{id, "wrong", "jarvis-logs", "Invalid node credentials"},
		{"nope", key, "jarvis-logs", "Node not found"},
		{id, key, "jarvis-tts", "Node is not authorized to access service 'jarvis-tts'"},
	} {
		v := validate(c.id, c.key, c.svc)
		if v["valid"] != false || v["reason"] != c.reason || keysOf(v) != "household_id,household_member_ids,node_id,reason,valid" || v["node_id"] != nil {
			t.Fatalf("%+v: %v", c, v)
		}
	}
	e.expect(401, "Missing app credentials", "POST", "/internal/validate-node", map[string]any{}, nil)

	// Grants take effect immediately, through the cache.
	e.expect(201, "", "POST", "/admin/nodes/"+id+"/services", map[string]any{"service_id": "jarvis-tts"}, hAdmin)
	e.expect(400, "Node already has access to this service", "POST", "/admin/nodes/"+id+"/services", map[string]any{"service_id": "jarvis-tts"}, hAdmin)
	if v := validate(id, key, "jarvis-tts"); v["valid"] != true {
		t.Fatal(v)
	}
	e.expect(204, "", "DELETE", "/admin/nodes/"+id+"/services/jarvis-tts", nil, hAdmin)
	e.expect(404, "Service access not found", "DELETE", "/admin/nodes/"+id+"/services/jarvis-tts", nil, hAdmin)
	if v := validate(id, key, "jarvis-tts"); v["valid"] != false {
		t.Fatal(v)
	}

	det := e.expect(200, "", "GET", "/admin/nodes/"+id, nil, hAdmin)
	if svcs := det["services"].([]any); len(svcs) != 1 || keysOf(svcs[0].(map[string]any)) != "granted_at,granted_by,service_id" {
		t.Fatal(det)
	}
	if l := e.list("GET", "/admin/nodes", hAdmin); len(l) != 1 || l[0]["services"].([]any)[0] != "jarvis-logs" {
		t.Fatal(l)
	}

	// Key rotation: the old key stops working at once (it was cached as verified).
	rot := e.expect(200, "", "POST", "/admin/nodes/"+id+"/rotate-key", nil, hAdmin)
	if v := validate(id, key, "jarvis-logs"); v["reason"] != "Invalid node credentials" {
		t.Fatal(v)
	}
	if v := validate(id, rot["node_key"].(string), "jarvis-logs"); v["valid"] != true {
		t.Fatal(v)
	}
	// In-process Authority shares the code.
	nv, err := e.m.ValidateNode(context.Background(), id, rot["node_key"].(string), "jarvis-logs")
	if err != nil || !nv.Valid || nv.Node.HouseholdID != u.household || nv.HouseholdMemberIDs[0] != u.id {
		t.Fatal(nv, err)
	}
	e.expect(200, "", "DELETE", "/admin/nodes/"+id, nil, hAdmin)
	if v := validate(id, rot["node_key"].(string), "jarvis-logs"); v["reason"] != "Node is inactive" {
		t.Fatal(v)
	}
	e.expect(404, "Node not found", "DELETE", "/admin/nodes/nope", nil, hAdmin)
	e.expect(404, "Node not found", "POST", "/admin/nodes/nope/rotate-key", nil, hAdmin)

	e.expect(400, "node_id already exists", "POST", "/admin/nodes", map[string]any{"node_id": id, "household_id": u.household, "name": "x"}, hAdmin)
	e.expect(404, "Household not found", "POST", "/admin/nodes", map[string]any{"node_id": "n-x", "household_id": "nope", "name": "x"}, hAdmin)
	e.expect(404, "User not found", "POST", "/admin/nodes", map[string]any{"node_id": "n-x", "household_id": u.household, "name": "x", "registered_by_user_id": 999}, hAdmin)
}

func TestHouseholdNodes(t *testing.T) {
	e := newEnv(t)
	owner := e.register()
	member := e.register()
	e.expect(201, "", "POST", "/households/"+owner.household+"/members", map[string]any{"user_id": member.id}, bearerH(owner.access))
	base := "/households/" + owner.household + "/nodes"
	body := map[string]any{"node_id": "hn-1", "household_id": owner.household, "name": "Den", "services": []string{"jarvis-command-center"}}
	e.expect(403, "Requires power_user role or higher", "POST", base, body, bearerH(member.access))
	e.expect(400, "Household ID in payload must match URL", "POST", base, map[string]any{"node_id": "x", "household_id": member.household, "name": "x"}, bearerH(owner.access))
	m := e.expect(201, "", "POST", base, body, bearerH(owner.access))
	if m["registered_by_user_id"] != float64(owner.id) || keysOf(m) != "created_at,household_id,name,node_id,node_key,registered_by_user_id,services" {
		t.Fatal(m)
	}
	if l := e.list("GET", base, bearerH(member.access)); len(l) != 1 {
		t.Fatal(l)
	}
}

func TestAppClients(t *testing.T) {
	e := newEnv(t)
	e.expect(401, "Unauthorized", "GET", "/admin/app-clients", nil, nil)
	e.expect(401, "Unauthorized", "GET", "/admin/app-clients", nil, hdrs{"X-Jarvis-Admin-Token": "wrong"})
	e.expect(400, "app_id already exists", "POST", "/admin/app-clients", map[string]any{"app_id": "test-app", "name": "x"}, hAdmin)
	m := e.expect(200, "", "GET", "/internal/app-ping", nil, e.appH())
	if m["app_id"] != "test-app" || m["name"] != "Test" {
		t.Fatal(m)
	}
	e.expect(401, "Missing app credentials", "GET", "/internal/app-ping", nil, hdrs{"X-Jarvis-App-Id": "test-app"})
	e.expect(401, "Invalid app credentials", "GET", "/internal/app-ping", nil, hdrs{"X-Jarvis-App-Id": "test-app", "X-Jarvis-App-Key": "bad"})
	e.expect(401, "Invalid app credentials", "GET", "/internal/app-ping", nil, hdrs{"X-Jarvis-App-Id": "nope", "X-Jarvis-App-Key": "bad"})

	e.expect(200, "", "POST", "/admin/app-clients/test-app/revoke", nil, hAdmin)
	e.expect(401, "Invalid app credentials", "GET", "/internal/app-ping", nil, e.appH())
	if _, ok, _ := e.m.ValidateApp(context.Background(), e.app.id, e.app.key); ok {
		t.Fatal("revoked app validates in-process")
	}
	// Rotation re-activates (legacy) and the old key dies at once.
	old := e.app.key
	r := e.expect(200, "", "POST", "/admin/app-clients/test-app/rotate", nil, hAdmin)
	e.app.key = r["key"].(string)
	e.expect(200, "", "GET", "/internal/app-ping", nil, e.appH())
	e.expect(401, "Invalid app credentials", "GET", "/internal/app-ping", nil, hdrs{"X-Jarvis-App-Id": "test-app", "X-Jarvis-App-Key": old})
	e.expect(404, "App client not found", "POST", "/admin/app-clients/nope/rotate", nil, hAdmin)
	l := e.list("GET", "/admin/app-clients", hAdmin)
	if len(l) != 1 || keysOf(l[0]) != "app_id,created_at,is_active,last_rotated_at,name" {
		t.Fatal(l)
	}
}

func TestInternalLookups(t *testing.T) {
	e := newEnv(t)
	u := e.register()
	h := e.appH()
	m := e.expect(200, "", "POST", "/internal/validate-household-access", map[string]any{"user_id": u.id, "household_id": u.household, "required_role": "admin"}, h)
	if m["valid"] != true || m["role"] != "admin" || keysOf(m) != "household_id,reason,role,user_id,valid" {
		t.Fatal(m)
	}
	other := e.register()
	m = e.expect(200, "", "POST", "/internal/validate-household-access", map[string]any{"user_id": other.id, "household_id": u.household, "required_role": "member"}, h)
	if m["valid"] != false || m["reason"] != "User is not a member of this household" {
		t.Fatal(m)
	}
	e.expect(201, "", "POST", "/households/"+u.household+"/members", map[string]any{"user_id": other.id}, bearerH(u.access))
	m = e.expect(200, "", "POST", "/internal/validate-household-access", map[string]any{"user_id": other.id, "household_id": u.household, "required_role": "power_user"}, h)
	if m["reason"] != "User has member role, requires power_user or higher" {
		t.Fatal(m)
	}

	// Internal registration always grants the calling app.
	m = e.expect(201, "", "POST", "/internal/nodes/register", map[string]any{"node_id": "in-1", "household_id": u.household, "name": "x", "services": []string{"jarvis-tts"}}, h)
	if keysOf(m) != "node_id,node_key" {
		t.Fatal(m)
	}
	if v, _ := e.m.ValidateNode(context.Background(), "in-1", m["node_key"].(string), "test-app"); !v.Valid {
		t.Fatal(v)
	}
	if m := e.expect(200, "", "POST", "/internal/validate-node-household", map[string]any{"node_id": "in-1", "household_id": other.household}, h); m["reason"] != "Node does not belong to this household" {
		t.Fatal(m)
	}
	if m := e.expect(200, "", "POST", "/internal/validate-node-household", map[string]any{"node_id": "in-1", "household_id": u.household}, h); m["valid"] != true {
		t.Fatal(m)
	}
	e.expect(200, "", "DELETE", "/internal/nodes/in-1", nil, h)

	m = e.expect(200, "", "GET", fmt.Sprintf("/internal/users/batch?user_ids=%d&user_ids=%d&user_ids=999", u.id, other.id), nil, h)
	if users := m["users"].(map[string]any); len(users) != 2 || users[fmt.Sprint(u.id)] == nil {
		t.Fatal(m)
	}
	if c, _ := e.do("GET", "/internal/users/batch", nil, h); c != 422 {
		t.Fatal(c)
	}
	q := strings.Repeat("user_ids=1&", 101)
	e.expect(400, "Maximum 100 user IDs per request", "GET", "/internal/users/batch?"+q, nil, h)
}

func TestSuperuserAndTempPassword(t *testing.T) {
	clock := withClock(t, time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC))
	e := newEnv(t)
	su := e.superuser()
	u := e.register()
	e.expect(403, "Superuser access required", "GET", "/superuser/users", nil, bearerH(u.access))
	users := e.list("GET", "/superuser/users", bearerH(su.access))
	if len(users) != 2 || len(users[0]["households"].([]any)) != 1 {
		t.Fatal(users)
	}
	if hh := e.list("GET", "/superuser/households", bearerH(su.access)); len(hh) != 2 {
		t.Fatal(hh)
	}
	createNode(e, u.household)
	if n := e.list("GET", "/superuser/nodes", bearerH(su.access)); len(n) != 1 {
		t.Fatal(n)
	}

	path := fmt.Sprintf("/superuser/users/%d/temp-password", u.id)
	m := e.expect(200, "", "POST", path, nil, bearerH(su.access)) // no body: generated password
	temp := m["temp_password"].(string)
	if len(temp) != 14 || m["must_change_password"] != true {
		t.Fatal(m)
	}
	// Every session was revoked; the temp password forces a change.
	e.expect(401, "Invalid refresh token", "POST", "/auth/refresh", map[string]any{"refresh_token": u.refresh}, nil)
	lm := e.expect(200, "", "POST", "/auth/login", map[string]any{"email": u.email, "password": temp}, nil)
	if lm["must_change_password"] != true {
		t.Fatal(lm)
	}
	*clock = clock.Add(25 * time.Hour)
	e.expect(401, "Temporary password expired. Ask your administrator for a new one.", "POST", "/auth/login", map[string]any{"email": u.email, "password": temp}, nil)
	e.expect(401, "Temporary password expired. Ask your administrator for a new one.", "POST", "/auth/refresh", map[string]any{"refresh_token": lm["refresh_token"]}, nil)

	m = e.expect(200, "", "POST", path, map[string]any{"temp_password": "chosen-temp-1", "expires_in_hours": 2}, bearerH(e.relogin(su)))
	if m["temp_password"] != "chosen-temp-1" {
		t.Fatal(m)
	}
	if c, _ := e.do("POST", path, map[string]any{"expires_in_hours": 500}, bearerH(e.relogin(su))); c != 422 {
		t.Fatal(c)
	}
	e.expect(404, "User not found", "POST", "/superuser/users/9999/temp-password", nil, bearerH(e.relogin(su)))
}

// relogin returns a fresh access token (the clock may have moved past the old one's expiry).
func (e *env) relogin(u *testUser) string {
	m := e.expect(200, "", "POST", "/auth/login", map[string]any{"email": u.email, "password": u.password}, nil)
	return m["access_token"].(string)
}

func TestAdminUsers(t *testing.T) {
	e := newEnv(t)
	u := e.register()
	path := fmt.Sprintf("/admin/users/%d/superuser", u.id)
	m := e.expect(200, "", "PUT", path, map[string]any{"is_superuser": true}, hAdmin)
	if m["message"] != "Superuser access granted for user "+u.email || keysOf(m) != "email,is_superuser,message,success,user_id" {
		t.Fatal(m)
	}
	m = e.expect(200, "", "PUT", path, map[string]any{"is_superuser": true}, hAdmin)
	if m["message"] != fmt.Sprintf("User %s already has is_superuser=True", u.email) {
		t.Fatal(m)
	}
	e.expect(404, "User with ID 999 not found", "PUT", "/admin/users/999/superuser", map[string]any{"is_superuser": true}, hAdmin)
	m = e.expect(200, "", "GET", "/admin/users/by-email/"+u.email, nil, hAdmin)
	if m["is_superuser"] != true || keysOf(m) != "email,id,is_active,is_superuser,username" {
		t.Fatal(m)
	}
	e.expect(404, "User with email x@y.z not found", "GET", "/admin/users/by-email/x@y.z", nil, hAdmin)
	e.expect(401, "Unauthorized", "GET", fmt.Sprintf("/admin/users/%d", u.id), nil, nil)
}

func TestSettingsGuards(t *testing.T) {
	e := newEnv(t)
	u := e.register()
	su := e.superuser()
	m := e.expect(200, "", "GET", "/settings/", nil, e.appH())
	if m["total"] != float64(4) {
		t.Fatal(m)
	}
	e.expect(200, "", "GET", "/settings/auth.algorithm", nil, bearerH(su.access))
	e.expect(401, "Missing authentication. Provide either Bearer token or app credentials.", "GET", "/settings/", nil, nil)
	e.expect(401, "Invalid app credentials", "GET", "/settings/", nil, hdrs{"X-Jarvis-App-Id": "test-app", "X-Jarvis-App-Key": "x"})
	e.expect(403, "Superuser access required", "GET", "/settings/", nil, bearerH(u.access))
	e.expect(401, "Invalid token: Not enough segments", "GET", "/settings/", nil, bearerH("garbage"))
	// Writes: superuser JWT only, never app credentials.
	e.expect(401, "Not authenticated", "PUT", "/settings/auth.algorithm", map[string]any{"value": "HS256"}, e.appH())
	e.expect(403, "Superuser access required", "PUT", "/settings/auth.algorithm", map[string]any{"value": "HS256"}, bearerH(u.access))
	e.expect(200, "", "PUT", "/settings/auth.token.access_expire_minutes", map[string]any{"value": 5}, bearerH(su.access))
	m = e.expect(200, "", "POST", "/auth/login", map[string]any{"email": u.email, "password": u.password}, nil)
	if _, c := claimsOf(t, m["access_token"].(string)); c["exp"].(float64)-c["iat"].(float64) != 300 {
		t.Fatal(c)
	}
}

func TestRateLimit(t *testing.T) {
	e := newEnv(t, func(m *Module) { m.RateLimit = RateLimit{IPPerMinute: 3, LoginMaxFailures: 2} })
	for range 3 {
		e.do("POST", "/auth/refresh", map[string]any{"refresh_token": "x"}, nil)
	}
	rec := e.raw("POST", "/auth/login", map[string]any{"email": "a@example.com", "password": "x"}, nil)
	if rec.Code != 429 || rec.Header().Get("Retry-After") != "60" || !strings.Contains(rec.Body.String(), "Too many requests. Please slow down.") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}

	// Failed-login lockout per (email, IP).
	e2 := newEnv(t, func(m *Module) { m.RateLimit = RateLimit{LoginMaxFailures: 2} })
	u := e2.register()
	for range 2 {
		e2.expect(401, "Invalid email or password", "POST", "/auth/login", map[string]any{"email": u.email, "password": "wrong-pass"}, nil)
	}
	rec = e2.raw("POST", "/auth/login", map[string]any{"email": u.email, "password": u.password}, nil)
	if rec.Code != 429 || rec.Header().Get("Retry-After") != "900" || !strings.Contains(rec.Body.String(), "Too many failed login attempts. Please try again later.") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

// Hashes made by jarvis-auth's passlib CryptContext(schemes=["bcrypt"]) verify unchanged,
// including passlib's silent 72-byte truncation.
func TestLegacyBcryptHashes(t *testing.T) {
	if !verifySecret("correct horse battery", "$2b$12$/tzK4IvdiH/qsBMoIMFAs.b19bFRd1AkxH88YsQFJQSNJABdDdLXm") {
		t.Fatal("passlib hash rejected")
	}
	if verifySecret("correct horse batterY", "$2b$12$/tzK4IvdiH/qsBMoIMFAs.b19bFRd1AkxH88YsQFJQSNJABdDdLXm") {
		t.Fatal("wrong password accepted")
	}
	long := "$2b$12$utXQYBED3Lsb9ayVIX1CjOq2rLKwkVyl..OwGeT.CVIV5jqa/TCAG" // passlib hash of "x"*80
	if !verifySecret(strings.Repeat("x", 80), long) || !verifySecret(strings.Repeat("x", 72)+"yyy", long) {
		t.Fatal("long password not truncated like passlib")
	}

	// A legacy user row logs in.
	e := newEnv(t)
	if _, err := e.db.Write.Exec(`INSERT INTO auth_users (email, username, password_hash) VALUES ('old@example.com', 'old', ?)`,
		"$2b$12$/tzK4IvdiH/qsBMoIMFAs.b19bFRd1AkxH88YsQFJQSNJABdDdLXm"); err != nil {
		t.Fatal(err)
	}
	e.expect(200, "", "POST", "/auth/login", map[string]any{"email": "old@example.com", "password": "correct horse battery"}, nil)
}

// The attack the algorithm-family rule exists for: an HS256 token signed with the published
// RS256 public key as the HMAC secret must be rejected, with or without AUTH_SECRET_KEY.
func TestForgedHS256WithPublicKey(t *testing.T) {
	for _, secret := range []string{"", "a-real-hs256-secret-for-tests"} {
		t.Run(fmt.Sprintf("secret=%v", secret != ""), func(t *testing.T) {
			e := newEnv(t, func(m *Module) { m.HMACSecret = secret })
			victim := e.superuser()
			pub := e.expect(200, "", "GET", "/auth/public-key", nil, nil)["public_key"].(string)
			forged, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
				"sub": fmt.Sprint(victim.id), "email": victim.email, "is_superuser": true,
				"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(),
			}).SignedString([]byte(pub))
			if err != nil {
				t.Fatal(err)
			}
			e.expect(401, "Could not validate credentials", "GET", "/auth/me", nil, bearerH(forged))
			e.expect(401, "Could not validate credentials", "GET", "/superuser/users", nil, bearerH(forged))
			e.expect(401, "Invalid token: Signature verification failed.", "GET", "/settings/", nil, bearerH(forged))
			// alg=none is rejected too.
			none := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`)) + "." +
				base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"sub":"%d","exp":%d}`, victim.id, time.Now().Add(time.Hour).Unix()))) + "."
			e.expect(401, "Could not validate credentials", "GET", "/auth/me", nil, bearerH(none))
		})
	}
}

func TestHS256Minting(t *testing.T) {
	const secret = "a-real-hs256-secret-for-tests"
	e := newEnv(t, func(m *Module) { m.HMACSecret = secret })
	if err := e.m.settings.Set(context.Background(), settingAlgorithm, "HS256", settings.Scope{}); err != nil {
		t.Fatal(err)
	}
	u := e.register()
	hdr, _ := claimsOf(t, u.access)
	if hdr["alg"] != "HS256" {
		t.Fatal(hdr)
	}
	// Python services verifying with the shared secret accept it.
	if _, err := (authn.Keys{HMAC: []byte(secret)}).Verify(u.access, time.Now()); err != nil {
		t.Fatal(err)
	}
	e.expect(200, "", "GET", "/auth/me", nil, bearerH(u.access))

	// Without the secret, HS256 can be neither minted nor verified.
	e2 := newEnv(t)
	e2.m.settings.Set(context.Background(), settingAlgorithm, "HS256", settings.Scope{})
	u2 := e2.register()
	if hdr, _ := claimsOf(t, u2.access); hdr["alg"] != "RS256" {
		t.Fatal(hdr)
	}
	tok, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": fmt.Sprint(u2.id), "exp": time.Now().Add(time.Hour).Unix()}).SignedString([]byte(""))
	e2.expect(401, "Could not validate credentials", "GET", "/auth/me", nil, bearerH(tok))
}

// The key is generated once and persists; a newer key mints while older ones still verify.
func TestSigningKeyPersistenceAndRotation(t *testing.T) {
	e := newEnv(t)
	u := e.register()
	kid1, _ := claimsOf(t, u.access)

	// A fresh module on the same DB (a restart) loads the same key.
	m2 := &Module{AdminToken: adminToken, RateLimit: RateLimit{Disabled: true}}
	mux := http.NewServeMux()
	m2.Register(mux, e.m.deps)
	keys, err := m2.loadKeys(context.Background())
	if err != nil || len(keys) != 1 || keys[0].kid != kid1["kid"] {
		t.Fatal(keys, err)
	}

	// Rotate: retire the key and add a new active one.
	k, _ := generateKey()
	pemStr, _ := privateKeyPEM(k.priv)
	if _, err := e.db.Write.Exec(`UPDATE auth_signing_keys SET is_active = 0`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Write.Exec(`INSERT INTO auth_signing_keys (kid, private_pem, public_pem) VALUES (?, ?, ?)`, k.kid, pemStr, k.pubPEM); err != nil {
		t.Fatal(err)
	}
	m2.keys = nil
	e.m = m2
	e.h = mux
	e.expect(200, "", "GET", "/auth/me", nil, bearerH(u.access)) // old kid still verifies
	m := e.expect(200, "", "POST", "/auth/login", map[string]any{"email": u.email, "password": u.password}, nil)
	if hdr, _ := claimsOf(t, m["access_token"].(string)); hdr["kid"] != k.kid {
		t.Fatal(hdr)
	}
	if pk := e.expect(200, "", "GET", "/auth/public-key", nil, nil); pk["kid"] != k.kid {
		t.Fatal(pk)
	}
	// A legacy base64 AUTH_PRIVATE_KEY copied verbatim also loads.
	if _, err := parsePrivatePEM(base64.StdEncoding.EncodeToString([]byte(pemStr))); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteMe(t *testing.T) {
	e := newEnv(t)
	u := e.register()
	h := bearerH(u.access)
	e.expect(401, "Incorrect password", "DELETE", "/auth/me", map[string]any{"password": "wrong"}, h)

	// Nodes guard.
	n := e.expect(201, "", "POST", "/households/"+u.household+"/nodes", map[string]any{"node_id": "dn", "household_id": u.household, "name": "x"}, h)
	_ = n
	e.expect(409, "Cannot delete account with nodes registered to it", "DELETE", "/auth/me", map[string]any{"password": u.password}, h)
	e.expect(200, "", "DELETE", "/admin/nodes/dn", nil, hAdmin)

	// Sole admin of a shared household.
	other := e.register()
	e.expect(201, "", "POST", "/households/"+u.household+"/members", map[string]any{"user_id": other.id}, h)
	e.expect(409, "Cannot delete your account while you are the only admin of a household with other members. Make another member an admin first.",
		"DELETE", "/auth/me", map[string]any{"password": u.password}, h)
	e.expect(200, "", "PATCH", fmt.Sprintf("/households/%s/members/%d", u.household, other.id), map[string]any{"role": "admin"}, h)

	// A failing hook rolls everything back.
	var calls []int64
	boom := true
	e.m.OnUserDeleted(func(ctx context.Context, tx *sql.Tx, id int64) error {
		calls = append(calls, id)
		if boom {
			return errors.New("boom")
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM auth_settings WHERE user_id = ?`, id) // writes via the tx
		return err
	})
	e.expect(500, "", "DELETE", "/auth/me", map[string]any{"password": u.password}, h)
	e.expect(200, "", "GET", "/auth/me", nil, h)

	boom = false
	if err := e.m.settings.Set(context.Background(), settingAccessMinutes, float64(9), settings.Scope{UserID: u.id}); err != nil {
		t.Fatal(err)
	}
	e.expect(204, "", "DELETE", "/auth/me", map[string]any{"password": u.password}, h)
	if len(calls) != 2 || calls[1] != u.id {
		t.Fatal(calls)
	}
	e.expect(401, "Could not validate credentials", "GET", "/auth/me", nil, h)
	// The shared household survives with the other admin; the user's settings are gone.
	e.expect(200, "", "GET", "/households/"+u.household, nil, bearerH(other.access))
	if n, _ := count(context.Background(), e.db.Read, `SELECT COUNT(*) FROM auth_settings WHERE user_id = ?`, u.id); n != 0 {
		t.Fatal(n)
	}

	// A solo user's household is deleted with them.
	solo := e.register()
	e.expect(204, "", "DELETE", "/auth/me", map[string]any{"password": solo.password}, bearerH(solo.access))
	if _, found, _ := householdExists(context.Background(), e.db.Read, solo.household); found {
		t.Fatal("solo household survived")
	}
}

// The legacy HTTP purge for services jarvisd doesn't serve yet: 2xx/404 continue,
// unreachable is skipped, 5xx and other 4xx abort with 502 before anything is deleted.
func TestDeleteMeDownstreamPurge(t *testing.T) {
	var (
		status int
		seen   []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
		w.WriteHeader(status)
	}))
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port

	e := newEnv(t)
	if _, err := e.db.Write.Exec(`CREATE TABLE config_services (name TEXT, host TEXT, port INTEGER, scheme TEXT)`); err != nil {
		t.Fatal(err)
	}
	e.db.Write.Exec(`INSERT INTO config_services VALUES ('jarvis-command-center', '127.0.0.1', ?, 'http')`, port)
	e.db.Write.Exec(`INSERT INTO config_services VALUES ('jarvis-notifications', '127.0.0.1', 1, 'http')`) // unreachable

	for _, c := range []struct {
		status int
		want   int
	}{{500, 502}, {403, 502}, {404, 204}} {
		status = c.status
		seen = nil
		u := e.register()
		code, m := e.do("DELETE", "/auth/me", map[string]any{"password": u.password}, bearerH(u.access))
		if code != c.want {
			t.Fatalf("downstream %d: got %d %v", c.status, code, m)
		}
		if c.want == 502 && m["detail"] != deletionFailed {
			t.Fatal(m)
		}
		if len(seen) != 1 || seen[0] != "DELETE /api/v0/me/data Bearer "+u.access {
			t.Fatal(seen)
		}
	}

	// A service jarvisd serves itself is not called over HTTP.
	e.m.InProcess = []string{"jarvis-command-center"}
	seen = nil
	u := e.register()
	e.expect(204, "", "DELETE", "/auth/me", map[string]any{"password": u.password}, bearerH(u.access))
	if len(seen) != 0 {
		t.Fatal(seen)
	}
}

func TestVerifyUser(t *testing.T) {
	e := newEnv(t)
	u := e.register()
	p, err := e.m.VerifyUser(context.Background(), u.access)
	if err != nil || p.ID != u.id || p.HouseholdID != u.household {
		t.Fatal(p, err)
	}
	if _, err := e.m.VerifyUser(context.Background(), "x.y.z"); err == nil {
		t.Fatal("garbage verified")
	}
}

func TestDeleteHouseholdCascades(t *testing.T) {
	e := newEnv(t)
	owner := e.register()
	member := e.register()
	oh := bearerH(owner.access)
	hh := e.expect(201, "", "POST", "/households", map[string]any{"name": "Cabin"}, oh)["id"].(string)
	if c := e.expect(200, "", "GET", "/households/"+hh, nil, oh); keysOf(c) != "created_at,id,name,updated_at" || c["name"] != "Cabin" {
		t.Fatal(c)
	}
	e.expect(201, "", "POST", "/households/"+hh+"/members", map[string]any{"user_id": member.id}, oh)
	id, key := createNode(e, hh, "jarvis-logs")
	if v, _ := e.m.ValidateNode(context.Background(), id, key, "jarvis-logs"); !v.Valid {
		t.Fatal(v)
	}
	e.expect(403, "Requires admin role or higher", "DELETE", "/households/"+hh, nil, bearerH(member.access))
	e.expect(204, "", "DELETE", "/households/"+hh, nil, oh)
	if v, _ := e.m.ValidateNode(context.Background(), id, key, "jarvis-logs"); v.Reason != "Node not found" {
		t.Fatal(v)
	}
	e.expect(403, "Not a member of this household", "GET", "/households/"+hh, nil, oh)
	// Re-registering the same node id gets a new key; the old cached proof doesn't carry over.
	id2 := e.expect(201, "", "POST", "/admin/nodes", map[string]any{"node_id": id, "household_id": owner.household, "name": "x", "services": []string{"jarvis-logs"}}, hAdmin)
	if v, _ := e.m.ValidateNode(context.Background(), id, key, "jarvis-logs"); v.Reason != "Invalid node credentials" {
		t.Fatal(v, id2)
	}
}

func TestClientIP(t *testing.T) {
	m := &Module{limiter: newRateLimiter(RateLimit{})}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.9:5555"
	r.Header.Set("X-Forwarded-For", "1.1.1.1, 2.2.2.2")
	if ip := m.clientIP(r); ip != "10.0.0.9" {
		t.Fatal(ip) // XFF is untrusted by default
	}
	m.limiter.cfg.TrustForwardedFor = true
	if ip := m.clientIP(r); ip != "2.2.2.2" {
		t.Fatal(ip) // right-most hop: the one the trusted proxy appended
	}
}

func TestExpiredTokens(t *testing.T) {
	clock := withClock(t, time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC))
	e := newEnv(t)
	su := e.superuser()
	*clock = clock.Add(31 * time.Minute)
	e.expect(401, "Could not validate credentials", "GET", "/auth/me", nil, bearerH(su.access))
	e.expect(401, "Invalid token: Signature has expired.", "GET", "/settings/", nil, bearerH(su.access))
}

func TestVerifyUserErrorsAreSentinels(t *testing.T) {
	e := newEnv(t)
	for _, tok := range []string{"not-a-jwt", "a.b.c", ""} {
		if _, err := e.m.VerifyUser(context.Background(), tok); !errors.Is(err, authn.ErrInvalid) {
			t.Errorf("%q: %v, want authn.ErrInvalid", tok, err)
		}
	}
}

// D20/D49: membership ends → member hooks; a household deleted by any path → household hooks;
// a failing hook rolls the change back.
func TestHouseholdLifecycleHooks(t *testing.T) {
	e := newEnv(t)
	type removed struct {
		uid int64
		hh  string
	}
	var mu sync.Mutex
	var gone []removed
	var deleted []string
	fail := ""
	e.m.OnMemberRemoved(func(_ context.Context, _ *sql.Tx, uid int64, hh string) error {
		mu.Lock()
		defer mu.Unlock()
		if fail == "member" {
			return errors.New("boom")
		}
		gone = append(gone, removed{uid, hh})
		return nil
	})
	e.m.OnHouseholdDeleted(func(_ context.Context, _ *sql.Tx, hh string) error {
		mu.Lock()
		defer mu.Unlock()
		if fail == "household" {
			return errors.New("boom")
		}
		deleted = append(deleted, hh)
		return nil
	})
	owner, u := e.register(), e.register()
	oh := bearerH(owner.access)
	base := "/households/" + owner.household
	e.expect(201, "", "POST", base+"/members", map[string]any{"user_id": u.id}, oh)

	// Kick, with a failing hook first: rolled back, still a member.
	fail = "member"
	e.expect(500, "", "DELETE", fmt.Sprintf("%s/members/%d", base, u.id), nil, oh)
	if ms := e.list("GET", base+"/members", oh); len(ms) != 2 {
		t.Fatalf("rolled back kick: %v", ms)
	}
	fail = ""
	e.expect(204, "", "DELETE", fmt.Sprintf("%s/members/%d", base, u.id), nil, oh)
	if len(gone) != 1 || gone[0] != (removed{u.id, owner.household}) || len(deleted) != 0 {
		t.Fatalf("kick: %v %v", gone, deleted)
	}

	// Leave (not the last member): member hook only.
	e.expect(201, "", "POST", base+"/members", map[string]any{"user_id": u.id}, oh)
	e.expect(200, "", "POST", base+"/leave", nil, bearerH(u.access))
	if len(gone) != 2 || len(deleted) != 0 {
		t.Fatalf("leave: %v %v", gone, deleted)
	}

	// Last member out: member hook, then household hook.
	cabin := e.expect(201, "", "POST", "/households", map[string]any{"name": "Cabin"}, oh)["id"].(string)
	e.expect(200, "", "POST", "/households/"+cabin+"/leave", nil, oh)
	if len(gone) != 3 || gone[2] != (removed{owner.id, cabin}) || len(deleted) != 1 || deleted[0] != cabin {
		t.Fatalf("last out: %v %v", gone, deleted)
	}

	// Admin deletes a household; a failing household hook keeps it.
	barn := e.expect(201, "", "POST", "/households", map[string]any{"name": "Barn"}, oh)["id"].(string)
	fail = "household"
	e.expect(500, "", "DELETE", "/households/"+barn, nil, oh)
	if _, found, _ := householdExists(context.Background(), e.db.Read, barn); !found {
		t.Fatal("household deleted despite a failing hook")
	}
	fail = ""
	code, _ := e.do("DELETE", "/households/"+barn, nil, oh)
	if code/100 != 2 || deleted[len(deleted)-1] != barn {
		t.Fatalf("admin delete: %d %v", code, deleted)
	}

	// Account deletion of a solo user deletes their household through the hook.
	solo := e.register()
	e.expect(204, "", "DELETE", "/auth/me", map[string]any{"password": solo.password}, bearerH(solo.access))
	if deleted[len(deleted)-1] != solo.household {
		t.Fatalf("solo account deletion: %v", deleted)
	}
}
