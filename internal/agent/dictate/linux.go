package dictate

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// LinuxKeyboard types with the first tool that works in this session:
//
//   - Wayland: wtype (the virtual-keyboard protocol: Hyprland, sway and other wlroots
//     compositors; not GNOME, and KDE only for input methods), else ydotool (uinput: any
//     compositor, but it needs the ydotoold daemon and types US-layout ASCII only);
//   - X11: xdotool, else ydotool.
//
// Text goes on stdin, never the command line (other processes can read argv).
type LinuxKeyboard struct {
	X Exec
}

const (
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

// ydotoolSocket is where ydotoold listens ($YDOTOOL_SOCKET, else its defaults).
func (l *LinuxKeyboard) ydotoolWorks() bool {
	if !l.X.LookPath("ydotool") {
		return false
	}
	for _, p := range []string{l.X.Getenv("YDOTOOL_SOCKET"), filepath.Join(l.X.Getenv("XDG_RUNTIME_DIR"), ".ydotool_socket"), "/tmp/.ydotool_socket"} {
		if p != "" && p != ".ydotool_socket" && l.X.Exists(p) {
			return true
		}
	}
	return false
}

func (l *LinuxKeyboard) x11() bool { return !l.wayland() && l.X.Getenv("DISPLAY") != "" }

// backend is the tool to use now, or "" with the reason.
func (l *LinuxKeyboard) backend() (string, string) {
	switch {
	case l.wtypeWorks():
		return beWtype, ""
	case l.x11() && l.X.LookPath("xdotool"):
		return beXdotool, ""
	case (l.wayland() || l.x11()) && l.ydotoolWorks():
		return beYdotool, ""
	case l.wayland():
		return "", "typing on this Wayland desktop needs wtype (Hyprland, sway and other wlroots compositors) or ydotool with ydotoold running (GNOME, KDE)"
	case l.x11():
		return "", "typing on X11 needs xdotool installed"
	}
	return "", "typing needs a graphical session (no WAYLAND_DISPLAY or DISPLAY)"
}

func (l *LinuxKeyboard) Name() string {
	if b, _ := l.backend(); b != "" {
		return b
	}
	return "none"
}

func (l *LinuxKeyboard) Available(context.Context) (bool, string) {
	b, why := l.backend()
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
	switch b, why := l.backend(); b {
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

func (l *LinuxKeyboard) Press(ctx context.Context, k Key) error {
	name, ok := xkbKeys[k]
	if !ok {
		return fmt.Errorf("%w: unknown key %q", errBadArgs, k)
	}
	shift := k == KeyShiftTab
	var c Cmd
	switch b, why := l.backend(); b {
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
