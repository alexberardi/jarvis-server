package computer

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"
	"unicode"

	"github.com/alexberardi/jarvis-server/internal/agent/ax"
	"github.com/alexberardi/jarvis-server/internal/agent/x11"
	"github.com/alexberardi/jarvis-server/internal/agent/xkb"
	"github.com/alexberardi/jarvis-server/internal/computeruse"
)

// X11 desktop recording (plan P3 piece 37): the RECORD extension hands over every client's key
// presses and releases and button presses (x11.Display.Record, on a data connection of its
// own); x11Keys turns them into the same raw input as the macOS tap, and DesktopSource groups
// them. The element under a click is AT-SPI's GetAccessibleAtPoint in the window under the
// point (its _NET_WM_PID); typing goes to the active window's focused element, and keystrokes
// into a password text never leave the recorder (masked). Wayland lets no app see another's
// input, so there only the browser is recorded (decision 2026-10-19-taught-workflows, Q8).
// Platform-free so the tests run everywhere; record_linux.go wires the real display.

// ErrWaylandRecording: computer_record_start {mode: desktop} in a Wayland session.
var ErrWaylandRecording = errors.New("On Wayland I can only record the browser")

// x11Desk is the slice of *x11.Display an X11 recording uses.
type x11Desk interface {
	Session() (bool, string)
	Wayland() bool
	Record(send func(x11.RecordedInput)) (x11.Keymap, func(), error)
	WindowAt(ctx context.Context, x, y int) (x11.Owner, error)
	ActiveOwner(ctx context.Context) (x11.Owner, error)
}

// x11Access is the accessibility tree's part (atspi.ElementAt, atspi.Focused).
type x11Access struct {
	ElementAt func(pid, x, y int) (*computeruse.Element, error)
	Focused   func(pid int) (*computeruse.Element, error)
}

func newX11Source(dir string, d x11Desk, a x11Access, keyframe func(context.Context) ([]byte, error), log *slog.Logger) *DesktopSource {
	p := &x11Platform{d: d, a: a}
	return &DesktopSource{Dir: dir, Tap: p.tap, Log: log,
		Platform: DeskPlatform{ElementAt: p.elementAt, Focused: p.focused, Keyframe: keyframe}}
}

type x11Platform struct {
	d x11Desk
	a x11Access
}

func (p *x11Platform) tap(send func(DeskInput)) (func(), error) {
	if p.d.Wayland() {
		return nil, ErrWaylandRecording
	}
	if ok, _ := p.d.Session(); !ok {
		return nil, ErrDesktopRecording
	}
	keys := &x11Keys{}
	ready := make(chan struct{}) // the keymap is known
	km, stop, err := p.d.Record(func(ev x11.RecordedInput) {
		<-ready
		if in, ok := keys.input(ev, time.Now()); ok {
			send(in)
		}
	})
	if err != nil {
		return nil, err
	}
	keys.km = km
	close(ready)
	return stop, nil
}

func (p *x11Platform) elementAt(x, y float64) (*ax.Recorded, error) {
	w, err := p.d.WindowAt(context.Background(), round(x), round(y))
	if err != nil || w.ID == 0 || w.PID == 0 {
		return nil, err
	}
	el, err := p.a.ElementAt(w.PID, round(x), round(y))
	return recordedX11(el, w), err
}

func (p *x11Platform) focused() (*ax.Recorded, error) {
	w, err := p.d.ActiveOwner(context.Background())
	if err != nil || w.ID == 0 || w.PID == 0 {
		return nil, err
	}
	el, err := p.a.Focused(w.PID)
	return recordedX11(el, w), err
}

// recordedX11 is an AT-SPI element as the recorder keeps it: the app is the window's WM_CLASS.
func recordedX11(el *computeruse.Element, w x11.Owner) *ax.Recorded {
	if el == nil {
		return nil
	}
	return &ax.Recorded{Role: el.Role, Name: el.Name, Secure: el.Secure, PID: w.PID, App: w.Class}
}

// Keysyms (keysymdef.h), modifier masks and buttons a recording tells apart.
const (
	symBackSpace   = 0xff08
	symTab         = 0xff09
	symReturn      = 0xff0d
	symEscape      = 0xff1b
	symKPEnter     = 0xff8d
	symISOLeftTab  = 0xfe20
	symISOLevel3   = 0xfe03 // AltGr: a level, not a shortcut
	symEuroSign    = 0x20ac
	symShiftL      = 0xffe1
	symShiftR      = 0xffe2
	symControlL    = 0xffe3
	symControlR    = 0xffe4
	symCapsLock    = 0xffe5
	symMetaL       = 0xffe7
	symMetaR       = 0xffe8
	symAltL        = 0xffe9
	symAltR        = 0xffea
	symSuperL      = 0xffeb
	symSuperR      = 0xffec
	x11ShiftMask   = 1 << 0
	x11LockMask    = 1 << 1
	x11ControlMask = 1 << 2
	x11Mod1Mask    = 1 << 3 // Alt
	x11Mod4Mask    = 1 << 6 // Super
	x11ButtonLeft  = 1
	x11ButtonRight = 3
	x11WheelUp     = 4
	x11WheelDown   = 5
	x11WheelLeft   = 6
	x11WheelRight  = 7
)

var x11Named = map[uint32]string{symReturn: KeyEnter, symKPEnter: KeyEnter, symTab: KeyTab, symISOLeftTab: KeyTab,
	symEscape: KeyEscape, symBackSpace: KeyBackspace}

// x11Modifiers are the modifier keys and what they hold.
var x11Modifiers = map[uint32]string{symShiftL: "shift", symShiftR: "shift", symControlL: "ctrl",
	symControlR: "ctrl", symAltL: "alt", symAltR: "alt", symMetaL: "alt", symMetaR: "alt",
	symSuperL: "super", symSuperR: "super", symISOLevel3: "altgr"}

// x11Keys decodes recorded device events into raw input. It tracks the modifier keys itself
// (RECORD's state field isn't reliable for device events) and also trusts the state's bits.
// Only the tap's goroutine calls it.
type x11Keys struct {
	km   x11.Keymap
	held map[byte]string // modifier keycodes down
	caps bool            // Caps Lock toggled on during the recording
}

func (k *x11Keys) input(ev x11.RecordedInput, at time.Time) (DeskInput, bool) {
	switch ev.Type {
	case x11.ButtonPress:
		in := DeskInput{At: at, X: float64(ev.RootX), Y: float64(ev.RootY)}
		switch ev.Detail {
		case x11ButtonLeft, x11ButtonRight:
			in.Kind, in.Button = InClick, "left"
			if ev.Detail == x11ButtonRight {
				in.Button = "right"
			}
		case x11WheelUp, x11WheelDown, x11WheelLeft, x11WheelRight:
			in.Kind = InScroll
			in.DY = map[byte]int{x11WheelUp: -1, x11WheelDown: 1}[ev.Detail]
			in.DX = map[byte]int{x11WheelLeft: -1, x11WheelRight: 1}[ev.Detail]
		default:
			return DeskInput{}, false // the middle button
		}
		return in, true
	case x11.KeyRelease:
		delete(k.held, ev.Detail)
		return DeskInput{}, false
	case x11.KeyPress:
	default:
		return DeskInput{}, false
	}
	sym := k.km.Sym(ev.Detail, 0)
	if m, ok := x11Modifiers[sym]; ok {
		if k.held == nil {
			k.held = map[byte]string{}
		}
		k.held[ev.Detail] = m
		return DeskInput{}, false
	}
	if sym == symCapsLock {
		k.caps = !k.caps
		return DeskInput{}, false
	}
	mods := map[string]bool{
		"shift": ev.State&x11ShiftMask != 0, "ctrl": ev.State&x11ControlMask != 0,
		"alt": ev.State&x11Mod1Mask != 0, "super": ev.State&x11Mod4Mask != 0,
	}
	for _, m := range k.held {
		mods[m] = true
	}
	in := DeskInput{Kind: InKey, At: at}
	named := x11Named[sym]
	shortcut := mods["ctrl"] || mods["alt"] || mods["super"]
	if named == KeyTab && !shortcut && mods["shift"] {
		in.Key = "shift+tab" // moves focus back, so a replay must not press plain Tab
		return in, true
	}
	if shortcut {
		base := strings.ToLower(named)
		if r := x11Rune(sym); base == "" && r != 0 && !unicode.IsSpace(r) {
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
	r := x11Rune(sym)
	upper := mods["shift"] != ((k.caps || ev.State&x11LockMask != 0) && unicode.IsLetter(r))
	level := 0
	if mods["altgr"] && k.km.Per > 4 { // the core mapping's group 1 levels 3 and 4
		level = 4
	}
	if upper {
		level++
	}
	if s := k.km.Sym(ev.Detail, level); level > 0 && s != 0 {
		r = x11Rune(s)
	} else if upper {
		r = unicode.ToUpper(r)
	}
	if r == 0 || !typable(string(r)) {
		in.Key = keyOther
		return in, true
	}
	in.Text = string(r)
	return in, true
}

// x11Rune is the character a keysym types: Latin-1 and Unicode keysyms, and the euro sign's
// legacy one (what AltGr+E gives on European layouts).
func x11Rune(sym uint32) rune {
	if sym == symEuroSign {
		return '€'
	}
	return xkb.Rune(sym)
}
