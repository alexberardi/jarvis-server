// Package reminders fires the Pi's reminders: the schedule of N:agents/reminder_agent.py. The
// reminders themselves are the reminder built-in's (builtins.Reminder, the one owner of their
// state, persisted in the agent's store); each poll asks it for the due ones, which it marks
// announced and pushes, and queues each as a priority-3 alert, which the announcer speaks
// between turns, the LED shows and the button reads.
package reminders

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/sdkhost"
)

// The longest waits between polls: Refresh while a reminder is pending, Idle otherwise. A poll
// is never later than the next reminder's due time (Next), and Wake polls at once, so these
// only bound how late a change made elsewhere (the app) is seen. N:reminder_agent.py polled
// only at these (60 s and 300 s), so a reminder could fire five minutes late. A failed poll
// (the sidecar still installing at first boot) is retried at Refresh.
const (
	Refresh = 60 * time.Second
	Idle    = 60 * time.Second
)

// AlertTTL is how long a fired reminder stays queued unheard (N: now + 10 minutes).
const AlertTTL = 10 * time.Minute

// Due is one reminder the check found due and marked announced.
type Due struct {
	ID     string
	Text   string
	UserID *int64
}

// Poller polls Check on its schedule and queues what is due in Alerts.
type Poller struct {
	// Check is one run of the reminder agent: the due reminders and how many were pending
	// before it.
	Check  func(ctx context.Context) ([]Due, int, error)
	Alerts *sdkhost.Alerts
	Log    *slog.Logger
	// Next is the earliest time a pending reminder comes due (false: none); a poll waits no
	// longer than that. nil: the fixed intervals only.
	Next func() (time.Time, bool)
	// Now and After are the clock (nil: time.Now, time.After); tests drive their own.
	Now   func() time.Time
	After func(time.Duration) <-chan time.Time

	lastErr  string
	wakeOnce sync.Once
	wake     chan struct{}
}

// Wake makes Run poll now (a reminder set or changed, the node connected, so a held push goes
// out). It never blocks; wakes before the next poll collapse into one.
func (p *Poller) Wake() {
	select {
	case p.wakeC() <- struct{}{}:
	default:
	}
}

func (p *Poller) wakeC() chan struct{} {
	p.wakeOnce.Do(func() { p.wake = make(chan struct{}, 1) })
	return p.wake
}

func (p *Poller) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *Poller) log() *slog.Logger {
	if p.Log == nil {
		return slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return p.Log
}

// Poll runs Check once, queues the due reminders and returns the wait before the next poll.
func (p *Poller) Poll(ctx context.Context) time.Duration {
	due, pending, err := p.Check(ctx)
	if err != nil {
		// Once per new error: a node without the built-in commands would log it every minute.
		if msg := err.Error(); msg != p.lastErr {
			p.lastErr = msg
			p.log().Warn("reminders: not checked", "err", err)
		}
		return Refresh
	}
	p.lastErr = ""
	now := p.now()
	for _, d := range due {
		title := "Reminder: " + d.Text
		p.log().Info("reminders: due", "id", d.ID, "text", d.Text)
		p.Alerts.Add(sdkhost.Alert{ID: newID(), SourceAgent: "reminder_alerts", Title: title, Summary: title,
			Priority: 3, CreatedAt: now, ExpiresAt: now.Add(AlertTTL), Category: "general"})
	}
	wait := Idle
	if pending > 0 {
		wait = Refresh
	}
	if p.Next != nil {
		if at, ok := p.Next(); ok {
			// The next due time, a little after it so the check finds it past; never a busy loop.
			wait = min(wait, max(at.Sub(now)+nextSlack, nextSlack))
		}
	}
	return wait
}

// nextSlack is how long after a reminder's due time its poll runs.
const nextSlack = 100 * time.Millisecond

// Run polls at once (run_on_startup), then on the schedule, until ctx ends.
func (p *Poller) Run(ctx context.Context) {
	after := p.After
	if after == nil {
		after = time.After
	}
	for {
		wait := p.Poll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-after(wait):
		case <-p.wakeC():
		}
	}
}

// newID is a fresh alert id (the SDK's Alert takes a uuid4).
func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
