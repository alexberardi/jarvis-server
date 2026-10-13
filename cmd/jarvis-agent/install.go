package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/agent/appid"
	"github.com/alexberardi/jarvis-server/internal/agent/browser"
	"github.com/alexberardi/jarvis-server/internal/agent/install"
	"github.com/alexberardi/jarvis-server/internal/agent/portal"
)

// installEnv describes this machine for install's plan.
func installEnv() (install.Env, error) {
	exe, err := agentExe()
	if err != nil {
		return install.Env{}, err
	}
	if strings.Contains(exe, string(filepath.Separator)+"go-build") {
		return install.Env{}, fmt.Errorf("%s is a temporary `go run` binary; build jarvis-agent first", exe)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return install.Env{}, err
	}
	getenv := os.Getenv
	if runtime.GOOS == "linux" {
		desktop := xdgUserDir(home, "XDG_DESKTOP_DIR")
		getenv = func(k string) string {
			if k == "XDG_DESKTOP_DIR" && os.Getenv(k) == "" {
				return desktop
			}
			return os.Getenv(k)
		}
	}
	return install.Env{GOOS: runtime.GOOS, Home: home, UID: os.Getuid(), Exe: exe, Getenv: getenv,
		SMAppService: install.SMAppServiceAvailable()}, nil
}

// xdgUserDir reads one entry of user-dirs.dirs (XDG_DESKTOP_DIR="$HOME/Desktop"), "" if absent.
func xdgUserDir(home, key string) string {
	cfg := os.Getenv("XDG_CONFIG_HOME")
	if cfg == "" {
		cfg = filepath.Join(home, ".config")
	}
	f, err := os.Open(filepath.Join(cfg, "user-dirs.dirs"))
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !ok || k != key {
			continue
		}
		if s, err := strconv.Unquote(v); err == nil {
			v = s
		}
		return strings.Replace(v, "$HOME", home, 1)
	}
	return ""
}

// browserHosts registers the native-messaging host for the browsers found (internal/agent/browser).
type browserHosts struct{ env browser.Env }

func (b browserHosts) Register(exe string) error {
	var errs []error
	for _, r := range browser.Register(b.env, exe, false, browser.NativeRegistry()) {
		if r.Err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", r.Target.Label, r.Err))
		}
	}
	return errors.Join(errs...)
}

func (b browserHosts) Unregister() error {
	var errs []error
	for _, r := range browser.Unregister(b.env, browser.NativeRegistry()) {
		if r.Err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", r.Target.Label, r.Err))
		} else if r.Done {
			pruneEmpty(filepath.Dir(r.Path), b.env.Home) // the NativeMessagingHosts dir, if that left it empty
		}
	}
	return errors.Join(errs...)
}

// pruneEmpty removes dir and its parents while they are empty, stopping below home.
func pruneEmpty(dir, home string) {
	home = filepath.Clean(home)
	for ; dir != home && strings.HasPrefix(dir, home+string(filepath.Separator)); dir = filepath.Dir(dir) {
		if os.Remove(dir) != nil {
			return
		}
	}
}

// installSys is install.Sys on this machine.
func installSys(w io.Writer) (install.Sys, error) {
	benv, err := browserEnv()
	if err != nil {
		return install.Sys{}, err
	}
	logFile := ""
	if cache, err := os.UserCacheDir(); err == nil {
		logFile = filepath.Join(cache, "jarvis-agent", "agent.log")
	}
	s := install.NativeSys(w, logFile)
	s.Web = browserHosts{benv}
	s.Portal = func() (bool, error) { return portal.MigrateLegacy(os.Getenv, nil) }
	return s, nil
}

func installCmd(ctx context.Context, args []string, w io.Writer) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.SetOutput(w)
	noStart := fs.Bool("no-start", false, "don't start the agent now (it still starts at the next login)")
	noBrowser := fs.Bool("no-browser", false, "don't register the browsers' native-messaging host")
	shortcut := fs.Bool("desktop-shortcut", runtime.GOOS == "windows", "also put a shortcut on the desktop (Windows: on by default)")
	loginItem := fs.String("login-item", install.LoginAuto, "macOS: auto (SMAppService when run from the app, else a LaunchAgent), smappservice or launchagent")
	filesOnly := fs.Bool("files-only", false, "write the files only: no service manager, LaunchServices or start (packaging checks)")
	dryRun := fs.Bool("dry-run", false, "print what would be done, and do nothing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	e, err := installEnv()
	if err != nil {
		return err
	}
	ops, err := install.Plan(e, install.Options{Browser: !*noBrowser, DesktopShortcut: *shortcut, Start: !*noStart,
		FilesOnly: *filesOnly, LoginItem: *loginItem})
	if err != nil {
		return err
	}
	if *dryRun {
		fmt.Fprint(w, install.Dump(e, ops))
		return nil
	}
	s, err := installSys(w)
	if err != nil {
		return err
	}
	if err := install.Apply(ctx, ops, s, install.ApplyOptions{FilesOnly: *filesOnly}); err != nil {
		return err
	}
	fmt.Fprintf(w, "Installed %s for this user: it starts at login", appid.DisplayName)
	switch {
	case *filesOnly:
		fmt.Fprintln(w, " (files only: nothing was started or registered with the system).")
	case *noStart:
		fmt.Fprintln(w, ".")
	default:
		fmt.Fprintln(w, ", and is starting now.")
	}
	if runtime.GOOS == "linux" {
		fmt.Fprintf(w, "The agent runs from %s; the app menu has %q.\n", install.LinuxBin(e), appid.DisplayName)
	}
	return nil
}

func uninstallCmd(ctx context.Context, args []string, w io.Writer) error {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	fs.SetOutput(w)
	purge := fs.Bool("purge", false, "also delete the agent's data: its sign-in to Jarvis, settings, caches and logs")
	filesOnly := fs.Bool("files-only", false, "remove the files only: leave the service manager and processes alone")
	dryRun := fs.Bool("dry-run", false, "print what would be done, and do nothing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	e, err := installEnv()
	if err != nil {
		return err
	}
	ops, err := install.UninstallPlan(e, install.UninstallOptions{Purge: *purge, FilesOnly: *filesOnly})
	if err != nil {
		return err
	}
	if *dryRun {
		fmt.Fprint(w, install.Dump(e, ops))
		return nil
	}
	s, err := installSys(w)
	if err != nil {
		return err
	}
	err = install.Apply(ctx, ops, s, install.ApplyOptions{FilesOnly: *filesOnly, BestEffort: true})
	if *purge {
		fmt.Fprintf(w, "Removed %s and its data.\n", appid.DisplayName)
	} else {
		fmt.Fprintf(w, "Removed %s. Its sign-in and settings are kept (uninstall --purge deletes them).\n", appid.DisplayName)
	}
	return err
}
