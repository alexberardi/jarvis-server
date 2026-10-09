package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/legacyimport"
	authmod "github.com/alexberardi/jarvis-server/internal/modules/auth"
	ccmod "github.com/alexberardi/jarvis-server/internal/modules/cc"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	configmod "github.com/alexberardi/jarvis-server/internal/modules/config"
	logsmod "github.com/alexberardi/jarvis-server/internal/modules/logs"
	ocrmod "github.com/alexberardi/jarvis-server/internal/modules/ocr"
	recipesmod "github.com/alexberardi/jarvis-server/internal/modules/recipes"
	sttmod "github.com/alexberardi/jarvis-server/internal/modules/stt"
	ttsmod "github.com/alexberardi/jarvis-server/internal/modules/tts"
	"github.com/alexberardi/jarvis-server/internal/platform/config"
	"github.com/alexberardi/jarvis-server/internal/platform/db"
	"github.com/alexberardi/jarvis-server/internal/platform/module"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
	"github.com/alexberardi/jarvis-server/internal/platform/scheduler"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// import-legacy: carry a legacy (Python/Postgres) install's users, households, nodes and the
// data hanging off them into a fresh jarvisd database, keeping ids (docs/install/legacy-import.md,
// ID6r). It reads the legacy Postgres directly and read-only. A dry run unless --apply.

// importSettingDefs are the setting definitions legacy settings rows are checked against,
// per jarvisd module. The llm module is absent: legacy model settings are not carried over.
func importSettingDefs() map[string][]settings.Definition {
	return map[string][]settings.Definition{
		"auth":    authmod.Definitions(),
		"cc":      ccmod.Definitions(),
		"config":  configmod.Definitions,
		"tts":     ttsmod.Definitions,
		"stt":     sttmod.Definitions,
		"ocr":     ocrmod.Definitions,
		"recipes": recipesmod.Definitions,
		"logs":    logsmod.Definitions,
	}
}

// legacySource opens the legacy Postgres; it is a variable so tests can substitute fixtures.
var legacySource = func(ctx context.Context, pg legacyimport.PGConfig) (legacyimport.Source, error) {
	return legacyimport.OpenPostgres(ctx, pg)
}

func runImportLegacy(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("import-legacy", flag.ContinueOnError)
	fs.SetOutput(stdout)
	apply := fs.Bool("apply", false, "write the import (default: a dry run that validates every row and writes nothing)")
	compose := fs.String("compose", "", "the legacy compose directory (~/.jarvis/compose), or a ./jarvis source checkout: credentials come from its env files")
	from := fs.String("from", "", "postgres://USER:PASSWORD@HOST:PORT of the legacy Postgres, instead of --compose")
	dbEnv := fs.String("databases-env", "", "a source checkout's DB_NAME_* file (default ~/.jarvis/databases.env)")
	pgAddr := fs.String("pg-addr", "", "HOST:PORT to reach the legacy Postgres at instead of the one --compose found (a container IP, an ssh tunnel)")
	logPath := fs.String("log", "", "write the per-row import log here (0600; default with --apply: <home>/import-legacy-<time>.log)")
	var heads, names multiFlag
	fs.Var(&heads, "accept-head", "DB=HEAD: accept a legacy database at another alembic head (repeatable)")
	fs.Var(&names, "db", "KEY=NAME: a legacy database's name when not the default, e.g. cc=jarvis_command_center (repeatable)")
	fs.Usage = func() {
		fmt.Fprintln(stdout, "usage: jarvisd import-legacy (--compose DIR | --from postgres://…) [--apply] [--accept-head DB=HEAD]... [--db KEY=NAME]... [--log FILE]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		fs.Usage()
		return fmt.Errorf("import-legacy takes no arguments (got %q)", fs.Arg(0))
	}
	if (*compose == "") == (*from == "") {
		fs.Usage()
		return errors.New("import-legacy needs exactly one of --compose DIR or --from postgres://…")
	}
	accept, err := keyValues(heads, "--accept-head", "DB=HEAD")
	if err != nil {
		return err
	}
	dbNames, err := keyValues(names, "--db", "KEY=NAME")
	if err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if _, err := os.Stat(cfg.Home); err == nil {
		if err := checkImportUser(cfg.Home); err != nil {
			return err
		}
	}
	if err := refuseWhileRunning(ctx, cfg); err != nil {
		return err
	}

	var pg legacyimport.PGConfig
	if *compose != "" {
		envFile := *dbEnv
		if envFile == "" {
			if h, err := os.UserHomeDir(); err == nil {
				envFile = filepath.Join(h, ".jarvis", "databases.env")
			}
		}
		found, err := legacyimport.DiscoverCompose(*compose, envFile)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "legacy compose: %s layout, credentials from %s\n", found.Layout, strings.Join(found.Files, ", "))
		for _, n := range found.Notes {
			fmt.Fprintf(stdout, "  note: %s\n", n)
		}
		pg = found.PG
	} else {
		if pg, err = legacyimport.ParsePostgresURL(*from); err != nil {
			return err
		}
	}
	if pg.DBNames == nil {
		pg.DBNames = map[string]string{}
	}
	for k, v := range dbNames {
		pg.DBNames[k] = v
	}
	if *pgAddr != "" {
		host, port, err := net.SplitHostPort(*pgAddr)
		p, perr := strconv.Atoi(port)
		if err != nil || perr != nil || host == "" || p <= 0 || p > 65535 {
			return fmt.Errorf("--pg-addr takes HOST:PORT, got %q", *pgAddr)
		}
		pg.Host, pg.Port = host, p
	}
	fmt.Fprintf(stdout, "legacy Postgres: %s (password not shown)\n", pg)

	src, err := legacySource(ctx, pg)
	if err != nil {
		return err
	}
	defer src.Close()

	// The target: the home's database with --apply; a throwaway copy of it for a dry run, so
	// a dry run writes nothing to the home (not even migrations).
	target := cfg.DBPath()
	if !*apply {
		tmp, err := os.MkdirTemp("", "jarvisd-import-dryrun-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(tmp)
		copyPath := filepath.Join(tmp, "jarvis.db")
		if _, err := os.Stat(target); err == nil {
			if err := snapshotReadOnly(ctx, target, copyPath); err != nil {
				return fmt.Errorf("copy %s for the dry run: %w", target, err)
			}
		}
		target = copyPath
	} else if err := secureHome(cfg.Home); err != nil {
		return err
	}
	d, err := migratedDB(ctx, cfg, target)
	if err != nil {
		return err
	}
	defer d.Close()

	rep, runErr := legacyimport.Run(ctx, d, src, legacyimport.Options{
		Apply: *apply, AcceptHeads: accept, Settings: importSettingDefs(),
		PromptProvider: func(name string) error { _, err := prompts.Lookup(name); return err },
	})
	rep.Write(stdout)
	if rep.Committed {
		after(ctx, cfg, d, stdout)
	}
	lp := *logPath
	if lp == "" && rep.Committed {
		lp = filepath.Join(cfg.Home, "import-legacy-"+time.Now().UTC().Format("20060102T150405Z")+".log")
	}
	if lp != "" {
		if err := writeImportLog(lp, rep); err != nil {
			fmt.Fprintf(stdout, "could not write the import log: %v\n", err)
		} else {
			fmt.Fprintf(stdout, "import log (row by row, holds emails; keep it private): %s\n", lp)
		}
	}
	if errors.Is(runErr, legacyimport.ErrRefused) {
		return errImportRefused
	}
	return runErr
}

// after checks what an applied import means for first-run setup.
func after(ctx context.Context, cfg config.Config, d *db.DB, stdout io.Writer) {
	var supers int
	_ = d.Read.QueryRowContext(ctx, `SELECT COUNT(*) FROM auth_users WHERE is_superuser = 1 AND is_active = 1`).Scan(&supers)
	if supers == 0 {
		fmt.Fprintln(stdout, "\nNext: start jarvisd; no superuser came over, so the setup link creates the admin account.")
		return
	}
	// An imported superuser closes first-run setup: jarvisd removes the token at its next start,
	// but `jarvisd setup-link` reads the file before that, so a stale one would print a dead link.
	if err := os.Remove(authmod.SetupTokenPath(cfg.Home)); err == nil {
		fmt.Fprintln(stdout, "\nremoved the old setup token: setup is closed now that an admin account exists")
	} else if !errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(stdout, "\ncould not remove the old setup token %s: %v\n", authmod.SetupTokenPath(cfg.Home), err)
	}
	fmt.Fprintln(stdout, "\nNext: start jarvisd, open the admin and sign in with a legacy admin account (its old password);")
	fmt.Fprintln(stdout, "the setup wizard resumes at Hardware, then models and privacy choices.")
}

// refuseWhileRunning refuses when jarvisd (an installed service, or any process answering on
// this home's config port) is running: the import must land before its first start.
func refuseWhileRunning(ctx context.Context, cfg config.Config) error {
	for _, user := range []bool{false, true} {
		_, st := installedService(ctx, user)
		if st.Installed && st.Running && (st.Home == "" || filepath.Clean(st.Home) == filepath.Clean(cfg.Home)) {
			return fmt.Errorf("the jarvisd service is running on %s: stop it first (jarvisd service stop%s); import-legacy runs before jarvisd's first start",
				cfg.Home, map[bool]string{true: " --user"}[user])
		}
	}
	// A home without a database never ran jarvisd (a dry run into a scratch home next to a
	// running legacy stack, whose config-service answers on the same port, is fine).
	if _, err := os.Stat(cfg.DBPath()); err != nil {
		return nil
	}
	if url := healthURL(cfg.Home); probeHealth(ctx, url) == "" {
		return fmt.Errorf("something answers on %s (jarvisd, or the legacy config-service): stop it first; import-legacy runs with jarvisd stopped", url)
	}
	return nil
}

// snapshotReadOnly copies a SQLite database through a read-only connection (VACUUM INTO).
func snapshotReadOnly(ctx context.Context, src, dst string) error {
	c, err := sql.Open("sqlite", "file:"+filepath.ToSlash(src)+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	defer c.Close()
	_, err = c.ExecContext(ctx, "VACUUM INTO ?", dst)
	return err
}

// migratedDB opens path and applies every migration jarvisd's start would, plus the settings
// tables the import writes (modules create those when they register).
func migratedDB(ctx context.Context, cfg config.Config, path string) (*db.DB, error) {
	d, err := db.Open(ctx, path)
	if err != nil {
		return nil, err
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	q := queue.New(d, log)
	r := &module.Runner{Deps: module.Deps{Config: cfg, DB: d, Log: log, Queue: q, Scheduler: scheduler.New(d, q, log)}, Modules: modules()}
	if err := r.Migrate(ctx); err != nil {
		d.Close()
		return nil, err
	}
	for name, defs := range importSettingDefs() {
		svc, err := settings.New(d, name, defs, log)
		if err == nil {
			err = svc.Migrate(ctx)
		}
		if err != nil {
			d.Close()
			return nil, fmt.Errorf("%s settings: %w", name, err)
		}
	}
	return d, nil
}

func writeImportLog(path string, rep *legacyimport.Report) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	rep.Write(f)
	fmt.Fprintln(f, "\nrows:")
	for _, l := range rep.Log {
		fmt.Fprintln(f, l)
	}
	return f.Close()
}

func keyValues(vals []string, flagName, form string) (map[string]string, error) {
	m := map[string]string{}
	for _, v := range vals {
		k, val, ok := strings.Cut(v, "=")
		if !ok || strings.TrimSpace(k) == "" || strings.TrimSpace(val) == "" {
			return nil, fmt.Errorf("%s takes %s, got %q", flagName, form, v)
		}
		m[strings.TrimSpace(k)] = strings.TrimSpace(val)
	}
	return m, nil
}
