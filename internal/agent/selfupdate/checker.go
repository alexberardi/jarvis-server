package selfupdate

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/update"
)

// CheckEvery is how often the agent checks the server's version on its own: daily, plus at once
// when it first learns the version or the version changes.
const CheckEvery = 24 * time.Hour

// Pending is jarvisd's update dispatch (the heartbeat's pending_update): a household member
// asked for this computer to be updated.
type Pending struct {
	TaskID string
	Target string // "" when jarvisd named none: the server's version
}

// Reporter tells jarvisd an update task failed.
type Reporter func(ctx context.Context, taskID, msg string) error

// Checker decides when the running agent updates itself (01-no-cli-plan 1.7): when jarvisd's
// heartbeat answer carries an update request, and otherwise once a day (and when the server's
// version changes), to the server's version. A version that failed isn't tried again on its own
// for a day.
type Checker struct {
	Current string
	// Auto is agent.json's auto_update (default on). Off, the agent never updates by itself and
	// refuses update requests from jarvisd; `jarvis-agent update` still works.
	Auto func() bool
	// Start stages o and hands it to `update --finish` (which restarts this agent). It runs on
	// Run's goroutine.
	Start func(ctx context.Context, o StageOptions) error
	// Status shows a line in the status icon's menu (and the island).
	Status func(text string)
	// Done records that a reported failure was reported (Result.Reported).
	Done func()
	Now  func() time.Time
	Log  *slog.Logger

	mu        sync.Mutex
	server    string
	pending   *Pending
	report    Reporter
	tried     map[string]time.Time // version -> last automatic attempt
	owed      *Result              // a failure to report on its task, once we can
	started   bool                 // an update was handed over: this process is about to be replaced
	wakeOnce  sync.Once
	wakeCh    chan struct{}
	everyTick func() <-chan time.Time
}

func (c *Checker) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Checker) log() *slog.Logger {
	if c.Log != nil {
		return c.Log
	}
	return slog.New(slog.DiscardHandler)
}

func (c *Checker) auto() bool { return c.Auto == nil || c.Auto() }

func (c *Checker) status(s string) {
	if c.Status != nil {
		c.Status(s)
	}
}

func (c *Checker) wake() chan struct{} {
	c.wakeOnce.Do(func() { c.wakeCh = make(chan struct{}, 1) })
	return c.wakeCh
}

func (c *Checker) poke() {
	select {
	case c.wake() <- struct{}{}:
	default:
	}
}

// Startup reads what the last update left: the marker of the update that started this
// process, and the last result. It sets the status line, remembers a failed version so it isn't
// retried at once, and keeps a failure to report on jarvisd's task.
func (c *Checker) Startup(m *Marker, r *Result) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tried == nil {
		c.tried = map[string]time.Time{}
	}
	now := c.now()
	if m != nil && m.State == StateSwapped && m.To == c.Current {
		c.status("Updated to " + c.Current)
		return
	}
	if r == nil || now.Sub(r.At) > CheckEvery {
		return
	}
	switch r.Outcome {
	case OutcomeSucceeded:
		if r.To == c.Current {
			c.status("Updated to " + c.Current)
		}
	case OutcomeRolledBack, OutcomeFailed:
		if r.To != "" {
			c.tried[r.To] = r.At
		}
		if Newer(r.To, c.Current) {
			c.status("Update to " + r.To + " failed; still on " + c.Current)
		}
		if r.TaskID != "" && !r.Reported {
			c.owed = r
		}
	}
}

// OnBeat takes jarvisd's heartbeat answer: the server's version, an update request, and how to
// report on a task. It never blocks; Run acts on it.
func (c *Checker) OnBeat(server string, p *Pending, report Reporter) {
	c.mu.Lock()
	changed := server != "" && server != c.server
	if server != "" {
		c.server = server
	}
	if p != nil {
		c.pending = p
	}
	c.report = report
	poke := changed || p != nil || c.owed != nil
	c.mu.Unlock()
	if poke {
		c.poke()
	}
}

// Run acts on what OnBeat learned, and checks daily, until ctx ends.
func (c *Checker) Run(ctx context.Context) {
	tick := c.everyTick
	if tick == nil {
		t := time.NewTicker(time.Hour) // the daily rule is in decide; this only wakes it
		defer t.Stop()
		tick = func() <-chan time.Time { return t.C }
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.wake():
		case <-tick():
		}
		c.Step(ctx)
	}
}

// Step does what is due now: report an owed failure, answer an update request, or follow the
// server's version.
func (c *Checker) Step(ctx context.Context) {
	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		return
	}
	report := c.report
	owed := c.owed
	if owed != nil && report != nil {
		c.owed = nil
	}
	o, refuse := c.decide()
	c.mu.Unlock()

	if owed != nil && report != nil {
		if err := report(ctx, owed.TaskID, "update to "+owed.To+" failed: "+owed.Reason); err != nil {
			c.log().Warn("update: couldn't report the failed update to jarvisd", "err", err)
		} else if c.Done != nil {
			c.Done()
		}
	}
	if refuse != nil {
		c.log().Info("update: refusing jarvisd's update request", "task", refuse.taskID, "reason", refuse.reason)
		if report != nil {
			if err := report(ctx, refuse.taskID, refuse.reason); err != nil {
				c.log().Warn("update: couldn't report the refusal to jarvisd", "err", err)
			}
		}
		return
	}
	if o == nil {
		return
	}
	c.log().Info("update: updating", "from", c.Current, "to", o.Target, "by", o.By)
	err := c.Start(ctx, *o)
	c.mu.Lock()
	if err == nil {
		c.started = true
	}
	c.mu.Unlock()
	if err == nil {
		return
	}
	if o.By == ByAuto && errors.Is(err, update.ErrNoRelease) {
		// The server runs a build that was never published (a release dry run): nothing to
		// follow, so nothing failed. decide waits a day before looking again.
		c.log().Debug("update: the server's version has no published release", "to", o.Target, "err", err)
		return
	}
	c.log().Warn("update: not updated", "to", o.Target, "err", err)
	c.status("Update to " + o.Target + " failed; still on " + c.Current)
	if o.TaskID != "" && report != nil {
		if rerr := report(ctx, o.TaskID, err.Error()); rerr != nil {
			c.log().Warn("update: couldn't report the failure to jarvisd", "err", rerr)
		}
	}
}

type refusal struct{ taskID, reason string }

// decide picks what to do now (c.mu held). An update request is consumed here, whatever the
// answer; following the server's version happens at most daily per version.
func (c *Checker) decide() (*StageOptions, *refusal) {
	if c.tried == nil {
		c.tried = map[string]time.Time{}
	}
	now := c.now()
	if p := c.pending; p != nil {
		c.pending = nil
		target := p.Target
		if target == "" {
			target = c.server
		}
		switch {
		case !c.auto():
			return nil, &refusal{p.TaskID, "automatic updates are off on this computer (agent.json auto_update)"}
		case !IsRelease(c.Current):
			return nil, &refusal{p.TaskID, ErrDevBuild.Error()}
		case !IsRelease(target):
			return nil, &refusal{p.TaskID, "no release version to update to (" + target + ")"}
		case target == c.Current:
			return nil, nil // the next heartbeat's version completes the task
		case !Newer(target, c.Current):
			return nil, &refusal{p.TaskID, target + " is not newer than the running " + c.Current}
		}
		c.tried[target] = now
		return &StageOptions{Target: target, TaskID: p.TaskID, By: ByServer}, nil
	}
	s := c.server
	if !c.auto() || !IsRelease(c.Current) || !Newer(s, c.Current) {
		return nil, nil
	}
	if last, ok := c.tried[s]; ok && now.Sub(last) < CheckEvery {
		return nil, nil
	}
	c.tried[s] = now
	return &StageOptions{Target: s, By: ByAuto}, nil
}
