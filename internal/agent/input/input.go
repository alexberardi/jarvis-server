// Package input drives the pointer and keyboard for computer tasks: one interface, a backend per
// OS (macOS CGEvent; wlroots virtual pointer and keyboard; X11 and Windows later).
package input

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Input moves the pointer and presses keys. Coordinates are global logical coordinates (points
// on macOS, top-left of the main display is 0,0); computeruse.Geometry.ToGlobal maps a capture's
// pixels to them.
type Input interface {
	// Available says whether input can be posted, and why not.
	Available(ctx context.Context) (bool, string)
	// Move puts the pointer at x, y.
	Move(ctx context.Context, x, y int) error
	// Button presses (down) or releases button b (left, right, middle) where the pointer is.
	Button(ctx context.Context, b string, down bool) error
	// Click moves to x, y and clicks button b n times (2 is a double click).
	Click(ctx context.Context, x, y int, b string, n int) error
	// Drag presses the left button at path[0], moves through the rest and releases at the last.
	Drag(ctx context.Context, path [][2]int) error
	// Scroll moves to x, y and turns the wheel dx, dy notches; positive dy scrolls down (content
	// moves up) and positive dx scrolls right.
	Scroll(ctx context.Context, x, y, dx, dy int) error
	// Keys presses a chord: modifiers (cmd, ctrl, alt, shift; meta and super are cmd) plus at
	// most one other key, names as computeruse.KeyNames.
	Keys(ctx context.Context, chord []string) error
	// Type types text, whatever the keyboard layout.
	Type(ctx context.Context, text string) error
	// Cursor is where the pointer is.
	Cursor(ctx context.Context) (x, y int, err error)
	// Injecting is when the latest call began and ended posting events, so the input monitor
	// can tell our events from the user's. end is zero while a call is still posting.
	Injecting() (start, end time.Time)
}

// ErrUnavailable is returned when input can't be posted (no backend, no permission).
var ErrUnavailable = errors.New("input not available")

// injectWindow records when the latest call began and ended posting (Injecting). Its lock is
// never held across posting, so the input monitor can poll it mid-call.
type injectWindow struct {
	winMu      sync.Mutex
	start, end time.Time
}

func (w *injectWindow) Injecting() (start, end time.Time) {
	w.winMu.Lock()
	defer w.winMu.Unlock()
	return w.start, w.end
}

func (w *injectWindow) set(start, end time.Time) {
	w.winMu.Lock()
	w.start, w.end = start, end
	w.winMu.Unlock()
}
