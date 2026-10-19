//go:build linux

package vptr

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/alexberardi/jarvis-server/internal/agent/wlwire"
)

// A scripted compositor on a socketpair, as vkbd's: it advertises globals (with versions),
// answers syncs, gives each xdg_output a logical geometry and records every request.

type req struct {
	id   uint32
	op   uint16
	args []byte
}

type global struct {
	iface   string
	version uint32
	box     Box // wl_output: its logical geometry
}

type fake struct {
	c *net.UnixConn

	mu      sync.Mutex
	globals map[uint32]global // by name
	reqs    []req
	ids     map[string]uint32
	reg     uint32
	outputs map[uint32]uint32 // wl_output id → global name
}

func newFake(t *testing.T, gs ...global) (*fake, func() (*wlwire.Conn, error)) {
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
	f := &fake{c: conn(fds[0]), globals: map[uint32]global{}, ids: map[string]uint32{}, outputs: map[uint32]uint32{}}
	for i, g := range gs {
		f.globals[uint32(i+1)] = g
	}
	client := conn(fds[1])
	t.Cleanup(func() { f.c.Close() })
	go f.serve()
	used := false
	return f, func() (*wlwire.Conn, error) {
		if used {
			t.Fatal("dialed twice")
		}
		used = true
		return wlwire.NewConn(client), nil
	}
}

func (f *fake) event(id uint32, op uint16, a *wlwire.Args) {
	body := a.Bytes()
	m := binary.LittleEndian.AppendUint32(nil, id)
	m = binary.LittleEndian.AppendUint32(m, uint32(8+len(body))<<16|uint32(op))
	_, _ = f.c.Write(append(m, body...))
}

func (f *fake) serve() {
	var buf []byte
	tmp := make([]byte, 65536)
	for {
		n, err := f.c.Read(tmp)
		if err != nil {
			return
		}
		buf = append(buf, tmp[:n]...)
		for len(buf) >= 8 {
			size := int(binary.LittleEndian.Uint32(buf[4:]) >> 16)
			if len(buf) < size {
				break
			}
			r := req{id: binary.LittleEndian.Uint32(buf), op: uint16(binary.LittleEndian.Uint32(buf[4:])), args: append([]byte(nil), buf[8:size]...)}
			buf = buf[size:]
			f.mu.Lock()
			f.reqs = append(f.reqs, r)
			f.mu.Unlock()
			f.answer(r)
		}
	}
}

func (f *fake) answer(r req) {
	a := wlwire.NewReader(r.args)
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.id == wlwire.DisplayID && r.op == wlwire.OpDisplayGetRegistry:
		f.reg = a.U32()
		for name := uint32(1); name <= uint32(len(f.globals))+8; name++ {
			if g, ok := f.globals[name]; ok {
				f.event(f.reg, wlwire.EvGlobal, wlwire.NewArgs().U32(name).Str(g.iface).U32(g.version))
			}
		}
	case r.id == wlwire.DisplayID && r.op == wlwire.OpDisplaySync:
		f.event(a.U32(), wlwire.EvCallbackDone, wlwire.NewArgs().U32(0))
	case r.id == f.reg && r.op == wlwire.OpRegistryBind:
		name, iface, v, id := a.U32(), a.Str(), a.U32(), a.U32()
		if iface == IfaceOutput {
			f.outputs[id] = name
		} else {
			f.ids[iface] = id
			f.ids[iface+".v"] = v
		}
	case r.id == f.ids[IfaceManager] && r.op == opCreateVirtualPointer:
		f.ids["ptr.seat"], f.ids["ptr"] = a.U32(), a.U32()
	case r.id == f.ids[IfaceOutputManager] && r.op == opGetXdgOutput:
		x, wl := a.U32(), a.U32()
		b := f.globals[f.outputs[wl]].box
		f.event(x, evLogicalPosition, wlwire.NewArgs().I32(int32(b.X)).I32(int32(b.Y)))
		f.event(x, evLogicalSize, wlwire.NewArgs().I32(int32(b.W)).I32(int32(b.H)))
	}
}

// addOutput announces a new output global; removeOutput withdraws one.
func (f *fake) addOutput(name uint32, b Box) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.globals[name] = global{IfaceOutput, 4, b}
	f.event(f.reg, wlwire.EvGlobal, wlwire.NewArgs().U32(name).Str(IfaceOutput).U32(4))
}

func (f *fake) removeOutput(name uint32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.globals, name)
	f.event(f.reg, evGlobalRemove, wlwire.NewArgs().U32(name))
}

func (f *fake) id(k string) uint32 { f.mu.Lock(); defer f.mu.Unlock(); return f.ids[k] }

// events decodes the requests on the virtual pointer, in order.
func (f *fake) events() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.reqs {
		if r.id != f.ids["ptr"] || f.ids["ptr"] == 0 {
			continue
		}
		a := wlwire.NewReader(r.args)
		switch r.op {
		case opMotionAbsolute:
			_ = a.U32()
			out = append(out, fmt.Sprintf("abs %d %d / %d %d", a.U32(), a.U32(), a.U32(), a.U32()))
		case opButton:
			_ = a.U32()
			out = append(out, fmt.Sprintf("button %#x %d", a.U32(), a.U32()))
		case opAxis:
			_ = a.U32()
			out = append(out, fmt.Sprintf("axis %d %d", a.U32(), a.I32()))
		case opAxisDiscrete:
			_ = a.U32()
			out = append(out, fmt.Sprintf("discrete %d %d %d", a.U32(), a.I32(), a.I32()))
		case opAxisSource:
			out = append(out, fmt.Sprintf("source %d", a.U32()))
		case opFrame:
			out = append(out, "frame")
		case opDestroy:
			out = append(out, "destroy")
		default:
			out = append(out, fmt.Sprintf("op %d", r.op))
		}
	}
	return out
}

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

var wlEnv = env(map[string]string{"WAYLAND_DISPLAY": "wayland-9"})

const guard = 5 * time.Second // a hang guard, never waited on when the fake answers

var (
	seat   = global{IfaceSeat, 7, Box{}}
	mgrV1  = global{IfaceManager, 1, Box{}}
	mgrV2  = global{IfaceManager, 2, Box{}}
	xdgMgr = global{IfaceOutputManager, 3, Box{}}
	// A 1920x1080 output at the origin and a 1280x800 one to its left, 200 lower: the layout
	// is -1280,0 3200x1080.
	main = global{IfaceOutput, 4, Box{0, 0, 1920, 1080}}
	left = global{IfaceOutput, 4, Box{-1280, 200, 1280, 800}}
)

func join(s []string) string { return strings.Join(s, ", ") }

func TestPointerMotionExtent(t *testing.T) {
	f, dial := newFake(t, global{"wl_compositor", 6, Box{}}, seat, mgrV2, main, xdgMgr, left)
	p := &Pointer{Getenv: wlEnv, Dial: dial, Timeout: guard, Settle: -1}
	if err := p.Send(context.Background(), Motion(100, 50), Motion(-1280, 1079)); err != nil {
		t.Fatal(err)
	}
	if f.id("ptr") == 0 || f.id("ptr.seat") != f.id(IfaceSeat) || f.id(IfaceSeat+".v") != 1 ||
		f.id(IfaceManager+".v") != 2 || f.id(IfaceOutputManager+".v") != 1 {
		t.Fatalf("binds: %v", f.ids)
	}
	want := "abs 1380 50 / 3200 1080, frame, abs 0 1079 / 3200 1080, frame"
	if got := join(f.events()); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	// Off the layout: refused, nothing sent.
	for _, pt := range [][2]int{{1920, 10}, {-1281, 300}, {10, -1}, {10, 1080}} {
		if err := p.Send(context.Background(), Motion(pt[0], pt[1])); err == nil || !strings.Contains(err.Error(), "outside the screen") {
			t.Fatalf("%v: err %v", pt, err)
		}
	}
	if got := join(f.events()); got != want {
		t.Fatalf("sent off-screen motion: %s", got)
	}
}

// The outputs are followed while the pointer lives: an unplugged one leaves the layout, a new
// one joins it.
func TestPointerFollowsOutputs(t *testing.T) {
	f, dial := newFake(t, seat, mgrV2, xdgMgr, main, left)
	p := &Pointer{Getenv: wlEnv, Dial: dial, Timeout: guard, Settle: -1}
	ctx := context.Background()
	if err := p.Send(ctx, Motion(0, 0)); err != nil {
		t.Fatal(err)
	}
	f.removeOutput(5) // left
	f.addOutput(9, Box{1920, 0, 1000, 500})
	if err := p.Send(ctx, Motion(0, 0)); err != nil {
		t.Fatal(err)
	}
	want := "abs 1280 0 / 3200 1080, frame, abs 0 0 / 2920 1080, frame"
	if got := join(f.events()); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestButtonPressRelease(t *testing.T) {
	f, dial := newFake(t, seat, mgrV2, xdgMgr, main)
	p := &Pointer{Getenv: wlEnv, Dial: dial, Timeout: guard, Settle: -1}
	ctx := context.Background()
	if err := p.Send(ctx, Motion(10, 20), Press(BtnLeft), Release(BtnLeft)); err != nil {
		t.Fatal(err)
	}
	ptr := f.id("ptr")
	// The same pointer serves later calls (Dial fails the test if called again).
	if err := p.Send(ctx, Press(BtnRight)); err != nil {
		t.Fatal(err)
	}
	if err := p.Send(ctx, Release(BtnRight)); err != nil {
		t.Fatal(err)
	}
	if f.id("ptr") != ptr {
		t.Fatal("a second pointer was created")
	}
	p.Close()
	want := "abs 10 20 / 1920 1080, frame, button 0x110 1, frame, button 0x110 0, frame, " +
		"button 0x111 1, frame, button 0x111 0, frame, destroy"
	if got := join(f.events()); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestAxisFixed(t *testing.T) {
	for _, tc := range []struct {
		mgr  global
		want string
	}{
		// Version 1: axis, the value a wl_fixed (15 per notch, ×256).
		{mgrV1, "source 0, axis 0 11520, axis 1 -3840, frame"},
		// Version 2: axis_discrete, with the notch count.
		{mgrV2, "source 0, discrete 0 11520 3, discrete 1 -3840 -1, frame"},
	} {
		f, dial := newFake(t, seat, tc.mgr, xdgMgr, main)
		p := &Pointer{Getenv: wlEnv, Dial: dial, Timeout: guard, Settle: -1}
		if err := p.Send(context.Background(), Wheel(-1, 3)); err != nil {
			t.Fatal(err)
		}
		if got := join(f.events()); got != tc.want {
			t.Fatalf("v%d: got  %s\nwant %s", tc.mgr.version, got, tc.want)
		}
	}
}

func TestAvailable(t *testing.T) {
	ctx := context.Background()
	_, dial := newFake(t, seat, mgrV2, xdgMgr, main)
	if ok, why := (&Pointer{Getenv: wlEnv, Dial: dial, Timeout: guard}).Available(ctx); !ok {
		t.Fatal(why)
	}
	for _, tc := range []struct {
		gs   []global
		want string
	}{
		{[]global{seat, xdgMgr}, "virtual-pointer"},
		{[]global{mgrV2, xdgMgr}, "seat"},
		{[]global{seat, mgrV2}, "xdg-output"},
	} {
		_, dial := newFake(t, tc.gs...)
		if ok, why := (&Pointer{Getenv: wlEnv, Dial: dial, Timeout: guard}).Available(ctx); ok || !strings.Contains(why, tc.want) {
			t.Fatalf("%v: %v %q", tc.gs, ok, why)
		}
		_, dial = newFake(t, tc.gs...)
		if err := (&Pointer{Getenv: wlEnv, Dial: dial, Timeout: guard}).Send(ctx, Press(BtnLeft)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%v: send err %v", tc.gs, err)
		}
	}
	never := func() (*wlwire.Conn, error) { t.Fatal("dialed"); return nil, nil }
	if ok, _ := (&Pointer{Getenv: env(nil), Dial: never}).Available(ctx); ok {
		t.Fatal("available without Wayland")
	}
}

// A new pointer waits DefaultSettle before its first event (clients bind their wl_pointer only
// once the seat has one); a live one doesn't wait again.
func TestSettleOnCreate(t *testing.T) {
	f, dial := newFake(t, seat, mgrV2, xdgMgr, main)
	var slept []time.Duration
	var eventsAtSleep int
	p := &Pointer{Getenv: wlEnv, Dial: dial, Timeout: guard, Sleep: func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		eventsAtSleep = len(f.events())
		return nil
	}}
	ctx := context.Background()
	for range 2 {
		if err := p.Send(ctx, Motion(1, 1), Press(BtnLeft), Release(BtnLeft)); err != nil {
			t.Fatal(err)
		}
	}
	if len(slept) != 1 || slept[0] != DefaultSettle || eventsAtSleep != 0 {
		t.Fatalf("slept %v with %d events sent", slept, eventsAtSleep)
	}
}
