package computer

import (
	"errors"
	"fmt"
	"sync"
)

// What a computer action targets (Allow's argument, for its error).
const (
	TargetBrowser = "browser" // through the browser extension
	TargetDesktop = "desktop" // the real cursor and keyboard
)

// ErrOff: computer control is off on this computer.
var ErrOff = errors.New(`computer control is off on this computer: turn it on with "Computer control" in the Jarvis Automation menu`)

// Switch is computer control's opt-in on this computer (COMPUTER-CONTROL.md C11): the status
// icon's "Computer control" toggle, `jarvis-agent computer on|off` and the control socket set
// it; agent.json's computer_control keeps it. Off by default, on every install and after an
// upgrade. While it is off the agent refuses every computer action, browser and desktop target
// alike (a macro's replay too): the executors call Allow before each batch, so jarvisd's view
// of the switch is never what enforces it (C8). Turning it off stops a task going on, as "Stop
// Jarvis" does. Recording isn't gated by it: each recording is an explicit act ("Record a
// macro…"), shown while it runs, and turning the switch off doesn't end one. A nil *Switch is
// off.
type Switch struct {
	// Persist saves the choice (agent.json); nil saves nothing.
	Persist func(on bool) error

	activity *Activity

	mu      sync.Mutex
	on      bool
	watches []func(bool)
}

// NewSwitch is a switch starting on or off; turning it off stops activity's runs.
func NewSwitch(on bool, activity *Activity) *Switch {
	return &Switch{on: on, activity: activity}
}

// On reports whether computer control is on.
func (s *Switch) On() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.on
}

// Allow is nil when Jarvis may act on this computer (what: TargetBrowser or TargetDesktop),
// else an error wrapping ErrOff that says where to turn it on.
func (s *Switch) Allow(what string) error {
	if s.On() {
		return nil
	}
	return fmt.Errorf("%s: %w", what, ErrOff)
}

// Watch calls f with the new state on every change (the tray's checkmark, the heartbeat).
func (s *Switch) Watch(f func(on bool)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.watches = append(s.watches, f)
}

// Set turns computer control on or off now and saves the choice. Off stops a task going on,
// not a recording. A save error is returned, but the switch has changed: it lasts until
// the agent restarts.
func (s *Switch) Set(on bool) error {
	var err error
	if s.Persist != nil {
		err = s.Persist(on)
	}
	s.mu.Lock()
	was := s.on
	s.on = on
	watches := s.watches
	s.mu.Unlock()
	if k, _ := s.activity.Status(); !on && k != Recording {
		// Also when it was already off: a run may be starting.
		s.activity.Stop()
	}
	if was != on {
		for _, f := range watches {
			f(on)
		}
	}
	return err
}
