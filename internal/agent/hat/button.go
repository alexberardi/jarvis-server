package hat

import (
	"cmp"
	"log/slog"
	"sync"
	"time"
)

// Button timings, from services/button_service.py.
const (
	ShutdownWarningAt = 1 * time.Second       // hold this long: LEDs flash shutdown_warning
	HoldForPoweroff   = 3 * time.Second       // hold this long: power off
	Debounce          = 50 * time.Millisecond // gpiozero bounce_time
	ButtonGPIO        = 17                    // the HAT's user button, active low with pull-up
)

// Button is the HAT button's behaviour, fed with press and release edges: a short press (under
// HoldForPoweroff) runs OnShortPress; holding shows the shutdown warning on the LEDs after
// ShutdownWarningAt and calls OnHold after HoldForPoweroff (Python: poweroff). Python also
// publishes button/notifications_request and button/shutdown over MQTT; jarvisd's ACL denies
// both (go-pi-node.md §1.4), so they are not sent.
type Button struct {
	LEDs         *LEDs  // shutdown_warning overlay; nil: none
	OnShortPress func() // run on its own goroutine (Python: speak queued alerts)
	OnHold       func() // run on its own goroutine (Python: sudo -n systemctl poweroff)
	Log          *slog.Logger
	// WarnAfter and HoldAfter default to ShutdownWarningAt and HoldForPoweroff (tests shorten them).
	WarnAfter, HoldAfter time.Duration

	mu      sync.Mutex
	pressed bool
	at      time.Time
	held    bool
	warn    *time.Timer
	hold    *time.Timer
	last    time.Time // last accepted edge, for the debounce
	now     func() time.Time
	// after schedules the warning and the hold (time.AfterFunc; tests fire them themselves).
	after func(time.Duration, func()) *time.Timer
}

func (b *Button) clock() time.Time {
	if b.now != nil {
		return b.now()
	}
	return time.Now()
}

func (b *Button) afterFunc(d time.Duration, f func()) *time.Timer {
	if b.after != nil {
		return b.after(d, f)
	}
	return time.AfterFunc(d, f)
}

// Edge feeds one edge: pressed is the new state (line low).
func (b *Button) Edge(pressed bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.clock()
	if pressed == b.pressed || (!b.last.IsZero() && now.Sub(b.last) < Debounce) {
		return
	}
	b.last = now
	b.pressed = pressed
	if pressed {
		b.at, b.held = now, false
		b.warn = b.afterFunc(cmp.Or(b.WarnAfter, ShutdownWarningAt), func() {
			if b.LEDs != nil {
				b.LEDs.SetTransient(ShutdownWarning)
			}
		})
		b.hold = b.afterFunc(cmp.Or(b.HoldAfter, HoldForPoweroff), b.onHold)
		b.debug("hat: button pressed")
		return
	}
	held := now.Sub(b.at)
	b.stopTimersLocked()
	if b.LEDs != nil { // Python clears the overlay on every release, a hold included
		b.LEDs.SetTransient("")
	}
	b.debug("hat: button released", "held_s", held.Seconds(), "was_held", b.held)
	if b.held {
		return // OnHold already ran
	}
	if f := b.OnShortPress; f != nil {
		go f()
	}
}

func (b *Button) onHold() {
	b.mu.Lock()
	if !b.pressed || b.held {
		b.mu.Unlock()
		return
	}
	b.held = true
	b.mu.Unlock()
	if b.LEDs != nil {
		b.LEDs.SetTransient(ShutdownWarning)
	}
	if b.Log != nil {
		b.Log.Info("hat: button held", "hold_s", cmp.Or(b.HoldAfter, HoldForPoweroff).Seconds())
	}
	if f := b.OnHold; f != nil {
		go f()
	}
}

func (b *Button) stopTimersLocked() {
	for _, t := range []*time.Timer{b.warn, b.hold} {
		if t != nil {
			t.Stop()
		}
	}
	b.warn, b.hold = nil, nil
}

// Close stops any pending hold.
func (b *Button) Close() {
	b.mu.Lock()
	b.stopTimersLocked()
	b.mu.Unlock()
}

func (b *Button) debug(msg string, args ...any) {
	if b.Log != nil {
		b.Log.Debug(msg, args...)
	}
}
