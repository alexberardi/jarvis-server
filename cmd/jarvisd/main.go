// Command jarvisd is the single-binary Jarvis server.
//
// See docs/PLAN.md for the architecture and docs/STATUS.md for current progress.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/alexberardi/jarvis-server/internal/platform/config"
	"github.com/alexberardi/jarvis-server/internal/platform/db"
	"github.com/alexberardi/jarvis-server/internal/platform/module"
)

var version = "dev"

// modules lists every module jarvisd serves. Modules are added here as they are ported.
func modules() []module.Module {
	return nil
}

const usage = `usage: jarvisd <command>

commands:
  serve            run the server
  migrate status   show each module's migration state
  version          print the version
`

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "jarvisd:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stdout, usage)
		return errors.New("no command")
	}
	switch args[0] {
	case "version":
		fmt.Fprintln(stdout, version)
		return nil
	case "serve":
		return serve(ctx)
	case "migrate":
		if len(args) < 2 || args[1] != "status" {
			return errors.New("usage: jarvisd migrate status")
		}
		return migrateStatus(ctx, stdout)
	default:
		fmt.Fprint(stdout, usage)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func newLogger() *slog.Logger {
	level := slog.LevelInfo
	switch strings.ToLower(os.Getenv("JARVIS_LOG_LEVEL")) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}

func openDeps(ctx context.Context) (module.Deps, error) {
	cfg, err := config.Load()
	if err != nil {
		return module.Deps{}, err
	}
	d, err := db.Open(ctx, cfg.DBPath())
	if err != nil {
		return module.Deps{}, err
	}
	return module.Deps{Config: cfg, DB: d, Log: newLogger()}, nil
}

func serve(ctx context.Context) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	deps, err := openDeps(ctx)
	if err != nil {
		return err
	}
	defer deps.DB.Close()
	mods := modules()
	if len(mods) == 0 {
		deps.Log.Warn("no modules are ported yet; nothing to serve")
	}
	deps.Log.Info("starting jarvisd", "version", version, "home", deps.Config.Home)
	return (&module.Runner{Deps: deps, Modules: mods}).Run(ctx)
}

func migrateStatus(ctx context.Context, stdout io.Writer) error {
	deps, err := openDeps(ctx)
	if err != nil {
		return err
	}
	defer deps.DB.Close()
	fmt.Fprintf(stdout, "%-20s %8s %8s %8s\n", "MODULE", "CURRENT", "LATEST", "PENDING")
	for _, m := range modules() {
		fsys := m.Migrations()
		if fsys == nil {
			continue
		}
		st, err := db.Status(ctx, deps.DB, m.Name(), fsys)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%-20s %8d %8d %8d\n", st.Module, st.Current, st.Latest, st.Pending)
	}
	return nil
}
