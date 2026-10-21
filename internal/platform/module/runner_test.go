package module

import (
	"bufio"
	"context"
	"errors"
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
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
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
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if a := r.Addr(listener); a != "" {
			return a
		}
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
	ready := make(chan struct{})
	r := &Runner{Deps: deps(t), Modules: []Module{cc, notif, ccExtra}, OnReady: func() { close(ready) }}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	// Listeners are bound before modules start; wait for both.
	select {
	case <-ready:
	case <-time.After(30 * time.Second): // hang guard
		t.Fatal("OnReady never called")
	}
	ccAddr, nAddr := r.Addr(config.ListenerCC), r.Addr(config.ListenerNotifications)
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
	case <-time.After(30 * time.Second): // hang guard
		t.Fatal("Run did not return after cancel")
	}
}

// handlerModule registers handler funcs on a listener.
type handlerModule struct {
	name, listener string
	routes         map[string]http.HandlerFunc
}

func (h *handlerModule) Name() string      { return h.name }
func (h *handlerModule) Listener() string  { return h.listener }
func (h *handlerModule) Migrations() fs.FS { return nil }
func (h *handlerModule) Register(mux *http.ServeMux, _ Deps) {
	for pattern, f := range h.routes {
		mux.HandleFunc(pattern, f)
	}
}

func TestRunnerMountsListenersOnTheConfigPort(t *testing.T) {
	cfg := &fakeModule{name: "config", listener: config.ListenerConfig, routes: map[string]string{"GET /info": "config"}}
	cc := &handlerModule{name: "cc", listener: config.ListenerCC, routes: map[string]http.HandlerFunc{
		"GET /health":  func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "cc") },
		"GET /prefix":  func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, httpx.MountPrefix(r)) },
		"GET /raw/{x}": func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, r.URL.RawPath) },
	}}
	ready := make(chan struct{})
	r := &Runner{Deps: deps(t), Modules: []Module{cfg, cc}, Mounts: map[string]string{config.ListenerCC: "/cc"},
		OnReady: func() { close(ready) }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	select {
	case <-ready:
	case <-time.After(30 * time.Second): // hang guard
		t.Fatal("OnReady never called")
	}
	cfgAddr, ccAddr := r.Addr(config.ListenerConfig), r.Addr(config.ListenerCC)
	for _, c := range []struct {
		addr, path string
		code       int
		body       string
	}{
		{cfgAddr, "/cc/health", 200, "cc"},
		{cfgAddr, "/info", 200, "config"},
		{cfgAddr, "/cc/prefix", 200, "/cc"},
		{ccAddr, "/prefix", 200, ""},
		{cfgAddr, "/cc/raw/a%2Fb", 200, "/raw/a%2Fb"},
		{cfgAddr, "/nope/health", 404, ""},
		{ccAddr, "/health", 200, "cc"}, // the own port is unchanged
	} {
		code, b := get(t, c.addr, c.path)
		if code != c.code || (c.code == 200 && b != c.body) {
			t.Errorf("%s%s = %d %q, want %d %q", c.addr, c.path, code, b, c.code, c.body)
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second): // hang guard
		t.Fatal("Run did not return after cancel")
	}
}

func TestMountedStreamsFlush(t *testing.T) {
	cfg := &fakeModule{name: "config", listener: config.ListenerConfig, routes: map[string]string{"GET /info": "config"}}
	sse := &sseModule{listener: config.ListenerCC}
	r := &Runner{Deps: deps(t), Modules: []Module{cfg, sse}, Mounts: map[string]string{config.ListenerCC: "/cc"}}
	ctx, cancel := context.WithCancel(context.Background())
	done := openStreamAt(t, ctx, r, config.ListenerConfig, "/cc/stream")
	cancel()
	select {
	case <-done:
	case <-time.After(30 * time.Second): // hang guard
		t.Fatal("Run never returned")
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

// TestRunnerDowngradeGuard: an older binary (fewer migrations) refuses a database a newer one
// migrated, unless AllowDowngrade.
func TestRunnerDowngradeGuard(t *testing.T) {
	v1 := fstest.MapFS{"00001_users.sql": {Data: []byte("-- +goose Up\nCREATE TABLE auth_users (id INTEGER PRIMARY KEY);\n")}}
	v2 := fstest.MapFS{
		"00001_users.sql": v1["00001_users.sql"],
		"00002_more.sql":  {Data: []byte("-- +goose Up\nCREATE TABLE auth_more (id INTEGER PRIMARY KEY);\n")},
	}
	d := deps(t)
	newer := &Runner{Deps: d, Modules: []Module{&fakeModule{name: "auth", listener: config.ListenerAuth, migrations: v2}}}
	if err := newer.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	older := &Runner{Deps: d, Modules: []Module{&fakeModule{name: "auth", listener: config.ListenerAuth, migrations: v1}}}
	var de *db.DowngradeError
	if err := older.Run(context.Background()); !errors.As(err, &de) || de.Module != "auth" || de.Unknown[0] != 2 {
		t.Fatalf("older binary: %v", err)
	}
	older.AllowDowngrade = true
	if err := older.Migrate(context.Background()); err != nil {
		t.Fatalf("allowed downgrade: %v", err)
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
	var ready atomic.Bool
	r := &Runner{Deps: d, Modules: []Module{
		&fakeModule{name: "auth", listener: config.ListenerAuth},
		&fakeModule{name: "cc", listener: config.ListenerCC},
	}, OnReady: func() { ready.Store(true) }}
	if err := r.Run(context.Background()); err == nil {
		t.Fatal("want bind error")
	}
	if ready.Load() {
		t.Fatal("OnReady after a failed bind")
	}
}

// OnReady (sd_notify READY=1, SCM Running) fires once every listener answers.
func TestRunnerOnReadyAfterListenersBound(t *testing.T) {
	cc := &fakeModule{name: "cc", listener: config.ListenerCC, routes: map[string]string{"GET /health": "ok"}}
	auth := &fakeModule{name: "auth", listener: config.ListenerAuth, routes: map[string]string{"GET /health": "ok"}}
	readyc := make(chan [2]string, 1)
	var r *Runner
	r = &Runner{Deps: deps(t), Modules: []Module{cc, auth}, OnReady: func() {
		readyc <- [2]string{r.Addr(config.ListenerCC), r.Addr(config.ListenerAuth)}
	}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	select {
	case addrs := <-readyc:
		for _, a := range addrs {
			if _, b := get(t, a, "/health"); b != "ok" {
				t.Errorf("%s not serving at ready", a)
			}
		}
		if !cc.started.Load() || !auth.started.Load() {
			t.Error("ready before modules started")
		}
	case <-time.After(30 * time.Second): // hang guard
		t.Fatal("OnReady never called")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
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

// sseModule serves GET /stream like the admin log tail: one event, then it holds the
// connection until the request's context ends.
type sseModule struct {
	cancelled atomic.Bool
	listener  string // "" = admin
}

func (*sseModule) Name() string { return "sse" }
func (m *sseModule) Listener() string {
	if m.listener == "" {
		return config.ListenerAdmin
	}
	return m.listener
}
func (*sseModule) Migrations() fs.FS { return nil }
func (m *sseModule) Register(mux *http.ServeMux, _ Deps) {
	mux.HandleFunc("GET /stream", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: hi\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		m.cancelled.Store(true)
	})
}

// openStream starts r with ctx and holds one SSE request open; it returns Run's result channel.
func openStream(t *testing.T, ctx context.Context, r *Runner) chan error {
	t.Helper()
	return openStreamAt(t, ctx, r, config.ListenerAdmin, "/stream")
}

// openStreamAt is openStream with the stream read from path on listener's address.
func openStreamAt(t *testing.T, ctx context.Context, r *Runner, listener, path string) chan error {
	t.Helper()
	ready := make(chan struct{})
	r.OnReady = func() { close(ready) }
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	<-ready
	// A stream that never flushes sends no headers and no event: cancel the request after
	// 10s so that fails here instead of at go test's timeout.
	reqCtx, cancelReq := context.WithCancel(context.Background())
	t.Cleanup(cancelReq)
	timer := time.AfterFunc(10*time.Second, cancelReq)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, "http://"+r.Addr(listener)+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d, want 200", path, resp.StatusCode)
	}
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if !timer.Stop() {
		t.Fatalf("GET %s: no event within 10s (not flushed?)", path)
	}
	if err != nil {
		t.Fatal(err)
	}
	if line != "data: hi\n" {
		t.Fatalf("GET %s: first line %q, want %q", path, line, "data: hi\n")
	}
	return done
}

func TestShutdownCancelsOpenStreams(t *testing.T) {
	old := ShutdownTimeout
	ShutdownTimeout = 5 * time.Second
	defer func() { ShutdownTimeout = old }()
	sse := &sseModule{}
	r := &Runner{Deps: deps(t), Modules: []Module{sse}}
	ctx, cancel := context.WithCancel(context.Background())
	done := openStream(t, ctx, r)
	start := time.Now()
	cancel()
	select {
	case <-done:
	case <-time.After(30 * time.Second): // hang guard
		t.Fatal("Run never returned")
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Errorf("Run took %v to stop with an SSE stream open; want well under ShutdownTimeout (%v)", el, ShutdownTimeout)
	}
	if !sse.cancelled.Load() {
		t.Error("the stream handler's request context was not cancelled")
	}
}

func TestShutdownDrainsQueueBeforeReturning(t *testing.T) {
	old, oldDrain := ShutdownTimeout, DrainTimeout
	ShutdownTimeout, DrainTimeout = time.Second, 10*time.Second
	defer func() { ShutdownTimeout, DrainTimeout = old, oldDrain }()
	d := deps(t)
	d.Queue = queue.New(d.DB, d.Log)
	d.Queue.PollInterval = 10 * time.Millisecond
	d.Scheduler = scheduler.New(d.DB, d.Queue, d.Log)
	running := make(chan struct{})
	var finished atomic.Bool
	d.Queue.Register("slow", queue.Handler{Run: func(ctx context.Context, _ queue.Job) ([]byte, error) {
		close(running)
		<-ctx.Done()
		time.Sleep(1500 * time.Millisecond) // longer than ShutdownTimeout: the drain has its own budget
		finished.Store(true)
		return nil, ctx.Err()
	}})
	r := &Runner{Deps: d, Modules: []Module{&sseModule{}}}
	if err := r.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := openStream(t, ctx, r)
	if _, err := d.Queue.Enqueue(context.Background(), "slow", nil, queue.Options{}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-running:
	case <-time.After(30 * time.Second): // hang guard
		t.Fatal("job never started")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(30 * time.Second): // hang guard
		t.Fatal("Run never returned")
	}
	if !finished.Load() {
		t.Error("Run returned (and the caller would close the DB) while a job was still running")
	}
}

// waitModule is a Waiter whose background work finishes when release is closed.
type waitModule struct {
	sseModule
	release  chan struct{}
	finished atomic.Bool
}

func (m *waitModule) Wait() {
	<-m.release
	m.finished.Store(true)
}

func TestShutdownWaitsForModuleBackgroundWork(t *testing.T) {
	old, oldDrain := ShutdownTimeout, DrainTimeout
	ShutdownTimeout, DrainTimeout = time.Second, 10*time.Second
	defer func() { ShutdownTimeout, DrainTimeout = old, oldDrain }()
	w := &waitModule{release: make(chan struct{})}
	r := &Runner{Deps: deps(t), Modules: []Module{w}}
	ctx, cancel := context.WithCancel(context.Background())
	done := openStream(t, ctx, r)
	cancel()
	select {
	case <-done:
		t.Fatal("Run returned (and the caller would close the DB) before the module's Wait did")
	case <-time.After(200 * time.Millisecond):
	}
	close(w.release)
	select {
	case <-done:
	case <-time.After(30 * time.Second): // hang guard
		t.Fatal("Run never returned")
	}
	if !w.finished.Load() {
		t.Error("the module's Wait did not finish")
	}
}

func TestShutdownGivesUpOnModuleWaitAtDrainTimeout(t *testing.T) {
	old, oldDrain := ShutdownTimeout, DrainTimeout
	ShutdownTimeout, DrainTimeout = time.Second, 300*time.Millisecond
	defer func() { ShutdownTimeout, DrainTimeout = old, oldDrain }()
	w := &waitModule{release: make(chan struct{})}
	defer close(w.release)
	r := &Runner{Deps: deps(t), Modules: []Module{w}}
	ctx, cancel := context.WithCancel(context.Background())
	done := openStream(t, ctx, r)
	start := time.Now()
	cancel()
	select {
	case <-done:
	case <-time.After(30 * time.Second): // hang guard
		t.Fatal("Run never returned with a module stuck in Wait")
	}
	if el := time.Since(start); el < DrainTimeout || el > 5*time.Second {
		t.Errorf("Run returned after %v; want about DrainTimeout (%v)", el, DrainTimeout)
	}
}

// singlePortSpy records Deps.Config.SinglePort as Register saw it.
type singlePortSpy struct {
	fakeModule
	singlePort atomic.Bool
}

func (s *singlePortSpy) Register(mux *http.ServeMux, deps Deps) {
	s.singlePort.Store(deps.Config.SinglePort)
	s.fakeModule.Register(mux, deps)
}

// runLegacy runs config + cc + admin fakes with cc mounted at /cc and legacy as the Legacy
// hook's answer, until OnReady.
func runLegacy(t *testing.T, legacy bool) (*Runner, *singlePortSpy) {
	t.Helper()
	cfg := &singlePortSpy{fakeModule: fakeModule{name: "config", listener: config.ListenerConfig, routes: map[string]string{"GET /info": "config"}}}
	cc := &fakeModule{name: "cc", listener: config.ListenerCC, routes: map[string]string{"GET /health": "cc"}}
	admin := &fakeModule{name: "admin", listener: config.ListenerAdmin, routes: map[string]string{"GET /health": "admin"}}
	ready := make(chan struct{})
	r := &Runner{Deps: deps(t), Modules: []Module{cfg, cc, admin}, Mounts: map[string]string{config.ListenerCC: "/cc"},
		Legacy:  func(context.Context) (bool, error) { return legacy, nil },
		OnReady: func() { close(ready) }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(30 * time.Second): // hang guard
			t.Error("Run did not return after cancel")
		}
	})
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("Run returned early: %v", err)
	case <-time.After(30 * time.Second): // hang guard
		t.Fatal("OnReady never called")
	}
	if !cc.started.Load() {
		t.Error("cc not started")
	}
	return r, cfg
}

func TestRunnerLegacyOffBindsOnlyUnmounted(t *testing.T) {
	r, cfg := runLegacy(t, false)
	if a := r.Addr(config.ListenerCC); a != "" {
		t.Errorf("cc bound %s with legacy ports off", a)
	}
	if r.Addr(config.ListenerAdmin) == "" {
		t.Error("admin (not mounted) did not bind")
	}
	if code, b := get(t, r.Addr(config.ListenerConfig), "/cc/health"); code != 200 || b != "cc" {
		t.Errorf("/cc/health on config = %d %q", code, b)
	}
	if !cfg.singlePort.Load() {
		t.Error("Deps.Config.SinglePort was false in Register")
	}
}

func TestRunnerLegacyOnBindsAll(t *testing.T) {
	r, cfg := runLegacy(t, true)
	for _, l := range []string{config.ListenerConfig, config.ListenerCC, config.ListenerAdmin} {
		if r.Addr(l) == "" {
			t.Errorf("%s did not bind", l)
		}
	}
	if code, b := get(t, r.Addr(config.ListenerCC), "/health"); code != 200 || b != "cc" {
		t.Errorf("cc's own port /health = %d %q", code, b)
	}
	if code, b := get(t, r.Addr(config.ListenerConfig), "/cc/health"); code != 200 || b != "cc" {
		t.Errorf("/cc/health on config = %d %q", code, b)
	}
	if cfg.singlePort.Load() {
		t.Error("Deps.Config.SinglePort was true with legacy ports on")
	}
}

func TestRunnerLegacyError(t *testing.T) {
	boom := errors.New("boom")
	cfg := &fakeModule{name: "config", listener: config.ListenerConfig}
	r := &Runner{Deps: deps(t), Modules: []Module{cfg}, Mounts: map[string]string{config.ListenerCC: "/cc"},
		Legacy: func(context.Context) (bool, error) { return false, boom }}
	if err := r.Run(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("Run = %v, want %v", err, boom)
	}
	if r.Addr(config.ListenerConfig) != "" {
		t.Error("bound a listener after Legacy failed")
	}
}
