package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The .deb's postinst opens the setup link in the browser of the person at the screen
// (01-no-cli-plan §2.1 "First run"). A package manager runs postinst as root and outside any
// desktop session: App Center and GNOME Software through PackageKit's daemon, or `sudo apt` in
// a terminal. So install-from-app --open finds the active, local, graphical logind session and
// asks that user's own systemd manager to run xdg-open (systemd-run --user, as the user, on the
// session's bus). The browser then lives in the user's session, not in packagekitd's cgroup,
// which systemd stops (with everything in it) when the daemon idles out. Without a user manager
// it falls back to xdg-open as the user, detached. Best effort: no session (a headless box, an
// SSH login) or any failure only means the browser doesn't open; the link and QR code are
// printed either way.

// desktopSession is a logind session someone is sitting at.
type desktopSession struct {
	ID, User, Type, Display string
	UID                     int
}

// desktopRunner runs a command and returns its standard output.
type desktopRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

// desktopOpener holds what opening touches, swappable for tests.
type desktopOpener struct {
	run        desktopRunner
	runtimeDir func(uid int) string                   // /run/user/<uid>
	glob       func(pattern string) ([]string, error) // filepath.Glob
}

func newDesktopOpener() desktopOpener {
	return desktopOpener{
		run:        desktopExec,
		runtimeDir: func(uid int) string { return path.Join("/run/user", strconv.Itoa(uid)) },
		glob:       filepath.Glob,
	}
}

func desktopExec(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).Output()
}

// activeDesktopSession is the active, local, graphical session of a user, if there is one.
func (d desktopOpener) activeDesktopSession(ctx context.Context) (desktopSession, bool) {
	out, err := d.run(ctx, "loginctl", "list-sessions", "--no-legend")
	if err != nil {
		return desktopSession{}, false
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		props, err := d.run(ctx, "loginctl", "show-session", f[0],
			"--property=Active", "--property=Remote", "--property=Class", "--property=Type",
			"--property=Name", "--property=User", "--property=Display")
		if err != nil {
			continue
		}
		if s, ok := parseDesktopSession(f[0], string(props)); ok {
			return s, true
		}
	}
	return desktopSession{}, false
}

// parseDesktopSession reads `loginctl show-session` output (Key=Value lines): a session
// counts when it is active, not remote, a user's (not the greeter's), on a display server.
func parseDesktopSession(id, props string) (desktopSession, bool) {
	kv := map[string]string{}
	for _, line := range strings.Split(props, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			kv[k] = v
		}
	}
	uid, err := strconv.Atoi(kv["User"])
	if err != nil || uid == 0 || kv["Name"] == "" || kv["Active"] != "yes" || kv["Remote"] == "yes" || kv["Class"] != "user" {
		return desktopSession{}, false
	}
	switch kv["Type"] {
	case "x11", "wayland", "mir":
	default:
		return desktopSession{}, false
	}
	return desktopSession{ID: id, User: kv["Name"], UID: uid, Type: kv["Type"], Display: kv["Display"]}, true
}

// sessionEnv is what a program needs to reach the session's bus and display.
func (d desktopOpener) sessionEnv(s desktopSession) (bus, display []string) {
	rt := d.runtimeDir(s.UID)
	bus = []string{"XDG_RUNTIME_DIR=" + rt, "DBUS_SESSION_BUS_ADDRESS=unix:path=" + path.Join(rt, "bus")}
	if s.Display != "" {
		display = append(display, "DISPLAY="+s.Display)
	}
	if s.Type == "wayland" {
		if socks, _ := d.glob(path.Join(rt, "wayland-*")); len(socks) > 0 {
			for _, p := range socks {
				if !strings.HasSuffix(p, ".lock") {
					display = append(display, "WAYLAND_DISPLAY="+path.Base(p))
					break
				}
			}
		}
	}
	return bus, display
}

// openArgv is the command that opens url in s: xdg-open in a transient unit of the user's
// own systemd manager. KillMode=process leaves the browser running when xdg-open exits; the
// description (not the URL, which holds the setup token) is what the user's journal records.
func (d desktopOpener) openArgv(s desktopSession, url string) []string {
	bus, display := d.sessionEnv(s)
	argv := append([]string{"runuser", "-u", s.User, "--", "env"}, bus...)
	argv = append(argv, "systemd-run", "--user", "--collect", "--quiet",
		"--description=Open the Jarvis Automation setup page", "--property=KillMode=process")
	for _, e := range display {
		argv = append(argv, "--setenv="+e)
	}
	return append(argv, "xdg-open", url)
}

// fallbackArgv runs xdg-open as the user directly, in a session of its own.
func (d desktopOpener) fallbackArgv(s desktopSession, url string) []string {
	bus, display := d.sessionEnv(s)
	argv := append([]string{"runuser", "-u", s.User, "--", "env"}, bus...)
	argv = append(argv, display...)
	return append(argv, "setsid", "xdg-open", url)
}

// open opens url in the active desktop session's browser and says whose; false when there
// is no such session or nothing worked.
func (d desktopOpener) open(ctx context.Context, url string, w io.Writer) bool {
	s, ok := d.activeDesktopSession(ctx)
	if !ok {
		return false
	}
	var errs []error
	for _, argv := range [][]string{d.openArgv(s, url), d.fallbackArgv(s, url)} {
		_, err := d.run(ctx, argv[0], argv[1:]...)
		if err == nil {
			fmt.Fprintf(w, "opened the setup page in %s's browser\n", s.User)
			return true
		}
		errs = append(errs, err)
	}
	fmt.Fprintf(w, "couldn't open a browser for %s (%v); open the link above\n", s.User, errors.Join(errs...))
	return false
}

// isSetupLink reports whether url is the first-run setup link (not the admin URL a set-up
// install prints): only that one is opened, so an upgrade doesn't pop a browser up.
func isSetupLink(url string) bool { return strings.Contains(url, "setup#token=") }
