// Package pkgagents runs the installed packages' background agents on a Pi (an IJarvisAgent in
// a package's agents/<name>/agent.py: comfyui's comfyui_jobs, which delivers finished videos
// to the inbox). It is N:services/agent_scheduler_service.py with the agents in the sidecar:
// this side owns the schedule and the alert queue, the sidecar runs each agent (agent_run),
// and what an agent posts (inbox items, pushes) goes out through the sidecar's backends.
//
// Not ported: the agents' context data in voice requests (get_context_data), and the mobile
// app's per-agent enable switch and auto-disable (the agent_registry): a failing agent keeps
// running at its interval and its package is reported failed (Failing) until a run succeeds.
package pkgagents

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/sdkhost"
	"github.com/alexberardi/jarvis-server/internal/agent/sidecar"
)

// CheckEvery is how often the schedule is checked (N:SCHEDULER_CHECK_INTERVAL).
const CheckEvery = 10 * time.Second

// FailAfter is how many runs in a row must fail before an agent's package is reported failing
// (N:AUTO_DISABLE_AFTER_FAILURES).
const FailAfter = 3

// Sidecar is the sidecar in service (*sidecar.Live): its agents, and one run of one.
type Sidecar interface {
	Agents() []sidecar.AgentDef
	RunAgent(ctx context.Context, name string) ([]json.RawMessage, error)
}

// Scheduler runs the agents of the sidecar in service on their schedules and queues their
// alerts. The agents are read again on every tick, so an install or uninstall (a new sidecar)
// starts or stops them; one that comes back starts over.
type Scheduler struct {
	Sidecar Sidecar
	Alerts  *sdkhost.Alerts
	Log     *slog.Logger
	// Now and After are the clock (nil: time.Now, time.After); tests drive their own.
	Now   func() time.Time
	After func(time.Duration) <-chan time.Time

	mu    sync.Mutex
	state map[string]*state // by agent name
}

// state is one agent's runs so far.
type state struct {
	pkg      string
	lastRun  time.Time // zero: never run; due one interval after seen, or at once on startup
	seen     time.Time
	failures int
	lastErr  string
}

func (s *Scheduler) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Scheduler) log() *slog.Logger {
	if s.Log == nil {
		return slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return s.Log
}

// Run ticks at once, then every CheckEvery, until ctx ends.
func (s *Scheduler) Run(ctx context.Context) {
	after := s.After
	if after == nil {
		after = time.After
	}
	for {
		s.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-after(CheckEvery):
		}
	}
}

// Tick runs every agent that is due, one at a time (the sidecar serves one request at a time).
func (s *Scheduler) Tick(ctx context.Context) {
	for _, a := range s.due() {
		if ctx.Err() != nil {
			return
		}
		alerts, err := s.Sidecar.RunAgent(ctx, a.Name)
		s.ran(a, err)
		for _, raw := range alerts {
			var al sdkhost.Alert
			if err := json.Unmarshal(raw, &al); err != nil {
				s.log().Warn("pkgagents: an alert couldn't be read", "agent", a.Name, "err", err)
				continue
			}
			if al.ID == "" {
				al.ID = newID()
			}
			if s.Alerts != nil {
				s.Alerts.Add(al)
			}
		}
	}
}

// due are the agents to run now; it forgets the ones no longer in the sidecar.
func (s *Scheduler) due() []sidecar.AgentDef {
	agents := s.Sidecar.Agents()
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == nil {
		s.state = map[string]*state{}
	}
	present := map[string]bool{}
	var out []sidecar.AgentDef
	for _, a := range agents {
		present[a.Name] = true
		st := s.state[a.Name]
		if st == nil {
			st = &state{pkg: a.Package, seen: now}
			s.state[a.Name] = st
			s.log().Info("pkgagents: agent loaded", "agent", a.Name, "package", a.Package, "every", a.Interval)
		}
		st.pkg = a.Package
		switch {
		case st.lastRun.IsZero() && a.RunOnStartup:
			out = append(out, a)
		case st.lastRun.IsZero():
			if !now.Before(st.seen.Add(a.Interval)) {
				out = append(out, a)
			}
		case !now.Before(st.lastRun.Add(a.Interval)):
			out = append(out, a)
		}
	}
	for name := range s.state {
		if !present[name] {
			delete(s.state, name)
		}
	}
	return out
}

// ran records one run's outcome. A failed run counts as a run: it is retried at its interval.
func (s *Scheduler) ran(a sidecar.AgentDef, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.state[a.Name]
	if st == nil {
		return
	}
	st.lastRun = s.now()
	if err == nil {
		if st.failures >= FailAfter {
			s.log().Info("pkgagents: agent recovered", "agent", a.Name)
		}
		st.failures, st.lastErr = 0, ""
		return
	}
	st.failures++
	// Once per new error: an agent failing every 20 s would fill the log.
	if msg := err.Error(); msg != st.lastErr {
		s.log().Warn("pkgagents: agent run failed", "agent", a.Name, "package", a.Package, "err", err)
		st.lastErr = msg
	}
}

// Failing are the packages with an agent whose last FailAfter runs failed, with the agent and
// its error (packages.Manager.AgentHealth).
func (s *Scheduler) Failing() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]string{}
	for name, st := range s.state {
		if st.failures >= FailAfter {
			out[st.pkg] = name + ": " + st.lastErr
		}
	}
	return out
}

// newID is a fresh alert id for one that came without (the SDK's Alert takes a uuid4).
func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
