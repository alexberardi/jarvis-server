//go:build linux

package vkbd

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/alexberardi/jarvis-server/internal/agent/wlwire"
	"github.com/alexberardi/jarvis-server/internal/agent/xkb"
)

const (
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

// Send types the strokes into whatever has keyboard focus.
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
	seat := c.Bind(reg, sg, 1, nil)
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

	for _, b := range xkb.Plan(strokes, 0) {
		if err := k.uploadKeymap(ctx, c, kb, b.Keymap()); err != nil {
			return err
		}
		if settle > 0 {
			if err := sleep(ctx, settle); err != nil {
				return err
			}
		}
		for i, s := range b.Strokes {
			if err := ctx.Err(); err != nil {
				return err
			}
			code := b.Keycode(s.Sym) - 8 // the protocol takes evdev codes
			if s.Shift {
				_ = c.Send(kb, opModifiers, wlwire.NewArgs().U32(xkb.ShiftMask).U32(0).U32(0).U32(0).Bytes(), -1)
			}
			_ = c.Send(kb, opKey, wlwire.NewArgs().U32(stamp()).U32(code).U32(keyPressed).Bytes(), -1)
			_ = c.Send(kb, opKey, wlwire.NewArgs().U32(stamp()).U32(code).U32(keyReleased).Bytes(), -1)
			if s.Shift {
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
