package x11

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/xkb"
)

// Idle is the input monitor on X11 (input.Monitor): the server's idle time
// (MIT-SCREEN-SAVER), which XTEST events reset too; the caller tells ours apart by time. One
// connection is kept between calls and replaced after an error.
type Idle struct {
	D   *Display
	Now func() time.Time // time.Now when nil

	mu sync.Mutex
	c  Conn
}

// Since is when the last input event came: now minus the server's idle time (read after it).
func (m *Idle) Since(context.Context) (time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.c == nil {
		c, err := m.D.open()
		if err != nil {
			return time.Time{}, err
		}
		m.c = c
	}
	idle, err := m.c.IdleTime()
	if err != nil {
		m.c.Close()
		m.c = nil
		return time.Time{}, err
	}
	now := time.Now
	if m.Now != nil {
		now = m.Now
	}
	// The server counts whole milliseconds, its clock and the event's time each truncated, so
	// the idle time may be up to 1 ms long: never date input before it came.
	return now().Add(-max(idle-time.Millisecond, 0)), nil
}

// Close closes the connection.
func (m *Idle) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.c != nil {
		m.c.Close()
		m.c = nil
	}
}

// Core modifier masks.
const (
	modLock    = 1 << 1
	modControl = 1 << 2
	mod1       = 1 << 3 // Alt
	mod2       = 1 << 4 // NumLock
)

// StopKeyLabel is the stop hotkey as the tray names it on X11.
const StopKeyLabel = "Ctrl+Alt+Esc"

// GrabStopKey grabs Ctrl+Alt+Escape for the whole display and calls on (on its own goroutine)
// at each press until ctx ends. It returns once the grab is in place; an error when it can't
// be (not an X11 session, no Escape key, or another program holds the combination).
func (d *Display) GrabStopKey(ctx context.Context, on func()) error {
	c, err := d.open()
	if err != nil {
		return err
	}
	code, err := keycodeOf(c, xkb.KeyEscape)
	if err != nil {
		c.Close()
		return err
	}
	// A grab matches its modifiers exactly: grab it with CapsLock and NumLock on as well.
	for _, locks := range []uint16{0, modLock, mod2, modLock | mod2} {
		if err := c.GrabKey(code, modControl|mod1|locks); err != nil {
			c.Close()
			return fmt.Errorf("another program holds %s: %w", StopKeyLabel, err)
		}
	}
	if err := c.Sync(); err != nil {
		c.Close()
		return err
	}
	stop := context.AfterFunc(ctx, c.Close)
	go func() {
		defer func() {
			if stop() { // the connection ended first
				c.Close()
			}
		}()
		for {
			if _, _, err := c.NextKeyPress(); err != nil {
				return
			}
			on()
		}
	}()
	return nil
}

// keycodeOf is the first key code whose mapping has sym.
func keycodeOf(c Conn, sym uint32) (byte, error) {
	lo, _ := c.KeycodeRange()
	per, syms, err := c.Keymap()
	if err != nil {
		return 0, err
	}
	if per > 0 {
		if i := slices.Index(syms, sym); i >= 0 {
			return lo + byte(i/per), nil
		}
	}
	return 0, errors.New("no key on this keyboard makes Escape")
}
