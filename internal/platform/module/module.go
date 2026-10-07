// Package module defines the contract every jarvisd module implements, and the runner that
// serves modules on their legacy listeners (PLAN §3.1).
package module

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/alexberardi/jarvis-server/internal/platform/blob"
	"github.com/alexberardi/jarvis-server/internal/platform/config"
	"github.com/alexberardi/jarvis-server/internal/platform/db"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
	"github.com/alexberardi/jarvis-server/internal/platform/scheduler"
)

// Module is one former service (or one part of command-center).
type Module interface {
	// Name is the module's identifier: its table prefix and its migration version table
	// (goose_<name>). Lowercase letters, digits and underscores.
	Name() string
	// Listener is the legacy listener (config.Listener*) whose port serves this module's routes.
	Listener() string
	// Migrations holds the module's goose SQL migrations, or nil if it has no tables.
	Migrations() fs.FS
	// Register mounts the module's routes. It is called once, after migrations ran.
	Register(mux *http.ServeMux, deps Deps)
}

// Starter is implemented by modules with background work (loops, queue workers). Start must
// return promptly; the work runs until ctx is cancelled.
type Starter interface {
	Start(ctx context.Context) error
}

// Deps are the shared platform services handed to every module. Modules register queue job
// handlers in Register; the runner starts the queue and scheduler after every module has
// registered. The MQTT broker and mDNS advertiser join once the auth and config modules
// exist to back them (Phase 1).
type Deps struct {
	Config    config.Config
	DB        *db.DB
	Log       *slog.Logger
	Queue     *queue.Queue
	Scheduler *scheduler.Scheduler
	Blobs     blob.Store
	// Handler returns the routes of another listener served by this process, for dispatching
	// a request to it in process (the admin gateway), or nil when that listener is not
	// served. The runner fills it in; call it at request time, after every module registered.
	// The returned handler is the bare mux, without the listener's recover/log middleware.
	Handler func(listener string) http.Handler
}
