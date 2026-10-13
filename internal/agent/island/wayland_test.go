//go:build linux

package island

import (
	"encoding/binary"
	"image"
	"image/color"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/alexberardi/jarvis-server/internal/agent/wlwire"
)

// The Wayland client against a scripted compositor on a socketpair: the requests it sends
// (and their argument encoding, file descriptor included), the order layer-shell requires
// (configure → ack → attach), the click-through input region and the pixel format.
// TestWaylandLiveProbe runs the same handshake against the real compositor without ever
// attaching a buffer, so nothing appears on screen.

type req struct {
	id   uint32
	op   uint16
	args []byte
	fd   int
}

type fakeCompositor struct {
	t    *testing.T
	c    *net.UnixConn
	mu   sync.Mutex
	reqs []req
	// object ids the client bound or created, by role
	ids map[string]uint32
	shm []byte
}

func newFakeCompositor(t *testing.T) (*fakeCompositor, *net.UnixConn) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	conn := func(fd int) *net.UnixConn {
		f := os.NewFile(uintptr(fd), "wl")
		c, err := net.FileConn(f)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		return c.(*net.UnixConn)
	}
	f := &fakeCompositor{t: t, c: conn(fds[0]), ids: map[string]uint32{}}
	go f.serve()
	return f, conn(fds[1])
}

func (f *fakeCompositor) event(id uint32, op uint16, a *wlwire.Args) {
	var body []byte
	if a != nil {
		body = a.Bytes()
	}
	m := binary.LittleEndian.AppendUint32(nil, id)
	m = binary.LittleEndian.AppendUint32(m, uint32(8+len(body))<<16|uint32(op))
	_, _ = f.c.Write(append(m, body...))
}

func (f *fakeCompositor) serve() {
	buf := []byte{}
	tmp := make([]byte, 4096)
	oob := make([]byte, unix.CmsgSpace(4*4))
	var fds []int
	for {
		n, oobn, _, _, err := f.c.ReadMsgUnix(tmp, oob)
		if err != nil {
			return
		}
		if oobn > 0 {
			msgs, _ := unix.ParseSocketControlMessage(oob[:oobn])
			for _, m := range msgs {
				got, _ := unix.ParseUnixRights(&m)
				fds = append(fds, got...)
			}
		}
		buf = append(buf, tmp[:n]...)
		for len(buf) >= 8 {
			size := int(binary.LittleEndian.Uint32(buf[4:]) >> 16)
			if len(buf) < size {
				break
			}
			r := req{id: binary.LittleEndian.Uint32(buf), op: uint16(binary.LittleEndian.Uint32(buf[4:])), args: append([]byte(nil), buf[8:size]...), fd: -1}
			buf = buf[size:]
			if f.isPool(r) {
				r.fd, fds = fds[0], fds[1:]
			}
			f.mu.Lock()
			f.reqs = append(f.reqs, r)
			f.mu.Unlock()
			f.answer(r)
		}
	}
}

func (f *fakeCompositor) isPool(r req) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return r.id == f.ids["wl_shm"] && r.op == opShmCreatePool
}

func (f *fakeCompositor) answer(r req) {
	a := wlwire.NewReader(r.args)
	f.mu.Lock()
	ids := f.ids
	f.mu.Unlock()
	set := func(k string, v uint32) { f.mu.Lock(); f.ids[k] = v; f.mu.Unlock() }
	switch {
	case r.id == displayID && r.op == opDisplayGetRegistry:
		reg := a.U32()
		set("registry", reg)
		for i, g := range []struct {
			iface string
			v     uint32
		}{{"wl_compositor", 6}, {"wl_shm", 1}, {"wl_seat", 9}, {"zwlr_layer_shell_v1", 4}, {"wl_output", 4}} {
			f.event(reg, evGlobal, wlwire.NewArgs().U32(uint32(i+1)).Str(g.iface).U32(g.v))
		}
	case r.id == displayID && r.op == opDisplaySync:
		f.event(a.U32(), evCallbackDone, wlwire.NewArgs().U32(0))
	case r.id == ids["registry"] && r.op == opRegistryBind:
		_, iface, v, id := a.U32(), a.Str(), a.U32(), a.U32()
		set(iface, id)
		set(iface+".version", v)
		if iface == "wl_output" {
			f.event(id, evOutputScale, wlwire.NewArgs().I32(2))
		}
	case r.id == ids["wl_compositor"] && r.op == opCreateSurface:
		set("surface", a.U32())
	case r.id == ids["wl_compositor"] && r.op == opCreateRegion:
		set("region", a.U32())
	case r.id == ids["zwlr_layer_shell_v1"] && r.op == opGetLayerSurface:
		set("layer", a.U32())
	case r.id == ids["surface"] && r.op == opSurfaceCommit && ids["configured"] == 0:
		set("configured", 1)
		f.event(ids["layer"], evLayerConfigure, wlwire.NewArgs().U32(77).U32(CanvasW).U32(CanvasH))
	case r.id == ids["wl_shm"] && r.op == opShmCreatePool:
		pool, size := a.U32(), a.I32()
		set("pool", pool)
		mem, err := unix.Mmap(r.fd, 0, int(size), unix.PROT_READ, unix.MAP_SHARED)
		if err == nil {
			f.mu.Lock()
			f.shm = mem
			f.mu.Unlock()
		}
	}
}

func (f *fakeCompositor) requests() []req {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]req(nil), f.reqs...)
}

func (f *fakeCompositor) id(k string) uint32 { f.mu.Lock(); defer f.mu.Unlock(); return f.ids[k] }

func TestWaylandClientSpeaksLayerShell(t *testing.T) {
	fc, conn := newFakeCompositor(t)
	b := &wayland{w: wlwire.NewConn(conn), scales: map[uint32]int32{}, memFD: -1, patience: hangGuard}
	if err := b.bind(); err != nil {
		t.Fatal(err)
	}
	if v := fc.id("wl_compositor.version"); v != 4 {
		t.Fatalf("bound wl_compositor v%d, want 4 (damage_buffer)", v)
	}
	if fc.id("wl_seat") != 0 {
		t.Fatal("bound a seat: the island takes no input")
	}
	scale, err := b.show()
	if err != nil {
		t.Fatal(err)
	}
	if scale != 2 {
		t.Fatalf("scale %v, want the output's 2", scale)
	}
	img := image.NewRGBA(image.Rect(0, 0, CanvasW*2, CanvasH*2))
	img.SetRGBA(0, 0, color.RGBA{R: 10, G: 20, B: 30, A: 40})
	if err := b.present(img); err != nil {
		t.Fatal(err)
	}
	if err := b.w.Roundtrip(hangGuard); err != nil {
		t.Fatal(err)
	}

	surface, layer, region := fc.id("surface"), fc.id("layer"), fc.id("region")
	var seq []string
	for _, r := range fc.requests() {
		a := wlwire.NewReader(r.args)
		switch {
		case r.id == surface && r.op == opSurfaceSetInputRegion:
			if got := a.U32(); got != region {
				t.Fatalf("input region %d, want the empty region %d", got, region)
			}
			seq = append(seq, "input_region")
		case r.id == region:
			if r.op != opRegionDestroy {
				t.Fatalf("region op %d: the input region must stay empty", r.op)
			}
		case r.id == fc.id("zwlr_layer_shell_v1"):
			_, s, out, lay, ns := a.U32(), a.U32(), a.U32(), a.U32(), a.Str()
			if s != surface || out != 0 || lay != layerOverlay || ns != "jarvis-island" {
				t.Fatalf("get_layer_surface(surface %d, output %d, layer %d, %q)", s, out, lay, ns)
			}
		case r.id == layer && r.op == opLayerSetSize:
			if w, h := a.U32(), a.U32(); w != CanvasW || h != CanvasH {
				t.Fatalf("size %dx%d", w, h)
			}
		case r.id == layer && r.op == opLayerSetAnchor:
			if a.U32() != anchorTop {
				t.Fatal("not anchored to the top edge alone (centred)")
			}
		case r.id == layer && r.op == opLayerSetKeyboardInteractivity:
			if a.U32() != 0 {
				t.Fatal("keyboard interactivity must be none")
			}
		case r.id == layer && r.op == opLayerAckConfigure:
			if a.U32() != 77 {
				t.Fatal("acked the wrong serial")
			}
			seq = append(seq, "ack")
		case r.id == surface && r.op == opSurfaceSetBufferScale:
			if a.I32() != 2 {
				t.Fatal("buffer scale")
			}
		case r.id == surface && r.op == opSurfaceAttach:
			seq = append(seq, "attach")
		case r.id == surface && r.op == opSurfaceCommit:
			seq = append(seq, "commit")
		case r.id == fc.id("wl_shm") && r.op == opShmCreatePool:
			if r.fd < 0 {
				t.Fatal("create_pool without a file descriptor")
			}
		case r.id == fc.id("pool") && r.op == opPoolCreateBuffer:
			_, off, w, h, stride, format := a.U32(), a.I32(), a.I32(), a.I32(), a.I32(), a.U32()
			if w != CanvasW*2 || h != CanvasH*2 || stride != w*4 || format != shmFormatARGB8888 || (off != 0 && off != stride*h) {
				t.Fatalf("buffer off %d %dx%d stride %d format %d", off, w, h, stride, format)
			}
		}
	}
	want := []string{"input_region", "commit", "ack", "attach", "commit"}
	if len(seq) != len(want) {
		t.Fatalf("sequence %v, want %v", seq, want)
	}
	for i := range want {
		if seq[i] != want[i] {
			t.Fatalf("sequence %v, want %v", seq, want)
		}
	}
	fc.mu.Lock()
	px := append([]byte(nil), fc.shm[:4]...)
	fc.mu.Unlock()
	if px[0] != 30 || px[1] != 20 || px[2] != 10 || px[3] != 40 {
		t.Fatalf("first pixel in shm % d, want B G R A = 30 20 10 40", px)
	}
	b.close()
}

func TestWaylandRefusesACompositorWithoutLayerShell(t *testing.T) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	mk := func(fd int) *net.UnixConn {
		f := os.NewFile(uintptr(fd), "wl")
		c, _ := net.FileConn(f)
		f.Close()
		return c.(*net.UnixConn)
	}
	server, client := mk(fds[0]), mk(fds[1])
	// A GNOME-like compositor: globals, but no zwlr_layer_shell_v1.
	go func() {
		fc := &fakeCompositor{t: t, c: server, ids: map[string]uint32{}}
		buf := make([]byte, 4096)
		for {
			n, err := server.Read(buf)
			if err != nil {
				return
			}
			for b := buf[:n]; len(b) >= 8; {
				size := int(binary.LittleEndian.Uint32(b[4:]) >> 16)
				id, op := binary.LittleEndian.Uint32(b), uint16(binary.LittleEndian.Uint32(b[4:]))
				a := wlwire.NewReader(b[8:size])
				switch {
				case id == displayID && op == opDisplayGetRegistry:
					reg := a.U32()
					fc.event(reg, evGlobal, wlwire.NewArgs().U32(1).Str("wl_compositor").U32(6))
					fc.event(reg, evGlobal, wlwire.NewArgs().U32(2).Str("wl_shm").U32(1))
				case id == displayID && op == opDisplaySync:
					fc.event(a.U32(), evCallbackDone, wlwire.NewArgs().U32(0))
				}
				b = b[size:]
			}
		}
	}()
	b := &wayland{w: wlwire.NewConn(client), scales: map[uint32]int32{}, memFD: -1, patience: hangGuard}
	defer b.w.Close()
	if err := b.bind(); err == nil {
		t.Fatal("bound without layer-shell")
	}
}

func TestWaylandArgEncoding(t *testing.T) {
	got := wlwire.NewArgs().U32(7).Str("wl_shm").I32(-1).Bytes()
	want := []byte{7, 0, 0, 0, 7, 0, 0, 0, 'w', 'l', '_', 's', 'h', 'm', 0, 0, 0xff, 0xff, 0xff, 0xff}
	if string(got) != string(want) {
		t.Fatalf("% x", got)
	}
	r := wlwire.NewReader(got)
	if r.U32() != 7 || r.Str() != "wl_shm" || r.I32() != -1 {
		t.Fatal("round trip")
	}
}

// TestWaylandLiveProbe binds the real compositor's globals and runs a layer surface through
// configure, but never attaches a buffer, so nothing is ever drawn. Opt-in.
func TestWaylandLiveProbe(t *testing.T) {
	if os.Getenv("JARVIS_ISLAND_WAYLAND_PROBE") == "" {
		t.Skip("JARVIS_ISLAND_WAYLAND_PROBE=1 to talk to this session's compositor")
	}
	b, err := newWayland()
	if err != nil {
		t.Fatal(err)
	}
	defer b.w.Close()
	b.scale = b.outputScale()
	t.Logf("compositor v%d, output scale %d", b.compVersion, b.scale)
	w := b.w
	b.surface = w.NewID()
	_ = w.Send(b.compositor, opCreateSurface, wlwire.NewArgs().U32(b.surface).Bytes(), -1)
	region := w.NewID()
	_ = w.Send(b.compositor, opCreateRegion, wlwire.NewArgs().U32(region).Bytes(), -1)
	_ = w.Send(b.surface, opSurfaceSetInputRegion, wlwire.NewArgs().U32(region).Bytes(), -1)
	_ = w.Send(region, opRegionDestroy, nil, -1)
	_ = w.Send(b.surface, opSurfaceSetBufferScale, wlwire.NewArgs().I32(int32(b.scale)).Bytes(), -1)
	b.layer = w.NewID()
	cfg := make(chan [3]uint32, 1)
	w.Handle(b.layer, func(op uint16, a []byte) {
		if op == evLayerConfigure {
			r := wlwire.NewReader(a)
			cfg <- [3]uint32{r.U32(), r.U32(), r.U32()}
		}
	})
	_ = w.Send(b.layerShell, opGetLayerSurface, wlwire.NewArgs().U32(b.layer).U32(b.surface).U32(0).U32(layerOverlay).Str("jarvis-island-probe").Bytes(), -1)
	_ = w.Send(b.layer, opLayerSetSize, wlwire.NewArgs().U32(CanvasW).U32(CanvasH).Bytes(), -1)
	_ = w.Send(b.layer, opLayerSetAnchor, wlwire.NewArgs().U32(anchorTop).Bytes(), -1)
	_ = w.Send(b.layer, opLayerSetKeyboardInteractivity, wlwire.NewArgs().U32(0).Bytes(), -1)
	_ = w.Send(b.surface, opSurfaceCommit, nil, -1)
	select {
	case c := <-cfg:
		t.Logf("configure serial %d size %dx%d", c[0], c[1], c[2])
		_ = w.Send(b.layer, opLayerAckConfigure, wlwire.NewArgs().U32(c[0]).Bytes(), -1)
	case <-time.After(2 * time.Second):
		t.Fatal("no configure")
	}
	// A pool and buffers too (never attached), to check create_pool's fd passing.
	if err := b.alloc(); err != nil {
		t.Fatal(err)
	}
	if err := w.Roundtrip(2 * time.Second); err != nil {
		t.Fatal(err)
	}
	b.hide()
	if err := w.Err(); err != nil {
		t.Fatal(err)
	}
}
