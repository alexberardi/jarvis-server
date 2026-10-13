package install

import (
	"context"
	"os"
	"strconv"

	"github.com/alexberardi/jarvis-server/internal/agent/appid"
	"github.com/alexberardi/jarvis-server/internal/agent/deploy"
)

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
