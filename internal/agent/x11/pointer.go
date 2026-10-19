package x11

import (
	"context"
	"errors"
	"fmt"
	"image"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/agent/xkb"
)

// --- pointer (XTEST) ---

// Core pointer buttons: 1-3 are left, middle and right; 4-7 are wheel notches (up, down, left,
// right), each a press and a release.
const (
	ButtonLeft       byte = 1
	ButtonMiddle     byte = 2
	ButtonRight      byte = 3
	ButtonWheelUp    byte = 4
	ButtonWheelDown  byte = 5
	ButtonWheelLeft  byte = 6
	ButtonWheelRight byte = 7
)

// xtestConn opens a connection that can post XTEST events.
func (d *Display) xtestConn() (Conn, error) {
	c, err := d.open()
	if err != nil {
		return nil, err
	}
	if !c.HasXTest() {
		c.Close()
		return nil, errors.New("the X server has no XTEST extension")
	}
	return c, nil
}

// post runs events on an XTEST connection and syncs, so they have all been handled on return.
func (d *Display) post(ctx context.Context, events func(c Conn) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c, err := d.xtestConn()
	if err != nil {
		return err
	}
	defer c.Close()
	if err := events(c); err != nil {
		return err
	}
	return c.Sync()
}

// Move puts the pointer at x, y (root coordinates); a held button makes it a drag.
func (d *Display) Move(ctx context.Context, x, y int) error {
	return d.post(ctx, func(c Conn) error { return c.FakeMotion(x, y) })
}

// Button presses or releases b where the pointer is.
func (d *Display) Button(ctx context.Context, b byte, down bool) error {
	return d.post(ctx, func(c Conn) error { return c.FakeButton(b, down) })
}

// Click moves to x, y and clicks b n times (the server counts a quick second click as a double
// click).
func (d *Display) Click(ctx context.Context, x, y int, b byte, n int) error {
	return d.post(ctx, func(c Conn) error {
		if err := c.FakeMotion(x, y); err != nil {
			return err
		}
		for range n {
			if err := c.FakeButton(b, true); err != nil {
				return err
			}
			if err := c.FakeButton(b, false); err != nil {
				return err
			}
		}
		return nil
	})
}

// Drag presses the left button at path[0], moves through the rest and releases at the last.
func (d *Display) Drag(ctx context.Context, path [][2]int) error {
	if len(path) < 2 {
		return errors.New("drag needs at least two points")
	}
	return d.post(ctx, func(c Conn) error {
		if err := c.FakeMotion(path[0][0], path[0][1]); err != nil {
			return err
		}
		if err := c.FakeButton(ButtonLeft, true); err != nil {
			return err
		}
		for _, p := range path[1:] {
			if err := c.FakeMotion(p[0], p[1]); err != nil {
				_ = c.FakeButton(ButtonLeft, false)
				return err
			}
		}
		return c.FakeButton(ButtonLeft, false)
	})
}

// Scroll moves to x, y and turns the wheel dx, dy notches; positive dy scrolls down and positive
// dx right.
func (d *Display) Scroll(ctx context.Context, x, y, dx, dy int) error {
	return d.post(ctx, func(c Conn) error {
		if err := c.FakeMotion(x, y); err != nil {
			return err
		}
		for _, w := range []struct {
			n        int
			neg, pos byte
		}{{dy, ButtonWheelUp, ButtonWheelDown}, {dx, ButtonWheelLeft, ButtonWheelRight}} {
			b, n := w.pos, w.n
			if n < 0 {
				b, n = w.neg, -n
			}
			for range n {
				if err := c.FakeButton(b, true); err != nil {
					return err
				}
				if err := c.FakeButton(b, false); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// Pointer is where the pointer is (root coordinates).
func (d *Display) Pointer(context.Context) (x, y int, err error) {
	c, err := d.open()
	if err != nil {
		return 0, 0, err
	}
	defer c.Close()
	return c.QueryPointer()
}

// --- chords ---

// Modifier and named-key keysyms (keysymdef.h) for chords.
const (
	keyBackSpace = 0xff08
	keyDelete    = 0xffff
	keyInsert    = 0xff63
	keyHome      = 0xff50
	keyEnd       = 0xff57
	keyPageUp    = 0xff55
	keyPageDown  = 0xff56
	keyF1        = 0xffbe
	keyControlL  = 0xffe3
	keyControlR  = 0xffe4
	keyShiftR    = 0xffe2
	keyMetaL     = 0xffe7
	keyAltL      = 0xffe9
	keyAltR      = 0xffea
	keySuperL    = 0xffeb
	keySuperR    = 0xffec
)

// chordMods are the modifier names (computeruse.KeyNames) and the keysyms that can press them,
// preferred first. cmd and meta are Super, as on the other non-Mac backends.
var chordMods = map[string][]uint32{
	"ctrl":  {keyControlL, keyControlR},
	"alt":   {keyAltL, keyAltR, keyMetaL},
	"shift": {xkb.KeyShiftL, keyShiftR},
	"super": {keySuperL, keySuperR},
	"cmd":   {keySuperL, keySuperR},
	"meta":  {keySuperL, keySuperR},
}

var chordKeys = map[string]uint32{
	"enter": xkb.KeyReturn, "return": xkb.KeyReturn, "tab": xkb.KeyTab, "escape": xkb.KeyEscape,
	"esc": xkb.KeyEscape, "space": xkb.KeySpace, "backspace": keyBackSpace, "delete": keyDelete,
	"insert": keyInsert, "home": keyHome, "end": keyEnd, "pageup": keyPageUp, "pagedown": keyPageDown,
	"up": xkb.KeyUp, "down": xkb.KeyDown, "left": xkb.KeyLeft, "right": xkb.KeyRight,
}

// chordSym is a non-modifier key name's keysym: named keys, F1-F12, and the letters, digits and
// punctuation, which are their own (Latin-1) keysyms.
func chordSym(name string) (uint32, bool) {
	if s, ok := chordKeys[name]; ok {
		return s, true
	}
	var n int
	if _, err := fmt.Sscanf(name, "f%d", &n); err == nil && n >= 1 && n <= 12 && name == fmt.Sprintf("f%d", n) {
		return keyF1 + uint32(n-1), true
	}
	if r := []rune(name); len(r) == 1 && r[0] > 0x20 && r[0] < 0x7f {
		return uint32(r[0]), true
	}
	return 0, false
}

// Keys presses a chord: the modifiers in order, then the one other key (if any), then releases
// them in reverse. Keys are found on the keyboard mapping's first level; a chord of modifiers
// alone presses and releases them.
func (d *Display) Keys(ctx context.Context, names []string) error {
	type part struct {
		name string
		syms []uint32
	}
	var mods []part
	var key *part
	for _, n := range names {
		n = strings.ToLower(n)
		if syms, ok := chordMods[n]; ok {
			mods = append(mods, part{n, syms})
			continue
		}
		s, ok := chordSym(n)
		if !ok {
			return fmt.Errorf("unknown key %q", n)
		}
		if key != nil {
			return fmt.Errorf("chord has two keys, %q and %q", key.name, n)
		}
		key = &part{n, []uint32{s}}
	}
	if len(mods) == 0 && key == nil {
		return errors.New("empty chord")
	}
	return d.post(ctx, func(c Conn) error {
		lo, _ := c.KeycodeRange()
		per, syms, err := c.Keymap()
		if err != nil {
			return fmt.Errorf("keyboard mapping: %w", err)
		}
		if per < 1 {
			return errors.New("keyboard mapping: no keysyms per keycode")
		}
		code := func(p part) (byte, error) {
			for _, want := range p.syms {
				for i := 0; i*per < len(syms); i++ {
					if syms[i*per] == want {
						return byte(int(lo) + i), nil
					}
				}
			}
			return 0, fmt.Errorf("no %q key in the keyboard mapping", p.name)
		}
		var codes []byte
		for _, m := range mods {
			k, err := code(m)
			if err != nil {
				return err
			}
			codes = append(codes, k)
		}
		if key != nil {
			k, err := code(*key)
			if err != nil {
				return err
			}
			codes = append(codes, k)
		}
		var down []byte
		defer func() { // release whatever is down, last first, even after an error
			for i := len(down) - 1; i >= 0; i-- {
				_ = c.FakeKey(down[i], false)
			}
		}()
		for _, k := range codes {
			if err := c.FakeKey(k, true); err != nil {
				return err
			}
			down = append(down, k)
		}
		return nil
	})
}

// --- capture ---

// Capture is an image of a window's area of the screen and where it is.
type Capture struct {
	Image *image.RGBA
	// X, Y, W, H is the captured rectangle in root coordinates (X11 has one coordinate space for
	// every monitor, in pixels: scale 1).
	X, Y, W, H int
}

// Capture grabs win's area of the screen (0: the whole root window). The pixels come from the
// root window, so they are what is on screen there (a window on top included), clipped to the
// screen.
func (d *Display) Capture(_ context.Context, win uint32) (Capture, error) {
	c, err := d.open()
	if err != nil {
		return Capture{}, err
	}
	defer c.Close()
	root := c.Root()
	rx, ry, rw, rh, err := c.Geometry(root)
	if err != nil {
		return Capture{}, fmt.Errorf("screen geometry: %w", err)
	}
	r := image.Rect(rx, ry, rx+rw, ry+rh)
	if win != 0 && win != root {
		x, y, w, h, err := c.Geometry(win)
		if err != nil {
			return Capture{}, fmt.Errorf("window geometry: %w", err)
		}
		r = image.Rect(x, y, x+w, y+h).Intersect(r)
		if r.Empty() {
			return Capture{}, errors.New("the window isn't on the screen")
		}
	}
	depth, data, err := c.GetImage(root, r)
	if err != nil {
		return Capture{}, err
	}
	img, err := decodeZPixmap(depth, r.Dx(), r.Dy(), data)
	if err != nil {
		return Capture{}, err
	}
	return Capture{Image: img, X: r.Min.X, Y: r.Min.Y, W: r.Dx(), H: r.Dy()}, nil
}

// decodeZPixmap reads a 24/32-bit ZPixmap (32 bits per pixel, little-endian: B, G, R, pad) into
// an opaque RGBA image.
func decodeZPixmap(depth byte, w, h int, data []byte) (*image.RGBA, error) {
	if depth != 24 && depth != 32 {
		return nil, fmt.Errorf("unsupported screen depth %d (want 24 or 32 bits)", depth)
	}
	if len(data) < w*h*4 {
		return nil, fmt.Errorf("short image: %d bytes for %dx%d", len(data), w, h)
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < w*h; i++ {
		s, p := data[4*i:4*i+4], img.Pix[4*i:4*i+4]
		p[0], p[1], p[2], p[3] = s[2], s[1], s[0], 0xff
	}
	return img, nil
}
