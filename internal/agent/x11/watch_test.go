package x11

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/xkb"
)

func TestIdleSince(t *testing.T) {
	f := newFake()
	f.idle = 1500 * time.Millisecond
	d, _ := display(f)
	now := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	m := &Idle{D: d, Now: func() time.Time { return now }}
	got, err := m.Since(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// The server counts in whole milliseconds (its clock and the event's both truncated): an
	// idle time may be 1 ms long, which would date our own injection before it began.
	if want := now.Add(-1499 * time.Millisecond); !got.Equal(want) {
		t.Fatalf("got %v, want %v (1 ms of rounding back)", got, want)
	}
	f.idle = 0
	if got, _ := m.Since(context.Background()); !got.Equal(now) {
		t.Fatalf("idle 0: got %v, want now", got)
	}
	f.idleErr = errors.New("no MIT-SCREEN-SAVER")
	if _, err := m.Since(context.Background()); err == nil {
		t.Fatal("want the extension's error")
	}
}

func TestIdleKeepsItsConnection(t *testing.T) {
	f := newFake()
	dials := 0
	d := &Display{Getenv: x11Env, Dial: func() (Conn, error) { dials++; return f, nil }}
	m := &Idle{D: d}
	for range 3 {
		if _, err := m.Since(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if dials != 1 {
		t.Fatalf("dialed %d times, want once", dials)
	}
	f.idleErr = errors.New("broken pipe") // a broken connection is replaced on the next call
	_, _ = m.Since(context.Background())
	f.idleErr = nil
	if _, err := m.Since(context.Background()); err != nil || dials != 2 {
		t.Fatalf("after an error: err %v, %d dials; want a new connection", err, dials)
	}
	m.Close()
}

func TestIdleNotX11(t *testing.T) {
	m := &Idle{D: &Display{Getenv: func(k string) string { return map[string]string{"WAYLAND_DISPLAY": "w"}[k] }}}
	if _, err := m.Since(context.Background()); err == nil {
		t.Fatal("a Wayland session: want an error")
	}
}

// escFake has Escape on keycode 14.
func escFake() *fakeConn {
	f := newFake()
	f.keymap[12] = xkb.KeyEscape
	f.presses = make(chan uint16, 4)
	return f
}

func TestStopKeyGrabsCtrlAltEscapeWithLocks(t *testing.T) {
	f := escFake()
	d, _ := display(f)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	closed := make(chan struct{})
	f.onClose = func() { close(closed); close(f.presses) }
	pressed := make(chan struct{}, 4)
	if err := d.GrabStopKey(ctx, func() { pressed <- struct{}{} }); err != nil {
		t.Fatal(err)
	}
	// Control|Mod1, also with CapsLock (Lock) and NumLock (Mod2) on: a grab matches exactly.
	want := []string{"grab 14 0xc", "grab 14 0xe", "grab 14 0x1c", "grab 14 0x1e", "sync"}
	if !slices.Equal(f.log, want) {
		t.Fatalf("got  %v\nwant %v", f.log, want)
	}
	f.presses <- 0xc
	f.presses <- 0x1c
	for range 2 {
		select {
		case <-pressed:
		case <-time.After(5 * time.Second):
			t.Fatal("a press didn't call on")
		}
	}
	cancel()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the connection wasn't closed when ctx ended")
	}
}

func TestStopKeyHeldByAnotherClient(t *testing.T) {
	f := escFake()
	f.grabErr = errors.New("BadAccess")
	closed := false
	f.onClose = func() { closed = true }
	d, _ := display(f)
	err := d.GrabStopKey(context.Background(), func() {})
	if err == nil || !strings.Contains(err.Error(), "another program") {
		t.Fatalf("err = %v, want one saying another program holds it", err)
	}
	if !closed {
		t.Fatal("connection left open")
	}
}

func TestStopKeyNeedsEscape(t *testing.T) {
	f := newFake() // no Escape in the mapping
	d, _ := display(f)
	if err := d.GrabStopKey(context.Background(), func() {}); err == nil {
		t.Fatal("want an error: no keycode makes Escape")
	}
}
