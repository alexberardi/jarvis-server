package dictate

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/xkb"
)

// LinuxKeyboard types with the first backend that works in this session:
//
//   - Wayland: the virtual-keyboard protocol spoken directly (Wayland, built in: Hyprland, sway
//     and other wlroots compositors; not GNOME, and KDE only for input methods), else wtype
//     (the same protocol, if installed), else ydotool (uinput: any compositor, but it needs the
//     ydotoold daemon and types US-layout ASCII only);
//   - X11: xdotool, else ydotool.
//
// The helpers are fallbacks for when the built-in backend can't run. Their text goes on stdin,
// never the command line (other processes can read argv).
type LinuxKeyboard struct {
	X Exec
	// Wayland is the built-in virtual keyboard (vkbd.Keyboard); nil for none.
	Wayland Injector
}

// Injector is a built-in typing backend: it sends keysym strokes to whatever has focus.
type Injector interface {
	Name() string
	// Available says whether it works in this session now, and why not.
	Available(ctx context.Context) (bool, string)
	Send(ctx context.Context, strokes []xkb.Stroke) error
}

const (
	beWayland = "wayland"
	beWtype   = "wtype"
	beYdotool = "ydotool"
	beXdotool = "xdotool"
)

// wtypeSettle is wtype's -s (ms) before its first key: a fresh virtual keyboard's keymap must
// reach the client first, or the first character is sometimes lost (seen on sway: "abc2" for
// "Xabc2" in 1 of 3 runs without it, 0 of 8 with 30 ms).
const wtypeSettle = "40"

func (l *LinuxKeyboard) wayland() bool { return l.X.Getenv("WAYLAND_DISPLAY") != "" }

// wtypeWorks: the compositor speaks virtual-keyboard. GNOME doesn't; KDE only grants it to
// input methods.
func (l *LinuxKeyboard) wtypeWorks() bool {
	if !l.wayland() || !l.X.LookPath("wtype") {
		return false
	}
	d := strings.ToLower(l.X.Getenv("XDG_CURRENT_DESKTOP"))
	return !strings.Contains(d, "gnome") && !strings.Contains(d, "kde")
}

// ydotoolWorks: ydotoold is listening ($YDOTOOL_SOCKET, else its defaults). path.Join, not
// filepath: these are Linux paths whatever OS runs the code (the tests run on Windows too).
func (l *LinuxKeyboard) ydotoolWorks() bool {
	if !l.X.LookPath("ydotool") {
		return false
	}
	for _, p := range []string{l.X.Getenv("YDOTOOL_SOCKET"), path.Join(l.X.Getenv("XDG_RUNTIME_DIR"), ".ydotool_socket"), "/tmp/.ydotool_socket"} {
		if p != "" && p != ".ydotool_socket" && l.X.Exists(p) {
			return true
		}
	}
	return false
}

func (l *LinuxKeyboard) x11() bool { return !l.wayland() && l.X.Getenv("DISPLAY") != "" }

// native: the built-in Wayland keyboard works now.
func (l *LinuxKeyboard) native(ctx context.Context) bool {
	if l.Wayland == nil || !l.wayland() {
		return false
	}
	ok, _ := l.Wayland.Available(ctx)
	return ok
}

// backend is the tool to use now, or "" with the reason.
func (l *LinuxKeyboard) backend(ctx context.Context) (string, string) {
	switch {
	case l.native(ctx):
		return beWayland, ""
	case l.wtypeWorks():
		return beWtype, ""
	case l.x11() && l.X.LookPath("xdotool"):
		return beXdotool, ""
	case (l.wayland() || l.x11()) && l.ydotoolWorks():
		return beYdotool, ""
	case l.wayland():
		return "", "this Wayland desktop offers no way to type into other apps (the virtual-keyboard protocol of Hyprland, sway and other wlroots compositors); ydotool with ydotoold running would work"
	case l.x11():
		return "", "typing on X11 needs xdotool installed"
	}
	return "", "typing needs a graphical session (no WAYLAND_DISPLAY or DISPLAY)"
}

func (l *LinuxKeyboard) Name() string {
	switch b, _ := l.backend(context.Background()); b {
	case "":
		return "none"
	case beWayland:
		return l.Wayland.Name()
	default:
		return b
	}
}

func (l *LinuxKeyboard) Available(ctx context.Context) (bool, string) {
	b, why := l.backend(ctx)
	return b != "", why
}

// utf8Env makes sure wtype reads non-ASCII text: it decodes with the C library's locale, and a
// systemd unit may run with none ("Failed to deencode input argv").
var utf8Env = []string{"LC_ALL=C.UTF-8"}

// typeTimeout grows with the text: xdotool types a few ms per character.
func typeTimeout(text string) time.Duration {
	return 15*time.Second + time.Duration(len([]rune(text)))*10*time.Millisecond
}

func (l *LinuxKeyboard) Type(ctx context.Context, text string) error {
	var c Cmd
	switch b, why := l.backend(ctx); b {
	case beWayland:
		strokes, ok := xkb.Strokes(text)
		if !ok {
			return fmt.Errorf("%w: text holds a character that can't be typed", errBadArgs)
		}
		return l.Wayland.Send(ctx, strokes)
	case beWtype:
		c = Cmd{Name: "wtype", Args: []string{"-s", wtypeSettle, "-"}, Env: utf8Env}
	case beXdotool:
		c = Cmd{Name: "xdotool", Args: []string{"type", "--clearmodifiers", "--delay", "6", "--file", "-"}, Env: utf8Env}
	case beYdotool:
		for _, r := range text {
			if r < ' ' || r > '~' {
				return fmt.Errorf("%w: ydotool can only type plain ASCII letters, digits and punctuation", ErrUnavailable)
			}
		}
		c = Cmd{Name: "ydotool", Args: []string{"type", "--file", "-"}}
	default:
		return fmt.Errorf("%w: %s", ErrUnavailable, why)
	}
	c.Stdin, c.Timeout = text, typeTimeout(text)
	_, err := l.X.Run(ctx, c)
	return err
}

// Key names per tool. ydotool takes evdev keycodes (linux/input-event-codes.h) as code:1/code:0.
var (
	xkbKeys = map[Key]string{KeyEnter: "Return", KeyEscape: "Escape", KeyTab: "Tab", KeyShiftTab: "Tab",
		KeySpace: "space", KeyUp: "Up", KeyDown: "Down", KeyLeft: "Left", KeyRight: "Right"}
	evdevKeys = map[Key]int{KeyEnter: 28, KeyEscape: 1, KeyTab: 15, KeyShiftTab: 15, KeySpace: 57,
		KeyUp: 103, KeyDown: 108, KeyLeft: 105, KeyRight: 106}
)

const evdevLeftShift = 42

// keyStrokes are the keys as keysyms, for the built-in backends.
var keyStrokes = map[Key]xkb.Stroke{KeyEnter: {Sym: xkb.KeyReturn}, KeyEscape: {Sym: xkb.KeyEscape},
	KeyTab: {Sym: xkb.KeyTab}, KeyShiftTab: {Sym: xkb.KeyTab, Shift: true}, KeySpace: {Sym: xkb.KeySpace},
	KeyUp: {Sym: xkb.KeyUp}, KeyDown: {Sym: xkb.KeyDown}, KeyLeft: {Sym: xkb.KeyLeft}, KeyRight: {Sym: xkb.KeyRight}}

func (l *LinuxKeyboard) Press(ctx context.Context, k Key) error {
	name, ok := xkbKeys[k]
	if !ok {
		return fmt.Errorf("%w: unknown key %q", errBadArgs, k)
	}
	shift := k == KeyShiftTab
	var c Cmd
	switch b, why := l.backend(ctx); b {
	case beWayland:
		return l.Wayland.Send(ctx, []xkb.Stroke{keyStrokes[k]})
	case beWtype:
		args := []string{"-s", wtypeSettle}
		if shift {
			args = append(args, "-M", "shift", "-k", name, "-m", "shift")
		} else {
			args = append(args, "-k", name)
		}
		c = Cmd{Name: "wtype", Args: args}
	case beXdotool:
		if shift {
			name = "shift+" + name
		}
		c = Cmd{Name: "xdotool", Args: []string{"key", "--clearmodifiers", name}}
	case beYdotool:
		code := evdevKeys[k]
		args := []string{"key"}
		if shift {
			args = append(args, fmt.Sprintf("%d:1", evdevLeftShift))
		}
		args = append(args, fmt.Sprintf("%d:1", code), fmt.Sprintf("%d:0", code))
		if shift {
			args = append(args, fmt.Sprintf("%d:0", evdevLeftShift))
		}
		c = Cmd{Name: "ydotool", Args: args}
	default:
		return fmt.Errorf("%w: %s", ErrUnavailable, why)
	}
	_, err := l.X.Run(ctx, c)
	return err
}
