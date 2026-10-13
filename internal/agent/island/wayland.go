//go:build linux

package island

import (
	"cmp"
	"errors"
	"fmt"
	"image"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/alexberardi/jarvis-server/internal/agent/wlwire"
)

// A minimal Wayland client: just enough of wl_compositor, wl_shm, wl_output and
// zwlr_layer_shell_v1 to put one click-through overlay surface at the top centre of the screen.
// Wayland compositors that implement wlr-layer-shell (Hyprland, Sway, river, KDE, COSMIC…) show
// it; GNOME doesn't, and the island stays off there.
//
// The wire (connection, ids, argument encoding) is internal/agent/wlwire, shared with the
// dictation virtual keyboard.

const (
	displayID = wlwire.DisplayID

	opDisplaySync, opDisplayGetRegistry = wlwire.OpDisplaySync, wlwire.OpDisplayGetRegistry
	opRegistryBind                      = wlwire.OpRegistryBind
	evGlobal                            = wlwire.EvGlobal
	evCallbackDone                      = wlwire.EvCallbackDone
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

// --- the island's backend ---

// wayland is the island on a wlr-layer-shell compositor.
type wayland struct {
	w                           *wlwire.Conn
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
	w, err := wlwire.Dial()
	if err != nil {
		return nil, err
	}
	b := &wayland{w: w, scales: map[uint32]int32{}, memFD: -1}
	if err := b.bind(); err != nil {
		w.Close()
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
	reg := b.w.NewID()
	b.w.Handle(reg, func(op uint16, a []byte) {
		if op == evGlobal {
			r := wlwire.NewReader(a)
			g := global{r.U32(), r.Str(), r.U32()}
			mu.Lock()
			globals = append(globals, g)
			mu.Unlock()
		}
	})
	if err := b.w.Send(displayID, opDisplayGetRegistry, wlwire.NewArgs().U32(reg).Bytes(), -1); err != nil {
		return err
	}
	if err := b.w.Roundtrip(b.wait()); err != nil {
		return err
	}
	mu.Lock()
	defer mu.Unlock()
	// on, when set, handles the new object's events. It is registered before the bind is
	// sent: the compositor may answer at once (wl_output sends its scale straight away), and
	// the reader drops events for ids it has no handler for.
	bindTo := func(g global, version uint32, on func(id uint32) func(op uint16, args []byte)) uint32 {
		id := b.w.NewID()
		if on != nil {
			b.w.Handle(id, on(id))
		}
		_ = b.w.Send(reg, opRegistryBind, wlwire.NewArgs().U32(g.name).Str(g.iface).U32(version).U32(id).Bytes(), -1)
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
						r := wlwire.NewReader(a)
						s := r.I32()
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
	return b.w.Roundtrip(b.wait()) // the outputs' scale events
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
	b.surface = w.NewID()
	if err := w.Send(b.compositor, opCreateSurface, wlwire.NewArgs().U32(b.surface).Bytes(), -1); err != nil {
		return 0, err
	}
	// An empty input region: every click goes to whatever is underneath.
	region := w.NewID()
	_ = w.Send(b.compositor, opCreateRegion, wlwire.NewArgs().U32(region).Bytes(), -1)
	_ = w.Send(b.surface, opSurfaceSetInputRegion, wlwire.NewArgs().U32(region).Bytes(), -1)
	_ = w.Send(region, opRegionDestroy, nil, -1)
	_ = w.Send(b.surface, opSurfaceSetBufferScale, wlwire.NewArgs().I32(int32(b.scale)).Bytes(), -1)

	b.layer = w.NewID()
	configured := make(chan uint32, 4)
	closed := make(chan struct{}, 1)
	w.Handle(b.layer, func(op uint16, a []byte) {
		switch op {
		case evLayerConfigure:
			r := wlwire.NewReader(a)
			select {
			case configured <- r.U32():
			default:
			}
		case evLayerClosed:
			select {
			case closed <- struct{}{}:
			default:
			}
		}
	})
	_ = w.Send(b.layerShell, opGetLayerSurface,
		wlwire.NewArgs().U32(b.layer).U32(b.surface).U32(0).U32(layerOverlay).Str("jarvis-island").Bytes(), -1)
	_ = w.Send(b.layer, opLayerSetSize, wlwire.NewArgs().U32(CanvasW).U32(CanvasH).Bytes(), -1)
	_ = w.Send(b.layer, opLayerSetAnchor, wlwire.NewArgs().U32(anchorTop).Bytes(), -1)
	_ = w.Send(b.layer, opLayerSetKeyboardInteractivity, wlwire.NewArgs().U32(0).Bytes(), -1)
	if err := w.Send(b.surface, opSurfaceCommit, nil, -1); err != nil {
		return 0, err
	}
	select {
	case serial := <-configured:
		_ = w.Send(b.layer, opLayerAckConfigure, wlwire.NewArgs().U32(serial).Bytes(), -1)
	case <-closed:
		b.hide()
		return 0, errors.New("the compositor closed the overlay")
	case <-w.Dead():
		return 0, w.Err()
	case <-time.After(b.wait()):
		b.hide()
		return 0, errors.New("the compositor never configured the overlay")
	}
	if err := b.alloc(); err != nil {
		b.hide()
		return 0, err
	}
	return float64(b.scale), w.Err()
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
	b.pool = b.w.NewID()
	if err := b.w.Send(b.shm, opShmCreatePool, wlwire.NewArgs().U32(b.pool).I32(int32(2*size)).Bytes(), fd); err != nil {
		return err
	}
	for i := range b.buffers {
		id := b.w.NewID()
		b.buffers[i] = wlBuffer{id: id}
		idx := i
		b.w.Handle(id, func(op uint16, _ []byte) {
			if op == evBufferRelease {
				b.mu.Lock()
				b.buffers[idx].busy = false
				b.mu.Unlock()
			}
		})
		_ = b.w.Send(b.pool, opPoolCreateBuffer, wlwire.NewArgs().U32(id).I32(int32(i*size)).I32(int32(pw)).I32(int32(ph)).
			I32(int32(stride)).U32(shmFormatARGB8888).Bytes(), -1)
	}
	return b.w.Err()
}

func (b *wayland) present(img *image.RGBA) error {
	if err := b.w.Err(); err != nil {
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
	_ = b.w.Send(b.surface, opSurfaceAttach, wlwire.NewArgs().U32(b.buffers[idx].id).I32(0).I32(0).Bytes(), -1)
	if b.compVersion >= 4 {
		_ = b.w.Send(b.surface, opSurfaceDamageBuffer, wlwire.NewArgs().I32(0).I32(0).I32(int32(pw)).I32(int32(ph)).Bytes(), -1)
	} else {
		_ = b.w.Send(b.surface, opSurfaceDamage, wlwire.NewArgs().I32(0).I32(0).I32(CanvasW).I32(CanvasH).Bytes(), -1)
	}
	return b.w.Send(b.surface, opSurfaceCommit, nil, -1)
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
		_ = w.Send(b.layer, opLayerDestroy, nil, -1)
		w.Forget(b.layer)
		b.layer = 0
	}
	if b.surface != 0 {
		_ = w.Send(b.surface, opSurfaceDestroy, nil, -1)
		b.surface = 0
	}
	for i := range b.buffers {
		if id := b.buffers[i].id; id != 0 {
			_ = w.Send(id, opBufferDestroy, nil, -1)
			w.Forget(id)
		}
		b.buffers[i] = wlBuffer{}
	}
	if b.pool != 0 {
		_ = w.Send(b.pool, opPoolDestroy, nil, -1)
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
	_ = w.Roundtrip(b.wait() / 2)
}

func (b *wayland) close() {
	b.hide()
	b.w.Close()
}
