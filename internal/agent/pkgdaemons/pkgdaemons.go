// Package pkgdaemons keeps the installed packages' daemons running on a node (an
// IJarvisDaemon in a package's daemons/<name>/daemon.py: a chat channel's connection,
// docs/CHANNELS.md). Each daemon is a Python process of its own (sidecar.Daemon), started when
// its package is installed and at agent start, restarted with a growing delay when it ends,
// restarted on new code or changed secrets, and stopped when its package is removed.
package pkgdaemons

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/packages"
)

const (
	// CheckEvery is how often the daemons are looked at: an ended one is noticed, a due one
	// started, an installed or removed package followed.
	CheckEvery = 5 * time.Second
	// FailAfter is how many times in a row a daemon must end before its package is reported
	// failing (as pkgagents.FailAfter).
	FailAfter = 3
	// firstDelay is the wait before a daemon that ended is started again; it doubles with
	// each end in a row, up to maxDelay.
	firstDelay = 5 * time.Second
	maxDelay   = 5 * time.Minute
	// steadyAfter: a daemon that stayed up this long had recovered, so its next end counts
	// as the first.
	steadyAfter = 10 * time.Minute
)

// Proc is a daemon's running process (*sidecar.Daemon).
type Proc interface {
	Done() <-chan struct{}
	Err() error
	Stop()
}

// Supervisor keeps Daemons running. Run (or Tick) is called from one goroutine; Restart and
// Failing from any.
type Supervisor struct {
	// Daemons are the daemons that should be running now (packages.Manager.Daemons).
	Daemons func() []packages.DaemonDef
	// Start starts one.
	Start func(packages.DaemonDef) (Proc, error)
	Log   *slog.Logger
	// Now and After are the clock (nil: time.Now, time.After); tests drive their own.
	Now   func() time.Time
	After func(time.Duration) <-chan time.Time

	mu      sync.Mutex
	state   map[string]*state // by package/name
	restart bool              // Restart was asked: every daemon is stopped at the next Tick
	kick    chan struct{}
}

type state struct {
	def     packages.DaemonDef
	proc    Proc // nil: not running
	started time.Time
	retryAt time.Time
	ends    int // in a row
	lastErr string
}

func (s *Supervisor) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Supervisor) kicked() chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.kick == nil {
		s.kick = make(chan struct{}, 1)
	}
	return s.kick
}

// Run keeps the daemons running until ctx ends, then stops them all.
func (s *Supervisor) Run(ctx context.Context) {
	after := s.After
	if after == nil {
		after = time.After
	}
	kick := s.kicked()
	for {
		s.Tick()
		select {
		case <-ctx.Done():
			s.stopAll()
			return
		case <-kick:
		case <-after(CheckEvery):
		}
	}
}

// Restart stops every daemon and starts it again at once (the node's secrets changed: a
// daemon read its token when it connected).
func (s *Supervisor) Restart() {
	kick := s.kicked()
	s.mu.Lock()
	s.restart = true
	s.mu.Unlock()
	select {
	case kick <- struct{}{}:
	default:
	}
}

// Tick looks at every daemon once: it stops the ones whose package went or changed, notes the
// ones that ended, and starts the ones that are due.
func (s *Supervisor) Tick() {
	now := s.now()
	want := map[string]packages.DaemonDef{}
	for _, d := range s.Daemons() {
		want[d.Package+"/"+d.Name] = d
	}
	s.mu.Lock()
	if s.state == nil {
		s.state = map[string]*state{}
	}
	restart := s.restart
	s.restart = false
	var stop []Proc
	for key, st := range s.state {
		d, ok := want[key]
		if ok && d == st.def && !restart {
			continue
		}
		if st.proc != nil {
			stop = append(stop, st.proc)
			st.proc = nil
		}
		if !ok {
			delete(s.state, key)
			continue
		}
		// New code or new secrets: a fresh start, whatever happened before.
		*st = state{def: d}
	}
	for key, d := range want {
		if s.state[key] == nil {
			s.state[key] = &state{def: d}
		}
	}
	var due []*state
	for _, st := range s.state {
		if st.proc != nil {
			select {
			case <-st.proc.Done():
				s.ended(st, now, st.proc.Err())
			default:
				continue
			}
		}
		if !now.Before(st.retryAt) {
			due = append(due, st)
		}
	}
	s.mu.Unlock()

	for _, p := range stop {
		p.Stop() // waits for the process: outside the lock
	}
	for _, st := range due {
		p, err := s.Start(st.def)
		s.mu.Lock()
		if err != nil {
			s.ended(st, now, err)
		} else {
			st.proc, st.started = p, now
			s.Log.Info("pkgdaemons: started", "package", st.def.Package, "daemon", st.def.Name)
		}
		s.mu.Unlock()
	}
}

// ended notes that st's daemon ended (or couldn't start) and when to try again. s.mu is held.
func (s *Supervisor) ended(st *state, now time.Time, err error) {
	if st.proc != nil && now.Sub(st.started) >= steadyAfter {
		st.ends = 0
	}
	st.proc = nil
	st.ends++
	st.lastErr = "ended"
	if err != nil {
		st.lastErr = err.Error()
	}
	delay := maxDelay
	if st.ends <= 6 { // 5 s doubling passes maxDelay after the sixth
		delay = min(firstDelay<<(st.ends-1), maxDelay)
	}
	st.retryAt = now.Add(delay)
	s.Log.Warn("pkgdaemons: a daemon ended; its own lines are above", "package", st.def.Package, "daemon", st.def.Name,
		"err", st.lastErr, "times", st.ends, "again_in", delay)
}

func (s *Supervisor) stopAll() {
	s.mu.Lock()
	var procs []Proc
	for _, st := range s.state {
		if st.proc != nil {
			procs = append(procs, st.proc)
			st.proc = nil
		}
	}
	s.mu.Unlock()
	for _, p := range procs {
		p.Stop()
	}
}

// Failing are the packages with a daemon that ended FailAfter times in a row, with the daemon
// and why (packages.Manager.AgentHealth).
func (s *Supervisor) Failing() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]string{}
	for _, st := range s.state {
		if st.ends >= FailAfter {
			out[st.def.Package] = st.def.Name + ": " + st.lastErr
		}
	}
	return out
}
