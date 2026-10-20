//go:build linux

package computer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/alexberardi/jarvis-server/internal/agent/atspi"
	"github.com/alexberardi/jarvis-server/internal/agent/ax"
	"github.com/alexberardi/jarvis-server/internal/agent/input"
	"github.com/alexberardi/jarvis-server/internal/agent/screen"
	"github.com/alexberardi/jarvis-server/internal/agent/x11"
	"github.com/alexberardi/jarvis-server/internal/computeruse"
)

// NativeDesktop is desktop tasks on Linux (plan P3 piece 35), picked per call as sessions come
// and go: on Hyprland and sway the compositor lists the windows (hyprctl, swaymsg), grim
// captures and the wlroots virtual pointer and keyboard act; in an X11 session EWMH lists the
// windows, GetImage captures and XTEST acts, all through the built-in X11 client. Elements come
// from AT-SPI where the app exposes them (GTK, Qt, Electron with accessibility on); an app that
// doesn't (Tk) gets screenshots only. GNOME and KDE on Wayland have no cross-client window list
// or input: tasks there fail with what's missing. The lock state is logind's LockedHint.
func NativeDesktop(apps Apps) *Desktop {
	in := input.Native()
	if in == nil {
		return nil
	}
	mon := input.NativeMonitor()
	// A compositor's idle notifications only say what happened after the monitor began:
	// begin now, so input before the first task counts.
	go mon.Since(context.Background()) //nolint:errcheck // a warm-up; tasks ask again
	wins := &linuxWindows{getenv: os.Getenv, wayland: &screen.Wayland{X: screen.OSExec{}}, x11: &x11.Display{}}
	return &Desktop{
		AX:      &linuxAX{windows: wins, getenv: os.Getenv, elementAt: atspi.ElementAt, focused: atspi.Focused},
		Windows: wins, Input: in, Monitor: mon,
		Capture:   wins.capture,
		Locked:    func(ctx context.Context) (bool, error) { return logindLocked(ctx, systemLogind{}) },
		Clipboard: NativeClipboard(),
		Apps:      apps,
	}
}

// x11Clients is the slice of *x11.Display the Linux desktop uses.
type x11Clients interface {
	Clients(ctx context.Context) ([]x11.Client, error)
	Capture(ctx context.Context, win uint32) (x11.Capture, error)
}

// linuxWindows is the windows on screen, front to back: the compositor's in a Wayland session,
// else the X11 window manager's.
type linuxWindows struct {
	getenv  func(string) string
	wayland *screen.Wayland
	x11     x11Clients
}

// errNoDesktop: neither a supported compositor nor an X11 session.
var errNoDesktop = errors.New("desktop tasks on Linux need Hyprland, sway or an X11 session (GNOME and KDE on Wayland aren't supported yet)")

func (l *linuxWindows) List(ctx context.Context) ([]DesktopWindow, error) {
	if l.getenv("WAYLAND_DISPLAY") != "" {
		ws, err := l.wayland.Windows(ctx)
		if errors.Is(err, screen.ErrUnavailable) {
			return nil, errNoDesktop
		}
		if err != nil {
			return nil, err
		}
		out := make([]DesktopWindow, len(ws))
		for i, w := range ws {
			out[i] = DesktopWindow{ID: w.ID, App: w.App, PID: w.PID, Title: w.Title, X: w.X, Y: w.Y, W: w.W, H: w.H, Focused: w.Focused}
		}
		return out, nil
	}
	if l.getenv("DISPLAY") == "" {
		return nil, errNoDesktop
	}
	cs, err := l.x11.Clients(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]DesktopWindow, len(cs))
	for i, c := range cs {
		out[i] = DesktopWindow{ID: x11.FormatID(c.ID), App: c.Class, PID: c.PID, Title: c.Title, X: c.X, Y: c.Y, W: c.W, H: c.H, Focused: c.Active}
	}
	return out, nil
}

// capture is what the screen shows in w's area: grim on Wayland (it is the one program a
// Wayland desktop task needs installed), GetImage on X11.
func (l *linuxWindows) capture(ctx context.Context, w DesktopWindow) (screen.Shot, error) {
	if l.getenv("WAYLAND_DISPLAY") != "" {
		if ok, why := l.wayland.Available(ctx); !ok {
			return screen.Shot{}, errors.New(why)
		}
		return l.wayland.CaptureWindow(ctx, screen.Window{ID: w.ID, App: w.App, PID: w.PID, Title: w.Title, X: w.X, Y: w.Y, W: w.W, H: w.H})
	}
	id, ok := x11.ParseID(w.ID)
	if !ok {
		return screen.Shot{}, fmt.Errorf("not an X11 window id: %q", w.ID)
	}
	c, err := l.x11.Capture(ctx, id)
	if err != nil {
		return screen.Shot{}, err
	}
	return screen.Shot{Image: c.Image, Title: w.Title, App: w.App,
		Geometry: computeruse.Geometry{OriginX: c.X, OriginY: c.Y, LogicalW: c.W, LogicalH: c.H, Scale: 1}}, nil
}

// linuxAX is AT-SPI as the executor's AX. The element under a point and the focused one are
// looked up in the window there (or with the focus), by its process; the app is the window's.
// AT-SPI's screen coordinates are only right on X11 (Wayland clients don't know where their
// windows are), so on Wayland the window list alone says what is under a point.
type linuxAX struct {
	windows   Windows
	getenv    func(string) string
	elementAt func(pid, x, y int) (*computeruse.Element, error)
	focused   func(pid int) (*computeruse.Element, error)
}

func (a *linuxAX) Snapshot(ctx context.Context, pid, max int) (computeruse.Snapshot, error) {
	return atspi.Snapshot(ctx, pid, max)
}
func (a *linuxAX) Press(gen int, ref string) error          { return atspi.Press(gen, ref) }
func (a *linuxAX) SetValue(gen int, ref, text string) error { return atspi.SetValue(gen, ref, text) }
func (a *linuxAX) Focus(gen int, ref string) error          { return atspi.Focus(gen, ref) }

func (a *linuxAX) At(x, y float64) (*ax.Recorded, error) {
	if a.getenv("WAYLAND_DISPLAY") != "" {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), recordedTimeout)
	defer cancel()
	ws, err := a.windows.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, w := range ws {
		if w.contains(round(x), round(y)) {
			if w.PID <= 0 {
				return nil, nil
			}
			el, err := a.elementAt(w.PID, round(x), round(y))
			return recordedIn(el, w), err
		}
	}
	return nil, nil
}

func (a *linuxAX) Focused() (*ax.Recorded, error) {
	ctx, cancel := context.WithTimeout(context.Background(), recordedTimeout)
	defer cancel()
	ws, err := a.windows.List(ctx)
	if err != nil {
		return nil, err
	}
	w, ok := findWindow(ws, "")
	if !ok || w.PID <= 0 {
		return nil, nil
	}
	el, err := a.focused(w.PID)
	return recordedIn(el, w), err
}

// recordedTimeout bounds a hit test's window lookup.
const recordedTimeout = 5 * time.Second

// recordedIn is an AT-SPI element as the executor checks it: the app is its window's.
func recordedIn(el *computeruse.Element, w DesktopWindow) *ax.Recorded {
	if el == nil {
		return nil
	}
	return &ax.Recorded{Role: el.Role, Name: el.Name, Secure: el.Secure, PID: w.PID, App: w.App}
}

// logind is the slice of org.freedesktop.login1 the lock state needs.
type logind interface {
	// Session is this process's session: XDG_SESSION_ID's, else the one its PID is in.
	Session(ctx context.Context) (dbus.ObjectPath, error)
	LockedHint(ctx context.Context, session dbus.ObjectPath) (bool, error)
}

// logindLocked is the session's LockedHint (set by lock screens: hyprlock, swaylock with
// logind integration, xss-lock). Without logind or a session for this process (a headless
// session, a container) there is no lock to see: the idle check still applies.
func logindLocked(ctx context.Context, l logind) (bool, error) {
	s, err := l.Session(ctx)
	if err != nil {
		return false, nil
	}
	locked, err := l.LockedHint(ctx, s)
	if err != nil {
		return false, fmt.Errorf("logind: %w", err)
	}
	return locked, nil
}

type systemLogind struct{}

const (
	login1        = "org.freedesktop.login1"
	logindTimeout = 2 * time.Second
)

func (systemLogind) Session(ctx context.Context) (dbus.ObjectPath, error) {
	c, err := dbus.SystemBus()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, logindTimeout)
	defer cancel()
	m := c.Object(login1, "/org/freedesktop/login1")
	var p dbus.ObjectPath
	if id := os.Getenv("XDG_SESSION_ID"); id != "" {
		err = m.CallWithContext(ctx, login1+".Manager.GetSession", 0, id).Store(&p)
	} else {
		err = m.CallWithContext(ctx, login1+".Manager.GetSessionByPID", 0, uint32(os.Getpid())).Store(&p)
	}
	return p, err
}

func (systemLogind) LockedHint(ctx context.Context, s dbus.ObjectPath) (bool, error) {
	c, err := dbus.SystemBus()
	if err != nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(ctx, logindTimeout)
	defer cancel()
	var v dbus.Variant
	if err := c.Object(login1, s).CallWithContext(ctx, "org.freedesktop.DBus.Properties.Get", 0, login1+".Session", "LockedHint").Store(&v); err != nil {
		return false, err
	}
	locked, ok := v.Value().(bool)
	if !ok {
		return false, errors.New("LockedHint isn't a boolean")
	}
	return locked, nil
}
