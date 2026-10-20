package computer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/alexberardi/jarvis-server/internal/agent/ax"
	"github.com/alexberardi/jarvis-server/internal/agent/dictate"
	"github.com/alexberardi/jarvis-server/internal/agent/evdev"
	"github.com/alexberardi/jarvis-server/internal/agent/screen"
	"github.com/alexberardi/jarvis-server/internal/agent/xkb"
	"github.com/alexberardi/jarvis-server/internal/computeruse"
)

// Wayland desktop recording on wlroots compositors (Hyprland, sway): Wayland shows no client
// another's input, so the keys and buttons come from the kernel (evdev: the keyboards and
// pointers under /dev/input, which needs the user in the input group, Linux's Input
// Monitoring), decoded with the desktop's keyboard layout. Where it happened comes from the
// compositor: the cursor (hyprctl cursorpos; sway has none to give) and the focused window
// (class, title, geometry), and AT-SPI's focused element in that window names what typing went
// into; a password text is masked. Keyframes (grim) mark the click or cursor, at most one a
// second, with a crop around it, for a vision model to make sense of the clicks. GNOME and KDE
// (no screencopy, no compositor IPC) refuse for now. Decision 2026-10-20-desktop-recording-wayland.
// Platform-free so the tests run everywhere; record_linux.go wires the real system.

var (
	// ErrWaylandRecording: computer_record_start {mode: desktop} on a Wayland desktop other than
	// Hyprland or sway.
	ErrWaylandRecording = errors.New("On Wayland I can record the desktop on Hyprland and sway; on GNOME and KDE I can only record the browser for now")
	// ErrInputGroup: the keyboard and mouse devices can't be read (macOS's ErrInputMonitoring).
	ErrInputGroup = errors.New("to record the desktop, Jarvis needs to read the keyboard and mouse: run " +
		"`sudo usermod -aG input $USER` in a terminal, then log out and back in " +
		"(`jarvis-agent permission input-monitoring` says when it's done)")
)

// wlSystem is what a Wayland recording asks the system.
type wlSystem struct {
	Windows *dictate.LinuxWindows // the compositor, its focused window and cursor
	Devices func() ([]evdev.Device, error)
	Open    func(devs []evdev.Device, send func(evdev.Event)) (stop func(), err error)
	Layout  func(context.Context) *xkb.Layout
	// Access says whether AT-SPI can be reached (atspi.Available); Focused is its focused
	// element in a process's active window (atspi.Focused).
	Access  func(context.Context) error
	Focused func(pid int) (*computeruse.Element, error)
	// Shot captures the screen for a keyframe (the focused monitor); nil: no keyframes.
	Shot func(context.Context) (screen.Shot, error)
}

// wlKeyframeEvery is the shortest gap between a Wayland recording's keyframes: they carry the
// clicks, which have no element to name them.
const wlKeyframeEvery = time.Second

func newWaylandSource(dir string, sys wlSystem, log *slog.Logger) *DesktopSource {
	p := &wlPlatform{sys: sys}
	plat := DeskPlatform{Focused: p.focused, Window: p.window, KeyframeEvery: wlKeyframeEvery, Context: p.context}
	if sys.Shot != nil {
		plat.Frame = p.frame
	}
	return &DesktopSource{Dir: dir, Tap: p.tap, Log: log, Platform: plat}
}

type wlPlatform struct {
	sys  wlSystem
	a11y atomic.Bool // AT-SPI answered at the last start

	// The last cursor position asked for (a scroll's ticks reuse it); only the tap's
	// callback uses them, under its lock.
	pos    Point
	posAt  time.Time
	hasPos bool
}

func wlCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 2*time.Second)
}

func (p *wlPlatform) context() string {
	ctx, cancel := wlCtx()
	defer cancel()
	ok := p.sys.Access != nil && p.sys.Access(ctx) == nil
	p.a11y.Store(ok)
	if !ok {
		return ContextNone
	}
	return ""
}

func (p *wlPlatform) tap(send func(DeskInput)) (func(), error) {
	if p.sys.Windows.Compositor() == "" {
		return nil, ErrWaylandRecording
	}
	devs, err := p.sys.Devices()
	if err != nil {
		return nil, fmt.Errorf("desktop recording: input devices: %w", err)
	}
	ctx, cancel := wlCtx()
	keys := &wlKeys{layout: p.sys.Layout(ctx)}
	cancel()
	var mu sync.Mutex // one goroutine per device; send is called one at a time
	stop, err := p.sys.Open(devs, func(ev evdev.Event) {
		mu.Lock()
		defer mu.Unlock()
		in, ok := keys.input(ev)
		if !ok {
			return
		}
		if in.Kind == InClick || in.Kind == InScroll {
			p.place(&in)
		}
		send(in)
	})
	switch {
	case errors.Is(err, evdev.ErrNoAccess):
		return nil, ErrInputGroup
	case err != nil:
		return nil, fmt.Errorf("desktop recording: %w", err)
	}
	return stop, nil
}

// place puts a click or scroll where the cursor is, asked at once (the window under it may
// change soon after); NoPoint when the compositor can't say.
func (p *wlPlatform) place(in *DeskInput) {
	if in.Kind == InScroll && p.hasPos && in.At.Sub(p.posAt) < deskScrollGap {
		in.X, in.Y = p.pos.X, p.pos.Y
		return
	}
	ctx, cancel := wlCtx()
	x, y, ok := p.sys.Windows.Cursor(ctx)
	cancel()
	if !ok {
		in.NoPoint = true
		return
	}
	in.X, in.Y = x, y
	p.pos, p.posAt, p.hasPos = Point{x, y}, in.At, true
}

// focused is what typing goes into: the focused window's process and app, and AT-SPI's
// focused element there (its role and name; Secure for a password text) when it answers.
func (p *wlPlatform) focused() (*ax.Recorded, error) {
	ctx, cancel := wlCtx()
	w, err := p.sys.Windows.FocusedWindow(ctx)
	cancel()
	if err != nil || w == nil {
		return nil, err
	}
	rec := &ax.Recorded{App: w.Class, PID: w.PID}
	if p.a11y.Load() && w.PID > 0 && p.sys.Focused != nil {
		if el, err := p.sys.Focused(w.PID); err == nil && el != nil {
			rec.Role, rec.Name, rec.Secure = el.Role, el.Name, el.Secure
		}
	}
	return rec, nil
}

func (p *wlPlatform) window() *EventWindow {
	ctx, cancel := wlCtx()
	defer cancel()
	w, err := p.sys.Windows.FocusedWindow(ctx)
	if err != nil || w == nil {
		return nil
	}
	return &EventWindow{Class: w.Class, Title: w.Title, X: w.X, Y: w.Y, W: w.W, H: w.H}
}

// frame is a keyframe with pt marked: the click's point, else the cursor's when known.
func (p *wlPlatform) frame(ctx context.Context, pt *Point) (Frame, error) {
	shot, err := p.sys.Shot(ctx)
	if err != nil {
		return Frame{}, err
	}
	if pt == nil {
		if x, y, ok := p.sys.Windows.Cursor(ctx); ok {
			pt = &Point{x, y}
		}
	}
	return markFrame(shot.Image, shot.Geometry, pt)
}

// wlModifiers are the modifier keys (evdev codes) and what they hold. Right Alt is AltGr on
// layouts that make it ISO_Level3_Shift.
var wlModifiers = map[uint16]string{evdev.KeyLeftShift: "shift", evdev.KeyRightShift: "shift",
	evdev.KeyLeftCtrl: "ctrl", evdev.KeyRightCtrl: "ctrl", evdev.KeyLeftAlt: "alt", evdev.KeyRightAlt: "alt",
	evdev.KeyLeftMeta: "super", evdev.KeyRightMeta: "super"}

var wlNamed = map[uint16]string{evdev.KeyEnter: KeyEnter, evdev.KeyKPEnter: KeyEnter, evdev.KeyTab: KeyTab,
	evdev.KeyEsc: KeyEscape, evdev.KeyBackspace: KeyBackspace}

// xkbOffset: XKB keycodes are evdev codes + 8.
const xkbOffset = 8

// wlKeys decodes evdev events into raw input: modifiers tracked from their own presses and
// releases, text through the layout's levels (Shift, Caps Lock), shortcuts as keys. Only the
// tap's callback calls it (under its lock).
type wlKeys struct {
	layout *xkb.Layout
	held   map[uint16]string // modifier keys down
	caps   bool              // Caps Lock toggled on during the recording
}

func (k *wlKeys) input(ev evdev.Event) (DeskInput, bool) {
	switch ev.Type {
	case evdev.EvRel:
		in := DeskInput{Kind: InScroll, At: ev.Time}
		switch ev.Code {
		case evdev.RelWheel:
			in.DY = -int(ev.Value) // the wheel turned away from the user: up
		case evdev.RelHWheel:
			in.DX = int(ev.Value)
		default:
			return DeskInput{}, false // motion
		}
		return in, in.DX != 0 || in.DY != 0
	case evdev.EvKey:
	default:
		return DeskInput{}, false
	}
	if ev.Code == evdev.BtnLeft || ev.Code == evdev.BtnRight {
		if ev.Value != 1 {
			return DeskInput{}, false
		}
		in := DeskInput{Kind: InClick, At: ev.Time, Button: "left"}
		if ev.Code == evdev.BtnRight {
			in.Button = "right"
		}
		return in, true
	}
	if ev.Code >= 0x100 && ev.Code < 0x160 { // BTN_MISC..BTN_GEAR_UP: the other buttons, touch
		return DeskInput{}, false
	}
	if m, ok := wlModifiers[ev.Code]; ok {
		if ev.Code == evdev.KeyRightAlt && k.layout.Sym(evdev.KeyRightAlt+xkbOffset, 0) == symISOLevel3 {
			m = "altgr"
		}
		if k.held == nil {
			k.held = map[uint16]string{}
		}
		if ev.Value == 0 {
			delete(k.held, ev.Code)
		} else {
			k.held[ev.Code] = m
		}
		return DeskInput{}, false
	}
	if ev.Value == 0 {
		return DeskInput{}, false
	}
	if ev.Code == evdev.KeyCapsLock {
		if ev.Value == 1 {
			k.caps = !k.caps
		}
		return DeskInput{}, false
	}
	mods := map[string]bool{}
	for _, m := range k.held {
		mods[m] = true
	}
	in := DeskInput{Kind: InKey, At: ev.Time}
	named := wlNamed[ev.Code]
	code := uint32(ev.Code) + xkbOffset
	shortcut := mods["ctrl"] || mods["alt"] || mods["super"]
	if named == KeyTab && !shortcut && mods["shift"] {
		in.Key = "shift+tab" // moves focus back, so a replay must not press plain Tab
		return in, true
	}
	if shortcut {
		base := strings.ToLower(named)
		if r := xkb.Rune(k.layout.Sym(code, 0)); base == "" && r != 0 && !unicode.IsSpace(r) {
			base = string(unicode.ToLower(r))
		}
		if base == "" {
			in.Key = keyOther
			return in, true
		}
		var names []string
		for _, m := range []string{"ctrl", "alt", "shift", "super"} {
			if mods[m] {
				names = append(names, m)
			}
		}
		in.Key = strings.Join(append(names, base), "+")
		return in, true
	}
	if named != "" {
		in.Key = named
		return in, true
	}
	r := xkb.Rune(k.layout.Sym(code, 0))
	if mods["altgr"] {
		r = 0 // the layout keeps levels 1 and 2 only
	}
	if r != 0 && mods["shift"] != (k.caps && unicode.IsLetter(r)) {
		if s := xkb.Rune(k.layout.Sym(code, 1)); s != 0 {
			r = s
		} else {
			r = unicode.ToUpper(r)
		}
	}
	if r == 0 || !typable(string(r)) {
		in.Key = keyOther
		return in, true
	}
	in.Text = string(r)
	return in, true
}
