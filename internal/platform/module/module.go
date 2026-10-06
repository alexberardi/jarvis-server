// Package module defines the contract every jarvisd module implements, and the runner that
// serves modules on their legacy listeners (PLAN §3.1).
package module

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/alexberardi/jarvis-server/internal/platform/config"
	"github.com/alexberardi/jarvis-server/internal/platform/db"
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

// Deps are the shared platform services handed to every module.
type Deps struct {
	Config config.Config
	DB     *db.DB
	Log    *slog.Logger
}
