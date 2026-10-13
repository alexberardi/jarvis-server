//go:build linux

// Package wlwire is a minimal Wayland client wire: one connection, object ids, requests with
// an optional file descriptor, events dispatched to per-object handlers, and sync round trips.
// It knows no protocol beyond wl_display and wl_registry; the island (layer-shell overlay) and
// the virtual keyboard (dictation) each speak their own interfaces over it. Pure Go: the agent
// builds with CGO_ENABLED=0, so there is no libwayland-client.
//
// Wire format: every message is (object id u32, size<<16 | opcode u32, args), little-endian on
// every platform that runs Wayland; strings are u32 length (with NUL) + bytes padded to 4; file
// descriptors travel out of band (SCM_RIGHTS) and take no space in the body. Object ids are
// never reused.
package wlwire

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const (
	// DisplayID is wl_display, object 1 on every connection.
	DisplayID = 1

	// wl_display requests and events.
	OpDisplaySync, OpDisplayGetRegistry = 0, 1
	EvDisplayError                      = 0
	// wl_registry.
	OpRegistryBind = 0
	EvGlobal       = 0
	// wl_callback.
	EvCallbackDone = 0
)

// Conn is one connection. Requests may come from any goroutine (writes are serialised);
// events are read and dispatched on the connection's reader goroutine.
type Conn struct {
	c    *net.UnixConn
	wmu  sync.Mutex
	next uint32

	mu       sync.Mutex
	handlers map[uint32]func(op uint16, args []byte)
	err      error // the connection broke or the compositor reported a protocol error
	dead     chan struct{}
}

// ProtocolError is a wl_display.error event: the compositor killed the connection.
type ProtocolError struct {
	Object, Code uint32
	Msg          string
}

func (e *ProtocolError) Error() string {
	return fmt.Sprintf("wayland protocol error on object %d (code %d): %s", e.Object, e.Code, e.Msg)
}

// SocketPath is the compositor's socket from WAYLAND_DISPLAY and XDG_RUNTIME_DIR.
func SocketPath(getenv func(string) string) (string, error) {
	name := getenv("WAYLAND_DISPLAY")
	if name == "" {
		return "", errors.New("not a Wayland session (WAYLAND_DISPLAY is unset)")
	}
	if filepath.IsAbs(name) {
		return name, nil
	}
	dir := getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		return "", errors.New("XDG_RUNTIME_DIR is unset")
	}
	return filepath.Join(dir, name), nil
}

// Dial connects to this session's compositor.
func Dial() (*Conn, error) {
	path, err := SocketPath(os.Getenv)
	if err != nil {
		return nil, err
	}
	c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	return NewConn(c), nil
}

// NewConn wraps a connected socket and starts reading events.
func NewConn(c *net.UnixConn) *Conn {
	w := &Conn{c: c, next: DisplayID + 1, handlers: map[uint32]func(uint16, []byte){}, dead: make(chan struct{})}
	w.Handle(DisplayID, func(op uint16, a []byte) {
		if op == EvDisplayError {
			r := NewReader(a)
			w.fail(&ProtocolError{Object: r.U32(), Code: r.U32(), Msg: r.Str()})
		}
	})
	go w.read()
	return w
}

// NewID allocates an object id.
func (w *Conn) NewID() uint32 {
	w.wmu.Lock()
	defer w.wmu.Unlock()
	id := w.next
	w.next++
	return id
}

// Handle routes object id's events to f (on the reader goroutine). Register it before the
// request that creates the object: the compositor may answer at once, and events for ids
// without a handler are dropped.
func (w *Conn) Handle(id uint32, f func(op uint16, args []byte)) {
	w.mu.Lock()
	w.handlers[id] = f
	w.mu.Unlock()
}

// Forget drops id's handler.
func (w *Conn) Forget(id uint32) {
	w.mu.Lock()
	delete(w.handlers, id)
	w.mu.Unlock()
}

func (w *Conn) fail(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err == nil {
		w.err = err
		close(w.dead)
	}
}

// Err is why the connection is unusable, or nil.
func (w *Conn) Err() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

// Dead is closed once the connection is unusable.
func (w *Conn) Dead() <-chan struct{} { return w.dead }

// Close closes the connection.
func (w *Conn) Close() {
	w.fail(errors.New("closed"))
	_ = w.c.Close()
}

// Send writes one request; fd >= 0 rides along.
func (w *Conn) Send(id uint32, op uint16, args []byte, fd int) error {
	if err := w.Err(); err != nil {
		return err
	}
	msg := make([]byte, 8, 8+len(args))
	binary.LittleEndian.PutUint32(msg, id)
	binary.LittleEndian.PutUint32(msg[4:], uint32(8+len(args))<<16|uint32(op))
	msg = append(msg, args...)
	var oob []byte
	if fd >= 0 {
		oob = unix.UnixRights(fd)
	}
	w.wmu.Lock()
	defer w.wmu.Unlock()
	_, _, err := w.c.WriteMsgUnix(msg, oob, nil)
	if err != nil {
		w.fail(err)
	}
	return err
}

func (w *Conn) read() {
	buf := make([]byte, 0, 8192)
	tmp := make([]byte, 4096)
	oob := make([]byte, unix.CmsgSpace(4*28))
	for {
		n, oobn, _, _, err := w.c.ReadMsgUnix(tmp, oob)
		if err != nil {
			w.fail(err)
			return
		}
		// Nothing here asks for an event that carries a file descriptor (wl_keyboard.keymap
		// would; no client binds a keyboard). Close any that arrive.
		if oobn > 0 {
			if msgs, err := unix.ParseSocketControlMessage(oob[:oobn]); err == nil {
				for _, m := range msgs {
					if fds, err := unix.ParseUnixRights(&m); err == nil {
						for _, fd := range fds {
							_ = unix.Close(fd)
						}
					}
				}
			}
		}
		buf = append(buf, tmp[:n]...)
		for len(buf) >= 8 {
			id := binary.LittleEndian.Uint32(buf)
			word := binary.LittleEndian.Uint32(buf[4:])
			size, op := int(word>>16), uint16(word&0xffff)
			if size < 8 {
				w.fail(fmt.Errorf("wayland: bad message size %d", size))
				return
			}
			if len(buf) < size {
				break
			}
			w.mu.Lock()
			h := w.handlers[id]
			w.mu.Unlock()
			if h != nil {
				h(op, buf[8:size])
			}
			buf = buf[size:]
		}
		if len(buf) == 0 {
			buf = buf[:0:cap(buf)]
		}
	}
}

// Roundtrip waits until the compositor has handled everything sent so far (a protocol error
// caused by any of it is returned).
func (w *Conn) Roundtrip(timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return w.RoundtripCtx(ctx)
}

// RoundtripCtx is Roundtrip bounded by ctx.
func (w *Conn) RoundtripCtx(ctx context.Context) error {
	id := w.NewID()
	done := make(chan struct{})
	w.Handle(id, func(op uint16, _ []byte) {
		if op == EvCallbackDone {
			close(done)
		}
	})
	defer w.Forget(id)
	if err := w.Send(DisplayID, OpDisplaySync, NewArgs().U32(id).Bytes(), -1); err != nil {
		return err
	}
	select {
	case <-done:
		// A protocol error raised before the sync is delivered before its done event.
		return w.Err()
	case <-w.dead:
		return w.Err()
	case <-ctx.Done():
		return errors.New("wayland: compositor did not answer")
	}
}

// Global is one wl_registry.global.
type Global struct {
	Name    uint32
	Iface   string
	Version uint32
}

// Registry gets the registry and collects every global (one round trip). The returned id is
// the registry, for Bind.
func (w *Conn) Registry(ctx context.Context) (uint32, []Global, error) {
	var mu sync.Mutex
	var globals []Global
	reg := w.NewID()
	w.Handle(reg, func(op uint16, a []byte) {
		if op == EvGlobal {
			r := NewReader(a)
			g := Global{r.U32(), r.Str(), r.U32()}
			mu.Lock()
			globals = append(globals, g)
			mu.Unlock()
		}
	})
	if err := w.Send(DisplayID, OpDisplayGetRegistry, NewArgs().U32(reg).Bytes(), -1); err != nil {
		return 0, nil, err
	}
	if err := w.RoundtripCtx(ctx); err != nil {
		return 0, nil, err
	}
	mu.Lock()
	defer mu.Unlock()
	return reg, append([]Global(nil), globals...), nil
}

// Bind binds global g at version on registry reg; on, when set, handles the new object's
// events (registered before the bind is sent).
func (w *Conn) Bind(reg uint32, g Global, version uint32, on func(id uint32) func(op uint16, args []byte)) uint32 {
	id := w.NewID()
	if on != nil {
		w.Handle(id, on(id))
	}
	_ = w.Send(reg, OpRegistryBind, NewArgs().U32(g.Name).Str(g.Iface).U32(version).U32(id).Bytes(), -1)
	return id
}

// --- argument encoding ---

// Args builds a request's arguments.
type Args struct{ b []byte }

// NewArgs starts an empty argument list.
func NewArgs() *Args { return &Args{} }

func (a *Args) U32(v uint32) *Args { a.b = binary.LittleEndian.AppendUint32(a.b, v); return a }
func (a *Args) I32(v int32) *Args  { return a.U32(uint32(v)) }
func (a *Args) Str(s string) *Args {
	a.U32(uint32(len(s) + 1))
	a.b = append(a.b, s...)
	a.b = append(a.b, 0)
	for len(a.b)%4 != 0 {
		a.b = append(a.b, 0)
	}
	return a
}

// Bytes is the encoded list (nil for an empty one).
func (a *Args) Bytes() []byte {
	if a == nil {
		return nil
	}
	return a.b
}

// Reader decodes an event's arguments; reading past the end yields zeros.
type Reader struct {
	b   []byte
	off int
}

// NewReader reads b.
func NewReader(b []byte) *Reader { return &Reader{b: b} }

func (r *Reader) U32() uint32 {
	if r.off+4 > len(r.b) {
		r.off = len(r.b)
		return 0
	}
	v := binary.LittleEndian.Uint32(r.b[r.off:])
	r.off += 4
	return v
}

func (r *Reader) I32() int32 { return int32(r.U32()) }

func (r *Reader) Str() string {
	n := int(r.U32())
	if n == 0 || r.off+n > len(r.b) {
		return ""
	}
	s := string(r.b[r.off : r.off+n-1])
	r.off += (n + 3) &^ 3
	return s
}
