package x11

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/xkb"
)

// fakeConn is a scripted X server: atoms, properties, a keyboard mapping, and a log of what
// was sent ("key 38 down", "map 200 [U+00E9]", "msg root _NET_ACTIVE_WINDOW 0x… [2 0 0 0 0]", "sync").
type fakeConn struct {
	xtest   bool
	lo      byte
	per     int
	keymap  []uint32
	shift   byte
	lock    bool
	atoms   map[string]uint32
	names   map[uint32]string
	props   map[string]prop // "win/ATOM"
	log     []string
	failMap bool
	pointer [2]int
	geoms   map[uint32][4]int // win -> root-relative x, y, w, h
	depth   byte
	pixels  []byte        // what GetImage returns
	idle    time.Duration // what IdleTime returns
	idleErr error
	grabErr error       // what GrabKey returns
	presses chan uint16 // NextKeyPress's key presses (their modifier state); closed: the connection ended
	onClose func()
}

type prop struct {
	typ    uint32
	format byte
	data   []byte
}

const root = 0x100

func newFake() *fakeConn {
	f := &fakeConn{xtest: true, lo: 8, per: 2, atoms: map[string]uint32{}, names: map[uint32]string{}, props: map[string]prop{}}
	// keycodes 8..15: 8 empty, 9 a/A, 10 Shift_L, 11 Tab/ISO_Left_Tab, 12 empty, 13 1/!, 14 empty, 15 Return
	f.keymap = []uint32{0, 0, 'a', 'A', xkb.KeyShiftL, 0, xkb.KeyTab, xkb.KeyISOLeftTab, 0, 0, '1', '!', 0, 0, xkb.KeyReturn, 0}
	f.shift = 10
	return f
}

func (f *fakeConn) Root() uint32 { return root }
func (f *fakeConn) Atom(n string) (uint32, error) {
	if a, ok := f.atoms[n]; ok {
		return a, nil
	}
	a := uint32(500 + len(f.atoms))
	f.atoms[n], f.names[a] = a, n
	return a, nil
}
func (f *fakeConn) Prop(win, atom uint32) (uint32, byte, []byte, error) {
	p := f.props[fmt.Sprintf("%#x/%s", win, f.names[atom])]
	return p.typ, p.format, p.data, nil
}
func (f *fakeConn) setProp32(win uint32, name string, vals ...uint32) {
	b := []byte{}
	for _, v := range vals {
		b = binary.LittleEndian.AppendUint32(b, v)
	}
	f.props[fmt.Sprintf("%#x/%s", win, name)] = prop{typ: 1, format: 32, data: b}
}
func (f *fakeConn) setAtoms(win uint32, name string, atoms ...string) {
	var vals []uint32
	for _, a := range atoms {
		v, _ := f.Atom(a)
		vals = append(vals, v)
	}
	f.setProp32(win, name, vals...)
}
func (f *fakeConn) setStr(win uint32, name, v string) {
	f.props[fmt.Sprintf("%#x/%s", win, name)] = prop{typ: 2, format: 8, data: []byte(v)}
}
func (f *fakeConn) ClientMessage(win, typ uint32, d [5]uint32) error {
	f.log = append(f.log, fmt.Sprintf("msg root %s %#x %v", f.names[typ], win, d))
	return nil
}
func (f *fakeConn) WindowMessage(win, typ uint32, d [5]uint32) error {
	f.log = append(f.log, fmt.Sprintf("msg window %s %#x %v", f.names[typ], win, d))
	return nil
}
func (f *fakeConn) HasXTest() bool                 { return f.xtest }
func (f *fakeConn) KeycodeRange() (byte, byte)     { return f.lo, f.lo + byte(len(f.keymap)/f.per) - 1 }
func (f *fakeConn) Keymap() (int, []uint32, error) { return f.per, slices.Clone(f.keymap), nil }
func (f *fakeConn) SetKeysyms(code byte, syms []uint32) error {
	if f.failMap {
		return errors.New("BadAlloc")
	}
	i := int(code-f.lo) * f.per
	copy(f.keymap[i:i+f.per], syms)
	var s []string
	for _, v := range syms {
		s = append(s, fmt.Sprintf("%#x", v))
	}
	f.log = append(f.log, fmt.Sprintf("map %d %s", code, strings.Join(s, ",")))
	return nil
}
func (f *fakeConn) ShiftKeycode() (byte, error) { return f.shift, nil }
func (f *fakeConn) LockOn() (bool, error)       { return f.lock, nil }
func (f *fakeConn) FakeKey(code byte, press bool) error {
	st := "up"
	if press {
		st = "down"
	}
	f.log = append(f.log, fmt.Sprintf("key %d %s", code, st))
	return nil
}
func (f *fakeConn) FakeMotion(x, y int) error {
	f.pointer = [2]int{x, y}
	f.log = append(f.log, fmt.Sprintf("motion %d,%d", x, y))
	return nil
}
func (f *fakeConn) FakeButton(b byte, down bool) error {
	st := "up"
	if down {
		st = "down"
	}
	f.log = append(f.log, fmt.Sprintf("button %d %s", b, st))
	return nil
}
func (f *fakeConn) QueryPointer() (int, int, error) { return f.pointer[0], f.pointer[1], nil }
func (f *fakeConn) GetImage(win uint32, r image.Rectangle) (byte, []byte, error) {
	f.log = append(f.log, fmt.Sprintf("image %#x %v", win, r))
	return f.depth, f.pixels, nil
}
func (f *fakeConn) Geometry(win uint32) (int, int, int, int, error) {
	g, ok := f.geoms[win]
	if !ok {
		return 0, 0, 0, 0, errors.New("BadWindow")
	}
	return g[0], g[1], g[2], g[3], nil
}
func (f *fakeConn) Sync() error { f.log = append(f.log, "sync"); return nil }
func (f *fakeConn) Close() {
	if f.onClose != nil {
		f.onClose()
	}
}
func (f *fakeConn) IdleTime() (time.Duration, error) { return f.idle, f.idleErr }
func (f *fakeConn) GrabKey(code byte, mods uint16) error {
	f.log = append(f.log, fmt.Sprintf("grab %d %#x", code, mods))
	return f.grabErr
}
func (f *fakeConn) NextKeyPress() (byte, uint16, error) {
	st, ok := <-f.presses
	if !ok {
		return 0, 0, errors.New("connection closed")
	}
	return 14, st, nil
}

var x11Env = func(k string) string { return map[string]string{"DISPLAY": ":0"}[k] }

func display(f *fakeConn) (*Display, *[]time.Duration) {
	var slept []time.Duration
	return &Display{Getenv: x11Env, Dial: func() (Conn, error) { return f, nil },
		Sleep: func(_ context.Context, d time.Duration) error {
			slept = append(slept, d)
			f.log = append(f.log, "settle")
			return nil
		}}, &slept
}

func strokes(s string) []xkb.Stroke { st, _ := xkb.Strokes(s); return st }

func TestSendUsesExistingKeysAndShift(t *testing.T) {
	f := newFake()
	d, slept := display(f)
	if err := d.Send(context.Background(), append(strokes("aA!"), xkb.Stroke{Sym: xkb.KeyTab, Shift: true})); err != nil {
		t.Fatal(err)
	}
	want := []string{"key 9 down", "key 9 up", "key 10 down", "key 9 down", "key 9 up", "key 10 up",
		"key 10 down", "key 13 down", "key 13 up", "key 10 up",
		"key 10 down", "key 11 down", "key 11 up", "key 10 up", "sync"}
	if !slices.Equal(f.log, want) {
		t.Fatalf("got  %v\nwant %v", f.log, want)
	}
	if len(*slept) != 0 {
		t.Fatal("settled without changing the mapping")
	}
}

func TestSendRemapsSpareKeycodesAndRestoresThem(t *testing.T) {
	f := newFake()
	before := slices.Clone(f.keymap)
	d, _ := display(f)
	if err := d.Send(context.Background(), strokes("éa日é")); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(f.log, "; ")
	want := "map 8 0xe9,0xe9; map 12 0x10065e5,0x10065e5; sync; settle; " +
		"key 8 down; key 8 up; key 9 down; key 9 up; key 12 down; key 12 up; key 8 down; key 8 up; sync; " +
		"sync; settle; map 8 0x0,0x0; map 12 0x0,0x0; sync"
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if !slices.Equal(f.keymap, before) {
		t.Fatalf("mapping not restored: %v", f.keymap)
	}
}

func TestSendRunsWhenSpareKeycodesRunOut(t *testing.T) {
	f := newFake() // spares: 8, 12, 14
	d, slept := display(f)
	if err := d.Send(context.Background(), strokes("αβγδα")); err != nil {
		t.Fatal(err)
	}
	maps, keys := 0, 0
	for _, l := range f.log {
		if strings.HasPrefix(l, "map ") && !strings.Contains(l, " 0x0,0x0") {
			maps++
		}
		if strings.HasSuffix(l, " down") {
			keys++
		}
	}
	// Run 1: α β γ on 8, 12, 14; run 2: δ α (two new keysyms) after a settle.
	if maps != 5 || keys != 5 {
		t.Fatalf("%d remaps, %d keys:\n%v", maps, keys, f.log)
	}
	if len(*slept) != 4 { // after map 1, before map 2, after map 2, before restore
		t.Fatalf("settles %v", *slept)
	}
	for _, s := range *slept {
		if s != DefaultSettle {
			t.Fatalf("settle %v", s)
		}
	}
}

func TestSendCapsLockAndErrors(t *testing.T) {
	f := newFake()
	f.lock = true
	d, _ := display(f)
	_ = d.Send(context.Background(), strokes("a1"))
	if want := []string{"key 10 down", "key 9 down", "key 9 up", "key 10 up", "key 13 down", "key 13 up", "sync"}; !slices.Equal(f.log, want) {
		t.Fatalf("caps lock: %v", f.log)
	}

	f = newFake()
	f.xtest = false
	d, _ = display(f)
	if err := d.Send(context.Background(), strokes("a")); err == nil || !strings.Contains(err.Error(), "XTEST") {
		t.Fatal(err)
	}
	f = newFake()
	f.keymap = []uint32{'a', 'A'}
	d, _ = display(f)
	if err := d.Send(context.Background(), strokes("é")); err == nil || len(f.log) != 0 {
		t.Fatalf("no spare: %v %v", err, f.log)
	}
	if ok, _ := (&Display{Getenv: func(k string) string {
		return map[string]string{"DISPLAY": ":0", "WAYLAND_DISPLAY": "wayland-1"}[k]
	}}).TypingAvailable(context.Background()); ok {
		t.Fatal("X11 typing on Wayland")
	}
}

func ewmhFake() *fakeConn {
	f := newFake()
	f.setProp32(root, "_NET_SUPPORTING_WM_CHECK", 0x200)
	f.setAtoms(root, "_NET_SUPPORTED", "_NET_CLIENT_LIST", "_NET_ACTIVE_WINDOW", "_NET_CLOSE_WINDOW", "_NET_CURRENT_DESKTOP", "_NET_WM_DESKTOP")
	f.setProp32(root, "_NET_CLIENT_LIST", 0x400001, 0x400002)
	f.setProp32(root, "_NET_ACTIVE_WINDOW", 0x400002)
	f.setProp32(root, "_NET_CURRENT_DESKTOP", 0)
	f.setStr(0x400001, "_NET_WM_NAME", "notes — ✓")
	f.setStr(0x400001, "WM_CLASS", "gedit\x00Gedit\x00")
	f.setProp32(0x400001, "_NET_WM_DESKTOP", 1)
	f.setStr(0x400002, "WM_NAME", "xterm")
	f.setStr(0x400002, "WM_CLASS", "xterm\x00XTerm\x00")
	f.setAtoms(0x400002, "_NET_WM_STATE", "_NET_WM_STATE_HIDDEN")
	f.setProp32(0x400002, "_NET_WM_DESKTOP", 0)
	return f
}

func TestEWMHWindows(t *testing.T) {
	ctx := context.Background()
	f := ewmhFake()
	d, _ := display(f)
	if ok, why := d.WMAvailable(ctx); !ok {
		t.Fatal(why)
	}
	ws, err := d.Windows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []Window{{ID: 0x400001, Title: "notes — ✓", Class: "gedit.Gedit"}, {ID: 0x400002, Title: "xterm", Class: "xterm.XTerm", Minimized: true}}
	if !slices.Equal(ws, want) {
		t.Fatalf("%+v", ws)
	}
	if a, _ := d.Active(ctx); a != 0x400002 {
		t.Fatalf("active %#x", a)
	}
	f.log = nil
	_ = d.Activate(ctx, 0x400001) // on desktop 1 while 0 is shown
	_ = d.Activate(ctx, 0x400002)
	_ = d.Iconify(ctx, 0x400001)
	_ = d.CloseWindow(ctx, 0x400001)
	wantLog := []string{
		"msg root _NET_CURRENT_DESKTOP 0x100 [1 0 0 0 0]", "msg root _NET_ACTIVE_WINDOW 0x400001 [2 0 0 0 0]", "sync",
		"msg root _NET_ACTIVE_WINDOW 0x400002 [2 0 0 0 0]", "sync",
		"msg root WM_CHANGE_STATE 0x400001 [3 0 0 0 0]", "sync",
		"msg root _NET_CLOSE_WINDOW 0x400001 [0 2 0 0 0]", "sync"}
	if !slices.Equal(f.log, wantLog) {
		t.Fatalf("got  %q\nwant %q", f.log, wantLog)
	}
}

func TestEWMHCloseWithoutNetCloseWindow(t *testing.T) {
	ctx := context.Background()
	f := ewmhFake()
	f.setAtoms(root, "_NET_SUPPORTED", "_NET_CLIENT_LIST", "_NET_ACTIVE_WINDOW")
	f.setAtoms(0x400001, "WM_PROTOCOLS", "WM_TAKE_FOCUS", "WM_DELETE_WINDOW")
	d, _ := display(f)
	f.log = nil
	if err := d.CloseWindow(ctx, 0x400001); err != nil {
		t.Fatal(err)
	}
	del, _ := f.Atom("WM_DELETE_WINDOW")
	if want := fmt.Sprintf("msg window WM_PROTOCOLS 0x400001 [%d 0 0 0 0]", del); len(f.log) != 2 || f.log[0] != want {
		t.Fatalf("%q", f.log)
	}
	if err := d.CloseWindow(ctx, 0x400002); err == nil {
		t.Fatal("closed a window that takes no close request (a kill is never the answer)")
	}
}

func TestEWMHNeedsAWindowManager(t *testing.T) {
	ctx := context.Background()
	d, _ := display(newFake())
	if ok, why := d.WMAvailable(ctx); ok || !strings.Contains(why, "EWMH") {
		t.Fatal(ok, why)
	}
	if _, err := d.Windows(ctx); err == nil {
		t.Fatal("listed without a WM")
	}
	f := ewmhFake()
	f.setAtoms(root, "_NET_SUPPORTED", "_NET_CLIENT_LIST")
	d, _ = display(f)
	if ok, why := d.WMAvailable(ctx); ok || !strings.Contains(why, "_NET_ACTIVE_WINDOW") {
		t.Fatal(ok, why)
	}
	never := &Display{Getenv: func(string) string { return "" }, Dial: func() (Conn, error) { t.Fatal("dialed"); return nil, nil }}
	if ok, _ := never.WMAvailable(ctx); ok {
		t.Fatal("no DISPLAY")
	}
}

func TestIDs(t *testing.T) {
	if FormatID(0x3a00007) != "0x03a00007" {
		t.Fatal(FormatID(0x3a00007))
	}
	for id, want := range map[string]uint32{"0x03a00007": 0x3a00007, "0x3A00007": 0x3a00007, "0x0": 0, "0x": 0,
		"3a00007": 0, "0xZZ": 0, "0x123456789": 0, "$(reboot)": 0} {
		if got, ok := ParseID(id); got != want || ok != (want != 0) {
			t.Errorf("ParseID(%q) = %#x %v", id, got, ok)
		}
	}
}

// Stacking is top first: the order windows were last raised, which dictation reads as focus
// history. A window manager that doesn't report it says so.
func TestEWMHStacking(t *testing.T) {
	ctx := context.Background()
	f := ewmhFake()
	d, _ := display(f)
	if _, err := d.Stacking(ctx); err == nil {
		t.Fatal("stacking without _NET_CLIENT_LIST_STACKING")
	}
	f.setAtoms(root, "_NET_SUPPORTED", "_NET_CLIENT_LIST", "_NET_CLIENT_LIST_STACKING", "_NET_ACTIVE_WINDOW")
	f.setProp32(root, "_NET_CLIENT_LIST_STACKING", 0x400002, 0x400001, 0x400003)
	got, err := d.Stacking(ctx)
	if err != nil || !slices.Equal(got, []uint32{0x400003, 0x400001, 0x400002}) {
		t.Fatalf("%#x %v", got, err)
	}
}
