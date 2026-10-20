//go:build linux

package computer

import (
	"context"
	"errors"
	"log/slog"
	"os"

	"github.com/alexberardi/jarvis-server/internal/agent/atspi"
	"github.com/alexberardi/jarvis-server/internal/agent/dictate"
	"github.com/alexberardi/jarvis-server/internal/agent/evdev"
	"github.com/alexberardi/jarvis-server/internal/agent/screen"
	"github.com/alexberardi/jarvis-server/internal/agent/vkbd"
	"github.com/alexberardi/jarvis-server/internal/agent/x11"
)

// NewDesktopSource is the recorder's desktop mode on Linux, with keyframes of the screen from
// shots (nil: none) kept in dir: in a Wayland session the input devices, the compositor and
// AT-SPI (Hyprland and sway; other compositors refuse at start), else X11 (RECORD and AT-SPI).
func NewDesktopSource(dir string, shots screen.Capturer, log *slog.Logger) Source {
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		return newWaylandSource(dir, wlSystem{
			Windows: &dictate.LinuxWindows{X: dictate.OSExec{}},
			Devices: func() ([]evdev.Device, error) { return evdev.Devices(evdev.SysRoot, evdev.DevRoot) },
			Open:    evdev.Open,
			Layout:  (&vkbd.Keyboard{}).Layout,
			Access:  atspi.Available,
			Focused: atspi.Focused,
			Shot:    shooter(shots),
		}, log)
	}
	return newX11Source(dir, &x11.Display{}, x11Access{ElementAt: atspi.ElementAt, Focused: atspi.Focused}, keyframer(shots), log)
}

// shooter is a Wayland keyframe's capture: the focused monitor (nil without a capturer).
func shooter(c screen.Capturer) func(context.Context) (screen.Shot, error) {
	if c == nil {
		return nil
	}
	return func(ctx context.Context) (screen.Shot, error) {
		if ok, why := c.Available(ctx); !ok {
			return screen.Shot{}, errors.New(why)
		}
		return c.Capture(ctx, screen.ScopeScreen, "")
	}
}
