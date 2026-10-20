//go:build linux

package input

import (
	"os"

	"github.com/alexberardi/jarvis-server/internal/agent/idlenotify"
	"github.com/alexberardi/jarvis-server/internal/agent/x11"
)

// NativeMonitor is the compositor's input-idle notification in a Wayland session (wlroots:
// Hyprland, sway; an error from Since where the compositor lacks ext-idle-notify), else the X
// server's idle time (MIT-SCREEN-SAVER). Both count our own injected events as input too; the
// computer guard tells them apart by its injection windows.
func NativeMonitor() Monitor {
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		return &idlenotify.Monitor{}
	}
	return &x11.Idle{D: &x11.Display{}}
}
