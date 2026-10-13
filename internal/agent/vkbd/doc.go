// Package vkbd types through the Wayland virtual-keyboard protocol
// (zwp_virtual_keyboard_manager_v1), which Hyprland, sway and other wlroots compositors offer
// to ordinary clients. It is what wtype does, spoken directly so the agent needs no helper
// program: read the desktop's keyboard layout from the compositor, build an XKB keymap that keeps
// every character on the keycode that layout has it on and puts the rest on keycodes it leaves
// empty (internal/agent/xkb), hand it to the compositor in a memfd, then press and release those
// keys. Clients that read keys with the layout rather than the keymap (XWayland on Hyprland)
// still get the right characters.
//
// GNOME doesn't offer the protocol and KDE reserves it for input methods; there typing goes
// through the desktop portal instead (DESIGN.md §14).
package vkbd

import (
	"context"
	"time"
)

// Name is the backend's name in status and logs.
const Name = "wayland virtual-keyboard"

// Interfaces this backend needs from the compositor.
const (
	IfaceManager = "zwp_virtual_keyboard_manager_v1"
	IfaceSeat    = "wl_seat"
)

// DefaultSettle is how long a new keymap gets before its first key. A fresh virtual
// keyboard's keymap has to reach the focused client before the first key event, or that key
// is read with the old keymap (wtype needed -s 40 on sway: the first character went missing
// in 1 of 3 runs without it). The round trip after the keymap makes sure the compositor has
// it; the settle covers the client.
const DefaultSettle = 40 * time.Millisecond

// sleepCtx waits d or until ctx ends.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
