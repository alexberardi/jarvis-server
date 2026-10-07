package module

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/db"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
	"github.com/alexberardi/jarvis-server/internal/platform/scheduler"
)

// ShutdownTimeout bounds graceful shutdown of each listener.
var ShutdownTimeout = 10 * time.Second

// Runner serves a set of modules.
type Runner struct {
	Deps    Deps
	Modules []Module
	// OnReady, if set, is called once every listener is bound and every module started,
	// e.g. to tell systemd READY=1 or the Windows SCM "running".
	OnReady func()
	// AllowDowngrade skips the downgrade guard (serve --allow-downgrade): run even though a
	// newer jarvisd migrated the database.
	AllowDowngrade bool

	mu    sync.Mutex
	addrs map[string]string         // listener -> bound address, filled once listening
	muxes map[string]*http.ServeMux // listener -> routes, filled before serving
}

// Addr returns the bound address of a listener (useful with port 0), or "" if not listening.
func (r *Runner) Addr(listener string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.addrs[listener]
}

// Handler returns a listener's routes, or nil if no module serves that listener (yet).
func (r *Runner) Handler(listener string) http.Handler {
	r.mu.Lock()
	defer r.mu.Unlock()
	if mux := r.muxes[listener]; mux != nil {
		return mux
	}
	return nil
}

// migrationSets lists the platform's and every module's migrations, in order.
func (r *Runner) migrationSets() []migrationSet {
	var sets []migrationSet
	if r.Deps.Queue != nil {
		sets = append(sets, migrationSet{queue.MigrationModule, queue.Migrations()})
	}
	if r.Deps.Scheduler != nil {
		sets = append(sets, migrationSet{scheduler.MigrationModule, scheduler.Migrations()})
	}
	for _, m := range r.Modules {
		if fsys := m.Migrations(); fsys != nil {
			sets = append(sets, migrationSet{m.Name(), fsys})
		}
	}
	return sets
}

type migrationSet struct {
	module string
	fsys   fs.FS
}

// CheckDowngrade refuses a database that a newer jarvisd migrated (ID10): an applied
// migration this binary doesn't have means its schema is unknown here.
func (r *Runner) CheckDowngrade(ctx context.Context) error {
	for _, s := range r.migrationSets() {
		if err := db.CheckDowngrade(ctx, r.Deps.DB, s.module, s.fsys); err != nil {
			return err
		}
	}
	return nil
}

// Migrate runs the platform's migrations, then every module's, in module order. Unless
// AllowDowngrade is set it first refuses a database a newer jarvisd migrated.
func (r *Runner) Migrate(ctx context.Context) error {
	if !r.AllowDowngrade {
		if err := r.CheckDowngrade(ctx); err != nil {
			return err
		}
	}
	if r.Deps.Queue != nil {
		if err := db.Migrate(ctx, r.Deps.DB, queue.MigrationModule, queue.Migrations()); err != nil {
			return err
		}
	}
	if r.Deps.Scheduler != nil {
		if err := db.Migrate(ctx, r.Deps.DB, scheduler.MigrationModule, scheduler.Migrations()); err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	for _, m := range r.Modules {
		if seen[m.Name()] {
			return fmt.Errorf("module: duplicate module name %q", m.Name())
		}
		seen[m.Name()] = true
		if fsys := m.Migrations(); fsys != nil {
			if err := db.Migrate(ctx, r.Deps.DB, m.Name(), fsys); err != nil {
				return err
			}
		}
	}
	return nil
}

// Run migrates, registers routes, binds one HTTP server per listener in use, starts
// background work, and blocks until ctx is cancelled or a listener fails. Every listener
// binds before any serves, so a port conflict fails startup cleanly.
func (r *Runner) Run(ctx context.Context) error {
	if err := r.Migrate(ctx); err != nil {
		return err
	}

	muxes := map[string]*http.ServeMux{}
	for _, m := range r.Modules {
		if muxes[m.Listener()] == nil {
			muxes[m.Listener()] = http.NewServeMux()
		}
	}
	r.mu.Lock()
	r.muxes = muxes
	r.mu.Unlock()
	deps := r.Deps
	deps.Handler = r.Handler
	for _, m := range r.Modules {
		m.Register(muxes[m.Listener()], deps)
	}

	names := make([]string, 0, len(muxes))
	for n := range muxes {
		names = append(names, n)
	}
	slices.Sort(names)

	var lc net.ListenConfig
	listeners := map[string]net.Listener{}
	closeAll := func() {
		for _, l := range listeners {
			l.Close()
		}
	}
	r.mu.Lock()
	r.addrs = map[string]string{}
	r.mu.Unlock()
	for _, n := range names {
		addr, err := r.Deps.Config.Addr(n)
		if err != nil {
			closeAll()
			return err
		}
		l, err := lc.Listen(ctx, "tcp", addr)
		if err != nil {
			closeAll()
			return fmt.Errorf("module: listen %s on %s: %w", n, addr, err)
		}
		listeners[n] = l
		r.mu.Lock()
		r.addrs[n] = l.Addr().String()
		r.mu.Unlock()
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	errc := make(chan error, len(listeners)+len(r.Modules))
	var servers []*http.Server
	var wg sync.WaitGroup
	for _, n := range names {
		srv := &http.Server{
			Handler:           httpx.Middleware(r.Deps.Log, n, muxes[n]),
			ReadHeaderTimeout: 10 * time.Second,
		}
		servers = append(servers, srv)
		l := listeners[n]
		r.Deps.Log.Info("listening", "listener", n, "addr", l.Addr().String())
		wg.Go(func() {
			if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errc <- fmt.Errorf("module: serve %s: %w", n, err)
			}
		})
	}

	if r.Deps.Queue != nil {
		r.Deps.Queue.Start(ctx)
	}
	if r.Deps.Scheduler != nil {
		r.Deps.Scheduler.Start(ctx)
	}
	started := true
	for _, m := range r.Modules {
		if s, ok := m.(Starter); ok {
			if err := s.Start(ctx); err != nil {
				errc <- fmt.Errorf("module: start %s: %w", m.Name(), err)
				started = false
				break
			}
		}
	}
	if started && r.OnReady != nil {
		r.OnReady()
	}

	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-errc:
	}
	cancel()

	sctx, scancel := context.WithTimeout(context.Background(), ShutdownTimeout)
	defer scancel()
	for _, srv := range servers {
		srv.Shutdown(sctx)
	}
	wg.Wait()
	if r.Deps.Queue != nil {
		// Running jobs saw ctx cancelled; let them finish before the caller closes the DB.
		done := make(chan struct{})
		go func() { r.Deps.Queue.Wait(); close(done) }()
		select {
		case <-done:
		case <-sctx.Done():
			r.Deps.Log.Warn("module: queue workers still running at shutdown timeout")
		}
	}
	return runErr
}
