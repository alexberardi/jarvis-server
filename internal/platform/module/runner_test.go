package module

import (
	"context"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/config"
	"github.com/alexberardi/jarvis-server/internal/platform/db"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
	"github.com/alexberardi/jarvis-server/internal/platform/scheduler"
)

type fakeModule struct {
	name, listener string
	migrations     fs.FS
	routes         map[string]string // pattern -> body
	started        atomic.Bool
}

func (f *fakeModule) Name() string      { return f.name }
func (f *fakeModule) Listener() string  { return f.listener }
func (f *fakeModule) Migrations() fs.FS { return f.migrations }
func (f *fakeModule) Register(mux *http.ServeMux, _ Deps) {
	for pattern, body := range f.routes {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, body) })
	}
}
func (f *fakeModule) Start(ctx context.Context) error {
	f.started.Store(true)
	return nil
}

func deps(t *testing.T) Deps {
	t.Helper()
	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "jarvis.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	ports := map[string]int{}
	for n := range config.DefaultPorts {
		ports[n] = 0 // ephemeral
	}
	return Deps{
		Config: config.Config{Home: t.TempDir(), Host: "127.0.0.1", Ports: ports},
		DB:     d,
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func get(t *testing.T, addr, path string) (int, string) {
	t.Helper()
	resp, err := http.Get("http://" + addr + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func waitAddr(t *testing.T, r *Runner, listener string) string {
	t.Helper()
	for range 200 {
		if a := r.Addr(listener); a != "" {
			return a
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("listener %s never bound", listener)
	return ""
}

func TestRunnerServesModulesOnTheirListeners(t *testing.T) {
	// The same path on two listeners must not collide (e.g. /api/v0/me/data on CC and notifications).
	cc := &fakeModule{name: "cc", listener: config.ListenerCC, routes: map[string]string{
		"GET /health":            "cc",
		"DELETE /api/v0/me/data": "cc-purge",
	}}
	notif := &fakeModule{name: "notifications", listener: config.ListenerNotifications, routes: map[string]string{
		"GET /health":            "notifications",
		"DELETE /api/v0/me/data": "notif-purge",
	}}
	// Two modules may share one listener.
	ccExtra := &fakeModule{name: "cc_phone", listener: config.ListenerCC, routes: map[string]string{
		"GET /api/v0/phone/ping": "phone",
	}}
	r := &Runner{Deps: deps(t), Modules: []Module{cc, notif, ccExtra}}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	ccAddr, nAddr := waitAddr(t, r, config.ListenerCC), waitAddr(t, r, config.ListenerNotifications)
	if _, b := get(t, ccAddr, "/health"); b != "cc" {
		t.Errorf("cc /health = %q", b)
	}
	if _, b := get(t, nAddr, "/health"); b != "notifications" {
		t.Errorf("notifications /health = %q", b)
	}
	if _, b := get(t, ccAddr, "/api/v0/phone/ping"); b != "phone" {
		t.Errorf("shared listener route = %q", b)
	}
	if code, _ := get(t, nAddr, "/api/v0/phone/ping"); code != 404 {
		t.Errorf("route leaked to another listener: %d", code)
	}
	if r.Addr(config.ListenerAuth) != "" {
		t.Error("an unused listener was bound")
	}
	if !cc.started.Load() || !notif.started.Load() {
		t.Error("Start not called")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

// gatewayModule forwards GET /via/{path...} to another listener's routes in process.
type gatewayModule struct{ target string }

func (g *gatewayModule) Name() string      { return "gw" }
func (g *gatewayModule) Listener() string  { return config.ListenerAdmin }
func (g *gatewayModule) Migrations() fs.FS { return nil }
func (g *gatewayModule) Register(mux *http.ServeMux, deps Deps) {
	mux.HandleFunc("GET /via/{path...}", func(w http.ResponseWriter, r *http.Request) {
		h := deps.Handler(g.target)
		if h == nil {
			http.Error(w, "not served", http.StatusServiceUnavailable)
			return
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/" + r.PathValue("path")
		h.ServeHTTP(w, r2)
	})
}

func TestRunnerHandlerLookup(t *testing.T) {
	// The gateway registers before its target: the lookup must resolve at request time.
	auth := &fakeModule{name: "auth", listener: config.ListenerAuth, routes: map[string]string{"GET /auth/me": "me"}}
	r := &Runner{Deps: deps(t), Modules: []Module{&gatewayModule{target: config.ListenerAuth}, auth}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	addr := waitAddr(t, r, config.ListenerAdmin)
	if code, b := get(t, addr, "/via/auth/me"); code != 200 || b != "me" {
		t.Errorf("in-process dispatch: %d %q", code, b)
	}
	if code, _ := get(t, addr, "/via/nope"); code != 404 {
		t.Errorf("unknown target path: %d", code)
	}
	if r.Handler(config.ListenerLLM) != nil {
		t.Error("an unserved listener returned a handler")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRunnerRunsMigrations(t *testing.T) {
	m := &fakeModule{name: "auth", listener: config.ListenerAuth, migrations: fstest.MapFS{
		"00001_users.sql": {Data: []byte("-- +goose Up\nCREATE TABLE auth_users (id INTEGER PRIMARY KEY);\n")},
	}}
	r := &Runner{Deps: deps(t), Modules: []Module{m}}
	if err := r.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Deps.DB.Write.Exec(`INSERT INTO auth_users VALUES (1)`); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerRejectsDuplicateNames(t *testing.T) {
	a := &fakeModule{name: "x", listener: config.ListenerCC}
	b := &fakeModule{name: "x", listener: config.ListenerAuth}
	r := &Runner{Deps: deps(t), Modules: []Module{a, b}}
	if err := r.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("err=%v", err)
	}
}

func TestRunnerFailsOnPortConflict(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	d := deps(t)
	d.Config.Ports[config.ListenerCC] = busy.Addr().(*net.TCPAddr).Port
	r := &Runner{Deps: d, Modules: []Module{
		&fakeModule{name: "auth", listener: config.ListenerAuth},
		&fakeModule{name: "cc", listener: config.ListenerCC},
	}}
	if err := r.Run(context.Background()); err == nil {
		t.Fatal("want bind error")
	}
}

func TestRunnerMigratesPlatformTables(t *testing.T) {
	d := deps(t)
	d.Queue = queue.New(d.DB, d.Log)
	d.Scheduler = scheduler.New(d.DB, d.Queue, d.Log)
	r := &Runner{Deps: d}
	if err := r.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"platform_jobs", "platform_triggers"} {
		var n int
		if err := d.DB.Read.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
			t.Errorf("%s: %v", table, err)
		}
	}
}
