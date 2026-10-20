//go:build linux

package vkbd

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"

	"github.com/alexberardi/jarvis-server/internal/agent/wlwire"
	"github.com/alexberardi/jarvis-server/internal/agent/xkb"
)

const (
	// wl_seat.get_keyboard, wl_seat.capabilities (keyboard bit); wl_keyboard.keymap
	opSeatGetKeyboard  = 1
	evSeatCapabilities = 0
	seatCapKeyboard    = 2
	evKeyboardKeymap   = 0
	// zwp_virtual_keyboard_manager_v1
	opCreateVirtualKeyboard = 0
	// zwp_virtual_keyboard_v1
	opKeymap, opKey, opModifiers, opDestroy = 0, 1, 2, 3

	keymapFormatXKBV1 = 1
	keyReleased       = 0
	keyPressed        = 1

	// syncEvery: a round trip every this many strokes keeps the compositor's queue short and
	// surfaces a protocol error early.
	syncEvery = 64
)

// Keyboard is the virtual-keyboard backend. The zero value is ready; fields are for tests.
type Keyboard struct {
	Getenv  func(string) string                        // os.Getenv when nil
	Dial    func() (*wlwire.Conn, error)               // wlwire.Dial when nil
	Settle  time.Duration                              // DefaultSettle when zero; <0: none
	Sleep   func(context.Context, time.Duration) error // a real timer when nil
	Timeout time.Duration                              // per round trip; 2 s when zero
	Now     func() time.Time                           // key timestamps; time.Now when nil
	// X11Focused, when set, says whether the focused window is an X11 (XWayland) one. Those
	// read keys with the desktop's layout, not the virtual keyboard's keymap, so text with a
	// character the layout lacks is refused rather than typed as something else.
	X11Focused func(context.Context) bool

	mu     sync.Mutex
	layout *xkb.Layout // the desktop's layout, last read from the compositor
}

func (k *Keyboard) Name() string { return Name }

func (k *Keyboard) getenv(s string) string {
	if k.Getenv != nil {
		return k.Getenv(s)
	}
	return os.Getenv(s)
}

func (k *Keyboard) dial() (*wlwire.Conn, error) {
	if k.Dial != nil {
		return k.Dial()
	}
	return wlwire.Dial()
}

func (k *Keyboard) timeout() time.Duration { return cmp.Or(k.Timeout, 2*time.Second) }

func (k *Keyboard) rt(ctx context.Context, c *wlwire.Conn) error {
	ctx, cancel := context.WithTimeout(ctx, k.timeout())
	defer cancel()
	return c.RoundtripCtx(ctx)
}

// excluded: desktops that list the protocol (or might) but don't let ordinary clients use it.
func (k *Keyboard) excluded() string {
	d := strings.ToLower(k.getenv("XDG_CURRENT_DESKTOP"))
	switch {
	case strings.Contains(d, "gnome"):
		return "GNOME has no virtual-keyboard protocol"
	case strings.Contains(d, "kde"):
		return "KDE reserves the virtual-keyboard protocol for input methods"
	}
	return ""
}

// globals connects and lists the compositor's globals.
func (k *Keyboard) globals(ctx context.Context) (*wlwire.Conn, uint32, map[string]wlwire.Global, error) {
	if k.getenv("WAYLAND_DISPLAY") == "" {
		return nil, 0, nil, errors.New("not a Wayland session")
	}
	c, err := k.dial()
	if err != nil {
		return nil, 0, nil, fmt.Errorf("can't reach the compositor: %w", err)
	}
	ctx2, cancel := context.WithTimeout(ctx, k.timeout())
	defer cancel()
	reg, gs, err := c.Registry(ctx2)
	if err != nil {
		c.Close()
		return nil, 0, nil, fmt.Errorf("compositor: %w", err)
	}
	m := map[string]wlwire.Global{}
	for _, g := range gs {
		if _, dup := m[g.Iface]; !dup { // the first seat is the default one
			m[g.Iface] = g
		}
	}
	return c, reg, m, nil
}

// Available: a Wayland session whose compositor advertises the virtual-keyboard manager and a
// seat. Probed per call (one connection and one round trip; nothing is created).
func (k *Keyboard) Available(ctx context.Context) (bool, string) {
	if why := k.excluded(); why != "" {
		return false, why
	}
	c, _, gs, err := k.globals(ctx)
	if err != nil {
		return false, err.Error()
	}
	c.Close()
	if _, ok := gs[IfaceManager]; !ok {
		return false, "the compositor doesn't offer the virtual-keyboard protocol"
	}
	if _, ok := gs[IfaceSeat]; !ok {
		return false, "the compositor has no seat"
	}
	return true, ""
}

// Send types the strokes into whatever has keyboard focus. A stroke with Mods is a chord:
// those modifiers are held around its key.
func (k *Keyboard) Send(ctx context.Context, strokes []xkb.Stroke) error {
	if why := k.excluded(); why != "" {
		return errors.New(why)
	}
	c, reg, gs, err := k.globals(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	mg, ok1 := gs[IfaceManager]
	sg, ok2 := gs[IfaceSeat]
	if !ok1 || !ok2 {
		return errors.New("the compositor doesn't offer the virtual-keyboard protocol")
	}
	// A seat that has never had a keyboard (a headless compositor with no input devices)
	// treats wl_seat.get_keyboard as a protocol error, so the layout is read only from a seat
	// that says it has one; the capabilities event comes with the bind.
	var caps atomic.Uint32
	caps.Store(seatCapKeyboard) // a compositor that never says keeps the old behaviour
	seat := c.Bind(reg, sg, 1, func(uint32) func(uint16, []byte) {
		return func(op uint16, a []byte) {
			if op == evSeatCapabilities {
				caps.Store(wlwire.NewReader(a).U32())
			}
		}
	})
	if err := k.rt(ctx, c); err != nil {
		return err
	}
	layout := k.desktopLayout(ctx, c, seat, caps.Load()&seatCapKeyboard != 0)
	batches, err := xkb.Plan(strokes, layout)
	if err != nil {
		return err
	}
	if k.X11Focused != nil {
		var missing []uint32
		for _, b := range batches {
			missing = append(missing, b.Extra...)
		}
		if len(missing) > 0 && k.X11Focused(ctx) {
			return &NotOnLayoutError{Syms: missing}
		}
	}
	mgr := c.Bind(reg, mg, 1, nil)
	kb := c.NewID()
	if err := c.Send(mgr, opCreateVirtualKeyboard, wlwire.NewArgs().U32(seat).U32(kb).Bytes(), -1); err != nil {
		return err
	}
	if err := k.rt(ctx, c); err != nil {
		return fmt.Errorf("creating the virtual keyboard: %w", err)
	}
	now := k.Now
	if now == nil {
		now = time.Now
	}
	stamp := func() uint32 { return uint32(now().UnixMilli()) }
	sleep := k.Sleep
	if sleep == nil {
		sleep = sleepCtx
	}
	settle := cmp.Or(k.Settle, DefaultSettle)

	for _, b := range batches {
		if err := k.uploadKeymap(ctx, c, kb, b.Keymap()); err != nil {
			return err
		}
		if err := k.activate(ctx, c, kb); err != nil {
			return err
		}
		if settle > 0 {
			if err := sleep(ctx, settle); err != nil {
				return err
			}
		}
		for i, p := range b.Presses {
			if err := ctx.Err(); err != nil {
				return err
			}
			code := p.Code - 8 // the protocol takes evdev codes
			// A chord's modifiers are held as the depressed mask around its key, as wtype -M does.
			mods := p.Mods
			if p.Shift {
				mods |= xkb.ShiftMask
			}
			if mods != 0 {
				_ = c.Send(kb, opModifiers, wlwire.NewArgs().U32(mods).U32(0).U32(0).U32(0).Bytes(), -1)
			}
			_ = c.Send(kb, opKey, wlwire.NewArgs().U32(stamp()).U32(code).U32(keyPressed).Bytes(), -1)
			_ = c.Send(kb, opKey, wlwire.NewArgs().U32(stamp()).U32(code).U32(keyReleased).Bytes(), -1)
			if mods != 0 {
				_ = c.Send(kb, opModifiers, wlwire.NewArgs().U32(0).U32(0).U32(0).U32(0).Bytes(), -1)
			}
			if (i+1)%syncEvery == 0 {
				if err := k.rt(ctx, c); err != nil {
					return err
				}
			}
		}
		if err := k.rt(ctx, c); err != nil {
			return err
		}
	}
	_ = c.Send(kb, opDestroy, nil, -1)
	return k.rt(ctx, c)
}

// NotOnLayoutError: the focused window is an X11 app and the text has characters the
// keyboard layout lacks; nothing was typed.
type NotOnLayoutError struct{ Syms []uint32 }

func (e *NotOnLayoutError) Error() string {
	var chars []string
	seen := map[uint32]bool{}
	for _, s := range e.Syms {
		if !seen[s] && len(chars) < 5 {
			seen[s] = true
			chars = append(chars, fmt.Sprintf("%q", xkb.Rune(s)))
		}
	}
	return "the window in front is an X11 app, which only takes characters on the keyboard layout, and " +
		strings.Join(chars, ", ") + " aren't on it"
}

// ErrorCode is the tool result's error_code.
func (e *NotOnLayoutError) ErrorCode() string { return "unsupported_characters" }

// Layout is the desktop's keyboard layout (desktopLayout), read once over a connection of its
// own: what a desktop recording decodes the keys it reads with. US when it can't be read.
func (k *Keyboard) Layout(ctx context.Context) *xkb.Layout {
	c, reg, gs, err := k.globals(ctx)
	if err != nil {
		return k.lastLayout(nil)
	}
	defer c.Close()
	sg, ok := gs[IfaceSeat]
	if !ok {
		return k.lastLayout(nil)
	}
	var caps atomic.Uint32
	caps.Store(seatCapKeyboard)
	seat := c.Bind(reg, sg, 1, func(uint32) func(uint16, []byte) {
		return func(op uint16, a []byte) {
			if op == evSeatCapabilities {
				caps.Store(wlwire.NewReader(a).U32())
			}
		}
	})
	if err := k.rt(ctx, c); err != nil {
		return k.lastLayout(nil)
	}
	return k.desktopLayout(ctx, c, seat, caps.Load()&seatCapKeyboard != 0)
}

// desktopLayout is the keyboard layout to type with: the keymap the compositor hands a new
// keyboard (the active keyboard's) when it is a whole layout, else the last one read, else US.
// The active keyboard can be another virtual keyboard (an input method's, wtype's) holding a
// fragment; that is never taken for the layout.
func (k *Keyboard) desktopLayout(ctx context.Context, c *wlwire.Conn, seat uint32, hasKeyboard bool) *xkb.Layout {
	if !hasKeyboard {
		// No id is allocated: new ids must stay consecutive, or the next bind is a protocol
		// error.
		return k.lastLayout(nil)
	}
	got := make(chan string, 1)
	wk := c.NewID()
	c.Handle(wk, func(op uint16, a []byte) {
		if op != evKeyboardKeymap {
			return
		}
		r := wlwire.NewReader(a)
		format, size := r.U32(), r.U32()
		fd := c.TakeFD()
		if fd < 0 {
			return
		}
		defer unix.Close(fd)
		if format != keymapFormatXKBV1 || size == 0 || size > 1<<24 {
			return
		}
		mem, err := unix.Mmap(fd, 0, int(size), unix.PROT_READ, unix.MAP_PRIVATE)
		if err != nil {
			return
		}
		text := strings.TrimRight(string(mem), "\x00")
		_ = unix.Munmap(mem)
		select {
		case got <- text:
		default:
		}
	})
	if c.Send(seat, opSeatGetKeyboard, wlwire.NewArgs().U32(wk).Bytes(), -1) == nil {
		_ = k.rt(ctx, c)
	}
	var l *xkb.Layout
	select {
	case text := <-got:
		if !strings.Contains(text, xkb.KeymapName) {
			l, _ = xkb.ParseKeymap(text)
		}
	default:
	}
	return k.lastLayout(l)
}

// lastLayout keeps l when it is a whole layout, and returns the last whole one read, else US.
func (k *Keyboard) lastLayout(l *xkb.Layout) *xkb.Layout {
	k.mu.Lock()
	defer k.mu.Unlock()
	if l.Complete() {
		k.layout = l
	}
	if k.layout != nil {
		return k.layout
	}
	return xkb.US()
}

// activate makes this keyboard the seat's active one before its first key, so the compositor
// hands its keymap to the focused client before that key and the settle has something to
// wait for. Hyprland switches the seat (or the input method's grab) to a keyboard only on a
// key or modifiers event; an all-zero modifiers event is the harmless one.
func (k *Keyboard) activate(ctx context.Context, c *wlwire.Conn, kb uint32) error {
	if err := c.Send(kb, opModifiers, wlwire.NewArgs().U32(0).U32(0).U32(0).U32(0).Bytes(), -1); err != nil {
		return err
	}
	return k.rt(ctx, c)
}

// uploadKeymap passes the keymap (NUL-terminated, as XKB text v1 requires) in a sealed memfd
// and waits until the compositor has it.
func (k *Keyboard) uploadKeymap(ctx context.Context, c *wlwire.Conn, kb uint32, keymap string) error {
	data := append([]byte(keymap), 0)
	fd, err := unix.MemfdCreate("jarvis-keymap", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return fmt.Errorf("keymap: %w", err)
	}
	defer unix.Close(fd)
	for off := 0; off < len(data); {
		n, err := unix.Write(fd, data[off:])
		if err != nil {
			return fmt.Errorf("keymap: %w", err)
		}
		off += n
	}
	// The compositor maps it read-only; sealing guarantees it can't change under it.
	_, _ = unix.FcntlInt(uintptr(fd), unix.F_ADD_SEALS, unix.F_SEAL_SHRINK|unix.F_SEAL_GROW|unix.F_SEAL_WRITE|unix.F_SEAL_SEAL)
	if err := c.Send(kb, opKeymap, wlwire.NewArgs().U32(keymapFormatXKBV1).U32(uint32(len(data))).Bytes(), fd); err != nil {
		return err
	}
	if err := k.rt(ctx, c); err != nil {
		return fmt.Errorf("the compositor refused the keymap: %w", err)
	}
	return nil
}
