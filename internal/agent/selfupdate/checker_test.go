package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/update"
)

type checkerRig struct {
	c        *Checker
	now      time.Time
	auto     bool
	starts   []StageOptions
	startErr error
	reports  []string
	status   []string
}

func newRig(cur string) *checkerRig {
	r := &checkerRig{now: time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC), auto: true}
	r.c = &Checker{
		Current: cur,
		Auto:    func() bool { return r.auto },
		Start: func(_ context.Context, o StageOptions) error {
			r.starts = append(r.starts, o)
			return r.startErr
		},
		Status: func(s string) { r.status = append(r.status, s) },
		Now:    func() time.Time { return r.now },
	}
	return r
}

func (r *checkerRig) report(_ context.Context, task, msg string) error {
	r.reports = append(r.reports, task+": "+msg)
	return nil
}

func (r *checkerRig) beat(server string, p *Pending) {
	r.c.OnBeat(server, p, r.report)
	r.c.Step(context.Background())
}

func TestCheckerFollowsTheServerDaily(t *testing.T) {
	r := newRig("v1.0.0")
	r.startErr = errors.New("GitHub unreachable")
	r.beat("v1.0.0", nil) // same version: nothing
	if len(r.starts) != 0 {
		t.Fatal(r.starts)
	}
	r.beat("v1.1.0", nil) // newer: at once
	if len(r.starts) != 1 || r.starts[0] != (StageOptions{Target: "v1.1.0", By: ByAuto}) {
		t.Fatalf("%+v", r.starts)
	}
	if r.status[len(r.status)-1] != "Update to v1.1.0 failed; still on v1.0.0" {
		t.Fatal(r.status)
	}
	r.now = r.now.Add(time.Hour)
	r.beat("v1.1.0", nil) // failed: not again within the day
	if len(r.starts) != 1 {
		t.Fatalf("retried within a day: %+v", r.starts)
	}
	r.now = r.now.Add(CheckEvery)
	r.c.Step(context.Background()) // the daily tick
	if len(r.starts) != 2 {
		t.Fatalf("no daily retry: %+v", r.starts)
	}
	// Off: never by itself.
	r.auto = false
	r.now = r.now.Add(2 * CheckEvery)
	r.beat("v1.2.0", nil)
	if len(r.starts) != 2 {
		t.Fatalf("updated with auto_update off: %+v", r.starts)
	}
	// Once one is handed over, this process does nothing more.
	r.auto, r.startErr = true, nil
	r.beat("v1.3.0", nil)
	r.beat("v1.4.0", &Pending{TaskID: "t9"})
	if len(r.starts) != 3 || r.starts[2].Target != "v1.3.0" {
		t.Fatalf("%+v", r.starts)
	}
}

func TestCheckerAnswersUpdateRequests(t *testing.T) {
	r := newRig("v1.0.0")
	r.beat("v1.1.0", &Pending{TaskID: "t1"}) // no target: the server's version
	if len(r.starts) != 1 || r.starts[0] != (StageOptions{Target: "v1.1.0", TaskID: "t1", By: ByServer}) {
		t.Fatalf("%+v", r.starts)
	}

	r = newRig("v1.0.0")
	r.startErr = errors.New("release v1.2.0 is not signed")
	r.beat("v1.1.0", &Pending{TaskID: "t2", Target: "v1.2.0"})
	if len(r.reports) != 1 || r.reports[0] != "t2: release v1.2.0 is not signed" {
		t.Fatalf("%v", r.reports)
	}

	for name, c := range map[string]struct {
		cur, target string
		auto        bool
		want        string
	}{
		"auto off":  {"v1.0.0", "v1.1.0", false, "t: automatic updates are off on this computer (agent.json auto_update)"},
		"dev build": {"dev-1234", "v1.1.0", true, "t: " + ErrDevBuild.Error()},
		"older":     {"v1.0.0", "v0.9.0", true, "t: v0.9.0 is not newer than the running v1.0.0"},
		"no target": {"v1.0.0", "", true, "t: no release version to update to ()"},
	} {
		r := newRig(c.cur)
		r.auto = c.auto
		r.beat("", &Pending{TaskID: "t", Target: c.target})
		if len(r.starts) != 0 || len(r.reports) != 1 || r.reports[0] != c.want {
			t.Errorf("%s: starts %v reports %v", name, r.starts, r.reports)
		}
	}
	// Already there: nothing to do or report (the heartbeat's version completes the task).
	r = newRig("v1.1.0")
	r.beat("v1.1.0", &Pending{TaskID: "t", Target: "v1.1.0"})
	if len(r.starts)+len(r.reports) != 0 {
		t.Fatal(r.starts, r.reports)
	}
}

func TestCheckerStartup(t *testing.T) {
	r := newRig("v1.1.0")
	r.c.Startup(&Marker{State: StateSwapped, To: "v1.1.0"}, nil)
	if len(r.status) != 1 || r.status[0] != "Updated to v1.1.0" {
		t.Fatal(r.status)
	}
	// A rollback: shown, its task reported once, and that version not retried at once.
	r = newRig("v1.0.0")
	done := 0
	r.c.Done = func() { done++ }
	r.c.Startup(nil, &Result{Outcome: OutcomeRolledBack, From: "v1.0.0", To: "v1.1.0", TaskID: "t1",
		Reason: "v1.1.0 didn't come up", At: r.now.Add(-time.Minute)})
	if r.status[0] != "Update to v1.1.0 failed; still on v1.0.0" {
		t.Fatal(r.status)
	}
	r.beat("v1.1.0", nil)
	if len(r.starts) != 0 || len(r.reports) != 1 || r.reports[0] != "t1: update to v1.1.0 failed: v1.1.0 didn't come up" || done != 1 {
		t.Fatalf("starts %v reports %v done %d", r.starts, r.reports, done)
	}
	r.beat("v1.1.0", nil)
	if len(r.reports) != 1 {
		t.Fatal("reported twice")
	}
	// An old result says nothing.
	r = newRig("v1.0.0")
	r.c.Startup(nil, &Result{Outcome: OutcomeSucceeded, To: "v1.0.0", At: r.now.Add(-2 * CheckEvery)})
	if len(r.status) != 0 {
		t.Fatal(r.status)
	}
}

// Run wakes on a beat and on its tick, and stops with its context.
func TestCheckerRun(t *testing.T) {
	r := newRig("v1.0.0")
	tick := make(chan time.Time)
	r.c.everyTick = func() <-chan time.Time { return tick }
	started := make(chan StageOptions, 1)
	r.c.Start = func(_ context.Context, o StageOptions) error { started <- o; return nil }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); r.c.Run(ctx) }()
	r.c.OnBeat("v1.1.0", nil, r.report)
	if o := <-started; o.Target != "v1.1.0" {
		t.Fatal(o)
	}
	cancel()
	<-done
}

// A server running an unpublished build (a release dry run) has no release to follow: the agent
// stays on its version without a word in the island, and waits a day before looking again.
func TestCheckerIgnoresAnUnpublishedServerVersion(t *testing.T) {
	r := newRig("v0.1.0-rc18")
	r.startErr = fmt.Errorf("release v0.1.0-rc18.3: %w", update.ErrNoRelease)
	r.beat("v0.1.0-rc18.3", nil)
	if len(r.starts) != 1 {
		t.Fatalf("%+v", r.starts)
	}
	if len(r.status) != 0 {
		t.Fatalf("status shown for an unpublished server version: %q", r.status)
	}
	if r.c.Current != "v0.1.0-rc18" {
		t.Fatal(r.c.Current)
	}
	r.now = r.now.Add(time.Hour)
	r.beat("v0.1.0-rc18.3", nil)
	if len(r.starts) != 1 {
		t.Fatalf("retried within a day: %+v", r.starts)
	}
	// Once it is published, the daily look finds it.
	r.startErr = nil
	r.now = r.now.Add(CheckEvery)
	r.c.Step(context.Background())
	if len(r.starts) != 2 {
		t.Fatalf("no daily retry: %+v", r.starts)
	}
}

// Asked by jarvisd for a version with no release, the agent still answers the task.
func TestCheckerReportsAnUnpublishedRequestedVersion(t *testing.T) {
	r := newRig("v0.1.0-rc18")
	r.startErr = fmt.Errorf("release v0.1.0-rc18.3: %w", update.ErrNoRelease)
	r.beat("v0.1.0-rc18.3", &Pending{TaskID: "t1"})
	if len(r.reports) != 1 || r.reports[0] != "t1: release v0.1.0-rc18.3: "+update.ErrNoRelease.Error() {
		t.Fatalf("%q", r.reports)
	}
	if len(r.status) != 1 || r.status[0] != "Update to v0.1.0-rc18.3 failed; still on v0.1.0-rc18" {
		t.Fatalf("%q", r.status)
	}
}
