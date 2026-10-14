package selfupdate

import (
	"context"
	"fmt"
	"os"
	"time"
)

// DefaultGate is how long a new version has to come up healthy before it is rolled back: start,
// reach jarvisd, connect to its broker. Generous, since a desktop may be busy at login.
const DefaultGate = 2 * time.Minute

// GatePoll is the pause between health checks.
const GatePoll = time.Second

// Agent is the running agent as Finish sees it.
type Agent struct {
	// Running reports whether an agent of this user answers its control socket now. When none
	// does (`jarvis-agent update` with the agent stopped), Finish swaps and checks the new
	// binary's version, and starts nothing.
	Running bool
	// Restart restarts it the way `jarvis-agent restart` does: quit over the control socket,
	// then the supervisor (systemctl --user, launchctl kickstart) or a detached start.
	Restart func(ctx context.Context) error
	// Health asks the agent's control socket for health: nil once it is connected to jarvisd,
	// with the version that answered.
	Health func() (version string, err error)
	// Pause waits between health checks (time.Sleep(GatePoll); a no-op in tests).
	Pause func()
	// Attempts bounds the health checks (DefaultGate / GatePoll).
	Attempts int
	// CheckInstalled runs the installed binary's `version` (when the agent isn't running).
	CheckInstalled func(tag string) error
}

// Finish installs the staged update: Swap, restart, and wait for the new version to answer the
// health check; if it doesn't, Rollback and restart the previous version. The result is written
// (last-update.json) and returned; the marker and staged files are removed either way.
func (u *Updater) Finish(ctx context.Context, a Agent) *Result {
	m, err := ReadMarker(u.Paths)
	if err != nil || m == nil {
		reason := "no staged update"
		if err != nil {
			reason = err.Error()
		}
		return &Result{Outcome: OutcomeFailed, From: u.Current, Reason: reason, At: u.now()}
	}
	res := &Result{From: m.From, To: m.To, TaskID: m.TaskID, By: m.By}
	done := func(outcome, reason string) *Result {
		res.Outcome, res.Reason, res.At = outcome, reason, u.now().UTC()
		if err := WriteResult(u.Paths, res); err != nil {
			u.log().Warn("update: couldn't record the result", "err", err)
		}
		_ = os.Remove(u.Paths.MarkerPath())
		u.cleanup(outcome == OutcomeSucceeded)
		return res
	}
	if err := u.Swap(m); err != nil {
		u.log().Warn("update: not installed", "to", m.To, "err", err)
		return done(OutcomeFailed, err.Error())
	}
	u.log().Info("update: swapped in", "from", m.From, "to", m.To)
	if !a.Running {
		if a.CheckInstalled != nil {
			if err := a.CheckInstalled(m.To); err != nil {
				return u.rollback(ctx, m, a, done, fmt.Sprintf("the installed %s doesn't run: %v", m.To, err))
			}
		}
		return done(OutcomeSucceeded, "")
	}
	if err := a.Restart(ctx); err != nil {
		return u.rollback(ctx, m, a, done, fmt.Sprintf("couldn't restart the agent as %s: %v", m.To, err))
	}
	if reason := waitHealthy(ctx, a, m.To); reason != "" {
		return u.rollback(ctx, m, a, done, fmt.Sprintf("%s didn't come up: %s", m.To, reason))
	}
	u.log().Info("update: healthy", "version", m.To)
	return done(OutcomeSucceeded, "")
}

// rollback restores the previous version and, if the agent was running, starts it again.
func (u *Updater) rollback(ctx context.Context, m *Marker, a Agent, done func(string, string) *Result, reason string) *Result {
	u.log().Warn("update: rolling back", "to", m.From, "reason", reason)
	if err := u.Rollback(m); err != nil {
		return done(OutcomeFailed, reason+"; and the previous version couldn't be restored: "+err.Error())
	}
	// Recorded before the restart: the restarted agent reads it at once (the status line, the
	// failure it reports on jarvisd's task).
	res := done(OutcomeRolledBack, reason)
	if a.Running {
		if err := a.Restart(ctx); err != nil {
			u.log().Warn("update: restarting the previous version failed", "err", err)
		} else if r := waitHealthy(ctx, a, ""); r != "" {
			u.log().Warn("update: the previous version isn't healthy either", "reason", r)
		}
	}
	return res
}

// waitHealthy polls the health check until it passes as version (any version when ""), up to
// a.Attempts times. It returns "" on success, else the last reason.
func waitHealthy(ctx context.Context, a Agent, version string) string {
	attempts := a.Attempts
	if attempts <= 0 {
		attempts = int(DefaultGate / GatePoll)
	}
	reason := "no answer"
	for n := 0; n < attempts && ctx.Err() == nil; n++ {
		if n > 0 && a.Pause != nil {
			a.Pause()
		}
		v, err := a.Health()
		switch {
		case err != nil:
			reason = err.Error()
		case version != "" && v != version:
			reason = fmt.Sprintf("version %q answered", v)
		default:
			return ""
		}
	}
	if ctx.Err() != nil {
		return ctx.Err().Error()
	}
	return reason + " (gave up waiting)"
}
