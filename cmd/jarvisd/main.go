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
	"path/filepath"
	"syscall"

	authmod "github.com/alexberardi/jarvis-server/internal/modules/auth"
	configmod "github.com/alexberardi/jarvis-server/internal/modules/config"
	"github.com/alexberardi/jarvis-server/internal/platform/blob"
	"github.com/alexberardi/jarvis-server/internal/platform/config"
	"github.com/alexberardi/jarvis-server/internal/platform/db"
	"github.com/alexberardi/jarvis-server/internal/platform/logging"
	"github.com/alexberardi/jarvis-server/internal/platform/module"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
	"github.com/alexberardi/jarvis-server/internal/platform/scheduler"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

var version = "dev"

// modules lists every module jarvisd serves. Modules are added here as they are ported.
func modules() []module.Module {
	mods := []module.Module{
		&configmod.Module{
			AdminToken: os.Getenv("JARVIS_CONFIG_ADMIN_TOKEN"),
			Advertise:  os.Getenv("JARVIS_MDNS") != "0",
		},
		&authmod.Module{
			AdminToken: os.Getenv("JARVIS_AUTH_ADMIN_TOKEN"),
			// Legacy HS256 secret: HS256 is minted (auth.algorithm=HS256) and verified only when set.
			HMACSecret: os.Getenv("AUTH_SECRET_KEY"),
		},
	}
	// The registry lists exactly the listeners jarvisd serves; account deletion skips the
	// legacy HTTP purge for the same services.
	var served, names []string
	for _, m := range mods {
		served = append(served, m.Listener())
		names = append(names, configmod.ServiceNames[m.Listener()])
	}
	var auth *authmod.Module
	for _, m := range mods {
		if a, ok := m.(*authmod.Module); ok {
			auth = a
		}
	}
	superuser := settings.SuperuserGuard(auth.VerifyUser)
	for _, m := range mods {
		switch c := m.(type) {
		case *configmod.Module:
			c.Served = served
			c.SettingsGuard = superuser
		case *authmod.Module:
			c.InProcess = names
		}
	}
	return mods
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
	return logging.New(os.Stderr, logging.ParseLevel(os.Getenv("JARVIS_LOG_LEVEL")), nil)
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
	log := newLogger()
	blobs, err := blob.NewFS(filepath.Join(cfg.Home, "blobs"), log)
	if err != nil {
		d.Close()
		return module.Deps{}, err
	}
	q := queue.New(d, log)
	return module.Deps{
		Config: cfg, DB: d, Log: log,
		Queue: q, Scheduler: scheduler.New(d, q, log), Blobs: blobs,
	}, nil
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
