package input

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/dictate"
)

// MacPoster posts CGEvents (mac_darwin.go: CoreGraphics over purego; nil elsewhere).
type MacPoster interface {
	dictate.MacPoster // Unicode strings for Type
	// Mouse posts one mouse event of CGEventType typ at x, y with kCGMouseEventClickState clicks.
	Mouse(typ uint32, x, y float64, button uint32, clicks int64) error
	// Wheel posts a line-unit scroll at the pointer; CoreGraphics' sign: wheel1 > 0 is up,
	// wheel2 > 0 is left.
	Wheel(wheel1, wheel2 int32) error
	// KeyEvent posts a virtual key code down or up with these CGEventFlags.
	KeyEvent(code uint16, down bool, flags uint64) error
	// Location is the pointer in global coordinates.
	Location() (x, y float64, err error)
}

// CGEventType values (CGEventTypes.h).
const (
	evLeftDown     = 1
	evLeftUp       = 2
	evRightDown    = 3
	evRightUp      = 4
	evMoved        = 5
	evLeftDragged  = 6
	evRightDragged = 7
	evOtherDown    = 25
	evOtherUp      = 26
	evOtherDragged = 27
)

// CGEventFlags masks for the chord modifiers.
const (
	flagShift = 0x20000
	flagCtrl  = 0x40000
	flagAlt   = 0x80000
	flagCmd   = 0x100000
)

type macButton struct {
	num               uint32 // CGMouseButton
	down, up, dragged uint32
}

var macButtons = map[string]macButton{
	"left":   {0, evLeftDown, evLeftUp, evLeftDragged},
	"right":  {1, evRightDown, evRightUp, evRightDragged},
	"middle": {2, evOtherDown, evOtherUp, evOtherDragged},
}

// macModifiers are the chord modifiers: their flag and their own key code (for a chord of
// modifiers alone).
var macModifiers = map[string]struct {
	flag uint64
	code uint16
}{
	"cmd": {flagCmd, 55}, "meta": {flagCmd, 55}, "super": {flagCmd, 55},
	"shift": {flagShift, 56}, "alt": {flagAlt, 58}, "ctrl": {flagCtrl, 59},
}

// macKeyCodes are kVK_* virtual key codes (HIToolbox Events.h, ANSI layout positions). backspace
// is the Mac's delete key; delete is forward delete, as on the other OSes.
var macKeyCodes = map[string]uint16{
	"a": 0, "s": 1, "d": 2, "f": 3, "h": 4, "g": 5, "z": 6, "x": 7, "c": 8, "v": 9, "b": 11,
	"q": 12, "w": 13, "e": 14, "r": 15, "y": 16, "t": 17, "o": 31, "u": 32, "i": 34, "p": 35,
	"l": 37, "j": 38, "k": 40, "n": 45, "m": 46,
	"1": 18, "2": 19, "3": 20, "4": 21, "6": 22, "5": 23, "9": 25, "7": 26, "8": 28, "0": 29,
	"=": 24, "-": 27, "]": 30, "[": 33, "'": 39, ";": 41, `\`: 42, ",": 43, "/": 44, ".": 47, "`": 50,
	"f1": 122, "f2": 120, "f3": 99, "f4": 118, "f5": 96, "f6": 97, "f7": 98, "f8": 100,
	"f9": 101, "f10": 109, "f11": 103, "f12": 111,
	"left": 123, "right": 124, "down": 125, "up": 126,
	"home": 115, "end": 119, "pageup": 116, "pagedown": 121,
	"backspace": 51, "delete": 117, "forwarddelete": 117, "insert": 114,
	"return": 36, "enter": 36, "tab": 48, "escape": 53, "esc": 53, "space": 49,
}

// Mac posts CGEvents. Posting needs the Accessibility grant the agent already asks for
// (macax.Trusted).
type Mac struct {
	Trusted func() bool
	Post    MacPoster

	mu   sync.Mutex      // serializes posting calls; held across post
	held map[string]bool // buttons down from Button, so Move drags them

	injectWindow
}

func (m *Mac) Available(context.Context) (bool, string) {
	if m.Post == nil {
		return false, "controlling the computer isn't supported in this build"
	}
	if m.Trusted == nil || !m.Trusted() {
		return false, "controlling the computer needs Accessibility for Jarvis Automation Agent: turn it on in System Settings > Privacy & Security > Accessibility"
	}
	return true, ""
}

// inject runs post with the injection window recorded around it. Calls are serialized.
func (m *Mac) inject(ctx context.Context, post func() error) error {
	if ok, why := m.Available(ctx); !ok {
		return fmt.Errorf("%w: %s", ErrUnavailable, why)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	start := time.Now()
	m.set(start, time.Time{})
	defer func() { m.set(start, time.Now()) }()
	return post()
}

func button(b string) (macButton, error) {
	if b == "" {
		b = "left"
	}
	mb, ok := macButtons[b]
	if !ok {
		return macButton{}, fmt.Errorf("unknown button %q", b)
	}
	return mb, nil
}

// move posts a move, or a drag of the first button held.
func (m *Mac) move(x, y int) error {
	typ, num := uint32(evMoved), uint32(0)
	for _, name := range []string{"left", "right", "middle"} {
		if m.held[name] {
			typ, num = macButtons[name].dragged, macButtons[name].num
			break
		}
	}
	return m.Post.Mouse(typ, float64(x), float64(y), num, 0)
}

func (m *Mac) Move(ctx context.Context, x, y int) error {
	return m.inject(ctx, func() error { return m.move(x, y) })
}

func (m *Mac) Button(ctx context.Context, b string, down bool) error {
	mb, err := button(b)
	if err != nil {
		return err
	}
	if b == "" {
		b = "left"
	}
	return m.inject(ctx, func() error {
		x, y, err := m.Post.Location()
		if err != nil {
			return err
		}
		typ := mb.up
		if down {
			typ = mb.down
		}
		if err := m.Post.Mouse(typ, x, y, mb.num, 1); err != nil {
			return err
		}
		if m.held == nil {
			m.held = map[string]bool{}
		}
		m.held[b] = down
		return nil
	})
}

func (m *Mac) Click(ctx context.Context, x, y int, b string, n int) error {
	mb, err := button(b)
	if err != nil {
		return err
	}
	if n < 1 || n > 3 {
		return fmt.Errorf("click count %d: want 1 to 3", n)
	}
	return m.inject(ctx, func() error {
		if err := m.move(x, y); err != nil {
			return err
		}
		fx, fy := float64(x), float64(y)
		for i := 1; i <= n; i++ {
			if err := m.Post.Mouse(mb.down, fx, fy, mb.num, int64(i)); err != nil {
				return err
			}
			if err := m.Post.Mouse(mb.up, fx, fy, mb.num, int64(i)); err != nil {
				return err
			}
		}
		return nil
	})
}

func (m *Mac) Drag(ctx context.Context, path [][2]int) error {
	if len(path) < 2 {
		return errors.New("drag needs at least two points")
	}
	return m.inject(ctx, func() error {
		p0 := path[0]
		if err := m.Post.Mouse(evMoved, float64(p0[0]), float64(p0[1]), 0, 0); err != nil {
			return err
		}
		if err := m.Post.Mouse(evLeftDown, float64(p0[0]), float64(p0[1]), 0, 1); err != nil {
			return err
		}
		for _, p := range path[1:] {
			if err := m.Post.Mouse(evLeftDragged, float64(p[0]), float64(p[1]), 0, 0); err != nil {
				return err
			}
		}
		last := path[len(path)-1]
		return m.Post.Mouse(evLeftUp, float64(last[0]), float64(last[1]), 0, 1)
	})
}

func (m *Mac) Scroll(ctx context.Context, x, y, dx, dy int) error {
	return m.inject(ctx, func() error {
		if err := m.move(x, y); err != nil {
			return err
		}
		// Our dy > 0 is down and dx > 0 right; CoreGraphics' wheels are the other way round.
		return m.Post.Wheel(int32(-dy), int32(-dx))
	})
}

// chord splits names into the modifier flags and the one key to press.
func chord(names []string) (code uint16, flags uint64, err error) {
	var key, lastMod string
	for _, n := range names {
		n = strings.ToLower(n)
		if mod, ok := macModifiers[n]; ok {
			flags |= mod.flag
			lastMod = n
			continue
		}
		if _, ok := macKeyCodes[n]; !ok {
			return 0, 0, fmt.Errorf("unknown key %q", n)
		}
		if key != "" {
			return 0, 0, fmt.Errorf("chord has two keys, %q and %q", key, n)
		}
		key = n
	}
	switch {
	case key != "":
		return macKeyCodes[key], flags, nil
	case lastMod != "":
		return macModifiers[lastMod].code, flags, nil
	}
	return 0, 0, errors.New("empty chord")
}

func (m *Mac) Keys(ctx context.Context, names []string) error {
	code, flags, err := chord(names)
	if err != nil {
		return err
	}
	return m.inject(ctx, func() error {
		if err := m.Post.KeyEvent(code, true, flags); err != nil {
			return err
		}
		return m.Post.KeyEvent(code, false, flags)
	})
}

func (m *Mac) Type(ctx context.Context, text string) error {
	return m.inject(ctx, func() error {
		return (&dictate.MacKeyboard{Trusted: m.Trusted, Post: m.Post}).Type(ctx, text)
	})
}

func (m *Mac) Cursor(ctx context.Context) (x, y int, err error) {
	if ok, why := m.Available(ctx); !ok {
		return 0, 0, fmt.Errorf("%w: %s", ErrUnavailable, why)
	}
	fx, fy, err := m.Post.Location()
	if err != nil {
		return 0, 0, err
	}
	return int(math.Round(fx)), int(math.Round(fy)), nil
}
