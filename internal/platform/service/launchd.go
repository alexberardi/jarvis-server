//go:build unix

package service

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	launchdPlist = "/Library/LaunchDaemons/" + LaunchdLabel + ".plist"
	helperPlist  = "/Library/LaunchDaemons/" + HelperLabel + ".plist"
)

// launchd manages the LaunchDaemon (00-installers §2.2): it runs before anyone logs in, as
// the installing user, with data in that user's ~/.jarvisd.
type launchd struct {
	out       io.Writer
	run       runFunc
	plistPath string
	// helperPath is the updater LaunchDaemon's plist (ID11).
	helperPath string

	euid       func() int
	getenv     func(string) string
	lookupUser func(string) (*user.User, error)
	chown      func(string, int, int) error
	// settle is how long to wait for a booted-out job to go away.
	settle time.Duration
}

func newLaunchd(out io.Writer) *launchd {
	return &launchd{
		out: out, run: execRun, plistPath: launchdPlist, helperPath: helperPlist,
		euid: os.Geteuid, getenv: os.Getenv, lookupUser: user.Lookup, chown: os.Lchown,
		settle: 30 * time.Second,
	}
}

func (l *launchd) Kind() Kind { return Launchd }

const (
	target       = "system/" + LaunchdLabel
	helperTarget = "system/" + HelperLabel
)

func (l *launchd) loaded(ctx context.Context) bool {
	_, err := l.run(ctx, "launchctl", "print", target)
	return err == nil
}

func (l *launchd) Install(ctx context.Context, o InstallOptions) error {
	if o.User {
		return errors.New("--user is for Linux; on macOS jarvisd installs as a LaunchDaemon running as you")
	}
	if l.euid() != 0 {
		return errors.New("a LaunchDaemon needs root: run `sudo jarvisd service install`")
	}
	runAs := o.RunAs
	if runAs == "" {
		runAs = l.getenv("SUDO_USER")
	}
	if runAs == "" || runAs == "root" {
		return errors.New("name the account jarvisd runs as: `sudo jarvisd service install --run-as <user>` (run with sudo from that account, it is picked up automatically)")
	}
	u, err := l.lookupUser(runAs)
	if err != nil {
		return fmt.Errorf("account %s: %w", runAs, err)
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	if o.Home == "" {
		o.Home = filepath.Join(u.HomeDir, ".jarvisd")
	}
	if err := makeHome(o.Home); err != nil {
		return err
	}
	logs := filepath.Join(o.Home, "logs")
	// The updater's request queue must exist for launchd to watch it.
	updates := filepath.Join(o.Home, "updates")
	requests := filepath.Join(updates, "requests")
	for _, d := range []string{logs, requests} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	envFile := filepath.Join(o.Home, "jarvisd.env")
	if _, err := writeEnvTemplate(envFile, 0o600); err != nil {
		return err
	}
	// Everything created as root here belongs to the account jarvisd runs as.
	for _, p := range []string{o.Home, logs, updates, requests, envFile} {
		if err := l.chown(p, uid, gid); err != nil {
			return err
		}
	}
	plist, err := RenderLaunchd(Plist{Binary: o.Binary, Home: o.Home, UserName: runAs, UserHome: u.HomeDir})
	if err != nil {
		return err
	}
	helper, err := RenderLaunchdHelper(HelperPlist{Binary: o.Binary, Home: o.Home, UserName: runAs})
	if err != nil {
		return err
	}
	if l.loaded(ctx) {
		if err := l.bootout(ctx); err != nil {
			return err
		}
	}
	if l.helperLoaded(ctx) {
		if err := l.bootoutHelper(ctx); err != nil {
			return err
		}
	}
	if err := writeFile(l.plistPath, plist, 0o644); err != nil {
		return err
	}
	if err := writeFile(l.helperPath, helper, 0o644); err != nil {
		return err
	}
	printf(l.out, "wrote %s (runs %s as %s, data in %s, log %s)\n", l.plistPath, o.Binary, runAs, o.Home, LogPath(o.Home))
	printf(l.out, "wrote %s (the self-update helper, runs as root, log %s)\n", l.helperPath, HelperLog)
	if o.NoStart {
		return nil
	}
	return l.Start(ctx)
}

// bootout unloads the job and waits for it to be gone, so a following bootstrap doesn't
// fail with "Input/output error".
func (l *launchd) bootout(ctx context.Context) error {
	if _, err := l.run(ctx, "launchctl", "bootout", target); err != nil && l.loaded(ctx) {
		return err
	}
	if !poll(ctx, l.settle, 200*time.Millisecond, func() bool { return !l.loaded(ctx) }) {
		return fmt.Errorf("launchd still has %s loaded after bootout", LaunchdLabel)
	}
	return nil
}

// helperLoaded reports whether launchd has the updater job.
func (l *launchd) helperLoaded(ctx context.Context) bool {
	_, err := l.run(ctx, "launchctl", "print", helperTarget)
	return err == nil
}

func (l *launchd) bootoutHelper(ctx context.Context) error {
	if _, err := l.run(ctx, "launchctl", "bootout", helperTarget); err != nil && l.helperLoaded(ctx) {
		return err
	}
	if !poll(ctx, l.settle, 200*time.Millisecond, func() bool { return !l.helperLoaded(ctx) }) {
		return fmt.Errorf("launchd still has %s loaded after bootout", HelperLabel)
	}
	return nil
}

// loadHelper bootstraps the updater job if its plist exists and it isn't loaded (RunAtLoad
// makes it look for pending work once).
func (l *launchd) loadHelper(ctx context.Context) error {
	if _, err := os.Stat(l.helperPath); err != nil || l.helperLoaded(ctx) {
		return nil
	}
	_, err := l.run(ctx, "launchctl", "bootstrap", "system", l.helperPath)
	return err
}

func (l *launchd) Uninstall(ctx context.Context) error {
	if _, err := os.Stat(l.plistPath); errors.Is(err, fs.ErrNotExist) {
		return ErrNotInstalled
	}
	home := l.InstalledHome()
	if l.helperLoaded(ctx) {
		if err := l.bootoutHelper(ctx); err != nil {
			return err
		}
	}
	if err := os.Remove(l.helperPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if l.loaded(ctx) {
		if err := l.bootout(ctx); err != nil {
			return err
		}
	}
	if err := os.Remove(l.plistPath); err != nil {
		return err
	}
	printf(l.out, "removed %s; data kept in %s\n", l.plistPath, home)
	return nil
}

// Start loads the job if needed and starts it. bootstrap alone may leave a job pending
// ("speculative" spawn) without running it (I0 finding 4), hence the kickstart.
func (l *launchd) Start(ctx context.Context) error {
	if _, err := os.Stat(l.plistPath); errors.Is(err, fs.ErrNotExist) {
		return ErrNotInstalled
	}
	if !l.loaded(ctx) {
		if _, err := l.run(ctx, "launchctl", "bootstrap", "system", l.plistPath); err != nil {
			return err
		}
	}
	if _, err := l.run(ctx, "launchctl", "enable", target); err != nil {
		return err
	}
	if err := l.loadHelper(ctx); err != nil {
		return err
	}
	_, err := l.run(ctx, "launchctl", "kickstart", target)
	return err
}

// Stop unloads the job: with KeepAlive, killing it would only restart it. It loads again at
// boot (or with `jarvisd service start`).
func (l *launchd) Stop(ctx context.Context) error {
	if _, err := os.Stat(l.plistPath); errors.Is(err, fs.ErrNotExist) {
		return ErrNotInstalled
	}
	if !l.loaded(ctx) {
		return nil
	}
	return l.bootout(ctx)
}

func (l *launchd) Restart(ctx context.Context) error {
	if _, err := os.Stat(l.plistPath); errors.Is(err, fs.ErrNotExist) {
		return ErrNotInstalled
	}
	if !l.loaded(ctx) {
		return l.Start(ctx)
	}
	_, err := l.run(ctx, "launchctl", "kickstart", "-k", target)
	return err
}

var (
	launchdState = regexp.MustCompile(`(?m)^\s*state = (.+)$`)
	launchdPID   = regexp.MustCompile(`(?m)^\s*pid = (\d+)$`)
	launchdExit  = regexp.MustCompile(`(?m)^\s*last exit code = (.+)$`)
)

func (l *launchd) Status(ctx context.Context) (Status, error) {
	st := Status{Kind: Launchd, State: "not installed", Home: l.InstalledHome()}
	if _, err := os.Stat(l.plistPath); errors.Is(err, fs.ErrNotExist) {
		return st, nil
	}
	st.Installed = true
	out, err := l.run(ctx, "launchctl", "print", target)
	if err != nil {
		st.State = "not loaded"
		return st, nil
	}
	if m := launchdState.FindSubmatch(out); m != nil {
		st.State = strings.TrimSpace(string(m[1]))
	}
	st.Running = st.State == "running"
	if m := launchdPID.FindSubmatch(out); m != nil {
		st.PID, _ = strconv.Atoi(string(m[1]))
	}
	st.Detail = l.plistPath
	if m := launchdExit.FindSubmatch(out); m != nil {
		st.Detail += ", last exit " + strings.TrimSpace(string(m[1]))
	}
	st.UpgradeHelper = l.helperStatus(ctx)
	return st, nil
}

// helperStatus describes the updater LaunchDaemon.
func (l *launchd) helperStatus(ctx context.Context) string {
	if _, err := os.Stat(l.helperPath); err != nil {
		return "none: run `sudo jarvisd service install` again to add it"
	}
	if !l.helperLoaded(ctx) {
		return HelperLabel + " (not loaded)"
	}
	return HelperLabel + " (loaded; log " + HelperLog + ")"
}

var plistString = regexp.MustCompile(`<string>([^<]*)</string>`)

func (l *launchd) InstalledHome() string {
	b, err := os.ReadFile(l.plistPath)
	if err != nil {
		return ""
	}
	var args []string
	for _, m := range plistString.FindAllSubmatch(b, -1) {
		args = append(args, html.UnescapeString(string(m[1])))
	}
	return homeFromArgs(args)
}

func (l *launchd) PurgePlan(home string) PurgePlan { return PurgePlan{Home: home} }

func (l *launchd) Purge(_ context.Context, p PurgePlan) error { return purgeFiles(p) }
