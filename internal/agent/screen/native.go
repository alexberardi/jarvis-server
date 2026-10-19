package screen

import (
	"context"
	"runtime"

	"github.com/alexberardi/jarvis-server/internal/agent/macax"
	"github.com/alexberardi/jarvis-server/internal/agent/x11"
)

// Native is this OS's capturer, or nil where capture isn't supported yet (Windows). It probes
// nothing at construction.
func Native() Capturer {
	switch runtime.GOOS {
	case "linux":
		return &Linux{Wayland: &Wayland{X: OSExec{}}, X11: &X11{D: &x11.Display{}}}
	case "darwin":
		return &Mac{X: OSExec{}, Trusted: macax.ScreenTrusted, Windows: windowList, Main: mainDisplay}
	}
	return nil
}

// Linux picks the session's capturer per call (sessions come and go): grim on a wlroots
// compositor, else GetImage on an X11 session (DISPLAY set, not Wayland).
type Linux struct {
	Wayland *Wayland
	X11     *X11
}

func (l *Linux) pick(ctx context.Context) (Capturer, string) {
	wok, wwhy := l.Wayland.Available(ctx)
	if wok {
		return l.Wayland, ""
	}
	xok, xwhy := l.X11.Available(ctx)
	if xok {
		return l.X11, ""
	}
	if l.Wayland.X.Getenv("WAYLAND_DISPLAY") == "" && l.Wayland.X.Getenv("DISPLAY") != "" {
		return nil, xwhy
	}
	return nil, wwhy
}

func (l *Linux) Available(ctx context.Context) (bool, string) {
	c, why := l.pick(ctx)
	return c != nil, why
}

func (l *Linux) Capture(ctx context.Context, scope, window string) (Shot, error) {
	c, _ := l.pick(ctx)
	if c == nil {
		return Shot{}, ErrUnavailable
	}
	return c.Capture(ctx, scope, window)
}
