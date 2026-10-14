package install

import (
	"context"
	"os"
	"strconv"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/agent/appid"
	"github.com/alexberardi/jarvis-server/internal/agent/deploy"
)

// Installed reports whether `install` has set this user up: the per-user copy (Linux), a login
// item (macOS), the Run value (Windows).
func Installed(e Env, s Sys) bool {
	switch e.GOOS {
	case "linux":
		_, err := os.Stat(e.linux().bin)
		return err == nil
	case "darwin":
		p := e.darwin()
		if _, err := os.Stat(p.plist); err == nil {
			return true
		}
		if s.Login != nil && p.bundle != "" {
			ok, _ := s.Login.Registered(BundlePlistName)
			return ok
		}
	case "windows":
		if s.Reg != nil {
			_, ok, _ := s.Reg.GetString(RunKey, displayName)
			return ok
		}
	}
	return false
}

// SetsUpOnFirstLaunch reports whether `start` installs first when this user isn't set up: a
// packaged copy (the .deb's /usr/lib/jarvis-agent, Q5; the app on macOS, whose dmg runs no
// installer). A copy in the user's home (a dev build) just starts; Windows' installer runs
// `install` itself.
func SetsUpOnFirstLaunch(e Env) bool {
	switch e.GOOS {
	case "linux":
		// Either separator: tests compute a Linux plan on every host.
		return !strings.HasPrefix(e.Exe, e.Home+"/") && !strings.HasPrefix(e.Exe, e.Home+`\`)
	case "darwin":
		return BundleOf(e.Exe) != ""
	}
	return false
}

// Start starts the agent the way the installed login item would: through systemd when its unit
// is installed and the graphical session's target is up (then systemd has the display and
// supervises it), through launchd when the LaunchAgent is loaded, and otherwise as a detached
// process of this session. login is the autostart entry's call at login: where systemd
// already starts the agent with the session it does nothing.
func Start(ctx context.Context, e Env, s Sys, login bool) error {
	switch e.GOOS {
	case "linux":
		p := e.linux()
		bin := p.bin
		if _, err := os.Stat(bin); err != nil {
			bin = e.Exe // not installed: this copy
		}
		if _, err := os.Stat(p.unit); err == nil &&
			s.Run(ctx, "systemctl", "--user", "is-active", "--quiet", "graphical-session.target") == nil {
			if login {
				return nil
			}
			return s.Run(ctx, "systemctl", "--user", "start", deploy.SystemdUnitName)
		}
		return s.Spawn(bin, "run")
	case "darwin":
		if s.Run(ctx, "launchctl", "kickstart", "gui/"+strconv.Itoa(e.UID)+"/"+appid.ID) == nil {
			return nil
		}
		return s.Spawn(e.Exe, "run")
	}
	return s.Spawn(e.Exe, "run")
}

// Restart restarts the agent so it reads its settings again (`jarvis-agent restart`). The
// running agent is first asked to quit over its control socket: a clean exit, which no
// supervisor restarts by itself. Then the supervisor starts it the way the login item does:
// `systemctl --user restart` where the unit runs it, `launchctl kickstart -k` under launchd
// (the plain LaunchAgent and the app's SMAppService one share the label). The kill in those
// covers an agent that didn't quit. Otherwise (no unit or graphical session, no LaunchAgent,
// Windows' Run key, which supervises nothing) it is a detached run, as `start` does.
func Restart(ctx context.Context, e Env, s Sys) error {
	var quitErr error
	if s.Quit != nil {
		quitErr = s.Quit()
	}
	switch e.GOOS {
	case "linux":
		if _, err := os.Stat(e.linux().unit); err == nil &&
			s.Run(ctx, "systemctl", "--user", "is-active", "--quiet", "graphical-session.target") == nil {
			return s.Run(ctx, "systemctl", "--user", "restart", deploy.SystemdUnitName)
		}
	case "darwin":
		if s.Run(ctx, "launchctl", "kickstart", "-k", "gui/"+strconv.Itoa(e.UID)+"/"+appid.ID) == nil {
			return nil
		}
	}
	if quitErr != nil {
		// Nothing would stop it, and a second copy would find it running and leave.
		return quitErr
	}
	return Start(ctx, e, s, false)
}
