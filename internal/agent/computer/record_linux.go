//go:build linux

package computer

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/atspi"
	"github.com/alexberardi/jarvis-server/internal/agent/dictate"
	"github.com/alexberardi/jarvis-server/internal/agent/evdev"
	"github.com/alexberardi/jarvis-server/internal/agent/screen"
	"github.com/alexberardi/jarvis-server/internal/agent/vkbd"
	"github.com/alexberardi/jarvis-server/internal/agent/x11"
	"github.com/alexberardi/jarvis-server/internal/computeruse"
)

// NewDesktopSource is the recorder's desktop mode on Linux, with keyframes of the screen from
// shots (nil: none) kept in dir: in a Wayland session the input devices, the compositor and
// AT-SPI (Hyprland and sway; other compositors refuse at start), else X11 (RECORD and AT-SPI).
func NewDesktopSource(dir string, shots screen.Capturer, log *slog.Logger) Source {
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		return withCapture(newWaylandSource(dir, wlSystem{
			Windows:   &dictate.LinuxWindows{X: dictate.OSExec{}},
			Devices:   func() ([]evdev.Device, error) { return evdev.Devices(evdev.SysRoot, evdev.DevRoot) },
			Open:      evdev.Open,
			Layout:    (&vkbd.Keyboard{}).Layout,
			Access:    atspi.Enable,
			Focused:   atspi.Focused,
			ElementAt: atspi.ElementAtWindow,
			Landings:  landings,
			Shot:      shooter(shots),
		}, log))
	}
	return withCapture(newX11Source(dir, &x11.Display{}, x11Access{ElementAt: atspi.ElementAt, Focused: atspi.Focused}, keyframer(shots), log))
}

// landedWait is how long a click waits for the app to say what took the focus. The click's
// event and keyframe wait with it.
const landedWait = 300 * time.Millisecond

func landings(ctx context.Context) (func(pid int, since time.Time) *computeruse.Element, func(), error) {
	l, err := atspi.Listen(ctx)
	if err != nil {
		return nil, nil, err
	}
	return func(pid int, since time.Time) *computeruse.Element {
		return l.After(context.Background(), pid, since, landedWait)
	}, l.Close, nil
}

// withCapture gives a recording computer control's window list and what AT-SPI shows on screen
// (read without refs), for its start event and each action's result.
func withCapture(s *DesktopSource) *DesktopSource {
	s.Platform.Windows, s.Platform.Elements = NativeWindows().List, atspi.Peek
	return s
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
