//go:build linux

package vptr

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/wlwire"
)

const (
	// wl_registry.global_remove
	evGlobalRemove = 1
	// zwlr_virtual_pointer_manager_v1
	opCreateVirtualPointer = 0
	// zwlr_virtual_pointer_v1
	opMotion, opMotionAbsolute, opButton, opAxis, opFrame, opAxisSource, opAxisStop, opAxisDiscrete, opDestroy = 0, 1, 2, 3, 4, 5, 6, 7, 8
	// zxdg_output_manager_v1.get_xdg_output; zxdg_output_v1 events
	opGetXdgOutput      = 1
	evLogicalPosition   = 0
	evLogicalSize       = 1
	buttonReleased      = 0
	buttonPressed       = 1
	axisVertical        = 0
	axisHorizontal      = 1
	axisSourceWheel     = 0
	managerVersionWheel = 2 // axis_discrete
)

// Pointer is the virtual-pointer backend. The zero value is ready; fields are for tests.
type Pointer struct {
	Getenv  func(string) string                        // os.Getenv when nil
	Dial    func() (*wlwire.Conn, error)               // wlwire.Dial when nil
	Timeout time.Duration                              // per round trip; 2 s when zero
	Now     func() time.Time                           // event timestamps; time.Now when nil
	Settle  time.Duration                              // DefaultSettle when zero; <0: none
	Sleep   func(context.Context, time.Duration) error // a real timer when nil

	mu sync.Mutex // serializes Send and Close; held across a call
	s  *session   // the live connection and pointer; nil until the first Send
}

// session is one connection with its virtual pointer and the outputs it tracks.
type session struct {
	c       *wlwire.Conn
	reg     uint32
	ptr     uint32
	version uint32 // the manager's, as bound
	xdgMgr  uint32

	mu      sync.Mutex               // guards the maps: the reader goroutine writes them
	globals map[uint32]wlwire.Global // wl_output globals by name, as the registry has them now
	outputs map[uint32]*output       // the ones bound, by global name
	order   []uint32                 // global names in arrival order, for the first seat
	all     map[uint32]wlwire.Global // every global at connect time
}

type output struct {
	box Box // logical position and size, from xdg-output
}

func (p *Pointer) Name() string { return Name }

func (p *Pointer) getenv(s string) string {
	if p.Getenv != nil {
		return p.Getenv(s)
	}
	return os.Getenv(s)
}

func (p *Pointer) dial() (*wlwire.Conn, error) {
	if p.Dial != nil {
		return p.Dial()
	}
	return wlwire.Dial()
}

func (p *Pointer) timeout() time.Duration { return cmp.Or(p.Timeout, 2*time.Second) }

func (p *Pointer) rt(ctx context.Context, c *wlwire.Conn) error {
	ctx, cancel := context.WithTimeout(ctx, p.timeout())
	defer cancel()
	return c.RoundtripCtx(ctx)
}

// Available: a Wayland session whose compositor offers the virtual-pointer manager, a seat and
// xdg-output (where the outputs are). Probed with one connection and one round trip, nothing
// created, unless a pointer is already live.
func (p *Pointer) Available(ctx context.Context) (bool, string) {
	if p.getenv("WAYLAND_DISPLAY") == "" {
		return false, "not a Wayland session"
	}
	p.mu.Lock()
	live := p.s != nil && p.s.c.Err() == nil
	p.mu.Unlock()
	if live {
		return true, ""
	}
	c, err := p.dial()
	if err != nil {
		return false, fmt.Sprintf("can't reach the compositor: %v", err)
	}
	defer c.Close()
	ctx2, cancel := context.WithTimeout(ctx, p.timeout())
	defer cancel()
	_, gs, err := c.Registry(ctx2)
	if err != nil {
		return false, fmt.Sprintf("compositor: %v", err)
	}
	have := map[string]bool{}
	for _, g := range gs {
		have[g.Iface] = true
	}
	return missing(have)
}

func missing(have map[string]bool) (bool, string) {
	switch {
	case !have[IfaceManager]:
		return false, "the compositor doesn't offer the virtual-pointer protocol"
	case !have[IfaceSeat]:
		return false, "the compositor has no seat"
	case !have[IfaceOutputManager]:
		return false, "the compositor doesn't say where its outputs are (no xdg-output)"
	}
	return true, ""
}

// Send posts the events in order, each followed by a frame, and waits until the compositor has
// them. The first call creates the pointer; a broken connection is replaced on the next call.
func (p *Pointer) Send(ctx context.Context, evs ...Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.s != nil && p.s.c.Err() != nil {
		p.s.c.Close()
		p.s = nil
	}
	if p.s == nil {
		s, err := p.connect(ctx)
		if err != nil {
			return err
		}
		p.s = s
	}
	if err := p.post(ctx, p.s, evs); err != nil {
		if p.s.c.Err() != nil {
			p.s.c.Close()
			p.s = nil
		}
		return err
	}
	return nil
}

// Close destroys the pointer and closes the connection.
func (p *Pointer) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.s == nil {
		return
	}
	if p.s.c.Send(p.s.ptr, opDestroy, nil, -1) == nil {
		_ = p.rt(context.Background(), p.s.c)
	}
	p.s.c.Close()
	p.s = nil
}

// connect dials, binds the seat, the manager and xdg-output, creates the pointer and starts
// tracking the outputs.
func (p *Pointer) connect(ctx context.Context) (*session, error) {
	if p.getenv("WAYLAND_DISPLAY") == "" {
		return nil, errors.New("not a Wayland session")
	}
	c, err := p.dial()
	if err != nil {
		return nil, fmt.Errorf("can't reach the compositor: %w", err)
	}
	s := &session{c: c, globals: map[uint32]wlwire.Global{}, outputs: map[uint32]*output{}, all: map[uint32]wlwire.Global{}}
	// The registry stays watched: outputs come and go while the pointer lives.
	reg := c.NewID()
	c.Handle(reg, func(op uint16, a []byte) {
		r := wlwire.NewReader(a)
		s.mu.Lock()
		defer s.mu.Unlock()
		switch op {
		case wlwire.EvGlobal:
			g := wlwire.Global{Name: r.U32(), Iface: r.Str(), Version: r.U32()}
			if s.all != nil {
				s.all[g.Name] = g
				s.order = append(s.order, g.Name)
			}
			if g.Iface == IfaceOutput {
				s.globals[g.Name] = g
			}
		case evGlobalRemove:
			name := r.U32()
			delete(s.globals, name)
			delete(s.outputs, name)
		}
	})
	fail := func(err error) (*session, error) { c.Close(); return nil, err }
	if err := c.Send(wlwire.DisplayID, wlwire.OpDisplayGetRegistry, wlwire.NewArgs().U32(reg).Bytes(), -1); err != nil {
		return fail(err)
	}
	if err := p.rt(ctx, c); err != nil {
		return fail(fmt.Errorf("compositor: %w", err))
	}
	s.mu.Lock()
	have := map[string]bool{}
	var seat, mgr, xdg wlwire.Global
	for _, name := range s.order {
		g := s.all[name]
		if !have[g.Iface] { // the first seat is the default one
			have[g.Iface] = true
			switch g.Iface {
			case IfaceSeat:
				seat = g
			case IfaceManager:
				mgr = g
			case IfaceOutputManager:
				xdg = g
			}
		}
	}
	s.all, s.order = nil, nil
	s.mu.Unlock()
	if ok, why := missing(have); !ok {
		return fail(errors.New(why))
	}
	seatID := c.Bind(reg, seat, 1, nil)
	s.version = min(mgr.Version, managerVersionWheel)
	mgrID := c.Bind(reg, mgr, s.version, nil)
	s.xdgMgr = c.Bind(reg, xdg, 1, nil)
	s.ptr = c.NewID()
	if err := c.Send(mgrID, opCreateVirtualPointer, wlwire.NewArgs().U32(seatID).U32(s.ptr).Bytes(), -1); err != nil {
		return fail(err)
	}
	s.bindOutputs(reg)
	if err := p.rt(ctx, c); err != nil {
		return fail(fmt.Errorf("creating the virtual pointer: %w", err))
	}
	s.reg = reg
	if settle := cmp.Or(p.Settle, DefaultSettle); settle > 0 {
		sleep := p.Sleep
		if sleep == nil {
			sleep = sleepCtx
		}
		if err := sleep(ctx, settle); err != nil {
			return fail(err)
		}
	}
	return s, nil
}

// bindOutputs binds the outputs the registry has and the session doesn't track yet, with an
// xdg_output each for its logical geometry. Called with Pointer.mu held, never from the reader
// goroutine: new ids must reach the compositor in order.
func (s *session) bindOutputs(reg uint32) (bound bool) {
	s.mu.Lock()
	var todo []wlwire.Global
	for name, g := range s.globals {
		if _, ok := s.outputs[name]; !ok {
			todo = append(todo, g)
		}
	}
	s.mu.Unlock()
	for _, g := range todo {
		o := &output{}
		wl := s.c.Bind(reg, g, 1, nil)
		x := s.c.NewID()
		s.c.Handle(x, func(op uint16, a []byte) {
			r := wlwire.NewReader(a)
			s.mu.Lock()
			defer s.mu.Unlock()
			switch op {
			case evLogicalPosition:
				o.box.X, o.box.Y = int(r.I32()), int(r.I32())
			case evLogicalSize:
				o.box.W, o.box.H = int(r.I32()), int(r.I32())
			}
		})
		_ = s.c.Send(s.xdgMgr, opGetXdgOutput, wlwire.NewArgs().U32(x).U32(wl).Bytes(), -1)
		s.mu.Lock()
		if _, still := s.globals[g.Name]; still {
			s.outputs[g.Name] = o
		}
		s.mu.Unlock()
	}
	return len(todo) > 0
}

// layout is the bounding box of the outputs.
func (s *session) layout() Box {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b Box
	for _, o := range s.outputs {
		b = b.union(o.box)
	}
	return b
}

func fixed(v float64) uint32 { return uint32(int32(v * 256)) }

func (p *Pointer) post(ctx context.Context, s *session, evs []Event) error {
	var box Box
	for _, e := range evs {
		if e.kind == evMotion {
			// A round trip brings the registry's and the outputs' latest news; outputs added
			// since the last call get bound, and a second one brings their geometry.
			if err := p.rt(ctx, s.c); err != nil {
				return err
			}
			if s.bindOutputs(s.reg) {
				if err := p.rt(ctx, s.c); err != nil {
					return err
				}
			}
			if box = s.layout(); box.W <= 0 || box.H <= 0 {
				return errors.New("the compositor reports no outputs")
			}
			break
		}
	}
	now := p.Now
	if now == nil {
		now = time.Now
	}
	stamp := func() uint32 { return uint32(now().UnixMilli()) }
	c := s.c
	for _, e := range evs {
		if err := ctx.Err(); err != nil {
			return err
		}
		switch e.kind {
		case evMotion:
			x, y := e.x-box.X, e.y-box.Y
			if x < 0 || y < 0 || x >= box.W || y >= box.H {
				return fmt.Errorf("%d,%d is outside the screen (%d,%d to %d,%d)", e.x, e.y, box.X, box.Y, box.X+box.W-1, box.Y+box.H-1)
			}
			_ = c.Send(s.ptr, opMotionAbsolute, wlwire.NewArgs().U32(stamp()).U32(uint32(x)).U32(uint32(y)).U32(uint32(box.W)).U32(uint32(box.H)).Bytes(), -1)
		case evPress, evRelease:
			state := uint32(buttonReleased)
			if e.kind == evPress {
				state = buttonPressed
			}
			_ = c.Send(s.ptr, opButton, wlwire.NewArgs().U32(stamp()).U32(e.button).U32(state).Bytes(), -1)
		case evWheel:
			_ = c.Send(s.ptr, opAxisSource, wlwire.NewArgs().U32(axisSourceWheel).Bytes(), -1)
			t := stamp()
			for _, ax := range [][2]int{{axisVertical, e.dy}, {axisHorizontal, e.dx}} {
				if ax[1] == 0 {
					continue
				}
				v := fixed(float64(ax[1] * NotchValue))
				if s.version >= managerVersionWheel {
					_ = c.Send(s.ptr, opAxisDiscrete, wlwire.NewArgs().U32(t).U32(uint32(ax[0])).U32(v).I32(int32(ax[1])).Bytes(), -1)
				} else {
					_ = c.Send(s.ptr, opAxis, wlwire.NewArgs().U32(t).U32(uint32(ax[0])).U32(v).Bytes(), -1)
				}
			}
		default:
			continue
		}
		if err := c.Send(s.ptr, opFrame, nil, -1); err != nil {
			return err
		}
	}
	return p.rt(ctx, c)
}

// sleepCtx waits d or until ctx ends.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
