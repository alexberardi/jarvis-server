package reminders

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/sdkhost"
)

type answer struct {
	due     []Due
	pending int
	err     error
}

// A fake clock: Run's waits are handed to the test, which fires them.
type fakeClock struct {
	now   time.Time
	waits chan time.Duration
	fire  chan time.Time
}

func newClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC), waits: make(chan time.Duration), fire: make(chan time.Time)}
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time { c.waits <- d; return c.fire }

// Run polls at once, then every minute (N:reminder_agent.py's five idle minutes made a reminder
// up to five minutes late); each due reminder is queued as a priority-3 "Reminder: …" alert for ten
// minutes; a failed poll is logged once and retried in a minute.
func TestPollerScheduleAndAlerts(t *testing.T) {
	answers := make(chan answer, 1)
	clock := newClock()
	q := &sdkhost.Alerts{Now: func() time.Time { return clock.now }}
	var logs bytes.Buffer
	p := &Poller{Alerts: q, Now: func() time.Time { return clock.now }, After: clock.After,
		Log:   slog.New(slog.NewTextHandler(&logs, nil)),
		Check: func(context.Context) ([]Due, int, error) { a := <-answers; return a.due, a.pending, a.err }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()

	step := func(a answer, want time.Duration) {
		t.Helper()
		answers <- a
		if got := <-clock.waits; got != want {
			t.Fatalf("after %+v waits %v, want %v", a, got, want)
		}
	}
	step(answer{}, Idle)
	clock.fire <- clock.now
	step(answer{pending: 1}, Refresh)
	clock.fire <- clock.now
	uid := int64(7)
	step(answer{pending: 2, due: []Due{{ID: "rem_1", Text: "check the oven", UserID: &uid}, {ID: "rem_2", Text: "call mum"}}}, Refresh)
	clock.fire <- clock.now
	step(answer{err: errors.New("sidecar exited")}, Refresh)
	clock.fire <- clock.now
	step(answer{err: errors.New("sidecar exited")}, Refresh)
	clock.fire <- clock.now
	step(answer{pending: 0}, Idle)
	cancel()
	<-done

	got := q.Pending()
	if len(got) != 2 {
		t.Fatalf("queued %+v", got)
	}
	a := got[0]
	if a.Title != "Reminder: check the oven" || a.Summary != a.Title || a.Priority != 3 || a.SourceAgent != "reminder_alerts" ||
		!a.CreatedAt.Equal(clock.now) || !a.ExpiresAt.Equal(clock.now.Add(10*time.Minute)) || a.ID == "" || a.ID == got[1].ID {
		t.Errorf("alert %+v", a)
	}
	if got[1].Title != "Reminder: call mum" {
		t.Errorf("second alert %+v", got[1])
	}
	if n := strings.Count(logs.String(), "reminders: not checked"); n != 1 {
		t.Errorf("the error was logged %d times:\n%s", n, logs.String())
	}
}

// A poll is never later than the next due time (a little after it, so it is past), and Wake
// polls at once: a reminder set while idle fires on time, not at the next interval.
func TestPollerWaitsForTheNextDueAndWakes(t *testing.T) {
	answers := make(chan answer, 1)
	clock := newClock()
	next, has := clock.now.Add(20*time.Second), true
	p := &Poller{Alerts: &sdkhost.Alerts{}, Now: func() time.Time { return clock.now }, After: clock.After,
		Next:  func() (time.Time, bool) { return next, has },
		Check: func(context.Context) ([]Due, int, error) { a := <-answers; return a.due, a.pending, a.err }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()

	answers <- answer{pending: 1}
	if got := <-clock.waits; got != 20*time.Second+nextSlack {
		t.Fatalf("next due in 20 s: waits %v", got)
	}
	// Woken before the wait ends (a reminder set): polls again at once, now with nothing due.
	has = false
	p.Wake()
	answers <- answer{}
	if got := <-clock.waits; got != Idle {
		t.Fatalf("after the wake: waits %v", got)
	}
	clock.fire <- clock.now
	next, has = clock.now.Add(-time.Second), true // overdue (snoozed elsewhere): no busy loop
	answers <- answer{pending: 1}
	if got := <-clock.waits; got != nextSlack {
		t.Fatalf("overdue: waits %v", got)
	}
	next = clock.now.Add(time.Hour) // far off: the interval still bounds the wait
	clock.fire <- clock.now
	answers <- answer{pending: 1}
	if got := <-clock.waits; got != Refresh {
		t.Fatalf("an hour off: waits %v", got)
	}
	cancel()
	<-done
	idle := &Poller{}
	idle.Wake()
	idle.Wake() // not running: the wakes collapse into one, never block
}
