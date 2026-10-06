package settings

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/platform/db"
)

var defs = []Definition{
	{Key: "memory.enabled", Category: "memory", Type: Bool, Default: true, Description: "Memory on"},
	{Key: "memory.pinned_max_chars", Category: "memory", Type: Int, Default: int64(500)},
	{Key: "model.temperature", Category: "model", Type: Float, Default: 0.7},
	{Key: "llm.proxy.url", Category: "llm", Type: String, Default: "http://localhost:7704", EnvFallback: "JARVIS_LLM_PROXY_API_URL"},
	{Key: "signals.automations", Category: "signals", Type: JSON, Default: map[string]any{}},
	{Key: "phone_calls.call_context", Category: "phone", Type: JSON, Default: nil, IsSecret: true},
	{Key: "llm.prompt_provider", Category: "llm", Type: String, Default: "Qwen3_8B_Compressed", RequiresReload: true,
		Options: []any{"Qwen3_8B_Compressed", "Qwen3_14B_Compressed"}},
}

func newService(t *testing.T, env map[string]string) *Service {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "jarvis.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	s, err := New(d, "cc", defs, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	s.getenv = func(k string) string { return env[k] }
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestCascade(t *testing.T) {
	s := newService(t, nil)
	ctx := context.Background()
	const k = "memory.pinned_max_chars"
	full := Scope{HouseholdID: "hh", NodeID: "n1", UserID: 7}

	expect := func(sc Scope, want int64, fromDB bool) {
		t.Helper()
		v, err := s.Get(ctx, k, sc)
		if err != nil {
			t.Fatal(err)
		}
		if v.Value != want || v.FromDB != fromDB {
			t.Fatalf("scope %+v: got %v (db=%v), want %d (db=%v)", sc, v.Value, v.FromDB, want, fromDB)
		}
	}
	expect(full, 500, false) // definition default

	steps := []struct {
		sc  Scope
		val int64
	}{
		{Scope{}, 1},                                // system
		{Scope{HouseholdID: "hh"}, 2},               // household
		{Scope{HouseholdID: "hh", NodeID: "n1"}, 3}, // node
		{Scope{UserID: 7}, 4},                       // user only
		{full, 5},                                   // user+node+household
	}
	for _, st := range steps {
		if err := s.Set(ctx, k, float64(st.val), st.sc); err != nil {
			t.Fatal(err)
		}
		expect(full, st.val, true) // each more specific level wins
	}
	// Other scopes still resolve to their own most-specific level.
	expect(Scope{HouseholdID: "hh", NodeID: "n2", UserID: 8}, 2, true)
	expect(Scope{HouseholdID: "other"}, 1, true)
	expect(Scope{UserID: 7, HouseholdID: "hh", NodeID: "n2"}, 4, true)
}

func TestSetUpsertsExactScope(t *testing.T) {
	s := newService(t, nil)
	ctx := context.Background()
	for _, v := range []float64{10, 20} {
		if err := s.Set(ctx, "memory.pinned_max_chars", v, Scope{}); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	s.db.Read.QueryRow(`SELECT count(*) FROM cc_settings`).Scan(&n)
	if n != 1 {
		t.Fatalf("rows=%d", n)
	}
	if got := s.Int(ctx, "memory.pinned_max_chars", Scope{}); got != 20 {
		t.Fatalf("got %d", got)
	}
}

func TestTypesAndSerialization(t *testing.T) {
	s := newService(t, nil)
	ctx := context.Background()
	s.Set(ctx, "memory.enabled", false, Scope{})
	s.Set(ctx, "model.temperature", float64(1), Scope{})
	s.Set(ctx, "signals.automations", map[string]any{"presence.seen": map[string]any{"enabled": true}}, Scope{})
	if s.Bool(ctx, "memory.enabled", Scope{}) {
		t.Error("bool")
	}
	if s.Float(ctx, "model.temperature", Scope{}) != 1 {
		t.Error("float")
	}
	var raw string
	s.db.Read.QueryRow(`SELECT value FROM cc_settings WHERE key='model.temperature'`).Scan(&raw)
	if raw != "1.0" { // Python str(1.0)
		t.Errorf("float stored as %q", raw)
	}
	s.db.Read.QueryRow(`SELECT value FROM cc_settings WHERE key='memory.enabled'`).Scan(&raw)
	if raw != "false" {
		t.Errorf("bool stored as %q", raw)
	}
	v, _ := s.Get(ctx, "signals.automations", Scope{})
	if m, ok := v.Value.(map[string]any); !ok || m["presence.seen"] == nil {
		t.Errorf("json %#v", v.Value)
	}
}

func TestCoercion(t *testing.T) {
	s := newService(t, nil)
	ctx := context.Background()
	ins := func(key, value, vt string) {
		s.db.Write.Exec(`DELETE FROM cc_settings WHERE key = ?`, key)
		s.db.Write.Exec(`INSERT INTO cc_settings (key, value, value_type) VALUES (?, ?, ?)`, key, value, vt)
	}
	for _, c := range []struct {
		key, value, vt string
		want           any
	}{
		{"memory.enabled", "yes", "bool", true},
		{"memory.enabled", "ON", "bool", true},
		{"memory.enabled", "nope", "bool", false},
		{"memory.enabled", "", "bool", true},                      // empty -> default
		{"memory.pinned_max_chars", "abc", "int", int64(500)},     // parse failure -> default
		{"memory.pinned_max_chars", "42", "string", "42"},         // the row's own type wins
		{"signals.automations", "{bad", "json", map[string]any{}}, // bad JSON -> default
	} {
		ins(c.key, c.value, c.vt)
		v, err := s.Get(ctx, c.key, Scope{})
		if err != nil {
			t.Fatal(err)
		}
		b1, _ := json.Marshal(v.Value)
		b2, _ := json.Marshal(c.want)
		if string(b1) != string(b2) {
			t.Errorf("%s=%q (%s): got %s want %s", c.key, c.value, c.vt, b1, b2)
		}
	}
}

func TestEnvFallback(t *testing.T) {
	s := newService(t, map[string]string{"JARVIS_LLM_PROXY_API_URL": "http://gpu:7704"})
	ctx := context.Background()
	if got := s.String(ctx, "llm.proxy.url", Scope{}); got != "http://gpu:7704" {
		t.Fatalf("got %q", got)
	}
	s.Set(ctx, "llm.proxy.url", "http://db:7704", Scope{})
	if got := s.String(ctx, "llm.proxy.url", Scope{}); got != "http://db:7704" {
		t.Fatalf("DB should beat env: %q", got)
	}
}

func TestUnknownKeyAndBadDefs(t *testing.T) {
	s := newService(t, nil)
	if _, err := s.Get(context.Background(), "nope", Scope{}); err != ErrUnknownKey {
		t.Fatal(err)
	}
	if err := s.Set(context.Background(), "nope", 1, Scope{}); err != ErrUnknownKey {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := New(s.db, "cc", []Definition{{Key: "a", Type: Int}, {Key: "a", Type: Int}}, log); err == nil {
		t.Error("duplicate key accepted")
	}
	if _, err := New(s.db, "cc", []Definition{{Key: "a", Type: "date"}}, log); err == nil {
		t.Error("bad type accepted")
	}
	if _, err := New(s.db, "Bad Name", nil, log); err == nil {
		t.Error("bad module accepted")
	}
}

// --- router ---

func allow(http.ResponseWriter, *http.Request) bool { return true }

func deny(w http.ResponseWriter, _ *http.Request) bool {
	w.WriteHeader(http.StatusForbidden)
	return false
}

func serve(t *testing.T, s *Service, method, path, body string, write Guard) (int, map[string]any) {
	t.Helper()
	mux := http.NewServeMux()
	s.Mount(mux, allow, write)
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	var m map[string]any
	json.Unmarshal(rec.Body.Bytes(), &m)
	return rec.Code, m
}

func TestRouterGetPutList(t *testing.T) {
	s := newService(t, nil)

	code, m := serve(t, s, "GET", "/settings/memory.pinned_max_chars", "", allow)
	if code != 200 || m["value"] != float64(500) || m["from_db"] != false || m["value_type"] != "int" {
		t.Fatalf("get default: %d %v", code, m)
	}
	if _, has := m["options"]; !has || m["options"] != nil {
		t.Errorf("single get should carry options:null, got %v", m["options"])
	}

	code, m = serve(t, s, "PUT", "/settings/llm.prompt_provider", `{"value":"Qwen3_14B_Compressed"}`, allow)
	if code != 200 || m["success"] != true || m["requires_reload"] != true ||
		m["message"] != "Service restart required for this change to take effect" {
		t.Fatalf("put: %d %v", code, m)
	}

	code, m = serve(t, s, "PUT", "/settings/memory.enabled?household_id=hh", `{"value":false}`, allow)
	if code != 200 || m["message"] != nil {
		t.Fatalf("put scoped: %d %v", code, m)
	}
	_, m = serve(t, s, "GET", "/settings/memory.enabled?household_id=hh", "", allow)
	if m["value"] != false || m["from_db"] != true {
		t.Fatalf("scoped get: %v", m)
	}
	_, m = serve(t, s, "GET", "/settings/memory.enabled", "", allow)
	if m["value"] != true {
		t.Fatalf("system scope should be untouched: %v", m)
	}

	code, m = serve(t, s, "GET", "/settings/?category=llm", "", allow)
	if code != 200 || m["total"] != float64(2) {
		t.Fatalf("list: %d %v", code, m)
	}
	first := m["settings"].([]any)[0].(map[string]any)
	if first["key"] != "llm.prompt_provider" || first["value"] != "Qwen3_14B_Compressed" {
		t.Fatalf("list sorted by (category,key): %v", first)
	}

	code, m = serve(t, s, "GET", "/settings/categories", "", allow)
	if code != 200 || len(m["categories"].([]any)) != 5 {
		t.Fatalf("categories: %v", m)
	}
}

func TestRouterSecretsMasked(t *testing.T) {
	s := newService(t, nil)
	serve(t, s, "PUT", "/settings/phone_calls.call_context", `{"value":{"fields":[]}}`, allow)
	_, m := serve(t, s, "GET", "/settings/phone_calls.call_context", "", allow)
	if m["value"] != "********" {
		t.Fatalf("secret not masked: %v", m)
	}
}

func TestRouterErrors(t *testing.T) {
	s := newService(t, nil)
	code, m := serve(t, s, "GET", "/settings/no.such.key", "", allow)
	errObj, _ := m["detail"].(map[string]any)["error"].(map[string]any)
	if code != 404 || errObj["message"] != "Setting not found: no.such.key" || errObj["code"] != "not_found" {
		t.Fatalf("404 shape: %d %v", code, m)
	}
	if code, _ := serve(t, s, "PUT", "/settings/memory.enabled", `{"value":true}`, deny); code != 403 {
		t.Fatalf("write guard: %d", code)
	}
	if code, _ := serve(t, s, "GET", "/settings/memory.enabled?user_id=abc", "", allow); code != 422 {
		t.Fatalf("bad user_id: %d", code)
	}
	code, m = serve(t, s, "POST", "/settings/invalidate-cache?key=memory.enabled", "", allow)
	if code != 200 || m["invalidated"] != "memory.enabled" {
		t.Fatalf("invalidate: %v", m)
	}
}
