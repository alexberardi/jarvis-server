//go:build linux

package island

import (
	"cmp"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// A minimal Wayland client: just enough of wl_compositor, wl_shm, wl_output and
// zwlr_layer_shell_v1 to put one click-through overlay surface at the top centre of the screen.
// Wayland compositors that implement wlr-layer-shell (Hyprland, Sway, river, KDE, COSMIC…) show
// it; GNOME doesn't, and the island stays off there.
//
// Protocol notes: every message is (object id u32, size<<16 | opcode u32, args), little-endian
// on every platform that runs Wayland; strings are u32 length (with NUL) + bytes padded to 4;
// file descriptors travel out of band (SCM_RIGHTS). Object ids are never reused here.

const (
	displayID = 1

	// wl_display
	opDisplaySync, opDisplayGetRegistry = 0, 1
	evDisplayError                      = 0
	// wl_registry
	opRegistryBind = 0
	evGlobal       = 0
	// wl_callback
	evCallbackDone = 0
	// wl_compositor
	opCreateSurface, opCreateRegion = 0, 1
	// wl_region
	opRegionDestroy = 0
	// wl_surface
	opSurfaceDestroy, opSurfaceAttach, opSurfaceDamage = 0, 1, 2
	opSurfaceSetInputRegion, opSurfaceCommit           = 5, 6
	opSurfaceSetBufferScale, opSurfaceDamageBuffer     = 8, 9
	// wl_shm, wl_shm_pool, wl_buffer
	opShmCreatePool                          = 0
	opPoolCreateBuffer, opPoolDestroy        = 0, 1
	opBufferDestroy                          = 0
	evBufferRelease                          = 0
	shmFormatARGB8888                 uint32 = 0
	// wl_output
	evOutputScale = 3
	// zwlr_layer_shell_v1
	opGetLayerSurface        = 0
	layerOverlay      uint32 = 3
	// zwlr_layer_surface_v1
	opLayerSetSize, opLayerSetAnchor           = 0, 1
	opLayerSetKeyboardInteractivity            = 4
	opLayerAckConfigure, opLayerDestroy        = 6, 7
	evLayerConfigure, evLayerClosed            = 0, 1
	anchorTop                           uint32 = 1
)

// wlConn is one connection. Requests may come from any goroutine (writes are serialised);
// events are read and dispatched on the connection's reader goroutine.
type wlConn struct {
	c    *net.UnixConn
	wmu  sync.Mutex
	next uint32

	mu       sync.Mutex
	handlers map[uint32]func(op uint16, args []byte)
	err      error // the connection broke or the compositor reported a protocol error
	dead     chan struct{}
}

func dialWayland() (*wlConn, error) {
	name := os.Getenv("WAYLAND_DISPLAY")
	if name == "" {
		return nil, errors.New("not a Wayland session (WAYLAND_DISPLAY is unset)")
	}
	path := name
	if !filepath.IsAbs(path) {
		dir := os.Getenv("XDG_RUNTIME_DIR")
		if dir == "" {
			return nil, errors.New("XDG_RUNTIME_DIR is unset")
		}
		path = filepath.Join(dir, name)
	}
	c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	w := newWlConn(c)
	return w, nil
}

func newWlConn(c *net.UnixConn) *wlConn {
	w := &wlConn{c: c, next: displayID + 1, handlers: map[uint32]func(uint16, []byte){}, dead: make(chan struct{})}
	w.handle(displayID, func(op uint16, a []byte) {
		if op == evDisplayError {
			r := argReader{b: a}
			obj, code, msg := r.u32(), r.u32(), r.str()
			w.fail(fmt.Errorf("wayland protocol error on object %d (code %d): %s", obj, code, msg))
		}
	})
	go w.read()
	return w
}

func (w *wlConn) newID() uint32 {
	w.wmu.Lock()
	defer w.wmu.Unlock()
	id := w.next
	w.next++
	return id
}

func (w *wlConn) handle(id uint32, f func(op uint16, args []byte)) {
	w.mu.Lock()
	w.handlers[id] = f
	w.mu.Unlock()
}

func (w *wlConn) forget(id uint32) {
	w.mu.Lock()
	delete(w.handlers, id)
	w.mu.Unlock()
}

func (w *wlConn) fail(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err == nil {
		w.err = err
		close(w.dead)
	}
}

func (w *wlConn) broken() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

func (w *wlConn) close() {
	w.fail(errors.New("closed"))
	_ = w.c.Close()
}

// send writes one request; fd >= 0 rides along.
func (w *wlConn) send(id uint32, op uint16, args []byte, fd int) error {
	if err := w.broken(); err != nil {
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

func (w *wlConn) read() {
	buf := make([]byte, 0, 8192)
	tmp := make([]byte, 4096)
	oob := make([]byte, unix.CmsgSpace(4*28))
	for {
		n, oobn, _, _, err := w.c.ReadMsgUnix(tmp, oob)
		if err != nil {
			w.fail(err)
			return
		}
		// We never ask for an event that carries a file descriptor; close any that arrive.
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

// roundtrip waits until the compositor has handled everything sent so far.
func (w *wlConn) roundtrip(timeout time.Duration) error {
	id := w.newID()
	done := make(chan struct{})
	w.handle(id, func(op uint16, _ []byte) {
		if op == evCallbackDone {
			close(done)
		}
	})
	defer w.forget(id)
	if err := w.send(displayID, opDisplaySync, args().u32(id).b, -1); err != nil {
		return err
	}
	select {
	case <-done:
		return nil
	case <-w.dead:
		return w.broken()
	case <-time.After(timeout):
		return errors.New("wayland: compositor did not answer")
	}
}

// --- argument encoding ---

type argBuf struct{ b []byte }

func args() *argBuf { return &argBuf{} }

func (a *argBuf) u32(v uint32) *argBuf { a.b = binary.LittleEndian.AppendUint32(a.b, v); return a }
func (a *argBuf) i32(v int32) *argBuf  { return a.u32(uint32(v)) }
func (a *argBuf) str(s string) *argBuf {
	a.u32(uint32(len(s) + 1))
	a.b = append(a.b, s...)
	a.b = append(a.b, 0)
	for len(a.b)%4 != 0 {
		a.b = append(a.b, 0)
	}
	return a
}

type argReader struct {
	b   []byte
	off int
}

func (r *argReader) u32() uint32 {
	if r.off+4 > len(r.b) {
		r.off = len(r.b)
		return 0
	}
	v := binary.LittleEndian.Uint32(r.b[r.off:])
	r.off += 4
	return v
}

func (r *argReader) i32() int32 { return int32(r.u32()) }

func (r *argReader) str() string {
	n := int(r.u32())
	if n == 0 || r.off+n > len(r.b) {
		return ""
	}
	s := string(r.b[r.off : r.off+n-1])
	r.off += (n + 3) &^ 3
	return s
}

// --- the island's backend ---

// wayland is the island on a wlr-layer-shell compositor.
type wayland struct {
	w                           *wlConn
	compositor, shm, layerShell uint32
	compVersion                 uint32

	// patience is how long to wait for the compositor's answers (compositorWait if zero).
	patience time.Duration

	mu     sync.Mutex
	scales map[uint32]int32 // wl_output id → scale

	// While shown:
	surface, layer, pool uint32
	buffers              [2]wlBuffer
	mem                  []byte
	memFD                int
	scale                int
}

type wlBuffer struct {
	id   uint32
	busy bool
}

func newWayland() (*wayland, error) {
	w, err := dialWayland()
	if err != nil {
		return nil, err
	}
	b := &wayland{w: w, scales: map[uint32]int32{}, memFD: -1}
	if err := b.bind(); err != nil {
		w.close()
		return nil, err
	}
	return b, nil
}

// compositorWait is how long the island waits for the compositor before giving up on it.
const compositorWait = 2 * time.Second

func (b *wayland) wait() time.Duration { return cmp.Or(b.patience, compositorWait) }

// bind collects the globals and binds what the island needs.
func (b *wayland) bind() error {
	type global struct {
		name    uint32
		iface   string
		version uint32
	}
	var mu sync.Mutex
	var globals []global
	reg := b.w.newID()
	b.w.handle(reg, func(op uint16, a []byte) {
		if op == evGlobal {
			r := argReader{b: a}
			g := global{r.u32(), r.str(), r.u32()}
			mu.Lock()
			globals = append(globals, g)
			mu.Unlock()
		}
	})
	if err := b.w.send(displayID, opDisplayGetRegistry, args().u32(reg).b, -1); err != nil {
		return err
	}
	if err := b.w.roundtrip(b.wait()); err != nil {
		return err
	}
	mu.Lock()
	defer mu.Unlock()
	// on, when set, handles the new object's events. It is registered before the bind is
	// sent: the compositor may answer at once (wl_output sends its scale straight away), and
	// the reader drops events for ids it has no handler for.
	bindTo := func(g global, version uint32, on func(id uint32) func(op uint16, args []byte)) uint32 {
		id := b.w.newID()
		if on != nil {
			b.w.handle(id, on(id))
		}
		_ = b.w.send(reg, opRegistryBind, args().u32(g.name).str(g.iface).u32(version).u32(id).b, -1)
		return id
	}
	for _, g := range globals {
		switch g.iface {
		case "wl_compositor":
			if g.version < 3 {
				continue // set_buffer_scale is version 3
			}
			b.compVersion = min(g.version, 4)
			b.compositor = bindTo(g, b.compVersion, nil)
		case "wl_shm":
			b.shm = bindTo(g, 1, nil)
		case "zwlr_layer_shell_v1":
			b.layerShell = bindTo(g, 1, nil)
		case "wl_output":
			if g.version < 2 {
				continue // scale is version 2
			}
			bindTo(g, 2, func(id uint32) func(uint16, []byte) {
				b.mu.Lock()
				b.scales[id] = 1
				b.mu.Unlock()
				return func(op uint16, a []byte) {
					if op == evOutputScale {
						r := argReader{b: a}
						s := r.i32()
						b.mu.Lock()
						b.scales[id] = s
						b.mu.Unlock()
					}
				}
			})
		}
	}
	switch {
	case b.layerShell == 0:
		return errors.New("the compositor has no wlr-layer-shell (GNOME?), so nothing can float over other windows")
	case b.compositor == 0:
		return errors.New("the compositor has no wl_compositor version 3")
	case b.shm == 0:
		return errors.New("the compositor has no wl_shm")
	}
	return b.w.roundtrip(b.wait()) // the outputs' scale events
}

// outputScale is the largest output scale: the buffer is sharp on every screen.
func (b *wayland) outputScale() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := int32(1)
	for _, v := range b.scales {
		s = max(s, v)
	}
	return int(s)
}

func (b *wayland) show() (float64, error) {
	w := b.w
	b.scale = b.outputScale()
	b.surface = w.newID()
	if err := w.send(b.compositor, opCreateSurface, args().u32(b.surface).b, -1); err != nil {
		return 0, err
	}
	// An empty input region: every click goes to whatever is underneath.
	region := w.newID()
	_ = w.send(b.compositor, opCreateRegion, args().u32(region).b, -1)
	_ = w.send(b.surface, opSurfaceSetInputRegion, args().u32(region).b, -1)
	_ = w.send(region, opRegionDestroy, nil, -1)
	_ = w.send(b.surface, opSurfaceSetBufferScale, args().i32(int32(b.scale)).b, -1)

	b.layer = w.newID()
	configured := make(chan uint32, 4)
	closed := make(chan struct{}, 1)
	w.handle(b.layer, func(op uint16, a []byte) {
		switch op {
		case evLayerConfigure:
			r := argReader{b: a}
			select {
			case configured <- r.u32():
			default:
			}
		case evLayerClosed:
			select {
			case closed <- struct{}{}:
			default:
			}
		}
	})
	_ = w.send(b.layerShell, opGetLayerSurface,
		args().u32(b.layer).u32(b.surface).u32(0).u32(layerOverlay).str("jarvis-island").b, -1)
	_ = w.send(b.layer, opLayerSetSize, args().u32(CanvasW).u32(CanvasH).b, -1)
	_ = w.send(b.layer, opLayerSetAnchor, args().u32(anchorTop).b, -1)
	_ = w.send(b.layer, opLayerSetKeyboardInteractivity, args().u32(0).b, -1)
	if err := w.send(b.surface, opSurfaceCommit, nil, -1); err != nil {
		return 0, err
	}
	select {
	case serial := <-configured:
		_ = w.send(b.layer, opLayerAckConfigure, args().u32(serial).b, -1)
	case <-closed:
		b.hide()
		return 0, errors.New("the compositor closed the overlay")
	case <-w.dead:
		return 0, w.broken()
	case <-time.After(b.wait()):
		b.hide()
		return 0, errors.New("the compositor never configured the overlay")
	}
	if err := b.alloc(); err != nil {
		b.hide()
		return 0, err
	}
	return float64(b.scale), w.broken()
}

// alloc makes two buffers in one shared-memory pool.
func (b *wayland) alloc() error {
	pw, ph := CanvasW*b.scale, CanvasH*b.scale
	stride := pw * 4
	size := stride * ph
	fd, err := unix.MemfdCreate("jarvis-island", unix.MFD_CLOEXEC)
	if err != nil {
		return err
	}
	if err := unix.Ftruncate(fd, int64(2*size)); err != nil {
		_ = unix.Close(fd)
		return err
	}
	mem, err := unix.Mmap(fd, 0, 2*size, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		_ = unix.Close(fd)
		return err
	}
	b.mem, b.memFD = mem, fd
	b.pool = b.w.newID()
	if err := b.w.send(b.shm, opShmCreatePool, args().u32(b.pool).i32(int32(2*size)).b, fd); err != nil {
		return err
	}
	for i := range b.buffers {
		id := b.w.newID()
		b.buffers[i] = wlBuffer{id: id}
		idx := i
		b.w.handle(id, func(op uint16, _ []byte) {
			if op == evBufferRelease {
				b.mu.Lock()
				b.buffers[idx].busy = false
				b.mu.Unlock()
			}
		})
		_ = b.w.send(b.pool, opPoolCreateBuffer, args().u32(id).i32(int32(i*size)).i32(int32(pw)).i32(int32(ph)).
			i32(int32(stride)).u32(shmFormatARGB8888).b, -1)
	}
	return b.w.broken()
}

func (b *wayland) present(img *image.RGBA) error {
	if err := b.w.broken(); err != nil {
		return err
	}
	pw, ph := CanvasW*b.scale, CanvasH*b.scale
	size := pw * ph * 4
	if img.Bounds().Dx() != pw || img.Bounds().Dy() != ph || len(b.mem) < 2*size {
		return fmt.Errorf("wayland: frame %v doesn't fit the %dx%d buffer", img.Bounds().Size(), pw, ph)
	}
	b.mu.Lock()
	idx := -1
	for i := range b.buffers {
		if !b.buffers[i].busy {
			idx = i
			b.buffers[i].busy = true
			break
		}
	}
	b.mu.Unlock()
	if idx < 0 {
		return nil // the compositor still holds both: drop this frame
	}
	toARGB(b.mem[idx*size:(idx+1)*size], img.Pix)
	_ = b.w.send(b.surface, opSurfaceAttach, args().u32(b.buffers[idx].id).i32(0).i32(0).b, -1)
	if b.compVersion >= 4 {
		_ = b.w.send(b.surface, opSurfaceDamageBuffer, args().i32(0).i32(0).i32(int32(pw)).i32(int32(ph)).b, -1)
	} else {
		_ = b.w.send(b.surface, opSurfaceDamage, args().i32(0).i32(0).i32(CanvasW).i32(CanvasH).b, -1)
	}
	return b.w.send(b.surface, opSurfaceCommit, nil, -1)
}

// toARGB converts premultiplied RGBA bytes to wl_shm's ARGB8888 (B, G, R, A in memory).
func toARGB(dst, src []byte) {
	for i := 0; i+3 < len(src) && i+3 < len(dst); i += 4 {
		dst[i], dst[i+1], dst[i+2], dst[i+3] = src[i+2], src[i+1], src[i], src[i+3]
	}
}

// hide destroys the surface (unmapping it) and frees the buffers; show makes new ones.
func (b *wayland) hide() {
	w := b.w
	if b.layer != 0 {
		_ = w.send(b.layer, opLayerDestroy, nil, -1)
		w.forget(b.layer)
		b.layer = 0
	}
	if b.surface != 0 {
		_ = w.send(b.surface, opSurfaceDestroy, nil, -1)
		b.surface = 0
	}
	for i := range b.buffers {
		if id := b.buffers[i].id; id != 0 {
			_ = w.send(id, opBufferDestroy, nil, -1)
			w.forget(id)
		}
		b.buffers[i] = wlBuffer{}
	}
	if b.pool != 0 {
		_ = w.send(b.pool, opPoolDestroy, nil, -1)
		b.pool = 0
	}
	if b.mem != nil {
		_ = unix.Munmap(b.mem)
		b.mem = nil
	}
	if b.memFD >= 0 {
		_ = unix.Close(b.memFD)
		b.memFD = -1
	}
	_ = w.roundtrip(b.wait() / 2)
}

func (b *wayland) close() {
	b.hide()
	b.w.close()
}
