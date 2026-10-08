package recipes

import (
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestHealthAndInfo(t *testing.T) {
	e := setup(t)
	if o := e.obj(t, 200, "GET", "/health", nil); len(o) != 1 || o["status"] != "ok" {
		t.Fatalf("health %v", o)
	}
	if o := e.obj(t, 200, "GET", "/info", nil); o["service"] != "jarvis-recipes-server" {
		t.Fatalf("info %v", o)
	}
}

func TestAuth(t *testing.T) {
	e := setup(t)
	for _, p := range []struct{ method, path string }{{"GET", "/ingredients/stock"}, {"GET", "/units/stock"}} {
		c, b := e.do(t, p.method, p.path, nil)
		if c != 401 || !strings.Contains(b, `"Not authenticated"`) {
			t.Fatalf("%s %s no token: %d %s", p.method, p.path, c, b)
		}
		e.expectDetail(t, 401, "Not authenticated", p.method, p.path, nil, "Authorization", "Basic Zm9vOmJhcg==")
		e.expectDetail(t, 401, "Invalid or expired token", p.method, p.path, nil, "Authorization", "Bearer nope")
	}
	if r := wwwAuth(t, e, "GET", "/units/stock"); r != "Bearer" {
		t.Fatalf("WWW-Authenticate %q", r)
	}
	e.expectDetail(t, 500, "Internal Server Error", "GET", "/units/stock", nil, "Authorization", "Bearer boom")
}

func wwwAuth(t *testing.T, e *env, method, p string) string {
	t.Helper()
	req, _ := http.NewRequest(method, p, nil)
	rec := &headerRecorder{h: http.Header{}}
	e.h.ServeHTTP(rec, req)
	return rec.h.Get("WWW-Authenticate")
}

type headerRecorder struct {
	h    http.Header
	code int
}

func (r *headerRecorder) Header() http.Header         { return r.h }
func (r *headerRecorder) Write(b []byte) (int, error) { return len(b), nil }
func (r *headerRecorder) WriteHeader(c int)           { r.code = c }

func TestStock(t *testing.T) {
	e := setup(t)
	h := tok(1, "")
	if n := len(e.list(t, "/ingredients/stock?q=&limit=1000", h...)); n != 198 {
		t.Fatalf("ingredients: %d", n)
	}
	units := e.list(t, "/units/stock?limit=100", h...)
	if len(units) != 51 {
		t.Fatalf("units: %d", len(units))
	}
	if n := len(e.list(t, "/ingredients/stock", h...)); n != 10 {
		t.Fatalf("default limit: %d", n)
	}
	for _, x := range e.list(t, "/ingredients/stock?q=SALT&limit=1000", h...) {
		if !strings.Contains(x.(map[string]any)["name"].(string), "salt") {
			t.Fatalf("q filter: %v", x)
		}
	}
	tb := e.list(t, "/units/stock?q=tbsp&limit=100", h...)
	if len(tb) != 1 || tb[0].(map[string]any)["name"] != "tablespoon" {
		t.Fatalf("abbreviation match: %v", tb)
	}
	nulls := 0
	for _, u := range units {
		if u.(map[string]any)["abbreviation"] == nil {
			nulls++
		}
	}
	if nulls == 0 {
		t.Fatalf("abbreviation should be null for some units")
	}
	e.expectValidation(t, "query.limit", "GET", "/ingredients/stock?limit=0", nil, h...)
	e.expectValidation(t, "query.limit", "GET", "/ingredients/stock?limit=1001", nil, h...)
	e.expectValidation(t, "query.limit", "GET", "/ingredients/stock?limit=x", nil, h...)
	e.expectValidation(t, "query.limit", "GET", "/units/stock?limit=101", nil, h...)

	// Re-seeding: skipped while the hash matches, and an upsert (no duplicates) when it doesn't.
	e.exec(t, `UPDATE recipes_stock_ingredients SET category = 'x' WHERE name = 'chicken breast'`)
	if err := e.m.seedStock(e.ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM recipes_stock_ingredients WHERE category = 'x'`); n != 1 {
		t.Fatalf("same hash should skip the upsert")
	}
	e.exec(t, `DELETE FROM recipes_meta`)
	if err := e.m.seedStock(e.ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM recipes_stock_ingredients`); n != 198 {
		t.Fatalf("reseed duplicated rows: %d", n)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM recipes_stock_ingredients WHERE category = 'x'`); n != 0 {
		t.Fatalf("reseed should restore the category")
	}
}

// TestOneScopingPredicate is §8 item 1: handlers never hand-write user/household filters; they
// go through caller.visible / caller.authorOnly (auth.go). The deletion hooks are exempt.
func TestOneScopingPredicate(t *testing.T) {
	files, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	raw := regexp.MustCompile(`(user_id|household_id)\s*(=|IN|IS)`)
	for _, f := range files {
		n := f.Name()
		if !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") || n == "auth.go" || n == "hooks.go" {
			continue
		}
		b, err := os.ReadFile(n)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if raw.MatchString(line) {
				t.Errorf("%s:%d hand-written scope filter: %s", n, i+1, strings.TrimSpace(line))
			}
		}
	}
}
