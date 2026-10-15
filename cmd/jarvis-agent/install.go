package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/appid"
	"github.com/alexberardi/jarvis-server/internal/agent/browser"
	"github.com/alexberardi/jarvis-server/internal/agent/control"
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
	e := install.Env{GOOS: runtime.GOOS, Home: home, UID: os.Getuid(), Exe: exe, Getenv: getenv,
		SMAppService: install.SMAppServiceAvailable()}
	if runtime.GOOS == "windows" {
		if l := filepath.Join(filepath.Dir(exe), install.LauncherName); fileExists(l) {
			e.Launcher = l
		}
	}
	return e, nil
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
			_ = os.Remove(filepath.Dir(r.Path)) // the NativeMessagingHosts dir, if that left it empty
		}
	}
	return errors.Join(errs...)
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
	pause := func() { time.Sleep(100 * time.Millisecond) }
	s.Quit = func() error {
		if err := control.Send(controlDial, control.Quit); err != nil {
			if errors.Is(err, control.ErrNotRunning) {
				return nil
			}
			return err
		}
		if !control.WaitGone(context.Background(), controlDial, 100, pause) {
			return errors.New("the running agent didn't stop")
		}
		return nil
	}
	if enrolled() {
		// Not set up, the agent exits at once: nothing would answer.
		s.Announce = func() error {
			return control.WaitSend(context.Background(), controlDial, control.ShowRunning, 100, pause)
		}
	}
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
	bin := fs.String("bin", "", "first put this binary at `path` (keeping a newer one there), then install that one (the Windows installer)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := noStrayArgs(fs); err != nil {
		return err
	}
	e, err := installEnv()
	if err != nil {
		return err
	}
	if *bin != "" && !sameFile(e.Exe, *bin) {
		if *dryRun {
			return fmt.Errorf("--dry-run with --bin: run it from %s", *bin)
		}
		return installAt(ctx, e.Exe, *bin, args, w)
	}
	start, setup := installStarts(*noStart, *filesOnly, enrolled())
	ops, err := install.Plan(e, install.Options{Browser: !*noBrowser, DesktopShortcut: *shortcut, Start: start,
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
	case setup || *noStart:
		fmt.Fprintln(w, ".")
	default:
		fmt.Fprintln(w, ", and is starting now.")
	}
	if runtime.GOOS == "linux" {
		fmt.Fprintf(w, "The agent runs from %s; the app menu has %q.\n", install.LinuxBin(e), appid.DisplayName)
	}
	if setup {
		// The agent would only exit (not set up): the setup page instead, as `start` does. It
		// starts the agent under its login item once this computer is added.
		fmt.Fprintln(w, "This computer isn't set up yet: opening the setup page in your browser (Ctrl-C closes it).")
		return firstRun(ctx)
	}
	return nil
}

// installAt is `install --bin dst` run from another copy (the Windows installer's payload): put
// this binary at dst unless a newer one is there (install.Place), then hand over to the binary
// at dst with the same arguments, so the login item, shortcuts and URL handler name it.
func installAt(ctx context.Context, self, dst string, args []string, w io.Writer) error {
	if !filepath.IsAbs(dst) {
		return errors.New("install: --bin must be an absolute path")
	}
	stop := func() {
		if s, err := installSys(io.Discard); err == nil && s.Quit != nil {
			_ = s.Quit() // the running agent holds dst open; install starts it again
		}
	}
	if err := install.Place(self, dst, version, func(p string) (string, error) { return programVersion(ctx, p) }, stop, w); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, dst, append([]string{"install"}, args...)...)
	cmd.Stdout, cmd.Stderr = w, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s install: %w", dst, err)
	}
	return nil
}

// programVersion is what `<bin> version` prints.
func programVersion(ctx context.Context, bin string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "version").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// sameFile reports whether a and b are the same existing file.
func sameFile(a, b string) bool {
	sa, err := os.Stat(a)
	if err != nil {
		return false
	}
	sb, err := os.Stat(b)
	return err == nil && os.SameFile(sa, sb)
}

// installStarts decides install's last step: start the agent (start), or, on a computer that
// isn't set up, open the setup page instead (setup), which starts the agent once it is.
func installStarts(noStart, filesOnly, enrolled bool) (start, setup bool) {
	if noStart || filesOnly {
		return !noStart, false
	}
	if !enrolled {
		return false, true
	}
	return true, false
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
	if err := noStrayArgs(fs); err != nil {
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
