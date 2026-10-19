//go:build linux

package x11

import (
	"fmt"
	"image"
	"io"
	"log"
	"os"
	"sync"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgb/xtest"
)

var osGetenv = os.Getenv

// xgb logs connection trouble to stderr itself; the agent reports errors its own way.
var quietOnce sync.Once

// xconn is Conn over github.com/jezek/xgb (pure Go X11).
type xconn struct {
	c     *xgb.Conn
	setup *xproto.SetupInfo
	root  xproto.Window
	xtest bool
	atoms map[string]uint32
}

func dialX() (Conn, error) {
	quietOnce.Do(func() { xgb.Logger = log.New(io.Discard, "", 0) })
	c, err := xgb.NewConn()
	if err != nil {
		return nil, err
	}
	x := &xconn{c: c, setup: xproto.Setup(c), atoms: map[string]uint32{}}
	x.root = x.setup.DefaultScreen(c).Root
	x.xtest = xtest.Init(c) == nil
	return x, nil
}

func (x *xconn) Close()       { x.c.Close() }
func (x *xconn) Root() uint32 { return uint32(x.root) }
func (x *xconn) HasXTest() bool {
	return x.xtest
}

func (x *xconn) Atom(name string) (uint32, error) {
	if a, ok := x.atoms[name]; ok {
		return a, nil
	}
	r, err := xproto.InternAtom(x.c, false, uint16(len(name)), name).Reply()
	if err != nil {
		return 0, err
	}
	x.atoms[name] = uint32(r.Atom)
	return uint32(r.Atom), nil
}

// maxPropLen bounds a property read (in 32-bit units): 1 MiB.
const maxPropLen = 1 << 18

func (x *xconn) Prop(win, atom uint32) (uint32, byte, []byte, error) {
	r, err := xproto.GetProperty(x.c, false, xproto.Window(win), xproto.Atom(atom), xproto.GetPropertyTypeAny, 0, maxPropLen).Reply()
	if err != nil {
		return 0, 0, nil, err
	}
	// xgb always connects little-endian, so format-32 values arrive as u32s() reads them.
	return uint32(r.Type), r.Format, r.Value, nil
}

func (x *xconn) message(dest xproto.Window, mask uint32, win, typ uint32, data [5]uint32) error {
	ev := xproto.ClientMessageEvent{
		Format: 32,
		Window: xproto.Window(win),
		Type:   xproto.Atom(typ),
		Data:   xproto.ClientMessageDataUnionData32New(data[:]),
	}
	return xproto.SendEventChecked(x.c, false, dest, mask, string(ev.Bytes())).Check()
}

func (x *xconn) ClientMessage(win, typ uint32, data [5]uint32) error {
	return x.message(x.root, xproto.EventMaskSubstructureRedirect|xproto.EventMaskSubstructureNotify, win, typ, data)
}

func (x *xconn) WindowMessage(win, typ uint32, data [5]uint32) error {
	return x.message(xproto.Window(win), xproto.EventMaskNoEvent, win, typ, data)
}

func (x *xconn) KeycodeRange() (byte, byte) {
	return byte(x.setup.MinKeycode), byte(x.setup.MaxKeycode)
}

func (x *xconn) Keymap() (int, []uint32, error) {
	lo, hi := x.KeycodeRange()
	r, err := xproto.GetKeyboardMapping(x.c, xproto.Keycode(lo), hi-lo+1).Reply()
	if err != nil {
		return 0, nil, err
	}
	out := make([]uint32, len(r.Keysyms))
	for i, k := range r.Keysyms {
		out[i] = uint32(k)
	}
	return int(r.KeysymsPerKeycode), out, nil
}

func (x *xconn) SetKeysyms(code byte, syms []uint32) error {
	ks := make([]xproto.Keysym, len(syms))
	for i, s := range syms {
		ks[i] = xproto.Keysym(s)
	}
	return xproto.ChangeKeyboardMappingChecked(x.c, 1, xproto.Keycode(code), byte(len(syms)), ks).Check()
}

func (x *xconn) ShiftKeycode() (byte, error) {
	r, err := xproto.GetModifierMapping(x.c).Reply()
	if err != nil {
		return 0, err
	}
	n := int(r.KeycodesPerModifier)
	for i := 0; i < n && i < len(r.Keycodes); i++ { // row 0 is Shift
		if r.Keycodes[i] != 0 {
			return byte(r.Keycodes[i]), nil
		}
	}
	return 0, nil
}

func (x *xconn) LockOn() (bool, error) {
	r, err := xproto.QueryPointer(x.c, x.root).Reply()
	if err != nil {
		return false, err
	}
	return r.Mask&xproto.ModMaskLock != 0, nil
}

func (x *xconn) FakeKey(code byte, press bool) error {
	t := byte(xproto.KeyRelease)
	if press {
		t = xproto.KeyPress
	}
	// Unchecked: one round trip per key would make long text slow. Keycodes come from the
	// server's own mapping, and the Sync after each run surfaces a broken connection.
	xtest.FakeInput(x.c, t, code, 0, x.root, 0, 0, 0)
	return nil
}

// Sync is a round trip: the server has handled everything sent before it.
func (x *xconn) Sync() error {
	_, err := xproto.GetInputFocus(x.c).Reply()
	return err
}

func (x *xconn) FakeMotion(px, py int) error {
	// Unchecked, like FakeKey: the caller's Sync surfaces a broken connection.
	xtest.FakeInput(x.c, xproto.MotionNotify, 0, 0, x.root, int16(px), int16(py), 0)
	return nil
}

func (x *xconn) FakeButton(b byte, down bool) error {
	t := byte(xproto.ButtonRelease)
	if down {
		t = xproto.ButtonPress
	}
	xtest.FakeInput(x.c, t, b, 0, x.root, 0, 0, 0)
	return nil
}

func (x *xconn) QueryPointer() (int, int, error) {
	r, err := xproto.QueryPointer(x.c, x.root).Reply()
	if err != nil {
		return 0, 0, err
	}
	return int(r.RootX), int(r.RootY), nil
}

func (x *xconn) GetImage(win uint32, r image.Rectangle) (byte, []byte, error) {
	rep, err := xproto.GetImage(x.c, xproto.ImageFormatZPixmap, xproto.Drawable(win),
		int16(r.Min.X), int16(r.Min.Y), uint16(r.Dx()), uint16(r.Dy()), 0xffffffff).Reply()
	if err != nil {
		return 0, nil, err
	}
	bpp := 0
	for _, f := range x.setup.PixmapFormats {
		if f.Depth == rep.Depth {
			bpp = int(f.BitsPerPixel)
		}
	}
	if bpp != 32 || x.setup.ImageByteOrder != xproto.ImageOrderLSBFirst {
		return 0, nil, fmt.Errorf("unsupported pixel format: depth %d at %d bits per pixel (byte order %d)", rep.Depth, bpp, x.setup.ImageByteOrder)
	}
	return rep.Depth, rep.Data, nil
}

func (x *xconn) Geometry(win uint32) (int, int, int, int, error) {
	g, err := xproto.GetGeometry(x.c, xproto.Drawable(win)).Reply()
	if err != nil {
		return 0, 0, 0, 0, err
	}
	t, err := xproto.TranslateCoordinates(x.c, xproto.Window(win), x.root, 0, 0).Reply()
	if err != nil {
		return 0, 0, 0, 0, err
	}
	return int(t.DstX), int(t.DstY), int(g.Width), int(g.Height), nil
}
