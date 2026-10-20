package input

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/vptr"
	"github.com/alexberardi/jarvis-server/internal/agent/xkb"
)

// WlPointer posts virtual-pointer events (vptr.Pointer).
type WlPointer interface {
	Available(ctx context.Context) (bool, string)
	Send(ctx context.Context, evs ...vptr.Event) error
}

// WlKeyboard types strokes through the virtual keyboard (vkbd.Keyboard).
type WlKeyboard interface {
	Available(ctx context.Context) (bool, string)
	Send(ctx context.Context, strokes []xkb.Stroke) error
}

var wlButtons = map[string]uint32{"left": vptr.BtnLeft, "right": vptr.BtnRight, "middle": vptr.BtnMiddle}

// wlModifiers are the chord modifiers: their XKB mask and their own key (for a chord of
// modifiers alone). cmd, meta and super are the Super (logo) key.
var wlModifiers = map[string]struct {
	mask uint32
	sym  uint32
}{
	"cmd": {xkb.Mod4Mask, xkb.KeySuperL}, "meta": {xkb.Mod4Mask, xkb.KeySuperL}, "super": {xkb.Mod4Mask, xkb.KeySuperL},
	"shift": {xkb.ShiftMask, xkb.KeyShiftL}, "alt": {xkb.Mod1Mask, xkb.KeyAltL}, "ctrl": {xkb.ControlMask, xkb.KeyControlL},
}

// wlKeysyms are the named keys' keysyms; letters, digits and punctuation are their own.
var wlKeysyms = map[string]uint32{
	"return": xkb.KeyReturn, "enter": xkb.KeyReturn, "tab": xkb.KeyTab, "escape": xkb.KeyEscape, "esc": xkb.KeyEscape,
	"space": xkb.KeySpace, "backspace": xkb.KeyBackSpace, "delete": xkb.KeyDelete, "forwarddelete": xkb.KeyDelete,
	"insert": xkb.KeyInsert, "home": xkb.KeyHome, "end": xkb.KeyEnd, "pageup": xkb.KeyPageUp, "pagedown": xkb.KeyPageDown,
	"left": xkb.KeyLeft, "right": xkb.KeyRight, "up": xkb.KeyUp, "down": xkb.KeyDown,
}

func init() {
	for i := range 12 {
		wlKeysyms[fmt.Sprintf("f%d", i+1)] = xkb.KeyF1 + uint32(i)
	}
}

// wlKeysym is a key name's keysym.
func wlKeysym(n string) (uint32, bool) {
	if s, ok := wlKeysyms[n]; ok {
		return s, true
	}
	if r := []rune(n); len(r) == 1 && r[0] > ' ' && r[0] < 0x7f {
		return uint32(r[0]), true
	}
	return 0, false
}

// wlChord is a chord as one stroke: the key (or the last modifier's own key) with the others held.
func wlChord(names []string) (xkb.Stroke, error) {
	var s xkb.Stroke
	var key, lastMod string
	for _, n := range names {
		n = strings.ToLower(n)
		if mod, ok := wlModifiers[n]; ok {
			if mod.mask == xkb.ShiftMask {
				s.Shift = true
			} else {
				s.Mods |= mod.mask
			}
			lastMod = n
			continue
		}
		sym, ok := wlKeysym(n)
		if !ok {
			return xkb.Stroke{}, fmt.Errorf("unknown key %q", n)
		}
		if key != "" {
			return xkb.Stroke{}, fmt.Errorf("chord has two keys, %q and %q", key, n)
		}
		key, s.Sym = n, sym
	}
	switch {
	case key != "":
		return s, nil
	case lastMod != "":
		s.Sym = wlModifiers[lastMod].sym
		return s, nil
	}
	return xkb.Stroke{}, errors.New("empty chord")
}

// Wlroots posts input on a wlroots compositor (Hyprland, sway): the virtual pointer for the
// pointer, the virtual keyboard for keys and text.
type Wlroots struct {
	Pointer  WlPointer
	Keyboard WlKeyboard
	Getenv   func(string) string // os.Getenv when nil
	// Output runs a command and returns its stdout (hyprctl); exec when nil.
	Output func(ctx context.Context, name string, args ...string) ([]byte, error)

	mu    sync.Mutex // serializes posting calls; held across post
	moved bool       // the pointer was put at last by Move, Click, Drag or Scroll
	last  [2]int

	injectWindow
}

func (w *Wlroots) getenv(s string) string {
	if w.Getenv != nil {
		return w.Getenv(s)
	}
	return os.Getenv(s)
}

func (w *Wlroots) Available(ctx context.Context) (bool, string) {
	if ok, why := w.Pointer.Available(ctx); !ok {
		return false, why
	}
	return w.Keyboard.Available(ctx)
}

// inject runs post with the injection window recorded around it. Calls are serialized.
// Availability is the backends' to report: each fails with its reason.
func (w *Wlroots) inject(ctx context.Context, post func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	start := time.Now()
	w.set(start, time.Time{})
	defer func() { w.set(start, time.Now()) }()
	return post()
}

// pointer posts evs.
func (w *Wlroots) pointer(ctx context.Context, evs ...vptr.Event) error {
	return w.inject(ctx, func() error { return w.Pointer.Send(ctx, evs...) })
}

func (w *Wlroots) at(x, y int) {
	w.moved, w.last = true, [2]int{x, y}
}

func wlButton(b string) (uint32, error) {
	if b == "" {
		b = "left"
	}
	code, ok := wlButtons[b]
	if !ok {
		return 0, fmt.Errorf("unknown button %q", b)
	}
	return code, nil
}

func (w *Wlroots) Move(ctx context.Context, x, y int) error {
	return w.inject(ctx, func() error {
		if err := w.Pointer.Send(ctx, vptr.Motion(x, y)); err != nil {
			return err
		}
		w.at(x, y)
		return nil
	})
}

// Button presses or releases where the pointer is; the compositor keeps a pressed button held,
// so a Move after it drags.
func (w *Wlroots) Button(ctx context.Context, b string, down bool) error {
	code, err := wlButton(b)
	if err != nil {
		return err
	}
	ev := vptr.Release(code)
	if down {
		ev = vptr.Press(code)
	}
	return w.pointer(ctx, ev)
}

func (w *Wlroots) Click(ctx context.Context, x, y int, b string, n int) error {
	code, err := wlButton(b)
	if err != nil {
		return err
	}
	if n < 1 || n > 3 {
		return fmt.Errorf("click count %d: want 1 to 3", n)
	}
	evs := []vptr.Event{vptr.Motion(x, y)}
	for range n {
		evs = append(evs, vptr.Press(code), vptr.Release(code))
	}
	return w.inject(ctx, func() error {
		if err := w.Pointer.Send(ctx, evs...); err != nil {
			return err
		}
		w.at(x, y)
		return nil
	})
}

func (w *Wlroots) Drag(ctx context.Context, path [][2]int) error {
	if len(path) < 2 {
		return errors.New("drag needs at least two points")
	}
	evs := []vptr.Event{vptr.Motion(path[0][0], path[0][1]), vptr.Press(vptr.BtnLeft)}
	for _, p := range path[1:] {
		evs = append(evs, vptr.Motion(p[0], p[1]))
	}
	evs = append(evs, vptr.Release(vptr.BtnLeft))
	last := path[len(path)-1]
	return w.inject(ctx, func() error {
		if err := w.Pointer.Send(ctx, evs...); err != nil {
			return err
		}
		w.at(last[0], last[1])
		return nil
	})
}

func (w *Wlroots) Scroll(ctx context.Context, x, y, dx, dy int) error {
	return w.inject(ctx, func() error {
		if err := w.Pointer.Send(ctx, vptr.Motion(x, y), vptr.Wheel(dx, dy)); err != nil {
			return err
		}
		w.at(x, y)
		return nil
	})
}

func (w *Wlroots) Keys(ctx context.Context, names []string) error {
	s, err := wlChord(names)
	if err != nil {
		return err
	}
	return w.inject(ctx, func() error { return w.Keyboard.Send(ctx, []xkb.Stroke{s}) })
}

func (w *Wlroots) Type(ctx context.Context, text string) error {
	strokes, ok := xkb.Strokes(text)
	if !ok {
		return errors.New("text has a control character")
	}
	return w.inject(ctx, func() error { return w.Keyboard.Send(ctx, strokes) })
}

// Cursor is where the pointer is: Hyprland says (hyprctl -j cursorpos); sway's IPC doesn't
// (get_seats lists devices, not the cursor), so there it is where Jarvis last put it.
func (w *Wlroots) Cursor(ctx context.Context) (x, y int, err error) {
	if w.getenv("HYPRLAND_INSTANCE_SIGNATURE") != "" {
		out, err := w.output(ctx, "hyprctl", "-j", "cursorpos")
		if err != nil {
			return 0, 0, fmt.Errorf("hyprctl cursorpos: %w", err)
		}
		var pos struct{ X, Y *float64 }
		if err := json.Unmarshal(out, &pos); err != nil || pos.X == nil || pos.Y == nil {
			return 0, 0, fmt.Errorf("hyprctl cursorpos: unexpected output %q", strings.TrimSpace(string(out)))
		}
		return int(math.Round(*pos.X)), int(math.Round(*pos.Y)), nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.moved {
		return 0, 0, errors.New("the compositor doesn't say where the pointer is, and Jarvis hasn't moved it yet")
	}
	return w.last[0], w.last[1], nil
}

func (w *Wlroots) output(ctx context.Context, name string, args ...string) ([]byte, error) {
	if w.Output != nil {
		return w.Output(ctx, name, args...)
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).Output()
}
