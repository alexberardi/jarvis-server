package module

import (
	"context"
	"errors"
	"fmt"
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

	mu    sync.Mutex
	addrs map[string]string // listener -> bound address, filled once listening
}

// Addr returns the bound address of a listener (useful with port 0), or "" if not listening.
func (r *Runner) Addr(listener string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.addrs[listener]
}

// Migrate runs the platform's migrations, then every module's, in module order.
func (r *Runner) Migrate(ctx context.Context) error {
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
		mux := muxes[m.Listener()]
		if mux == nil {
			mux = http.NewServeMux()
			muxes[m.Listener()] = mux
		}
		m.Register(mux, r.Deps)
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
	for _, m := range r.Modules {
		if s, ok := m.(Starter); ok {
			if err := s.Start(ctx); err != nil {
				errc <- fmt.Errorf("module: start %s: %w", m.Name(), err)
				break
			}
		}
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
	return runErr
}
