// Package vptr moves the pointer, clicks and scrolls through the wlroots virtual-pointer
// protocol (zwlr_virtual_pointer_manager_v1), which Hyprland, sway and other wlroots compositors
// offer to ordinary clients. It is what wlrctl and ydotool's Wayland mode do, spoken directly
// over wlwire so the agent needs no helper program.
//
// Positions are global logical coordinates (the compositor's layout, as xdg-output reports each
// output's logical position and size). motion_absolute takes a position within an extent, and
// wlroots maps that extent onto the whole output layout, so a point is sent relative to the
// layout's bounding box with the box's size as the extent.
//
// Unlike the virtual keyboard (one per typing call), the pointer is kept for as long as the
// agent runs: on a desktop with no other pointer (a headless compositor, a touch-only laptop)
// creating one gives the seat its pointer capability, and a client binds its wl_pointer only
// after that, so a pointer made per click would lose the click that races the client.
package vptr

import (
	"fmt"
	"time"
)

// Name is the backend's name in status and logs.
const Name = "wlroots virtual-pointer"

// Interfaces this backend uses.
const (
	IfaceManager       = "zwlr_virtual_pointer_manager_v1"
	IfaceSeat          = "wl_seat"
	IfaceOutput        = "wl_output"
	IfaceOutputManager = "zxdg_output_manager_v1"
)

// DefaultSettle is how long a new pointer gets before its first event: when it is the seat's
// first pointer, clients bind their wl_pointer only once the seat says it has one, and an
// event sent before that is lost (a click on a headless sway went missing without it).
const DefaultSettle = 100 * time.Millisecond

// Linux input event codes for the buttons (linux/input-event-codes.h).
const (
	BtnLeft   uint32 = 0x110
	BtnRight  uint32 = 0x111
	BtnMiddle uint32 = 0x112
)

// NotchValue is one wheel notch in axis units (what libinput reports for a mouse wheel click).
const NotchValue = 15

// Event is one pointer event; Motion, Press, Release and Wheel make them.
type Event struct {
	kind   eventKind
	x, y   int    // motion: global logical position
	button uint32 // press, release
	dx, dy int    // wheel: notches, positive right and down
}

type eventKind uint8

const (
	evMotion eventKind = iota + 1
	evPress
	evRelease
	evWheel
)

// Motion moves the pointer to x, y (global logical coordinates).
func Motion(x, y int) Event { return Event{kind: evMotion, x: x, y: y} }

// Press presses button b (BtnLeft…) where the pointer is.
func Press(b uint32) Event { return Event{kind: evPress, button: b} }

// Release releases button b.
func Release(b uint32) Event { return Event{kind: evRelease, button: b} }

// Wheel turns the wheel dx, dy notches where the pointer is; positive dy scrolls down and
// positive dx right (Wayland's axis direction).
func Wheel(dx, dy int) Event { return Event{kind: evWheel, dx: dx, dy: dy} }

func (e Event) String() string {
	switch e.kind {
	case evMotion:
		return fmt.Sprintf("motion %d,%d", e.x, e.y)
	case evPress:
		return fmt.Sprintf("press %#x", e.button)
	case evRelease:
		return fmt.Sprintf("release %#x", e.button)
	case evWheel:
		return fmt.Sprintf("wheel %d,%d", e.dx, e.dy)
	}
	return "none"
}

// Box is a rectangle in global logical coordinates.
type Box struct{ X, Y, W, H int }

// union is the bounding box of a and b; an empty box is ignored.
func (a Box) union(b Box) Box {
	if b.W <= 0 || b.H <= 0 {
		return a
	}
	if a.W <= 0 || a.H <= 0 {
		return b
	}
	x0, y0 := min(a.X, b.X), min(a.Y, b.Y)
	x1, y1 := max(a.X+a.W, b.X+b.W), max(a.Y+a.H, b.Y+b.H)
	return Box{x0, y0, x1 - x0, y1 - y0}
}
