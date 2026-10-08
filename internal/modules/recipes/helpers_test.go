package recipes

import (
	"context"
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
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/blob"
	"github.com/alexberardi/jarvis-server/internal/platform/db"
	"github.com/alexberardi/jarvis-server/internal/platform/module"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
	"github.com/alexberardi/jarvis-server/internal/platform/scheduler"
)

// fakeUsers verifies tokens of the form "user:<id>" or "user:<id>:<household claim>"; "boom"
// is an infrastructure failure.
type fakeUsers struct{}

func (fakeUsers) VerifyUser(_ context.Context, tok string) (authn.User, error) {
	if tok == "boom" {
		return authn.User{}, errors.New("db down")
	}
	parts := strings.Split(tok, ":")
	if len(parts) < 2 || parts[0] != "user" {
		return authn.User{}, authn.ErrInvalid
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return authn.User{}, authn.ErrInvalid
	}
	u := authn.User{ID: id}
	if len(parts) > 2 {
		u.HouseholdID = parts[2]
	}
	return u, nil
}

// fakeHouseholds is the membership table.
type fakeHouseholds struct {
	mu sync.Mutex
	m  map[int64][]string
}

func (f *fakeHouseholds) UserHouseholds(_ context.Context, id int64) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.m[id]...), nil
}

func (f *fakeHouseholds) set(id int64, hhs ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m[id] = hhs
}

type env struct {
	m     *Module
	h     http.Handler
	d     *db.DB
	q     *queue.Queue
	blobs blob.Store
	hh    *fakeHouseholds
	ctx   context.Context
}

func setup(t *testing.T) *env {
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
	}{{queue.MigrationModule, queue.Migrations()}, {scheduler.MigrationModule, scheduler.Migrations()}, {"recipes", Migrations()}} {
		if err := db.Migrate(ctx, d, mg.name, mg.fsys); err != nil {
			t.Fatal(err)
		}
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	bs, err := blob.NewFS(filepath.Join(t.TempDir(), "blobs"), log)
	if err != nil {
		t.Fatal(err)
	}
	q := queue.New(d, log)
	q.PollInterval = 10 * time.Millisecond
	hh := &fakeHouseholds{m: map[int64][]string{}}
	m := &Module{Users: fakeUsers{}, Households: hh}
	mux := http.NewServeMux()
	m.Register(mux, module.Deps{DB: d, Log: log, Queue: q, Blobs: bs})
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	q.Start(ctx)
	return &env{m: m, h: mux, d: d, q: q, blobs: bs, hh: hh, ctx: ctx}
}

// tok is a bearer header for user id with the token's household claim.
func tok(id int64, claim string) []string {
	t := "user:" + strconv.FormatInt(id, 10)
	if claim != "" {
		t += ":" + claim
	}
	return []string{"Authorization", "Bearer " + t}
}

func (e *env) do(t *testing.T, method, path string, body any, hdr ...string) (int, string) {
	t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		rd = strings.NewReader(string(raw))
	}
	r := httptest.NewRequest(method, path, rd)
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	return rec.Code, rec.Body.String()
}

// json does a request, checks the status and decodes the body.
func (e *env) json(t *testing.T, want int, method, path string, body any, hdr ...string) any {
	t.Helper()
	c, b := e.do(t, method, path, body, hdr...)
	if c != want {
		t.Fatalf("%s %s: want %d, got %d: %s", method, path, want, c, b)
	}
	if b == "" {
		return nil
	}
	var v any
	if err := json.Unmarshal([]byte(b), &v); err != nil {
		t.Fatalf("%s %s: bad json %q", method, path, b)
	}
	return v
}

func (e *env) obj(t *testing.T, want int, method, path string, body any, hdr ...string) map[string]any {
	t.Helper()
	v, _ := e.json(t, want, method, path, body, hdr...).(map[string]any)
	return v
}

func (e *env) list(t *testing.T, path string, hdr ...string) []any {
	t.Helper()
	v, _ := e.json(t, http.StatusOK, http.MethodGet, path, nil, hdr...).([]any)
	return v
}

// expectDetail checks a {"detail": "..."} error.
func (e *env) expectDetail(t *testing.T, status int, detail, method, path string, body any, hdr ...string) {
	t.Helper()
	o := e.obj(t, status, method, path, body, hdr...)
	if o["detail"] != detail {
		t.Fatalf("%s %s: detail %v, want %q", method, path, o["detail"], detail)
	}
}

// expectValidation checks the custom 422 and that one detail names field.
func (e *env) expectValidation(t *testing.T, field, method, path string, body any, hdr ...string) map[string]any {
	t.Helper()
	o := e.obj(t, http.StatusUnprocessableEntity, method, path, body, hdr...)
	if o["error_code"] != "validation_error" || o["message"] != "Invalid request payload." {
		t.Fatalf("%s %s: not the custom 422: %v", method, path, o)
	}
	if id, _ := o["job_id"].(string); len(id) != 36 {
		t.Fatalf("job_id %v", o["job_id"])
	}
	for _, d := range o["details"].([]any) {
		dm := d.(map[string]any)
		if dm["field"] == field {
			if _, ok := dm["message"].(string); !ok {
				t.Fatalf("detail without message: %v", dm)
			}
			return o
		}
	}
	t.Fatalf("%s %s: no detail for %q in %v", method, path, field, o["details"])
	return nil
}

func recipeBody(title string, extra map[string]any) map[string]any {
	b := map[string]any{
		"title":       title,
		"ingredients": []map[string]any{{"text": "1 cup flour", "quantity_display": "1", "unit": "cup"}},
		"steps":       []map[string]any{{"step_number": 1, "text": "Mix."}},
	}
	for k, v := range extra {
		b[k] = v
	}
	return b
}

func idOf(o map[string]any) int64 { return int64(o["id"].(float64)) }

func (e *env) create(t *testing.T, h []string, body map[string]any) map[string]any {
	t.Helper()
	return e.obj(t, http.StatusCreated, http.MethodPost, "/recipes", body, h...)
}

func path(f string, a ...any) string {
	for _, x := range a {
		f = strings.Replace(f, "%d", strconv.FormatInt(x.(int64), 10), 1)
	}
	return f
}

func (e *env) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := e.d.Read.QueryRowContext(e.ctx, query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (e *env) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := e.d.Write.ExecContext(e.ctx, query, args...); err != nil {
		t.Fatal(err)
	}
}

func authUser(id int64, hh string) authn.User { return authn.User{ID: id, HouseholdID: hh} }
