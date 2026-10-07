package notifications

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/db"
	"github.com/alexberardi/jarvis-server/internal/platform/module"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
	"github.com/alexberardi/jarvis-server/internal/platform/scheduler"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

type fakeAuth struct{}

func (fakeAuth) ValidateApp(_ context.Context, id, key string) (authn.App, bool, error) {
	if id == "boom" {
		return authn.App{}, false, errors.New("db down")
	}
	return authn.App{ID: id}, id == "jarvis-cc" && key == "k", nil
}

func (fakeAuth) ValidateNode(context.Context, string, string, string) (authn.NodeValidation, error) {
	return authn.NodeValidation{}, nil
}

func (fakeAuth) HouseholdRole(context.Context, int64, string) (authn.Role, bool, error) {
	return "", false, nil
}

var (
	appH   = []string{"X-Jarvis-App-Id", "jarvis-cc", "X-Jarvis-App-Key", "k"}
	adminH = []string{"X-Api-Key", "admin-key"}
	keys   = authn.Keys{HMAC: []byte("test-secret-test-secret")}
)

func userH(t *testing.T, id int64, household string) []string {
	t.Helper()
	tok, err := keys.Mint(authn.Claims{HouseholdID: household, RegisteredClaims: jwtSub(id)}, authn.HS256, time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return []string{"Authorization", "Bearer " + tok}
}

func jwtSub(id int64) jwt.RegisteredClaims {
	return jwt.RegisteredClaims{Subject: strconv.FormatInt(id, 10)}
}

type env struct {
	m   *Module
	h   http.Handler
	d   *db.DB
	q   *queue.Queue
	ctx context.Context
}

type opt func(*Module)

func setup(t *testing.T, opts ...opt) *env {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "jarvis.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	for _, mg := range []struct {
		name string
		fsys fs.FS
	}{{queue.MigrationModule, queue.Migrations()}, {scheduler.MigrationModule, scheduler.Migrations()}, {"notifications", Migrations()}} {
		if err := db.Migrate(ctx, d, mg.name, mg.fsys); err != nil {
			t.Fatal(err)
		}
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	q := queue.New(d, log)
	q.PollInterval = 10 * time.Millisecond
	m := &Module{Auth: fakeAuth{}, Users: KeysVerifier{Keys: keys}, AdminKey: "admin-key",
		PushRetryDelays: []time.Duration{10 * time.Millisecond, 10 * time.Millisecond, 10 * time.Millisecond}}
	for _, o := range opts {
		o(m)
	}
	mux := http.NewServeMux()
	m.Register(mux, module.Deps{DB: d, Log: log, Queue: q, Scheduler: scheduler.New(d, q, log)})
	if err := m.settings.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	q.Start(ctx)
	return &env{m: m, h: mux, d: d, q: q, ctx: ctx}
}

func (e *env) do(t *testing.T, method, path, body string, hdr ...string) (int, string) {
	t.Helper()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	return rec.Code, rec.Body.String()
}

func (e *env) json(t *testing.T, want int, method, path, body string, hdr ...string) any {
	t.Helper()
	c, b := e.do(t, method, path, body, hdr...)
	if c != want {
		t.Fatalf("%s %s: want %d, got %d: %s", method, path, want, c, b)
	}
	var v any
	if b != "" {
		dec := json.NewDecoder(strings.NewReader(b))
		dec.UseNumber()
		if err := dec.Decode(&v); err != nil {
			t.Fatalf("%s %s: bad json %q", method, path, b)
		}
	}
	return v
}

func detail(t *testing.T, body string) any {
	t.Helper()
	var m map[string]any
	json.Unmarshal([]byte(body), &m)
	return m["detail"]
}

func asObj(v any) map[string]any { m, _ := v.(map[string]any); return m }

func idsOf(v any) []string {
	out := []string{}
	for _, e := range v.([]any) {
		out = append(out, asObj(e)["id"].(string))
	}
	return out
}

func TestInfoHealth(t *testing.T) {
	e := setup(t)
	if c, b := e.do(t, "GET", "/info", ""); c != 200 || strings.TrimSpace(b) != `{"service":"jarvis-notifications"}` {
		t.Fatalf("info: %d %s", c, b)
	}
	if c, b := e.do(t, "GET", "/health", ""); c != 200 || strings.TrimSpace(b) != `{"service":"jarvis-notifications","status":"ok"}` {
		t.Fatalf("health: %d %s", c, b)
	}
}

func TestAuthErrors(t *testing.T) {
	e := setup(t)
	r := httptest.NewRequest("GET", "/api/v0/inbox", nil)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	if rec.Code != 401 || detail(t, rec.Body.String()) != "Not authenticated" || rec.Header().Get("WWW-Authenticate") != "Bearer" {
		t.Fatalf("no bearer: %d %s %v", rec.Code, rec.Body, rec.Header())
	}
	for _, h := range [][]string{{"Authorization", "Basic x"}, {"Authorization", "Bearer "}} {
		if c, b := e.do(t, "GET", "/api/v0/tokens/me", "", h...); c != 401 || detail(t, b) != "Not authenticated" {
			t.Fatalf("%v: %d %s", h, c, b)
		}
	}
	other := authn.Keys{HMAC: []byte("another-secret-another")}
	forged, _ := other.Mint(authn.Claims{RegisteredClaims: jwtSub(1)}, authn.HS256, time.Hour, time.Now())
	expired, _ := keys.Mint(authn.Claims{RegisteredClaims: jwtSub(1)}, authn.HS256, time.Hour, time.Now().Add(-2*time.Hour))
	for _, tok := range []string{"garbage", forged, expired} {
		if c, b := e.do(t, "DELETE", "/api/v0/me/data", "", "Authorization", "Bearer "+tok); c != 401 || detail(t, b) != "Invalid or expired token" {
			t.Fatalf("bad token: %d %s", c, b)
		}
	}
	// Auth runs before body validation.
	if c, b := e.do(t, "POST", "/api/v0/notify", "{}"); c != 401 || detail(t, b) != "Missing app credentials" {
		t.Fatalf("app missing: %d %s", c, b)
	}
	if c, b := e.do(t, "POST", "/api/v0/inbox", "{}", "X-Jarvis-App-Id", "jarvis-cc", "X-Jarvis-App-Key", "no"); c != 401 || detail(t, b) != "Invalid app credentials" {
		t.Fatalf("app invalid: %d %s", c, b)
	}
	if c, b := e.do(t, "POST", "/api/v0/inbox", "{}", "X-Jarvis-App-Id", "boom", "X-Jarvis-App-Key", "k"); c != 502 || !strings.HasPrefix(detail(t, b).(string), "Auth service unavailable: ") {
		t.Fatalf("app error: %d %s", c, b)
	}
	c, b := e.do(t, "GET", "/api/v0/admin/stats", "")
	if d, _ := detail(t, b).([]any); c != 422 || len(d) != 1 || !strings.Contains(b, `"loc":["header","X-Api-Key"]`) {
		t.Fatalf("admin missing: %d %s", c, b)
	}
	if c, b := e.do(t, "POST", "/api/v0/admin/cleanup", "", "X-Api-Key", "nope"); c != 401 || detail(t, b) != "Invalid admin key" {
		t.Fatalf("admin invalid: %d %s", c, b)
	}
	// An unset admin key rejects everything, including an empty header.
	e2 := setup(t, func(m *Module) { m.AdminKey = "" })
	if c, _ := e2.do(t, "GET", "/api/v0/admin/stats", "", "X-Api-Key", ""); c != 401 {
		t.Fatalf("unset admin key: %d", c)
	}
}

func TestNoHouseholdInToken(t *testing.T) {
	e := setup(t)
	u := userH(t, 1, "")
	if c, b := e.do(t, "GET", "/api/v0/inbox", "", u...); c != 400 || detail(t, b) != "No household_id in token" {
		t.Fatalf("inbox: %d %s", c, b)
	}
	if c, b := e.do(t, "POST", "/api/v0/tokens", `{"push_token":"t","device_type":"ios"}`, u...); c != 400 || detail(t, b) != "User JWT missing household_id claim" {
		t.Fatalf("tokens: %d %s", c, b)
	}
	e.json(t, 200, "GET", "/api/v0/tokens/me", "", u...) // no household needed
}

func TestInboxVisibility(t *testing.T) {
	e := setup(t)
	u, other := userH(t, 1, "hh1"), userH(t, 2, "hh2")
	create := func(body string) map[string]any {
		return asObj(e.json(t, 200, "POST", "/api/v0/inbox", body, appH...))
	}
	own := create(`{"household_id":"hh1","user_id":1,"title":"own","summary":"s","body":"b","category":"c-own","source_service":"cc","metadata":{"k":"v","n":12345678901234567}}`)
	if own["user_id"].(json.Number).String() != "1" || asObj(own["metadata"])["n"].(json.Number).String() != "12345678901234567" || own["is_read"] != false {
		t.Fatalf("own: %v", own)
	}
	if _, err := time.Parse("2006-01-02T15:04:05.999999", own["created_at"].(string)); err != nil {
		t.Fatalf("created_at is naive isoformat: %v", own["created_at"])
	}
	time.Sleep(2 * time.Millisecond)
	hh := create(`{"household_id":"hh1","title":"hh","summary":"s","body":"b","category":"c","source_service":"cc","metadata":{}}`)
	if hh["user_id"] != nil || hh["metadata"] != nil {
		t.Fatalf("household item: %v", hh)
	}
	theirs := create(`{"household_id":"hh1","user_id":3,"title":"t","summary":"s","body":"b","category":"c","source_service":"cc"}`)
	cross := create(`{"household_id":"hh2","user_id":2,"title":"x","summary":"s","body":"b","category":"c","source_service":"cc"}`)
	ownID, hhID, theirsID, crossID := own["id"].(string), hh["id"].(string), theirs["id"].(string), cross["id"].(string)

	if got := idsOf(e.json(t, 200, "GET", "/api/v0/inbox", "", u...)); len(got) != 2 || got[0] != hhID || got[1] != ownID {
		t.Fatalf("list: %v", got)
	}
	if got := idsOf(e.json(t, 200, "GET", "/api/v0/inbox?category=c-own", "", u...)); len(got) != 1 || got[0] != ownID {
		t.Fatalf("category: %v", got)
	}
	if got := idsOf(e.json(t, 200, "GET", "/api/v0/inbox?limit=1&offset=1", "", u...)); len(got) != 1 || got[0] != ownID {
		t.Fatalf("paging: %v", got)
	}
	if got := idsOf(e.json(t, 200, "GET", "/api/v0/inbox?is_read=yes", "", u...)); len(got) != 0 {
		t.Fatalf("is_read: %v", got)
	}
	for _, q := range []string{"limit=0", "limit=201", "offset=-1", "is_read=maybe", "limit=x"} {
		if c, b := e.do(t, "GET", "/api/v0/inbox?"+q, "", u...); c != 422 || !strings.Contains(b, `"loc":["query",`) {
			t.Fatalf("%s: %d %s", q, c, b)
		}
	}
	if v := asObj(e.json(t, 200, "GET", "/api/v0/inbox/unread-count", "", u...)); v["count"].(json.Number).String() != "2" {
		t.Fatalf("count: %v", v)
	}
	for _, id := range []string{theirsID, crossID, "nope"} {
		for _, mp := range [][2]string{{"GET", "/api/v0/inbox/" + id}, {"PATCH", "/api/v0/inbox/" + id + "/read"}, {"DELETE", "/api/v0/inbox/" + id}} {
			if c, b := e.do(t, mp[0], mp[1], "", u...); c != 404 || detail(t, b) != "Item not found" {
				t.Fatalf("%v: %d %s", mp, c, b)
			}
		}
	}
	if v := asObj(e.json(t, 200, "POST", "/api/v0/inbox/bulk/read", `{"ids":["`+hhID+`","`+theirsID+`","`+crossID+`","nope"]}`, u...)); v["updated"].(json.Number).String() != "1" {
		t.Fatalf("bulk read: %v", v)
	}
	if v := asObj(e.json(t, 200, "POST", "/api/v0/inbox/bulk/read", `{"ids":["`+hhID+`"]}`, u...)); v["updated"].(json.Number).String() != "0" {
		t.Fatalf("bulk read again: %v", v)
	}
	if c, b := e.do(t, "POST", "/api/v0/inbox/bulk/read", `{"ids":[1]}`, u...); c != 422 || !strings.Contains(b, `"loc":["body","ids",0]`) {
		t.Fatalf("bulk ids type: %d %s", c, b)
	}
	if v := asObj(e.json(t, 200, "GET", "/api/v0/inbox/"+ownID, "", u...)); v["is_read"] != true {
		t.Fatalf("auto-mark: %v", v)
	}
	if v := asObj(e.json(t, 200, "GET", "/api/v0/inbox/unread-count", "", u...)); v["count"].(json.Number).String() != "0" {
		t.Fatalf("count after: %v", v)
	}
	// The other user's item stays unread.
	if v := asObj(e.json(t, 200, "GET", "/api/v0/inbox/unread-count", "", other...)); v["count"].(json.Number).String() != "1" {
		t.Fatalf("other count: %v", v)
	}

	// App update: household-scoped, partial, metadata replaced.
	v := asObj(e.json(t, 200, "PATCH", "/api/v0/inbox/"+ownID, `{"household_id":"hh1","title":"v2","metadata":{"rev":2}}`, appH...))
	if v["title"] != "v2" || v["summary"] != "s" || asObj(v["metadata"])["k"] != nil || v["is_read"] != true {
		t.Fatalf("update: %v", v)
	}
	if v := asObj(e.json(t, 200, "PATCH", "/api/v0/inbox/"+hhID, `{"household_id":"hh1","metadata":{}}`, appH...)); v["metadata"] == nil {
		t.Fatalf("update with {} stores {}: %v", v)
	}
	if c, b := e.do(t, "PATCH", "/api/v0/inbox/"+ownID, `{"household_id":"hh2","title":"x"}`, appH...); c != 404 || detail(t, b) != "Item not found" {
		t.Fatalf("update wrong household: %d %s", c, b)
	}
	if c, b := e.do(t, "PATCH", "/api/v0/inbox/"+ownID, `{"title":"x"}`, appH...); c != 422 || !strings.Contains(b, `"loc":["body","household_id"]`) {
		t.Fatalf("update validation: %d %s", c, b)
	}

	if v := asObj(e.json(t, 200, "POST", "/api/v0/inbox/bulk/delete", `{"ids":["`+theirsID+`","`+crossID+`"]}`, u...)); v["deleted"].(json.Number).String() != "0" {
		t.Fatalf("bulk delete others: %v", v)
	}
	if c, b := e.do(t, "DELETE", "/api/v0/inbox/"+hhID, "", u...); c != 204 || b != "" {
		t.Fatalf("delete: %d %q", c, b)
	}
	if v := asObj(e.json(t, 200, "POST", "/api/v0/inbox/bulk/delete", `{"ids":["`+ownID+`","`+hhID+`"]}`, u...)); v["deleted"].(json.Number).String() != "1" {
		t.Fatalf("bulk delete: %v", v)
	}
	if got := idsOf(e.json(t, 200, "GET", "/api/v0/inbox", "", u...)); len(got) != 0 {
		t.Fatalf("after delete: %v", got)
	}
}

func TestBodyValidation(t *testing.T) {
	e := setup(t)
	cases := []struct{ body, loc string }{
		{``, `["body"]`},
		{`{bad`, `["body",`},
		{`[]`, `["body"]`},
		{`{"household_id":"h","summary":"s","body":"b","category":"c","source_service":"x"}`, `["body","title"]`},
		{`{"household_id":"h","title":1,"summary":"s","body":"b","category":"c","source_service":"x"}`, `["body","title"]`},
		{`{"household_id":"h","title":"t","summary":"s","body":"b","category":"c","source_service":"x","user_id":"abc"}`, `["body","user_id"]`},
		{`{"household_id":"h","title":"t","summary":"s","body":"b","category":"c","source_service":"x","metadata":[1]}`, `["body","metadata"]`},
	}
	for _, tc := range cases {
		c, b := e.do(t, "POST", "/api/v0/inbox", tc.body, appH...)
		if c != 422 || !strings.Contains(b, `"loc":`+tc.loc) {
			t.Fatalf("%q: %d %s", tc.body, c, b)
		}
	}
	// Lax ints: "5" is accepted for user_id.
	v := asObj(e.json(t, 200, "POST", "/api/v0/inbox", `{"household_id":"h","title":"t","summary":"s","body":"b","category":"c","source_service":"x","user_id":"5"}`, appH...))
	if v["user_id"].(json.Number).String() != "5" {
		t.Fatalf("lax int: %v", v)
	}
}

func TestTokens(t *testing.T) {
	e := setup(t)
	u := userH(t, 1, "hh1")
	if c, b := e.do(t, "POST", "/api/v0/tokens", `{"push_token":"t1","device_type":"windows"}`, u...); c != 400 || detail(t, b) != "device_type must be 'ios' or 'android'" {
		t.Fatalf("device_type: %d %s", c, b)
	}
	v := asObj(e.json(t, 200, "POST", "/api/v0/tokens", `{"push_token":"t1","device_type":"ios","device_name":"phone"}`, u...))
	id := v["id"]
	if v["device_name"] != "phone" || v["is_active"] != true {
		t.Fatalf("register: %v", v)
	}
	// Upsert: same id, fields replaced, and the token moves to the registering user.
	v = asObj(e.json(t, 200, "POST", "/api/v0/tokens", `{"push_token":"t1","device_type":"android"}`, userH(t, 2, "hh2")...))
	if v["id"] != id || v["device_type"] != "android" || v["device_name"] != nil {
		t.Fatalf("upsert: %v", v)
	}
	if got := e.json(t, 200, "GET", "/api/v0/tokens/me", "", u...).([]any); len(got) != 0 {
		t.Fatalf("moved token still listed for old user: %v", got)
	}
	u2 := userH(t, 2, "hh2")
	if got := e.json(t, 200, "GET", "/api/v0/tokens/me", "", u2...).([]any); len(got) != 1 {
		t.Fatalf("tokens/me: %v", got)
	}
	if v := asObj(e.json(t, 200, "DELETE", "/api/v0/tokens", `{"push_token":"t1"}`, u2...)); v["status"] != "ok" {
		t.Fatalf("unregister: %v", v)
	}
	e.json(t, 200, "DELETE", "/api/v0/tokens", `{"push_token":"t1"}`, u2...) // already inactive: still found
	if c, b := e.do(t, "DELETE", "/api/v0/tokens", `{"push_token":"nope"}`, u2...); c != 404 || detail(t, b) != "Token not found" {
		t.Fatalf("unknown: %d %s", c, b)
	}
	if got := e.json(t, 200, "GET", "/api/v0/tokens/me", "", u2...).([]any); len(got) != 0 {
		t.Fatalf("inactive listed: %v", got)
	}
}

// relay is a fake push relay.
type relay struct {
	t         *testing.T
	srv       *httptest.Server
	mu        sync.Mutex
	sends     []map[string]any
	auth      []string
	registers atomic.Int32
	// respond answers a /v1/send; nil means every token ok.
	respond func(n int, body map[string]any) (int, any)
}

func newRelay(t *testing.T) *relay {
	rl := &relay{t: t}
	rl.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/register":
			var b map[string]string
			json.NewDecoder(r.Body).Decode(&b)
			n := rl.registers.Add(1)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"jwt": "jwt-" + b["household_id"] + "-" + strconv.Itoa(int(n))})
		case "/v1/send":
			var b map[string]any
			json.NewDecoder(r.Body).Decode(&b)
			rl.mu.Lock()
			rl.sends = append(rl.sends, b)
			rl.auth = append(rl.auth, r.Header.Get("Authorization")+"|"+r.Header.Get("X-Household-Id"))
			n := len(rl.sends)
			rl.mu.Unlock()
			status, out := 200, any(nil)
			if rl.respond != nil {
				status, out = rl.respond(n, b)
			}
			if out == nil {
				res := []any{}
				for _, tok := range b["tokens"].([]any) {
					res = append(res, map[string]any{"status": "ok", "token": tok})
				}
				out = map[string]any{"results": res}
			}
			w.WriteHeader(status)
			json.NewEncoder(w).Encode(out)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(rl.srv.Close)
	return rl
}

func (rl *relay) count() int { rl.mu.Lock(); defer rl.mu.Unlock(); return len(rl.sends) }

func withRelay(rl *relay) opt { return func(m *Module) { m.RelayURL = rl.srv.URL + "/" } }

// logRow waits for a notification_log row to leave "pending" and returns it.
func (e *env) logRow(t *testing.T, id string) Delivery {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var d Delivery
		if err := e.d.Read.QueryRow(`SELECT id, delivery_status, token_count, success_count, failure_count FROM notifications_notification_log WHERE id = ?`, id).
			Scan(&d.ID, &d.DeliveryStatus, &d.TokenCount, &d.SuccessCount, &d.FailureCount); err != nil {
			t.Fatal(err)
		}
		if d.DeliveryStatus != "pending" || time.Now().After(deadline) {
			return d
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (e *env) registerToken(t *testing.T, userID int64, hh, tok string) {
	t.Helper()
	e.json(t, 200, "POST", "/api/v0/tokens", `{"push_token":"`+tok+`","device_type":"ios"}`, userH(t, userID, hh)...)
}

func notify(target, id, title string) string {
	return `{"target_type":"` + target + `","target_id":"` + id + `","title":"` + title + `","body":"b"}`
}

func TestNotifyValidationAndSkips(t *testing.T) {
	e := setup(t)
	if c, b := e.do(t, "POST", "/api/v0/notify", notify("node", "1", "t"), appH...); c != 400 || detail(t, b) != "target_type must be 'user' or 'household'" {
		t.Fatalf("target: %d %s", c, b)
	}
	if c, b := e.do(t, "POST", "/api/v0/notify", `{"target_type":"user","target_id":"1","title":"t","body":"b","priority":"urgent"}`, appH...); c != 400 || detail(t, b) != "priority must be 'default' or 'high'" {
		t.Fatalf("priority: %d %s", c, b)
	}
	if c, b := e.do(t, "POST", "/api/v0/notify", `{"target_type":"user","target_id":1,"title":"t","body":"b"}`, appH...); c != 422 || !strings.Contains(b, `["body","target_id"]`) {
		t.Fatalf("target_id type: %d %s", c, b)
	}
	skipped := func(v any) {
		t.Helper()
		d := asObj(v)
		if d["delivery_status"] != "skipped" || d["token_count"].(json.Number).String() != "0" {
			t.Fatalf("want skipped: %v", d)
		}
	}
	skipped(e.json(t, 200, "POST", "/api/v0/notify", notify("household", "empty", "t"), appH...))
	skipped(e.json(t, 200, "POST", "/api/v0/notify", notify("user", "not-a-number", "t"), appH...))

	// Devices but no relay: skipped with the token count (legacy RELAY_URL unset).
	e.registerToken(t, 1, "hh1", "tok-a")
	v := asObj(e.json(t, 200, "POST", "/api/v0/notify", notify("user", "1", "no relay"), appH...))
	if v["delivery_status"] != "skipped" || v["token_count"].(json.Number).String() != "1" {
		t.Fatalf("no relay: %v", v)
	}
	var src, target string
	var data, category sql.NullString
	e.d.Read.QueryRow(`SELECT source_service, target_type, data, category FROM notifications_notification_log WHERE id = ?`, v["id"]).Scan(&src, &target, &data, &category)
	if src != "jarvis-cc" || target != "user" || data.Valid || category.Valid {
		t.Fatalf("log row: %s %s %v %v", src, target, data, category)
	}

	// Batch.
	if c, b := e.do(t, "POST", "/api/v0/notify/batch", `{}`, appH...); c != 422 || !strings.Contains(b, `["body","notifications"]`) {
		t.Fatalf("batch missing: %d %s", c, b)
	}
	if c, b := e.do(t, "POST", "/api/v0/notify/batch", `{"notifications":[{"target_type":"user"}]}`, appH...); c != 422 || !strings.Contains(b, `["body","notifications",0,"title"]`) {
		t.Fatalf("batch item: %d %s", c, b)
	}
	many := []string{}
	for i := range 101 {
		many = append(many, notify("household", "empty", "m"+strconv.Itoa(i)))
	}
	if c, b := e.do(t, "POST", "/api/v0/notify/batch", `{"notifications":[`+strings.Join(many, ",")+`]}`, appH...); c != 400 || detail(t, b) != "Maximum 100 notifications per batch" {
		t.Fatalf("batch max: %d %s", c, b)
	}
	if c, b := e.do(t, "POST", "/api/v0/notify/batch", `{"notifications":[`+notify("household", "empty", "first")+`,`+notify("node", "n", "t")+`]}`, appH...); c != 400 || detail(t, b) != "target_type must be 'user' or 'household', got 'node'" {
		t.Fatalf("batch target: %d %s", c, b)
	}
	if got := e.json(t, 200, "POST", "/api/v0/notify/batch", `{"notifications":[]}`, appH...).([]any); len(got) != 0 {
		t.Fatalf("empty batch: %v", got)
	}
	if got := e.json(t, 200, "POST", "/api/v0/notify/batch", `{"notifications":[`+notify("household", "empty", "b1")+`,`+notify("household", "empty", "b2")+`]}`, appH...).([]any); len(got) != 2 {
		t.Fatalf("batch: %v", got)
	}
}

func TestPushDeliveredDedupedAndRelayed(t *testing.T) {
	rl := newRelay(t)
	e := setup(t, withRelay(rl))
	e.registerToken(t, 1, "hh1", "tok-a")
	e.registerToken(t, 1, "hh1", "tok-b")
	body := `{"target_type":"user","target_id":"1","title":"Hi","body":"there","priority":"high","category":"cat","data":{"inbox_item_id":"x","type":"t"}}`
	v := asObj(e.json(t, 200, "POST", "/api/v0/notify", body, appH...))
	if v["delivery_status"] != "pending" || v["token_count"].(json.Number).String() != "2" || v["success_count"].(json.Number).String() != "0" {
		t.Fatalf("pending: %v", v)
	}
	if d := e.logRow(t, v["id"].(string)); d.DeliveryStatus != "delivered" || d.SuccessCount != 2 || d.FailureCount != 0 {
		t.Fatalf("outcome: %+v", d)
	}
	rl.mu.Lock()
	sent, auth := rl.sends[0], rl.auth[0]
	rl.mu.Unlock()
	if sent["title"] != "Hi" || sent["body"] != "there" || sent["priority"] != "high" || asObj(sent["data"])["inbox_item_id"] != "x" || len(sent["tokens"].([]any)) != 2 {
		t.Fatalf("relay payload: %v", sent)
	}
	if auth != "Bearer jwt-hh1-1|hh1" {
		t.Fatalf("relay auth: %s", auth)
	}
	var used sql.NullString
	e.d.Read.QueryRow(`SELECT last_used_at FROM notifications_device_tokens WHERE push_token = 'tok-a'`).Scan(&used)
	if !used.Valid {
		t.Fatal("last_used_at not set")
	}

	// Dedup within the window: skipped, nothing sent.
	if v := asObj(e.json(t, 200, "POST", "/api/v0/notify", body, appH...)); v["delivery_status"] != "skipped" || v["token_count"].(json.Number).String() != "0" {
		t.Fatalf("dedup: %v", v)
	}
	// The household JWT is cached across sends.
	e.m.dedup.mu.Lock()
	e.m.dedup.seen = map[string]time.Time{}
	e.m.dedup.mu.Unlock()
	v = asObj(e.json(t, 200, "POST", "/api/v0/notify", notify("household", "hh1", "hh"), appH...))
	if d := e.logRow(t, v["id"].(string)); d.DeliveryStatus != "delivered" || d.TokenCount != 2 {
		t.Fatalf("household: %+v", d)
	}
	if n := rl.registers.Load(); n != 1 {
		t.Fatalf("registers: %d", n)
	}
	// The window expires.
	e.m.now = func() time.Time { return time.Now().Add(2 * DedupWindow) }
	if v := asObj(e.json(t, 200, "POST", "/api/v0/notify", notify("household", "hh1", "hh"), appH...)); v["delivery_status"] != "pending" {
		t.Fatalf("after window: %v", v)
	}
}

func TestPushOutcomes(t *testing.T) {
	t.Run("partial and DeviceNotRegistered", func(t *testing.T) {
		rl := newRelay(t)
		rl.respond = func(_ int, b map[string]any) (int, any) {
			return 200, map[string]any{"results": []any{
				map[string]any{"status": "ok"}, // token filled in by index
				map[string]any{"status": "error", "error": "DeviceNotRegistered", "token": "tok-b"},
			}}
		}
		e := setup(t, withRelay(rl))
		e.registerToken(t, 1, "hh1", "tok-a")
		e.registerToken(t, 1, "hh1", "tok-b")
		v := asObj(e.json(t, 200, "POST", "/api/v0/notify", notify("user", "1", "p"), appH...))
		if d := e.logRow(t, v["id"].(string)); d.DeliveryStatus != "partial" || d.SuccessCount != 1 || d.FailureCount != 1 {
			t.Fatalf("partial: %+v", d)
		}
		var activeB int
		e.d.Read.QueryRow(`SELECT is_active FROM notifications_device_tokens WHERE push_token = 'tok-b'`).Scan(&activeB)
		if activeB != 0 {
			t.Fatal("DeviceNotRegistered token still active")
		}
	})
	t.Run("5xx retried until delivered", func(t *testing.T) {
		rl := newRelay(t)
		rl.respond = func(n int, _ map[string]any) (int, any) {
			if n < 3 {
				return 503, map[string]any{}
			}
			return 200, nil
		}
		e := setup(t, withRelay(rl))
		e.registerToken(t, 1, "hh1", "tok-a")
		v := asObj(e.json(t, 200, "POST", "/api/v0/notify", notify("user", "1", "r"), appH...))
		deadline := time.Now().Add(5 * time.Second)
		for rl.count() < 3 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		var d Delivery
		for time.Now().Before(deadline) {
			if d = e.logRow(t, v["id"].(string)); d.DeliveryStatus == "delivered" {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		if d.DeliveryStatus != "delivered" || rl.count() != 3 {
			t.Fatalf("retry: %+v after %d sends", d, rl.count())
		}
	})
	t.Run("4xx fails without retry", func(t *testing.T) {
		rl := newRelay(t)
		rl.respond = func(int, map[string]any) (int, any) { return 400, map[string]any{} }
		e := setup(t, withRelay(rl))
		e.registerToken(t, 1, "hh1", "tok-a")
		v := asObj(e.json(t, 200, "POST", "/api/v0/notify", notify("user", "1", "f"), appH...))
		if d := e.logRow(t, v["id"].(string)); d.DeliveryStatus != "failed" || d.FailureCount != 1 {
			t.Fatalf("4xx: %+v", d)
		}
		time.Sleep(50 * time.Millisecond)
		if rl.count() != 1 {
			t.Fatalf("4xx retried: %d", rl.count())
		}
	})
	t.Run("401 refreshes the JWT once", func(t *testing.T) {
		rl := newRelay(t)
		rl.respond = func(n int, _ map[string]any) (int, any) {
			if n == 1 {
				return 401, map[string]any{}
			}
			return 200, nil
		}
		e := setup(t, withRelay(rl), func(m *Module) { m.RelayHouseholdJWT = "pinned" })
		e.registerToken(t, 1, "hh1", "tok-a")
		v := asObj(e.json(t, 200, "POST", "/api/v0/notify", notify("user", "1", "401"), appH...))
		if d := e.logRow(t, v["id"].(string)); d.DeliveryStatus != "delivered" {
			t.Fatalf("401: %+v", d)
		}
		rl.mu.Lock()
		defer rl.mu.Unlock()
		if rl.auth[0] != "Bearer pinned|hh1" || rl.auth[1] != "Bearer jwt-hh1-1|hh1" {
			t.Fatalf("auth: %v", rl.auth)
		}
	})
	t.Run("no relay JWT is a retried failure", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }))
		t.Cleanup(srv.Close)
		e := setup(t, func(m *Module) { m.RelayURL = srv.URL })
		e.registerToken(t, 1, "hh1", "tok-a")
		v := asObj(e.json(t, 200, "POST", "/api/v0/notify", notify("user", "1", "nojwt"), appH...))
		if d := e.logRow(t, v["id"].(string)); d.DeliveryStatus != "failed" || d.FailureCount != 1 {
			t.Fatalf("no jwt: %+v", d)
		}
	})
	t.Run("unreachable ends failed after retries", func(t *testing.T) {
		rl := newRelay(t)
		e := setup(t, func(m *Module) { m.RelayURL = "http://127.0.0.1:1"; m.RelayHouseholdJWT = "j" })
		_ = rl
		e.registerToken(t, 1, "hh1", "tok-a")
		v := asObj(e.json(t, 200, "POST", "/api/v0/notify", notify("user", "1", "u"), appH...))
		deadline := time.Now().Add(5 * time.Second)
		var state queue.State
		for time.Now().Before(deadline) {
			e.d.Read.QueryRow(`SELECT state FROM platform_jobs WHERE type = ?`, pushJobType).Scan(&state)
			if state == queue.Failed {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		if d := e.logRow(t, v["id"].(string)); state != queue.Failed || d.DeliveryStatus != "failed" || d.FailureCount != 1 {
			t.Fatalf("unreachable: job %s, %+v", state, d)
		}
	})
}

func TestInProcessAPI(t *testing.T) {
	rl := newRelay(t)
	e := setup(t, withRelay(rl))
	ctx := e.ctx
	uid := int64(7)
	e.registerToken(t, 7, "hh", "tok")

	// A rolled-back producer transaction leaves neither the item nor the push behind.
	boom := errors.New("producer failed")
	err := e.d.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := e.m.CreateInboxItem(ctx, tx, NewInboxItem{HouseholdID: "hh", UserID: &uid, Title: "t", Summary: "s", Body: "b", Category: "c", SourceService: "cc"}); err != nil {
			return err
		}
		if _, err := e.m.Notify(ctx, tx, "cc", Notification{TargetType: "user", TargetID: "7", Title: "t", Body: "rolled back"}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	var n int
	e.d.Read.QueryRow(`SELECT (SELECT COUNT(*) FROM notifications_inbox_items) + (SELECT COUNT(*) FROM notifications_notification_log) + (SELECT COUNT(*) FROM platform_jobs)`).Scan(&n)
	if n != 0 {
		t.Fatalf("rollback left %d rows", n)
	}

	// Committed: both land and the push is delivered.
	var item InboxItem
	var d Delivery
	err = e.d.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		if item, err = e.m.CreateInboxItem(ctx, tx, NewInboxItem{HouseholdID: "hh", Title: "t", Summary: "s", Body: "b", Category: "c", SourceService: "cc", Metadata: map[string]any{"a": 1}}); err != nil {
			return err
		}
		d, err = e.m.Notify(ctx, tx, "cc", Notification{TargetType: "household", TargetID: "hh", Title: "t", Body: "committed", Data: map[string]any{"inbox_item_id": item.ID}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if item.UserID != nil || item.Metadata["a"] == nil || d.DeliveryStatus != "pending" {
		t.Fatalf("item %+v delivery %+v", item, d)
	}
	if got := e.logRow(t, d.ID); got.DeliveryStatus != "delivered" {
		t.Fatalf("delivery: %+v", got)
	}

	title := "new"
	if it, err := e.m.UpdateInboxItem(ctx, nil, item.ID, "hh", InboxUpdate{Title: &title}); err != nil || it.Title != "new" || it.Summary != "s" {
		t.Fatalf("update: %+v %v", it, err)
	}
	if _, err := e.m.UpdateInboxItem(ctx, nil, item.ID, "other", InboxUpdate{Title: &title}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update other household: %v", err)
	}
	if _, err := e.m.Notify(ctx, nil, "cc", Notification{TargetType: "node"}); !errors.Is(err, ErrTargetType) {
		t.Fatal(err)
	}
	if _, err := e.m.Notify(ctx, nil, "cc", Notification{TargetType: "user", Priority: "x"}); !errors.Is(err, ErrPriority) {
		t.Fatal(err)
	}
}

func TestPurge(t *testing.T) {
	rl := newRelay(t)
	e := setup(t, withRelay(rl))
	u := userH(t, 1, "hh1")
	e.registerToken(t, 1, "hh1", "tok-1")
	e.registerToken(t, 2, "hh1", "tok-2")
	for _, b := range []string{
		`{"household_id":"hh1","user_id":1,"title":"mine","summary":"s","body":"b","category":"c","source_service":"x"}`,
		`{"household_id":"hh1","user_id":2,"title":"theirs","summary":"s","body":"b","category":"c","source_service":"x"}`,
		`{"household_id":"hh1","title":"household","summary":"s","body":"b","category":"c","source_service":"x"}`,
	} {
		e.json(t, 200, "POST", "/api/v0/inbox", b, appH...)
	}
	if c, b := e.do(t, "DELETE", "/api/v0/me/data", "", u...); c != 204 || b != "" {
		t.Fatalf("purge: %d %q", c, b)
	}
	e.json(t, 204, "DELETE", "/api/v0/me/data", "", u...) // idempotent
	var tokens, items int
	e.d.Read.QueryRow(`SELECT COUNT(*) FROM notifications_device_tokens`).Scan(&tokens)
	e.d.Read.QueryRow(`SELECT COUNT(*) FROM notifications_inbox_items`).Scan(&items)
	if tokens != 1 || items != 2 {
		t.Fatalf("after user purge: %d tokens, %d items", tokens, items)
	}

	// The in-process hook (auth.OnUserDeleted) does the same inside the deletion transaction,
	// and de-identifies the user's delivery log rows (D20) without deleting them.
	e.d.Write.Exec(`INSERT INTO notifications_notification_log (id, source_service, target_type, target_id, title, body)
		VALUES ('l1', 'cc', 'user', '2', 't', 'b'), ('l2', 'cc', 'user', '22', 't', 'b'), ('l3', 'cc', 'household', '2', 't', 'b')`)
	if err := e.d.Tx(e.ctx, func(tx *sql.Tx) error { return e.m.PurgeUser(e.ctx, tx, 2) }); err != nil {
		t.Fatal(err)
	}
	e.d.Read.QueryRow(`SELECT COUNT(*) FROM notifications_inbox_items`).Scan(&items)
	if items != 1 {
		t.Fatalf("after PurgeUser: %d items", items)
	}
	rows, _ := e.d.Read.Query(`SELECT id, target_id FROM notifications_notification_log ORDER BY id`)
	got := map[string]string{}
	for rows.Next() {
		var id, target string
		rows.Scan(&id, &target)
		got[id] = target
	}
	rows.Close()
	if got["l1"] != "deleted-user" || got["l2"] != "22" || got["l3"] != "2" {
		t.Fatalf("log after PurgeUser: %v", got)
	}
	if err := e.d.Tx(e.ctx, func(tx *sql.Tx) error { return e.m.PurgeHousehold(e.ctx, tx, "hh1") }); err != nil {
		t.Fatal(err)
	}
	e.d.Read.QueryRow(`SELECT COUNT(*) FROM notifications_inbox_items`).Scan(&items)
	if items != 0 {
		t.Fatalf("after PurgeHousehold: %d items", items)
	}
}

func TestQueuedPushSkipsPurgedTokens(t *testing.T) {
	rl := newRelay(t)
	e := setup(t, withRelay(rl))
	payload, _ := json.Marshal(pushJob{LogID: "log-1", Tokens: []string{"gone"}, Title: "t", Body: "b", Priority: "default", HouseholdID: "hh"})
	e.d.Write.Exec(`INSERT INTO notifications_notification_log (id, source_service, target_type, target_id, title, body, delivery_status, token_count, created_at)
		VALUES ('log-1', 'cc', 'user', '1', 't', 'b', 'pending', 1, ?)`, ts(time.Now()))
	if _, err := e.m.runPush(e.ctx, queue.Job{Payload: payload, Attempt: 1}); err != nil {
		t.Fatal(err)
	}
	if d := e.logRow(t, "log-1"); d.DeliveryStatus != "skipped" || rl.count() != 0 {
		t.Fatalf("purged token: %+v, %d sends", d, rl.count())
	}
}

func TestAdminStatsAndCleanup(t *testing.T) {
	e := setup(t)
	now := time.Now().UTC()
	old, recent := ts(now.Add(-40*24*time.Hour)), ts(now.Add(-time.Hour))
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := e.d.Write.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO notifications_notification_log (id, source_service, target_type, target_id, title, body, delivery_status, created_at) VALUES
		('a','s','user','1','t','b','delivered',?), ('b','s','user','1','t','b','skipped',?), ('c','s','user','1','t','b','failed',?)`, recent, recent, old)
	exec(`INSERT INTO notifications_device_tokens (id, user_id, household_id, push_token, device_type, is_active, last_used_at) VALUES
		('1', 1, 'hh1', 'fresh', 'ios', 1, ?), ('2', 1, 'hh1', 'stale', 'ios', 1, ?), ('3', 2, 'hh2', 'never', 'ios', 1, NULL), ('4', 2, 'hh2', 'off', 'ios', 0, NULL)`,
		recent, ts(now.Add(-100*24*time.Hour)))

	v := asObj(e.json(t, 200, "GET", "/api/v0/admin/stats", "", adminH...))
	tok, n24 := asObj(v["tokens"]), asObj(v["notifications_24h"])
	if tok["total"].(json.Number).String() != "4" || tok["active"].(json.Number).String() != "3" || len(tok["by_household"].([]any)) != 2 {
		t.Fatalf("tokens: %v", tok)
	}
	if n24["total"].(json.Number).String() != "2" || asObj(n24["by_status"])["delivered"].(json.Number).String() != "1" {
		t.Fatalf("24h: %v", n24)
	}
	v = asObj(e.json(t, 200, "POST", "/api/v0/admin/cleanup", "", adminH...))
	if v["status"] != "ok" || v["logs_pruned"].(json.Number).String() != "1" || v["tokens_deactivated"].(json.Number).String() != "1" {
		t.Fatalf("cleanup: %v", v)
	}
	var stale int
	e.d.Read.QueryRow(`SELECT is_active FROM notifications_device_tokens WHERE push_token = 'stale'`).Scan(&stale)
	if stale != 0 {
		t.Fatal("stale token still active")
	}
	// The scheduled job runs the same cleanup.
	if _, err := e.m.runCleanup(e.ctx, queue.Job{}); err != nil {
		t.Fatal(err)
	}
}

func TestStartSchedulesCleanup(t *testing.T) {
	e := setup(t)
	if err := e.m.Start(e.ctx); err != nil {
		t.Fatal(err)
	}
	st, err := e.m.deps.Scheduler.Status(e.ctx, cleanupJobType)
	if err != nil || st.NextFireAt.Before(time.Now().Add(23*time.Hour)) {
		t.Fatalf("cleanup trigger: %+v %v", st, err)
	}
	e2 := setup(t, func(m *Module) { m.CleanupInterval = -1 })
	if err := e2.m.Start(e2.ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := e2.m.deps.Scheduler.Status(e2.ctx, cleanupJobType); err == nil {
		t.Fatal("disabled cleanup was scheduled")
	}
}

// ID8: the relay is the relay.url setting (env fallback RELAY_URL), read per push, so turning
// it on in the wizard needs no restart; switching relays drops JWTs the old one issued.
func TestRelayFromSetting(t *testing.T) {
	a, b := newRelay(t), newRelay(t)
	e := setup(t)
	e.registerToken(t, 1, "hh1", "tok-a")
	n := 0
	send := func(want string) Delivery {
		t.Helper()
		n++ // distinct titles: identical pushes are deduplicated
		v := asObj(e.json(t, 200, "POST", "/api/v0/notify", notify("user", "1", "t"+strconv.Itoa(n)), appH...))
		if v["delivery_status"] != want {
			t.Fatalf("status %v, want %s", v, want)
		}
		if want == "skipped" {
			return Delivery{}
		}
		return e.logRow(t, v["id"].(string))
	}
	send("skipped") // off by default
	set := func(v any) {
		t.Helper()
		if err := e.m.Settings().Set(e.ctx, SettingRelayURL, v, settings.Scope{}); err != nil {
			t.Fatal(err)
		}
	}
	set(a.srv.URL + "/")
	if d := send("pending"); d.DeliveryStatus != "delivered" || a.count() != 1 {
		t.Fatalf("relay a: %+v sends=%d", d, a.count())
	}
	set(b.srv.URL)
	if d := send("pending"); d.DeliveryStatus != "delivered" || b.count() != 1 || b.registers.Load() != 1 {
		t.Fatalf("relay b: %+v sends=%d registers=%d", d, b.count(), b.registers.Load())
	}
	b.mu.Lock()
	auth := b.auth[0]
	b.mu.Unlock()
	if auth != "Bearer jwt-hh1-1|hh1" {
		t.Fatalf("relay b used a JWT from relay a: %s", auth)
	}
	set("")
	send("skipped")
}

func TestRelayEnvFallback(t *testing.T) {
	rl := newRelay(t)
	t.Setenv("RELAY_URL", rl.srv.URL)
	e := setup(t)
	e.registerToken(t, 1, "hh1", "tok-a")
	v := asObj(e.json(t, 200, "POST", "/api/v0/notify", notify("user", "1", "t"), appH...))
	if d := e.logRow(t, v["id"].(string)); d.DeliveryStatus != "delivered" || rl.count() != 1 {
		t.Fatalf("env relay: %+v", d)
	}
}
