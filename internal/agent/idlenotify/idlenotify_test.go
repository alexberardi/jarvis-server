//go:build linux

package idlenotify

import (
	"context"
	"encoding/binary"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/alexberardi/jarvis-server/internal/agent/wlwire"
)

// A scripted compositor on a socketpair, as vptr's: it advertises globals, answers syncs,
// records the idle notification asked for and sends it idled/resumed when the test says.

type global struct {
	iface   string
	version uint32
}

type fake struct {
	c *net.UnixConn

	mu      sync.Mutex
	globals []global
	reg     uint32
	ids     map[string]uint32 // interface → bound id; "notification" → the notification
	bound   map[string]uint32 // interface → bound version
	ask     string            // "input <ms> seat=<id>" or "plain <ms> seat=<id>"
	gone    []uint32          // destroyed objects
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
	f := &fake{c: conn(fds[0]), globals: gs, ids: map[string]uint32{}, bound: map[string]uint32{}}
	client := conn(fds[1])
	t.Cleanup(func() { f.c.Close() })
	go f.serve()
	dials := 0
	return f, func() (*wlwire.Conn, error) {
		if dials++; dials > 1 {
			t.Error("dialed twice")
		}
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
			id, op := binary.LittleEndian.Uint32(buf), uint16(binary.LittleEndian.Uint32(buf[4:]))
			f.answer(id, op, wlwire.NewReader(buf[8:size]))
			buf = buf[size:]
		}
	}
}

func (f *fake) answer(id uint32, op uint16, a *wlwire.Reader) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case id == wlwire.DisplayID && op == wlwire.OpDisplayGetRegistry:
		f.reg = a.U32()
		for i, g := range f.globals {
			f.event(f.reg, wlwire.EvGlobal, wlwire.NewArgs().U32(uint32(i+1)).Str(g.iface).U32(g.version))
		}
	case id == wlwire.DisplayID && op == wlwire.OpDisplaySync:
		f.event(a.U32(), wlwire.EvCallbackDone, wlwire.NewArgs().U32(0))
	case id == f.reg && op == wlwire.OpRegistryBind:
		_, iface, v, nid := a.U32(), a.Str(), a.U32(), a.U32()
		f.ids[iface], f.bound[iface] = nid, v
	case id == f.ids[IfaceNotifier] && (op == opGetIdleNotification || op == opGetInputIdleNotification):
		nid, ms, seat := a.U32(), a.U32(), a.U32()
		kind := "plain"
		if op == opGetInputIdleNotification {
			kind = "input"
		}
		f.ids["notification"] = nid
		f.ask = kind + " " + itoa(ms) + " seat=" + itoa(seat)
		if seat != f.ids[IfaceSeat] {
			f.ask += " (not the bound seat)"
		}
	case op == opDestroy:
		f.gone = append(f.gone, id)
	}
}

func itoa(v uint32) string { return strconv.FormatUint(uint64(v), 10) }

// idled and resumed are the notification's events, as the compositor sends them.
func (f *fake) idled()   { f.send(evIdled) }
func (f *fake) resumed() { f.send(evResumed) }
func (f *fake) send(op uint16) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.event(f.ids["notification"], op, wlwire.NewArgs())
}

func (f *fake) asked() string { f.mu.Lock(); defer f.mu.Unlock(); return f.ask }

// clock is a fake time.Now.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

var wlEnv = func(k string) string {
	if k == "WAYLAND_DISPLAY" {
		return "wayland-9"
	}
	return ""
}

var (
	v2 = []global{{"wl_compositor", 6}, {IfaceSeat, 9}, {IfaceNotifier, 2}}
	v1 = []global{{IfaceSeat, 9}, {IfaceNotifier, 1}}
)

func newMonitor(t *testing.T, gs ...global) (*Monitor, *fake, *clock) {
	f, dial := newFake(t, gs...)
	c := &clock{t: time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)}
	m := &Monitor{Getenv: wlEnv, Dial: dial, Now: c.Now}
	t.Cleanup(m.Close)
	return m, f, c
}

// since is Since after the monitor has read everything the compositor sent so far.
func since(t *testing.T, m *Monitor) time.Time {
	t.Helper()
	ctx := context.Background()
	m.mu.Lock()
	s := m.s
	m.mu.Unlock()
	if s != nil {
		if err := s.c.RoundtripCtx(ctx); err != nil {
			t.Fatal(err)
		}
	}
	got, err := m.Since(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestAsksForInputIdleNotification(t *testing.T) {
	m, f, _ := newMonitor(t, v2...)
	if got := since(t, m); !got.IsZero() {
		t.Fatalf("before any event: %v, want zero (nothing known)", got)
	}
	if got := f.asked(); got != "input 1000 seat="+itoa(f.ids[IfaceSeat]) {
		t.Fatalf("asked %q, want an input-idle notification of 1000 ms on the bound seat", got)
	}
	if f.bound[IfaceNotifier] != 2 {
		t.Fatalf("bound the notifier at v%d, want 2", f.bound[IfaceNotifier])
	}
}

func TestResumedIsInputNow(t *testing.T) {
	m, f, c := newMonitor(t, v2...)
	since(t, m)
	c.advance(Timeout + 100*time.Millisecond)
	f.idled() // no input since we watched: nothing to report
	if got := since(t, m); !got.IsZero() {
		t.Fatalf("idled with no input since the notification began: %v, want zero", got)
	}
	c.advance(3 * time.Second)
	at := c.Now()
	f.resumed()
	if got := since(t, m); !got.Equal(at) {
		t.Fatalf("after resumed: %v, want %v", got, at)
	}
}

func TestIdledDatesTheLastInput(t *testing.T) {
	// Input that started within the timeout of other input sends no resumed; the idled that
	// follows it, a timeout after it, dates it.
	m, f, c := newMonitor(t, v2...)
	since(t, m)
	c.advance(2 * time.Second)
	f.resumed()
	since(t, m)
	c.advance(700 * time.Millisecond) // more input meanwhile, unseen
	c.advance(Timeout)
	f.idled()
	want := c.Now().Add(-Timeout)
	if got := since(t, m); !got.Equal(want) {
		t.Fatalf("after idled: %v, want %v (a timeout before it)", got, want)
	}
}

func TestStillActiveIsRecentInput(t *testing.T) {
	// v2: no idled a timeout (plus lateness) after the latest event means input since, within
	// the last timeout.
	m, f, c := newMonitor(t, v2...)
	since(t, m)
	c.advance(time.Second)
	f.resumed()
	at := c.Now()
	since(t, m)
	c.advance(Timeout)
	if got := since(t, m); !got.Equal(at) {
		t.Fatalf("within the timeout: %v, want the resumed at %v", got, at)
	}
	c.advance(late + 10*time.Millisecond)
	want := c.Now().Add(-Timeout - late)
	if got := since(t, m); !got.Equal(want) {
		t.Fatalf("still active past the timeout: %v, want %v", got, want)
	}
	f.idled() // it stops; the idled dates it
	want = c.Now().Add(-Timeout)
	if got := since(t, m); !got.Equal(want) {
		t.Fatalf("after idled: %v, want %v", got, want)
	}
	c.advance(time.Minute)
	if got := since(t, m); !got.Equal(want) {
		t.Fatalf("idle a minute later: %v, want %v", got, want)
	}
}

func TestActiveFromTheStart(t *testing.T) {
	// The user was typing when we began watching and never stopped: no event at all.
	m, _, c := newMonitor(t, v2...)
	since(t, m)
	c.advance(Timeout + late + time.Millisecond)
	want := c.Now().Add(-Timeout - late)
	if got := since(t, m); !got.Equal(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestV1ReportsOnlyEvents(t *testing.T) {
	// ext_idle_notifier_v1 version 1 has only get_idle_notification, which an idle inhibitor
	// (a playing video) holds back: no idled proves nothing, so only events count.
	m, f, c := newMonitor(t, v1...)
	since(t, m)
	if got := f.asked(); !strings.HasPrefix(got, "plain 1000 ") {
		t.Fatalf("asked %q, want a plain idle notification", got)
	}
	c.advance(time.Minute)
	if got := since(t, m); !got.IsZero() {
		t.Fatalf("v1 with no events: %v, want zero", got)
	}
	f.resumed()
	at := c.Now()
	if got := since(t, m); !got.Equal(at) {
		t.Fatalf("v1 resumed: %v, want %v", got, at)
	}
}

func TestUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name string
		gs   []global
		want string
	}{
		{"no notifier", []global{{IfaceSeat, 9}}, "idle-notify"},
		{"no seat", []global{{IfaceNotifier, 2}}, "seat"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _, _ := newMonitor(t, tc.gs...)
			_, err := m.Since(context.Background())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one naming %q", err, tc.want)
			}
		})
	}
	m := &Monitor{Getenv: func(string) string { return "" }}
	if _, err := m.Since(context.Background()); err == nil {
		t.Fatal("no Wayland session: want an error")
	}
}

func TestCloseDestroys(t *testing.T) {
	m, f, _ := newMonitor(t, v2...)
	since(t, m)
	m.Close()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.gone) != 2 || f.gone[0] != f.ids["notification"] || f.gone[1] != f.ids[IfaceNotifier] {
		t.Fatalf("destroyed %v, want the notification %d then the notifier %d", f.gone, f.ids["notification"], f.ids[IfaceNotifier])
	}
}
