// Package x11 is the agent's X11 desktop control without helper programs: typing and keys
// through the XTEST extension (what xdotool does) and window list, focus, close and minimise
// through EWMH (what wmctrl does), spoken over a pure-Go X11 connection (github.com/jezek/xgb on
// Linux). The policy-free logic here talks to a narrow Conn, so it is unit-tested with a fake on
// every OS; the real Conn is xgb_linux.go.
package x11

import (
	"cmp"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/alexberardi/jarvis-server/internal/agent/xkb"
)

// Conn is the slice of the X protocol this package uses. One Conn is one short-lived
// connection (opened per call, never cached: sessions come and go).
type Conn interface {
	Root() uint32
	// Atom interns name (creating it if needed).
	Atom(name string) (uint32, error)
	// Prop reads a whole property: its type, format (8/16/32) and raw bytes (native order
	// already decoded to little-endian 32-bit values for format 32). A missing property is
	// typ 0 and no error.
	Prop(win, atom uint32) (typ uint32, format byte, data []byte, err error)
	// ClientMessage sends a 32-bit client message about win to the root window with
	// SubstructureRedirect|SubstructureNotify (how EWMH and ICCCM requests reach the WM).
	ClientMessage(win, typ uint32, data [5]uint32) error
	// WindowMessage sends a 32-bit client message to win itself (ICCCM WM_PROTOCOLS).
	WindowMessage(win, typ uint32, data [5]uint32) error
	HasXTest() bool
	KeycodeRange() (min, max byte)
	// Keymap is the core keyboard mapping for min..max: perCode keysyms per keycode.
	Keymap() (perCode int, syms []uint32, err error)
	// SetKeysyms replaces one keycode's keysyms (len = perCode).
	SetKeysyms(code byte, syms []uint32) error
	// ShiftKeycode is a keycode bound to the Shift modifier (0: none).
	ShiftKeycode() (byte, error)
	// LockOn reports whether Caps Lock is on.
	LockOn() (bool, error)
	// FakeKey presses or releases a key through XTEST.
	FakeKey(code byte, press bool) error
	// FakeMotion moves the pointer to x, y on the root window through XTEST.
	FakeMotion(x, y int) error
	// FakeButton presses or releases core pointer button b (1-3; 4-7 are the wheel) through XTEST.
	FakeButton(b byte, down bool) error
	// QueryPointer is where the pointer is, in root coordinates.
	QueryPointer() (x, y int, err error)
	// GetImage reads r of win as a ZPixmap: the drawable's depth and 32 bits per pixel,
	// little-endian (B, G, R, pad). Other pixel layouts are an error.
	GetImage(win uint32, r image.Rectangle) (depth byte, data []byte, err error)
	// Geometry is win's rectangle with its origin in root coordinates.
	Geometry(win uint32) (x, y, w, h int, err error)
	// Sync waits until the server has processed every request sent so far.
	Sync() error
	// IdleTime is how long since the last input event, ours (XTEST) included
	// (MIT-SCREEN-SAVER QueryInfo).
	IdleTime() (time.Duration, error)
	// GrabKey grabs key code with exactly modifiers mods on the root window (asynchronous
	// modes): its presses come to this connection only. An error: another client holds it.
	GrabKey(code byte, mods uint16) error
	// NextKeyPress waits for the next key press on this connection (a grabbed key's) and
	// returns its key code and modifier state; an error once the connection has ended.
	NextKeyPress() (code byte, state uint16, err error)
	Close()
}

// Display is an X11 display. The zero value uses the real connection for $DISPLAY.
type Display struct {
	Getenv func(string) string  // os.Getenv when nil
	Dial   func() (Conn, error) // the real connection when nil
	// Settle is how long clients get to take in a changed keyboard mapping before keys that
	// use it (and before it is put back after them). Zero: DefaultSettle; negative: none.
	Settle time.Duration
	Sleep  func(context.Context, time.Duration) error // a real timer when nil
}

// DefaultSettle: an X client reads a changed mapping lazily (on MappingNotify it drops its
// cache and asks again when it next translates a key), so a mapping must stay put while the
// keys that use it are still queued, and arrive before them. xdotool sleeps around its scratch
// keycode for the same reason.
const DefaultSettle = 50 * time.Millisecond

func (d *Display) getenv(k string) string {
	if d.Getenv != nil {
		return d.Getenv(k)
	}
	return osGetenv(k)
}

// Session says whether this is an X11 session: DISPLAY set and not a Wayland session
// (XWayland's DISPLAY would only reach XWayland clients and lie about the rest).
func (d *Display) Session() (bool, string) {
	if d.getenv("WAYLAND_DISPLAY") != "" || d.getenv("XDG_SESSION_TYPE") == "wayland" {
		return false, "a Wayland session (XWayland only reaches X11 apps)"
	}
	if d.getenv("DISPLAY") == "" {
		return false, "no X11 display (DISPLAY is unset)"
	}
	return true, ""
}

func (d *Display) open() (Conn, error) {
	if ok, why := d.Session(); !ok {
		return nil, errors.New(why)
	}
	dial := d.Dial
	if dial == nil {
		dial = dialX
	}
	c, err := dial()
	if err != nil {
		return nil, fmt.Errorf("can't reach the X server: %w", err)
	}
	return c, nil
}

func (d *Display) sleep(ctx context.Context, dur time.Duration) error {
	if d.Sleep != nil {
		return d.Sleep(ctx, dur)
	}
	if dur <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(dur)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d *Display) settle() time.Duration { return cmp.Or(d.Settle, DefaultSettle) }

// --- properties ---

func u32s(format byte, data []byte) []uint32 {
	if format != 32 {
		return nil
	}
	out := make([]uint32, len(data)/4)
	for i := range out {
		out[i] = binary.LittleEndian.Uint32(data[4*i:])
	}
	return out
}

func prop32(c Conn, win uint32, name string) ([]uint32, error) {
	a, err := c.Atom(name)
	if err != nil {
		return nil, err
	}
	_, f, data, err := c.Prop(win, a)
	if err != nil {
		return nil, err
	}
	return u32s(f, data), nil
}

func propString(c Conn, win uint32, name string) (string, error) {
	a, err := c.Atom(name)
	if err != nil {
		return "", err
	}
	_, f, data, err := c.Prop(win, a)
	if err != nil || f != 8 {
		return "", err
	}
	return string(data), nil
}

// FormatID is the id form wmctrl prints (and host.Linux validates): 0x + 8 hex digits.
func FormatID(w uint32) string { return fmt.Sprintf("0x%08x", w) }

// ParseID reads a FormatID id (any 0x-prefixed hex, as wmctrl's ids too).
func ParseID(id string) (uint32, bool) {
	s := strings.ToLower(id)
	if !strings.HasPrefix(s, "0x") || len(s) < 3 || len(s) > 10 {
		return 0, false
	}
	var v uint32
	for _, ch := range s[2:] {
		switch {
		case ch >= '0' && ch <= '9':
			v = v<<4 | uint32(ch-'0')
		case ch >= 'a' && ch <= 'f':
			v = v<<4 | uint32(ch-'a'+10)
		default:
			return 0, false
		}
	}
	return v, v != 0
}

// --- typing (XTEST) ---

// TypingAvailable: an X11 session whose server has XTEST.
func (d *Display) TypingAvailable(context.Context) (bool, string) {
	c, err := d.open()
	if err != nil {
		return false, err.Error()
	}
	defer c.Close()
	if !c.HasXTest() {
		return false, "the X server has no XTEST extension"
	}
	return true, ""
}

type keyPos struct {
	code  byte
	shift bool
}

// Send types strokes into whatever has focus. A keysym already on a key is typed on that key
// (with Shift for its second level); one that isn't is put on a spare keycode (one with no
// keysyms) for the duration and then taken off again, the way xdotool does it, several spare
// keycodes at a time.
func (d *Display) Send(ctx context.Context, strokes []xkb.Stroke) error {
	c, err := d.open()
	if err != nil {
		return err
	}
	defer c.Close()
	if !c.HasXTest() {
		return errors.New("the X server has no XTEST extension")
	}
	lo, _ := c.KeycodeRange()
	per, syms, err := c.Keymap()
	if err != nil {
		return fmt.Errorf("keyboard mapping: %w", err)
	}
	if per < 1 {
		return errors.New("keyboard mapping: no keysyms per keycode")
	}
	shiftCode, err := c.ShiftKeycode()
	if err != nil {
		return fmt.Errorf("modifier mapping: %w", err)
	}
	lock, _ := c.LockOn()

	have := map[uint32]keyPos{}
	var spare []byte
	for i := 0; i*per < len(syms); i++ {
		code := byte(int(lo) + i)
		row := syms[i*per : min((i+1)*per, len(syms))]
		empty := true
		for _, s := range row {
			if s != 0 {
				empty = false
			}
		}
		if empty {
			if code != 0 && code >= 8 {
				spare = append(spare, code)
			}
			continue
		}
		// Level 2 first, so level 1 wins for a keysym on both.
		if len(row) > 1 && row[1] != 0 && shiftCode != 0 {
			if _, ok := have[row[1]]; !ok {
				have[row[1]] = keyPos{code: code, shift: true}
			}
		}
		if row[0] != 0 {
			if p, ok := have[row[0]]; !ok || p.shift {
				have[row[0]] = keyPos{code: code}
			}
		}
	}

	// Plan: runs of strokes needing at most len(spare) keysyms that aren't on a key.
	type run struct {
		remap   map[uint32]byte
		order   []uint32 // remap's keys in the order they were assigned
		strokes []xkb.Stroke
	}
	var runs []run
	cur := run{remap: map[uint32]byte{}}
	for _, s := range strokes {
		if _, ok := have[s.Sym]; !ok {
			if _, ok := cur.remap[s.Sym]; !ok {
				if len(spare) == 0 {
					return fmt.Errorf("no spare keycode to type %q with", string(xkb.Rune(s.Sym)))
				}
				if len(cur.remap) == len(spare) {
					runs = append(runs, cur)
					cur = run{remap: map[uint32]byte{}}
				}
				cur.remap[s.Sym] = spare[len(cur.remap)]
				cur.order = append(cur.order, s.Sym)
			}
		}
		cur.strokes = append(cur.strokes, s)
	}
	if len(cur.strokes) > 0 {
		runs = append(runs, cur)
	}

	var remapped []byte // spare keycodes changed so far (each once)
	defer func() {
		if len(remapped) == 0 {
			return
		}
		// Keys still queued in a client must meet the mapping they were typed with.
		_ = c.Sync()
		_ = d.sleep(context.WithoutCancel(ctx), d.settle())
		blank := make([]uint32, per)
		for _, code := range remapped {
			_ = c.SetKeysyms(code, blank)
		}
		_ = c.Sync()
	}()

	for _, r := range runs {
		if len(r.remap) > 0 {
			if len(remapped) > 0 {
				// The previous run's keys must be read before their keycodes change.
				if err := c.Sync(); err != nil {
					return err
				}
				if err := d.sleep(ctx, d.settle()); err != nil {
					return err
				}
			}
			for _, sym := range r.order {
				code := r.remap[sym]
				row := make([]uint32, per)
				row[0] = sym
				if per > 1 {
					row[1] = sym // Shift or Caps Lock can't change it
				}
				if err := c.SetKeysyms(code, row); err != nil {
					return fmt.Errorf("keyboard mapping: %w", err)
				}
				if !slices.Contains(remapped, code) {
					remapped = append(remapped, code)
				}
			}
			if err := c.Sync(); err != nil {
				return err
			}
			if err := d.sleep(ctx, d.settle()); err != nil {
				return err
			}
		}
		for _, s := range r.strokes {
			if err := ctx.Err(); err != nil {
				return err
			}
			p, ok := have[s.Sym]
			if code, re := r.remap[s.Sym]; re {
				p, ok = keyPos{code: code}, true
			}
			if !ok {
				return fmt.Errorf("no key for keysym %#x", s.Sym)
			}
			shift := p.shift || s.Shift
			if lock && cased(s.Sym) && r.remap[s.Sym] == 0 {
				shift = !shift // Caps Lock flips the level of letters
			}
			if shift && shiftCode == 0 {
				return errors.New("no Shift key in the keyboard mapping")
			}
			if shift {
				if err := c.FakeKey(shiftCode, true); err != nil {
					return err
				}
			}
			if err := c.FakeKey(p.code, true); err != nil {
				return err
			}
			if err := c.FakeKey(p.code, false); err != nil {
				return err
			}
			if shift {
				if err := c.FakeKey(shiftCode, false); err != nil {
					return err
				}
			}
		}
		if err := c.Sync(); err != nil {
			return err
		}
	}
	return nil
}

// cased: a letter Caps Lock changes.
func cased(sym uint32) bool {
	r := xkb.Rune(sym)
	return r != 0 && unicode.IsLetter(r) && unicode.ToUpper(r) != unicode.ToLower(r)
}

// --- windows (EWMH) ---

// Window is one managed top-level window.
type Window struct {
	ID        uint32
	Title     string
	Class     string // WM_CLASS as wmctrl -x prints it: instance.Class
	Minimized bool
}

// WMAvailable: an X11 session with an EWMH window manager that lists its clients and
// activates windows on request.
func (d *Display) WMAvailable(context.Context) (bool, string) {
	c, err := d.open()
	if err != nil {
		return false, err.Error()
	}
	defer c.Close()
	return wmCheck(c)
}

func wmCheck(c Conn) (bool, string) {
	check, err := prop32(c, c.Root(), "_NET_SUPPORTING_WM_CHECK")
	if err != nil {
		return false, err.Error()
	}
	if len(check) == 0 || check[0] == 0 {
		return false, "no EWMH window manager is running"
	}
	sup, err := supported(c)
	if err != nil {
		return false, err.Error()
	}
	for _, need := range []string{"_NET_CLIENT_LIST", "_NET_ACTIVE_WINDOW"} {
		if !sup[need] {
			return false, "the window manager doesn't support " + need
		}
	}
	return true, ""
}

func supported(c Conn) (map[string]bool, error) {
	atoms, err := prop32(c, c.Root(), "_NET_SUPPORTED")
	if err != nil {
		return nil, err
	}
	want := []string{"_NET_CLIENT_LIST", "_NET_ACTIVE_WINDOW", "_NET_CLOSE_WINDOW", "_NET_CURRENT_DESKTOP", "_NET_WM_DESKTOP", "_NET_CLIENT_LIST_STACKING"}
	byAtom := map[uint32]string{}
	for _, n := range want {
		a, err := c.Atom(n)
		if err != nil {
			return nil, err
		}
		byAtom[a] = n
	}
	out := map[string]bool{}
	for _, a := range atoms {
		if n, ok := byAtom[a]; ok {
			out[n] = true
		}
	}
	return out, nil
}

// Windows lists the window manager's clients (_NET_CLIENT_LIST, in its order).
func (d *Display) Windows(context.Context) ([]Window, error) {
	c, err := d.open()
	if err != nil {
		return nil, err
	}
	defer c.Close()
	if ok, why := wmCheck(c); !ok {
		return nil, errors.New(why)
	}
	ids, err := prop32(c, c.Root(), "_NET_CLIENT_LIST")
	if err != nil {
		return nil, err
	}
	hidden, err := c.Atom("_NET_WM_STATE_HIDDEN")
	if err != nil {
		return nil, err
	}
	out := make([]Window, 0, len(ids))
	for _, id := range ids {
		w := Window{ID: id}
		title, _ := propString(c, id, "_NET_WM_NAME")
		if title == "" {
			title, _ = propString(c, id, "WM_NAME")
		}
		w.Title = strings.TrimSpace(title)
		if cls, _ := propString(c, id, "WM_CLASS"); cls != "" {
			parts := strings.Split(strings.TrimRight(cls, "\x00"), "\x00")
			w.Class = strings.Join(parts, ".")
		}
		if st, err := prop32(c, id, "_NET_WM_STATE"); err == nil {
			for _, a := range st {
				if a == hidden {
					w.Minimized = true
				}
			}
		}
		out = append(out, w)
	}
	return out, nil
}

// EWMH source indication: 2 = a pager or other tool acting for the user, which window
// managers don't hold back as focus stealing.
const sourcePager = 2

// Activate switches to the window's desktop if needed and asks the WM to activate it
// (raising, un-minimising and focusing it).
func (d *Display) Activate(_ context.Context, id uint32) error {
	c, err := d.open()
	if err != nil {
		return err
	}
	defer c.Close()
	if ok, why := wmCheck(c); !ok {
		return errors.New(why)
	}
	sup, err := supported(c)
	if err != nil {
		return err
	}
	if sup["_NET_CURRENT_DESKTOP"] && sup["_NET_WM_DESKTOP"] {
		wd, _ := prop32(c, id, "_NET_WM_DESKTOP")
		cd, _ := prop32(c, c.Root(), "_NET_CURRENT_DESKTOP")
		if len(wd) == 1 && len(cd) == 1 && wd[0] != 0xffffffff && wd[0] != cd[0] {
			a, err := c.Atom("_NET_CURRENT_DESKTOP")
			if err != nil {
				return err
			}
			if err := c.ClientMessage(c.Root(), a, [5]uint32{wd[0], 0}); err != nil {
				return err
			}
		}
	}
	a, err := c.Atom("_NET_ACTIVE_WINDOW")
	if err != nil {
		return err
	}
	if err := c.ClientMessage(id, a, [5]uint32{sourcePager, 0, 0}); err != nil {
		return err
	}
	return c.Sync()
}

// CloseWindow asks the WM to close the window gracefully (_NET_CLOSE_WINDOW); with a WM that
// lacks it, the window itself is asked (ICCCM WM_DELETE_WINDOW) if it takes that request.
// Never a kill.
func (d *Display) CloseWindow(_ context.Context, id uint32) error {
	c, err := d.open()
	if err != nil {
		return err
	}
	defer c.Close()
	sup, err := supported(c)
	if err != nil {
		return err
	}
	if sup["_NET_CLOSE_WINDOW"] {
		a, err := c.Atom("_NET_CLOSE_WINDOW")
		if err != nil {
			return err
		}
		if err := c.ClientMessage(id, a, [5]uint32{0, sourcePager}); err != nil {
			return err
		}
		return c.Sync()
	}
	protos, err := prop32(c, id, "WM_PROTOCOLS")
	if err != nil {
		return err
	}
	del, err := c.Atom("WM_DELETE_WINDOW")
	if err != nil {
		return err
	}
	for _, p := range protos {
		if p == del {
			wp, err := c.Atom("WM_PROTOCOLS")
			if err != nil {
				return err
			}
			if err := c.WindowMessage(id, wp, [5]uint32{del, 0}); err != nil {
				return err
			}
			return c.Sync()
		}
	}
	return errors.New("neither the window manager nor the window takes a close request")
}

// iconicState is ICCCM's IconicState for WM_CHANGE_STATE.
const iconicState = 3

// Iconify minimises the window (ICCCM WM_CHANGE_STATE, what XIconifyWindow sends).
func (d *Display) Iconify(_ context.Context, id uint32) error {
	c, err := d.open()
	if err != nil {
		return err
	}
	defer c.Close()
	a, err := c.Atom("WM_CHANGE_STATE")
	if err != nil {
		return err
	}
	if err := c.ClientMessage(id, a, [5]uint32{iconicState}); err != nil {
		return err
	}
	return c.Sync()
}

// Active is the window the WM says is active (_NET_ACTIVE_WINDOW; 0 for none).
func (d *Display) Active(context.Context) (uint32, error) {
	c, err := d.open()
	if err != nil {
		return 0, err
	}
	defer c.Close()
	v, err := prop32(c, c.Root(), "_NET_ACTIVE_WINDOW")
	if err != nil {
		return 0, err
	}
	if len(v) == 0 {
		return 0, nil
	}
	return v[0], nil
}

// Stacking is the window manager's clients top first (_NET_CLIENT_LIST_STACKING, which EWMH
// gives bottom to top). A window manager raises the window it focuses, so this is the order
// they were last used in: what dictation reads as focus history (DESIGN.md §14).
func (d *Display) Stacking(context.Context) ([]uint32, error) {
	c, err := d.open()
	if err != nil {
		return nil, err
	}
	defer c.Close()
	if ok, why := wmCheck(c); !ok {
		return nil, errors.New(why)
	}
	sup, err := supported(c)
	if err != nil {
		return nil, err
	}
	if !sup["_NET_CLIENT_LIST_STACKING"] {
		return nil, errors.New("the window manager doesn't report its stacking order")
	}
	ids, err := prop32(c, c.Root(), "_NET_CLIENT_LIST_STACKING")
	if err != nil {
		return nil, err
	}
	slices.Reverse(ids)
	return ids, nil
}

// --- the typing backend for dictation ---

// Keyboard is Display as a dictation typing backend.
type Keyboard struct{ D *Display }

// KeyboardName is the backend's name in status and logs.
const KeyboardName = "x11 XTEST"

func (k *Keyboard) Name() string { return KeyboardName }
func (k *Keyboard) Available(ctx context.Context) (bool, string) {
	return k.D.TypingAvailable(ctx)
}
func (k *Keyboard) Send(ctx context.Context, s []xkb.Stroke) error { return k.D.Send(ctx, s) }
