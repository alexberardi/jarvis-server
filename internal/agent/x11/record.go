package x11

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

// Recording the user's input (plan P3 piece 37) uses the RECORD extension. Its EnableContext
// request answers with a stream of replies, all with that request's sequence number, until the
// context is disabled from another connection; xgb hands a cookie one reply only, so the data
// connection is spoken here: the setup handshake, EnableContext and the replies. The control
// connection (CreateContext, DisableContext) is ordinary xgb (record_linux.go).

// Core device event types a recording sees (RecordedInput.Type).
const (
	KeyPress    = 2
	KeyRelease  = 3
	ButtonPress = 4
)

// RecordedInput is one device event: a key or button pressed, a key released.
type RecordedInput struct {
	Type   byte
	Detail byte // keycode, or button (1 left, 2 middle, 3 right, 4-7 the wheel)
	RootX  int  // the pointer on the root window
	RootY  int
	State  uint16 // modifiers and buttons held, as the server reports them
}

// Keymap is the core keyboard mapping: Per keysyms per keycode from Min on.
type Keymap struct {
	Min  byte
	Per  int
	Syms []uint32
}

// Sym is keycode's keysym at level (0 plain, 1 shifted); 0 when it has none.
func (k Keymap) Sym(code byte, level int) uint32 {
	if k.Per < 1 || code < k.Min || level < 0 {
		return 0
	}
	i := int(code-k.Min)*k.Per + level
	if level >= k.Per || i >= len(k.Syms) {
		return 0
	}
	return k.Syms[i]
}

// RECORD reply categories.
const (
	recFromServer  = 0
	recStartOfData = 4
	recEndOfData   = 5
)

// xauth is one connection's authorization (empty: none).
type xauth struct {
	name string
	data []byte
}

func pad4(n int) int { return (n + 3) &^ 3 }

// handshake is the X connection setup (little-endian client) on rw.
func handshake(rw io.ReadWriter, auth xauth) error {
	req := make([]byte, 12+pad4(len(auth.name))+pad4(len(auth.data)))
	req[0] = 'l'
	binary.LittleEndian.PutUint16(req[2:], 11)
	binary.LittleEndian.PutUint16(req[6:], uint16(len(auth.name)))
	binary.LittleEndian.PutUint16(req[8:], uint16(len(auth.data)))
	copy(req[12:], auth.name)
	copy(req[12+pad4(len(auth.name)):], auth.data)
	if _, err := rw.Write(req); err != nil {
		return err
	}
	head := make([]byte, 8)
	if _, err := io.ReadFull(rw, head); err != nil {
		return err
	}
	rest := make([]byte, 4*int(binary.LittleEndian.Uint16(head[6:])))
	if _, err := io.ReadFull(rw, rest); err != nil {
		return err
	}
	if head[0] != 1 {
		reason := rest[:min(int(head[1]), len(rest))]
		if head[0] == 2 { // Authenticate: the reason is the whole data
			reason = rest
		}
		return fmt.Errorf("the X server refused the connection: %s", string(reason))
	}
	return nil
}

// enableContext is a RECORD EnableContext request.
func enableContext(opcode byte, ctx uint32) []byte {
	b := []byte{opcode, 5, 2, 0, 0, 0, 0, 0}
	binary.LittleEndian.PutUint32(b[4:], ctx)
	return b
}

// readRecorded reads EnableContext's replies from r and calls send with each device event
// until EndOfData (nil) or a read error.
func readRecorded(r io.Reader, send func(RecordedInput)) error {
	head := make([]byte, 32)
	for {
		if _, err := io.ReadFull(r, head); err != nil {
			return err
		}
		switch head[0] {
		case 0:
			return fmt.Errorf("RECORD: X error %d", head[1])
		case 1:
		default:
			continue // an event: none is selected on this connection
		}
		data := make([]byte, 4*int(binary.LittleEndian.Uint32(head[4:])))
		if _, err := io.ReadFull(r, data); err != nil {
			return err
		}
		switch head[1] {
		case recEndOfData:
			return nil
		case recFromServer:
			order := binary.ByteOrder(binary.LittleEndian)
			if head[9] != 0 { // client-swapped: the recorded client's (big-endian) order
				order = binary.BigEndian
			}
			for ev := data; len(ev) >= 32; ev = ev[32:] {
				switch t := ev[0] & 0x7f; t {
				case KeyPress, KeyRelease, ButtonPress:
					send(RecordedInput{Type: t, Detail: ev[1], RootX: int(int16(order.Uint16(ev[20:]))),
						RootY: int(int16(order.Uint16(ev[22:]))), State: order.Uint16(ev[28:])})
				}
			}
		}
	}
}

// ErrNoRecord: the X server has no RECORD extension.
var ErrNoRecord = errors.New("the X server has no RECORD extension, which recording the desktop needs")

// Owner is a top-level window and the app it belongs to.
type Owner struct {
	ID    uint32
	PID   int    // _NET_WM_PID; 0 when the app doesn't set it
	Class string // WM_CLASS's class ("Gedit")
}

// WindowAt is the topmost managed window under root point (x, y) on the current desktop (ID 0:
// none), from the window manager's stacking order (its client list without one).
func (d *Display) WindowAt(_ context.Context, x, y int) (Owner, error) {
	c, err := d.open()
	if err != nil {
		return Owner{}, err
	}
	defer c.Close()
	ids, err := prop32(c, c.Root(), "_NET_CLIENT_LIST_STACKING")
	if err == nil && len(ids) == 0 {
		ids, err = prop32(c, c.Root(), "_NET_CLIENT_LIST")
	}
	if err != nil {
		return Owner{}, err
	}
	hidden, err := c.Atom("_NET_WM_STATE_HIDDEN")
	if err != nil {
		return Owner{}, err
	}
	cur, _ := prop32(c, c.Root(), "_NET_CURRENT_DESKTOP")
	for i := len(ids) - 1; i >= 0; i-- { // bottom to top
		id := ids[i]
		if st, _ := prop32(c, id, "_NET_WM_STATE"); slices.Contains(st, hidden) {
			continue
		}
		if desk, _ := prop32(c, id, "_NET_WM_DESKTOP"); len(cur) == 1 && len(desk) == 1 && desk[0] != cur[0] && desk[0] != 0xffffffff {
			continue
		}
		wx, wy, w, h, err := c.Geometry(id)
		if err != nil || x < wx || y < wy || x >= wx+w || y >= wy+h {
			continue
		}
		return owner(c, id), nil
	}
	return Owner{}, nil
}

// ActiveOwner is the active window and its app (ID 0: none).
func (d *Display) ActiveOwner(context.Context) (Owner, error) {
	c, err := d.open()
	if err != nil {
		return Owner{}, err
	}
	defer c.Close()
	v, err := prop32(c, c.Root(), "_NET_ACTIVE_WINDOW")
	if err != nil || len(v) == 0 || v[0] == 0 {
		return Owner{}, err
	}
	return owner(c, v[0]), nil
}

func owner(c Conn, id uint32) Owner {
	o := Owner{ID: id}
	if pid, _ := prop32(c, id, "_NET_WM_PID"); len(pid) == 1 {
		o.PID = int(pid[0])
	}
	if cls, _ := propString(c, id, "WM_CLASS"); cls != "" {
		parts := strings.Split(strings.TrimRight(cls, "\x00"), "\x00")
		o.Class = parts[len(parts)-1]
	}
	return o
}
