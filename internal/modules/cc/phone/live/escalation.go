package live

import (
	"context"
	"sync"
	"time"
)

// DefaultEscalationWindow is how long a call holds for the user's answer (~25 s). Timing out,
// then "I'll check and call you back", is the expected common path.
const DefaultEscalationWindow = 25 * time.Second

// EscalationWindow is one call's bounded wait for an answer to a mid-call question
// (services/escalation.py). One window at a time. Safe for concurrent use: Deliver arrives
// from the callback path while the call goroutine Waits.
type EscalationWindow struct {
	// Timeout overrides DefaultEscalationWindow when > 0.
	Timeout time.Duration

	mu     sync.Mutex
	open   bool
	answer string
	ready  chan struct{}
}

// IsOpen reports whether a window is waiting for an answer.
func (e *EscalationWindow) IsOpen() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.open
}

// Open starts a window; false if one is already pending.
func (e *EscalationWindow) Open() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.open {
		return false
	}
	e.open = true
	e.answer = ""
	e.ready = make(chan struct{}, 1)
	return true
}

// Deliver hands the answer to the waiting call; false if no window is open.
func (e *EscalationWindow) Deliver(answer string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.open {
		return false
	}
	e.answer = answer
	select {
	case e.ready <- struct{}{}:
	default:
	}
	return true
}

// Wait blocks for the answer; ok=false when the window times out or ctx ends. The window
// closes either way.
func (e *EscalationWindow) Wait(ctx context.Context) (string, bool) {
	e.mu.Lock()
	ready := e.ready
	timeout := e.Timeout
	e.mu.Unlock()
	if timeout <= 0 {
		timeout = DefaultEscalationWindow
	}
	defer func() {
		e.mu.Lock()
		e.open = false
		e.mu.Unlock()
	}()
	if ready == nil {
		return "", false
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-ready:
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.answer, true
	case <-t.C:
	case <-ctx.Done():
	}
	return "", false
}
