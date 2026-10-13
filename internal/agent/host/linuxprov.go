package host

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/agent/x11"
)

// Linux shells out: pactl / wpctl / amixer for volume and xdg-open for open. Windows on X11 go
// through EWMH spoken directly (X, internal/agent/x11: no helper program), falling back to
// wmctrl (+ xdotool for minimise) when that can't run. Two realities it has to survive:
//
//   - Headless boxes have no session: window capabilities are absent and open is refused
//     (xdg-open on a headless box exits 0 having done nothing, which fire-and-forget can't
//     tell from success).
//   - Wayland forbids one client from enumerating or controlling another's windows. That is
//     upstream security policy, not a missing library, so window capabilities are reported
//     absent rather than failing later.
//
// The file is deliberately not named *_linux.go: it compiles on every OS so the contract suite
// can run it everywhere.
type Linux struct {
	Sys Sys
	// X is the built-in X11 window control (EWMH); nil: wmctrl and xdotool only. Native sets
	// it; tests leave it nil or pass a fake.
	X X11
}

// X11 is the built-in X11 window control (*x11.Display).
type X11 interface {
	WMAvailable(ctx context.Context) (bool, string)
	Windows(ctx context.Context) ([]x11.Window, error)
	Activate(ctx context.Context, id uint32) error
	CloseWindow(ctx context.Context, id uint32) error
	Iconify(ctx context.Context, id uint32) error
}

// NewLinux builds the Linux backend. It touches nothing.
func NewLinux(s Sys) *Linux { return &Linux{Sys: s} }

func (*Linux) Platform() string { return "linux" }

func (l *Linux) Capabilities(ctx context.Context) []string {
	var caps []string
	if l.Sys.LookPath("xdg-open") && l.sessionAvailable() {
		caps = append(caps, CapOpen)
	}
	if l.mixer() != "" {
		caps = append(caps, CapVolumeGet, CapVolumeSet)
	}
	switch {
	case l.nativeX(ctx):
		caps = append(caps, AllWindowCaps...)
	case l.x11Available():
		caps = append(caps, CapWindowsList, CapWindowsFocus, CapWindowsClose)
		// wmctrl cannot iconify; minimise needs xdotool. Advertise only what works.
		if l.Sys.LookPath("xdotool") {
			caps = append(caps, CapWindowsMinimize)
		}
	}
	return caps
}

// nativeX: an X11 session with an EWMH window manager the built-in client can drive. Probed
// per call (one connection).
func (l *Linux) nativeX(ctx context.Context) bool {
	if l.X == nil || !l.x11Session() {
		return false
	}
	ok, _ := l.X.WMAvailable(ctx)
	return ok
}

func (l *Linux) mixer() string {
	for _, m := range []string{"pactl", "wpctl", "amixer"} {
		if l.Sys.LookPath(m) {
			return m
		}
	}
	return ""
}

func (l *Linux) sessionAvailable() bool {
	return l.Sys.Getenv("DISPLAY") != "" || l.Sys.Getenv("WAYLAND_DISPLAY") != ""
}

// x11Available: window control needs X11 and wmctrl. A Wayland session never qualifies, even
// with XWayland's DISPLAY set, because wmctrl would only see XWayland clients and lie about the
// rest. (A per-compositor backend, e.g. hyprctl, is an open question in docs/agent/DESIGN.md.)
func (l *Linux) x11Available() bool {
	return l.x11Session() && l.Sys.LookPath("wmctrl")
}

func (l *Linux) x11Session() bool {
	if l.Sys.Getenv("WAYLAND_DISPLAY") != "" || l.Sys.Getenv("XDG_SESSION_TYPE") == "wayland" {
		return false
	}
	return l.Sys.Getenv("DISPLAY") != ""
}

var (
	rePactl  = regexp.MustCompile(`(\d+)%`)
	reAmixer = regexp.MustCompile(`\[(\d+)%\]`)
	reWpctl  = regexp.MustCompile(`Volume:\s*([0-9.]+)`)
)

func (l *Linux) run(ctx context.Context, name string, args ...string) (string, error) {
	out, err := l.Sys.Run(ctx, name, args...)
	if err != nil {
		return "", Errorf(ErrOperationFailed, "%v", err)
	}
	return out, nil
}

func (l *Linux) GetVolume(ctx context.Context) (int, error) {
	switch l.mixer() {
	case "pactl":
		raw, err := l.run(ctx, "pactl", "get-sink-volume", "@DEFAULT_SINK@")
		if err != nil {
			return 0, err
		}
		m := rePactl.FindStringSubmatch(raw)
		if m == nil {
			return 0, Errorf(ErrOperationFailed, "could not parse pactl output %q", raw)
		}
		v, _ := strconv.Atoi(m[1])
		return v, nil
	case "wpctl":
		raw, err := l.run(ctx, "wpctl", "get-volume", "@DEFAULT_AUDIO_SINK@")
		if err != nil {
			return 0, err
		}
		m := reWpctl.FindStringSubmatch(raw)
		if m == nil {
			return 0, Errorf(ErrOperationFailed, "could not parse wpctl output %q", raw)
		}
		f, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			return 0, Errorf(ErrOperationFailed, "could not parse wpctl output %q", raw)
		}
		return int(f*100 + 0.5), nil
	case "amixer":
		raw, err := l.run(ctx, "amixer", "sget", "Master")
		if err != nil {
			return 0, err
		}
		m := reAmixer.FindStringSubmatch(raw)
		if m == nil {
			return 0, Errorf(ErrOperationFailed, "could not parse amixer output %q", raw)
		}
		v, _ := strconv.Atoi(m[1])
		return v, nil
	}
	return 0, Errorf(ErrNotSupported, "none of pactl, wpctl or amixer is installed")
}

func (l *Linux) ApplyVolume(ctx context.Context, level int) error {
	var err error
	switch l.mixer() {
	case "pactl":
		_, err = l.run(ctx, "pactl", "set-sink-volume", "@DEFAULT_SINK@", fmt.Sprintf("%d%%", level))
	case "wpctl":
		_, err = l.run(ctx, "wpctl", "set-volume", "@DEFAULT_AUDIO_SINK@", fmt.Sprintf("%d%%", level))
	case "amixer":
		_, err = l.run(ctx, "amixer", "sset", "Master", fmt.Sprintf("%d%%", level))
	default:
		err = Errorf(ErrNotSupported, "none of pactl, wpctl or amixer is installed")
	}
	return err
}

func (l *Linux) requireX11() error {
	if !l.x11Available() {
		return Errorf(ErrNotSupported, "window control needs X11 with an EWMH window manager (or wmctrl); this is headless, or Wayland, which forbids cross-app window control")
	}
	return nil
}

// reX11ID is what wmctrl prints. Ids are opaque to callers, but the backend validates its own
// format before handing one to a subprocess.
var (
	reX11ID      = regexp.MustCompile(`^0x[0-9a-fA-F]{1,16}$`)
	reWmctrlLine = regexp.MustCompile(`^(0x[0-9a-fA-F]{1,16})\s+-?\d+\s+(\S+)\s+\S+\s?(.*)$`)
)

func (l *Linux) ListWindows(ctx context.Context) ([]Window, error) {
	if l.nativeX(ctx) {
		ws, err := l.X.Windows(ctx)
		if err != nil {
			return nil, Errorf(ErrOperationFailed, "%v", err)
		}
		out := make([]Window, len(ws))
		for i, w := range ws {
			out[i] = Window{ID: x11.FormatID(w.ID), Title: w.Title, App: w.Class, Minimized: w.Minimized}
		}
		return out, nil
	}
	if err := l.requireX11(); err != nil {
		return nil, err
	}
	raw, err := l.run(ctx, "wmctrl", "-l", "-x")
	if err != nil {
		return nil, err
	}
	out := []Window{}
	for _, line := range strings.Split(raw, "\n") {
		// 0xHANDLE  desktop  CLASS  host  title
		m := reWmctrlLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		out = append(out, Window{ID: m[1], Title: strings.TrimSpace(m[3]), App: m[2]})
	}
	return out, nil
}

func (l *Linux) requireWindow(ctx context.Context, id string) error {
	if !reX11ID.MatchString(id) {
		return Errorf(ErrWindowNotFound, "not an X11 window id: %q", id)
	}
	ws, err := l.ListWindows(ctx)
	if err != nil {
		return err
	}
	want, _ := x11.ParseID(id) // wmctrl and the built-in client pad ids differently
	for _, w := range ws {
		if got, _ := x11.ParseID(w.ID); strings.EqualFold(w.ID, id) || (got != 0 && got == want) {
			return nil
		}
	}
	return Errorf(ErrWindowNotFound, "window %s no longer exists", id)
}

// xwin: the built-in client's id for a window that requireWindow found, or false to use the
// helpers.
func (l *Linux) xwin(ctx context.Context, id string) (uint32, bool) {
	if !l.nativeX(ctx) {
		return 0, false
	}
	return x11.ParseID(id)
}

func (l *Linux) xdo(err error) error {
	if err != nil {
		return Errorf(ErrOperationFailed, "%v", err)
	}
	return nil
}

func (l *Linux) MinimizeWindow(ctx context.Context, id string) error {
	if err := l.requireWindow(ctx, id); err != nil {
		return err
	}
	if w, ok := l.xwin(ctx, id); ok {
		return l.xdo(l.X.Iconify(ctx, w))
	}
	if !l.Sys.LookPath("xdotool") {
		return Errorf(ErrNotSupported, "minimise needs xdotool (wmctrl cannot iconify)")
	}
	_, err := l.run(ctx, "xdotool", "windowminimize", id)
	return err
}

func (l *Linux) FocusWindow(ctx context.Context, id string) error {
	if err := l.requireWindow(ctx, id); err != nil {
		return err
	}
	if w, ok := l.xwin(ctx, id); ok {
		return l.xdo(l.X.Activate(ctx, w))
	}
	_, err := l.run(ctx, "wmctrl", "-i", "-a", id)
	return err
}

// CloseWindow: _NET_CLOSE_WINDOW (or wmctrl -c) is a graceful close request, never a kill.
func (l *Linux) CloseWindow(ctx context.Context, id string) error {
	if err := l.requireWindow(ctx, id); err != nil {
		return err
	}
	if w, ok := l.xwin(ctx, id); ok {
		return l.xdo(l.X.CloseWindow(ctx, w))
	}
	_, err := l.run(ctx, "wmctrl", "-i", "-c", id)
	return err
}

func (l *Linux) Open(_ context.Context, target string) error {
	if !l.Sys.LookPath("xdg-open") {
		return Errorf(ErrNotSupported, "xdg-open is unavailable")
	}
	if !l.sessionAvailable() {
		return Errorf(ErrNotSupported, "no graphical session; xdg-open would silently do nothing")
	}
	if err := l.Sys.Start("xdg-open", target); err != nil {
		return Errorf(ErrOperationFailed, "could not open %q: %v", target, err)
	}
	return nil
}

// LaunchApp starts a .desktop entry. xdg-open would open the file in an editor, so this uses
// gio launch (GLib 2.67+), falling back to gtk-launch with the desktop-file id.
func (l *Linux) LaunchApp(_ context.Context, path string) error {
	if !l.sessionAvailable() {
		return Errorf(ErrNotSupported, "no graphical session to launch an app into")
	}
	var err error
	switch {
	case l.Sys.LookPath("gio"):
		err = l.Sys.Start("gio", "launch", path)
	case l.Sys.LookPath("gtk-launch"):
		err = l.Sys.Start("gtk-launch", strings.TrimSuffix(filepath.Base(path), ".desktop"))
	default:
		return Errorf(ErrNotSupported, "launching apps needs gio or gtk-launch")
	}
	if err != nil {
		return Errorf(ErrOperationFailed, "could not launch %q: %v", path, err)
	}
	return nil
}
