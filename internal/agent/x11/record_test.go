package x11

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"strings"
	"testing"
)

// recordStream is what a RECORD data connection reads: EnableContext's replies.
type recordStream struct{ bytes.Buffer }

func (s *recordStream) reply(category byte, swapped bool, data []byte) {
	head := make([]byte, 32)
	head[0], head[1] = 1, category
	binary.LittleEndian.PutUint16(head[2:], 2)
	binary.LittleEndian.PutUint32(head[4:], uint32(len(data)/4))
	if swapped {
		head[9] = 1
	}
	s.Write(head)
	s.Write(data)
}

// deviceEvent is a core KeyPress/KeyRelease/ButtonPress as RECORD hands it over.
func deviceEvent(order binary.ByteOrder, typ, detail byte, x, y int16, state uint16) []byte {
	b := make([]byte, 32)
	b[0], b[1] = typ, detail
	order.PutUint16(b[20:], uint16(x))
	order.PutUint16(b[22:], uint16(y))
	order.PutUint16(b[28:], state)
	return b
}

func TestReadRecorded(t *testing.T) {
	var s recordStream
	le := binary.LittleEndian
	s.reply(recStartOfData, false, nil)
	s.reply(recFromServer, false, join(deviceEvent(le, ButtonPress, 1, 120, 40, 0), deviceEvent(le, KeyPress, 38, 0, 0, 1)))
	s.reply(recFromServer, false, deviceEvent(le, 6 /* MotionNotify: not asked for */, 0, 1, 1, 0))
	s.reply(recFromServer, true, deviceEvent(binary.BigEndian, KeyRelease, 38, -5, 300, 0))
	s.reply(recEndOfData, false, nil)
	s.reply(recFromServer, false, deviceEvent(le, ButtonPress, 3, 1, 1, 0)) // after the end
	var got []RecordedInput
	if err := readRecorded(&s, func(in RecordedInput) { got = append(got, in) }); err != nil {
		t.Fatal(err)
	}
	want := []RecordedInput{
		{Type: ButtonPress, Detail: 1, RootX: 120, RootY: 40},
		{Type: KeyPress, Detail: 38, State: 1},
		{Type: KeyRelease, Detail: 38, RootX: -5, RootY: 300},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event %d: got %+v, want %+v", i, got[i], want[i])
		}
	}

	// An X error (BadContext) or a cut connection ends the stream with an error.
	var e recordStream
	e.Write(append([]byte{0, 1}, make([]byte, 30)...))
	if err := readRecorded(&e, func(RecordedInput) {}); err == nil || !strings.Contains(err.Error(), "X error") {
		t.Fatalf("error reply: %v", err)
	}
	var cut recordStream
	cut.reply(recFromServer, false, deviceEvent(le, KeyPress, 9, 0, 0, 0)[:16])
	if err := readRecorded(&cut, func(RecordedInput) {}); err == nil {
		t.Fatal("a cut stream read fine")
	}
}

func join(bs ...[]byte) []byte { return bytes.Join(bs, nil) }

// rw is a connection: what the server says, and what the client wrote.
type rw struct {
	io.Reader
	sent bytes.Buffer
}

func (c *rw) Write(b []byte) (int, error) { return c.sent.Write(b) }

func TestHandshake(t *testing.T) {
	ok := append([]byte{1, 0, 11, 0, 0, 0, 2, 0}, make([]byte, 8)...)
	c := &rw{Reader: bytes.NewReader(ok)}
	cookie := bytes.Repeat([]byte{0xab}, 16)
	if err := handshake(c, xauth{name: "MIT-MAGIC-COOKIE-1", data: cookie}); err != nil {
		t.Fatal(err)
	}
	b := c.sent.Bytes()
	if b[0] != 'l' || le16(b[2:]) != 11 || le16(b[6:]) != 18 || le16(b[8:]) != 16 || len(b) != 12+20+16 ||
		string(b[12:30]) != "MIT-MAGIC-COOKIE-1" || !bytes.Equal(b[32:], cookie) {
		t.Fatalf("setup request % x", b)
	}
	refused := append([]byte{0, 9, 11, 0, 0, 0, 3, 0}, []byte("No auth!    ")...)
	if err := handshake(&rw{Reader: bytes.NewReader(refused)}, xauth{}); err == nil || !strings.Contains(err.Error(), "No auth!") {
		t.Fatalf("refused: %v", err)
	}
	if got := enableContext(146, 0x00a00001); !bytes.Equal(got, []byte{146, 5, 2, 0, 1, 0, 0xa0, 0}) {
		t.Fatalf("EnableContext % x", got)
	}
}

func le16(b []byte) uint16 { return binary.LittleEndian.Uint16(b) }

func TestKeymapSym(t *testing.T) {
	k := Keymap{Min: 8, Per: 2, Syms: []uint32{0, 0, 'a', 'A'}}
	if k.Sym(9, 0) != 'a' || k.Sym(9, 1) != 'A' || k.Sym(7, 0) != 0 || k.Sym(10, 0) != 0 || k.Sym(9, 2) != 0 {
		t.Fatal("Sym")
	}
}

// The window under a point is the topmost one there on this desktop, not minimised; its app
// is its _NET_WM_PID and WM_CLASS.
func TestWindowAt(t *testing.T) {
	f := ewmhFake()
	f.setProp32(root, "_NET_CLIENT_LIST_STACKING", 0x400003, 0x400001, 0x400002, 0x400004) // bottom to top
	f.setProp32(0x400001, "_NET_WM_DESKTOP", 0)
	f.setProp32(0x400001, "_NET_WM_PID", 4242)
	f.setStr(0x400003, "WM_CLASS", "notes\x00Notes\x00")
	f.setProp32(0x400003, "_NET_WM_PID", 77)
	f.setProp32(0x400004, "_NET_WM_DESKTOP", 3) // another desktop
	f.geoms = map[uint32][4]int{
		0x400001: {100, 100, 400, 300},
		0x400002: {0, 0, 1000, 1000}, // minimised
		0x400003: {0, 0, 800, 600},
		0x400004: {0, 0, 1000, 1000},
	}
	d, _ := display(f)
	ctx := context.Background()
	if o, err := d.WindowAt(ctx, 150, 150); err != nil || o != (Owner{ID: 0x400001, PID: 4242, Class: "Gedit"}) {
		t.Fatalf("on gedit: %+v %v", o, err)
	}
	if o, _ := d.WindowAt(ctx, 50, 50); o != (Owner{ID: 0x400003, PID: 77, Class: "Notes"}) {
		t.Fatalf("on notes: %+v", o)
	}
	if o, _ := d.WindowAt(ctx, 900, 900); o.ID != 0 {
		t.Fatalf("on the desktop: %+v", o)
	}
	if o, _ := d.ActiveOwner(ctx); o != (Owner{ID: 0x400002, Class: "XTerm"}) {
		t.Fatalf("active: %+v", o)
	}
}
