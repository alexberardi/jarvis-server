//go:build linux

package idlenotify

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
	// ext_idle_notifier_v1 requests (destroy is op 0 on both interfaces).
	opDestroy                  = 0
	opGetIdleNotification      = 1
	opGetInputIdleNotification = 2 // since version 2: idle inhibitors don't hold it back
	// ext_idle_notification_v1 events.
	evIdled, evResumed = 0, 1

	// late is how much later than its timeout an idled may arrive (the compositor's timer and
	// the socket).
	late = 250 * time.Millisecond
)

// Monitor is the input monitor over ext-idle-notify (input.Monitor). The zero value is ready;
// fields are for tests. The connection is opened on the first Since and kept, its events read
// as they come; a broken one is replaced on the next call.
type Monitor struct {
	Getenv  func(string) string          // os.Getenv when nil
	Dial    func() (*wlwire.Conn, error) // wlwire.Dial when nil
	Timeout time.Duration                // per round trip; 2 s when zero
	Now     func() time.Time             // time.Now when nil

	mu sync.Mutex // guards s; held across connecting
	s  *session
}

// session is one connection with its notification and what it said.
type session struct {
	c        *wlwire.Conn
	notifier uint32
	notif    uint32
	input    bool // an input-idle notification (v2): no idled a timeout after input proves more input

	mu     sync.Mutex // the reader goroutine writes the rest
	begun  time.Time  // when the notification was asked for
	idle   bool       // idled is the latest event
	latest time.Time  // the latest event (or begun)
	last   time.Time  // the latest input the events date; zero: none since begun
}

func (m *Monitor) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *Monitor) getenv(k string) string {
	if m.Getenv != nil {
		return m.Getenv(k)
	}
	return os.Getenv(k)
}

// Since is the latest user (or our own) input the compositor's notification dates: a resumed
// is input at that moment; an idled, input a timeout before it; and, on version 2, no idled for
// longer than a timeout since the latest event is input within the last timeout (its earliest
// possible time is reported). Zero before any input since the first call.
func (m *Monitor) Since(ctx context.Context) (time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.s != nil && m.s.c.Err() != nil {
		m.s.c.Close()
		m.s = nil
	}
	if m.s == nil {
		s, err := m.connect(ctx)
		if err != nil {
			return time.Time{}, err
		}
		m.s = s
	}
	s := m.s
	now := m.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	last := s.last
	if s.input && !s.idle && now.Sub(s.latest) > Timeout+late {
		last = later(last, now.Add(-Timeout-late))
	}
	return last, nil
}

// Close destroys the notification and closes the connection.
func (m *Monitor) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.s == nil {
		return
	}
	c := m.s.c
	if c.Send(m.s.notif, opDestroy, nil, -1) == nil && c.Send(m.s.notifier, opDestroy, nil, -1) == nil {
		ctx, cancel := context.WithTimeout(context.Background(), m.timeout())
		_ = c.RoundtripCtx(ctx)
		cancel()
	}
	c.Close()
	m.s = nil
}

func (m *Monitor) timeout() time.Duration { return cmp.Or(m.Timeout, 2*time.Second) }

// connect binds the first seat and the notifier and asks for the notification.
func (m *Monitor) connect(ctx context.Context) (*session, error) {
	if m.getenv("WAYLAND_DISPLAY") == "" {
		return nil, errors.New("not a Wayland session")
	}
	dial := m.Dial
	if dial == nil {
		dial = wlwire.Dial
	}
	c, err := dial()
	if err != nil {
		return nil, fmt.Errorf("can't reach the compositor: %w", err)
	}
	fail := func(err error) (*session, error) { c.Close(); return nil, err }
	rctx, cancel := context.WithTimeout(ctx, m.timeout())
	defer cancel()
	reg, gs, err := c.Registry(rctx)
	if err != nil {
		return fail(fmt.Errorf("compositor: %w", err))
	}
	var seat, notifier *wlwire.Global
	for i := range gs {
		switch g := &gs[i]; {
		case g.Iface == IfaceSeat && seat == nil: // the first seat is the default one
			seat = g
		case g.Iface == IfaceNotifier && notifier == nil:
			notifier = g
		}
	}
	switch {
	case notifier == nil:
		return fail(errors.New("the compositor doesn't offer ext-idle-notify, so the user's input can't be seen"))
	case seat == nil:
		return fail(errors.New("the compositor has no seat"))
	}
	s := &session{c: c, input: notifier.Version >= 2}
	seatID := c.Bind(reg, *seat, 1, nil)
	s.notifier = c.Bind(reg, *notifier, min(notifier.Version, 2), nil)
	s.notif = c.NewID()
	c.Handle(s.notif, func(op uint16, _ []byte) {
		at := m.now()
		s.mu.Lock()
		defer s.mu.Unlock()
		switch op {
		case evIdled:
			// Input a timeout ago, unless that is when we began: no input since.
			if in := at.Add(-Timeout); in.After(s.begun.Add(late)) {
				s.last = later(s.last, in)
			}
			s.idle = true
		case evResumed:
			s.last = later(s.last, at)
			s.idle = false
		}
		s.latest = at
	})
	op := uint16(opGetIdleNotification)
	if s.input {
		op = opGetInputIdleNotification
	}
	s.begun = m.now()
	s.latest = s.begun
	args := wlwire.NewArgs().U32(s.notif).U32(uint32(Timeout / time.Millisecond)).U32(seatID).Bytes()
	if err := c.Send(s.notifier, op, args, -1); err != nil {
		return fail(err)
	}
	if err := c.RoundtripCtx(rctx); err != nil {
		return fail(fmt.Errorf("asking for idle notifications: %w", err))
	}
	return s, nil
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
