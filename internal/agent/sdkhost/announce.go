package sdkhost

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"
)

// ErrUnheard is what Announcer.Say wraps when nothing was played (no microphone is listening,
// or the turn in progress outlasted the wait): the alerts stay queued.
var ErrUnheard = errors.New("not said")

// NoAlerts is what a button press says over an empty queue (N:announce_pending_and_flush).
const NoAlerts = "No new notifications."

// SweepEvery is how often Run sweeps the queue, re-syncing its watchers (the LED) and retrying
// alerts not heard yet: the Python scheduler's tick.
const SweepEvery = 10 * time.Second

// Announcer speaks the queue's alerts: unasked, the announceable ones once the pipeline is
// between turns (N:core/alert_announcer.py's drain_alert_announcements), and on a button press
// every pending one (N:AlertQueueService.announce_pending_and_flush). Only what was spoken is
// removed: a low-priority alert stays for whats_up, and one added while speaking stays queued.
// Python's drain also listens 8 s for a reply ("snooze it") after each alert; the Go pipeline
// has no such hook, so a reply needs the wake word.
type Announcer struct {
	Alerts *Alerts
	// Say speaks text between turns and returns once it is said; an error wrapping ErrUnheard
	// means nothing was played.
	Say func(ctx context.Context, text string) error
	Log *slog.Logger

	mu sync.Mutex // one announcement at a time: the drain and the button
}

func (a *Announcer) log() *slog.Logger {
	if a.Log == nil {
		return slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return a.Log
}

// speak says each alert's summary in order and removes the ones attempted. It stops at the
// first one not heard (ErrUnheard, or ctx ended), which stays queued with the rest, and returns
// how many were said and that error. Any other failure is logged and the next one is tried,
// as Python's per-alert isolation does.
func (a *Announcer) speak(ctx context.Context, list []Alert) (int, error) {
	var done []string
	said := 0
	var stop error
	for _, al := range list {
		err := a.Say(ctx, al.Summary)
		if err != nil && (errors.Is(err, ErrUnheard) || ctx.Err() != nil) {
			stop = err
			break
		}
		if err != nil {
			a.log().Warn("alerts: not said", "title", al.Title, "err", err)
		} else {
			a.log().Info("alerts: announced", "title", al.Title, "priority", al.Priority)
			said++
		}
		done = append(done, al.ID)
	}
	if len(done) > 0 {
		a.Alerts.Remove(done)
	}
	return said, stop
}

// Drain speaks the announceable alerts (AnnouncePriority and above) and returns how many were
// said.
func (a *Announcer) Drain(ctx context.Context) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	var due []Alert
	for _, al := range a.Alerts.Pending() {
		if al.Priority >= AnnouncePriority {
			due = append(due, al)
		}
	}
	if len(due) == 0 {
		return 0
	}
	n, err := a.speak(ctx, due)
	if err != nil {
		a.log().Debug("alerts: kept for later", "said", n, "err", err) // every sweep while voice is off
	}
	return n
}

// ReadAll is the button's short press: every pending alert, most urgent first, or NoAlerts
// over an empty queue (after a sweep, so a stale LED goes back to normal). An error wrapping
// ErrUnheard: nothing more was played, and the rest stays queued.
func (a *Announcer) ReadAll(ctx context.Context) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	pending := a.Alerts.Pending()
	if len(pending) == 0 {
		a.Alerts.Sweep()
		err := a.Say(ctx, NoAlerts)
		if err != nil && !errors.Is(err, ErrUnheard) {
			a.log().Warn("alerts: not said", "text", NoAlerts, "err", err)
			err = nil
		}
		return 0, err
	}
	return a.speak(ctx, pending)
}

// Run drains whenever the queue holds an announceable alert: at once on a write that leaves
// one (an Add), and at each tick, when it sweeps the queue (time.NewTicker(SweepEvery).C; tests
// send their own). It returns when ctx ends.
func (a *Announcer) Run(ctx context.Context, tick <-chan time.Time) {
	poke := make(chan struct{}, 1)
	a.Alerts.Watch(func(n int) {
		if n > 0 {
			select {
			case poke <- struct{}{}:
			default:
			}
		}
	})
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick:
			a.Alerts.Sweep() // pokes when one is left
		case <-poke:
			a.Drain(ctx)
		}
	}
}
