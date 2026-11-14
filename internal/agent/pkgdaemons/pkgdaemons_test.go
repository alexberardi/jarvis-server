package pkgdaemons

import (
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/packages"
)

type fakeProc struct {
	done    chan struct{}
	err     error
	stopped bool
}

func (p *fakeProc) Done() <-chan struct{} { return p.done }
func (p *fakeProc) Err() error            { return p.err }
func (p *fakeProc) Stop() {
	if !p.stopped {
		p.stopped = true
		close(p.done)
	}
}

// die ends the process on its own, as a daemon whose run() raised.
func (p *fakeProc) die(why string) { p.err = errors.New(why); close(p.done) }

type env struct {
	t       *testing.T
	s       *Supervisor
	now     time.Time
	defs    []packages.DaemonDef
	started []string
	procs   map[string]*fakeProc // the latest of each
	failTo  error
}

func newEnv(t *testing.T, defs ...packages.DaemonDef) *env {
	e := &env{t: t, now: time.Date(2026, 11, 13, 22, 0, 0, 0, time.UTC), defs: defs, procs: map[string]*fakeProc{}}
	e.s = &Supervisor{Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return e.now },
		Daemons: func() []packages.DaemonDef { return e.defs },
		Start: func(d packages.DaemonDef) (Proc, error) {
			e.started = append(e.started, d.Name)
			if e.failTo != nil {
				return nil, e.failTo
			}
			p := &fakeProc{done: make(chan struct{})}
			e.procs[d.Name] = p
			return p, nil
		}}
	return e
}

// at moves the clock on by d, ticks, and checks which daemons that tick started.
func (e *env) at(d time.Duration, want ...string) {
	e.t.Helper()
	e.now = e.now.Add(d)
	e.started = nil
	e.s.Tick()
	if strings.Join(e.started, ",") != strings.Join(want, ",") {
		e.t.Fatalf("+%v started %v, want %v", d, e.started, want)
	}
}

var discord = packages.DaemonDef{Package: "channel_discord", Name: "discord", Root: "/p/channel_discord", Path: "daemons/discord/daemon.py", Stamp: "1.0.0"}

// A daemon is started once and left alone while it runs. One that ends is started again after
// a delay that doubles each time; after FailAfter ends in a row its package is failing.
func TestADaemonThatEndsIsRestartedWithAGrowingDelay(t *testing.T) {
	e := newEnv(t, discord)
	e.at(0, "discord")
	e.at(time.Hour)

	e.procs["discord"].die("exit status 1")
	e.at(0) // noticed, not started again yet
	e.at(4 * time.Second)
	e.at(time.Second, "discord")
	if f := e.s.Failing(); len(f) != 0 {
		t.Fatalf("failing after one end: %v", f)
	}

	e.procs["discord"].die("exit status 1")
	e.at(0)
	e.at(9 * time.Second)
	e.at(time.Second, "discord")

	e.procs["discord"].die("exit status 3")
	e.at(0)
	if f := e.s.Failing(); f["channel_discord"] != "discord: exit status 3" {
		t.Fatalf("failing = %v", f)
	}
	e.at(19 * time.Second)
	e.at(time.Second, "discord")

	// Up long enough to have recovered: its next end counts as the first again.
	e.at(steadyAfter)
	e.procs["discord"].die("exit status 1")
	e.at(0)
	if f := e.s.Failing(); len(f) != 0 {
		t.Fatalf("failing after it had recovered: %v", f)
	}
	e.at(firstDelay, "discord")
}

// The delay stops growing at maxDelay, and a daemon that can't be started counts as one that ended.
func TestTheDelayIsCapped(t *testing.T) {
	e := newEnv(t, discord)
	e.failTo = errors.New("no Python environment")
	e.at(0, "discord")
	for _, d := range []time.Duration{5, 10, 20, 40, 80, 160, 300, 300} {
		e.at(d*time.Second - time.Second)
		e.at(time.Second, "discord")
	}
	if f := e.s.Failing(); f["channel_discord"] != "discord: no Python environment" {
		t.Fatalf("failing = %v", f)
	}
}

// A removed package's daemon is stopped; an updated one's is restarted on the new code at
// once; changed secrets restart every daemon at once, whatever delay one was waiting out.
func TestDaemonsFollowInstallsAndSecrets(t *testing.T) {
	slack := packages.DaemonDef{Package: "channel_slack", Name: "slack", Root: "/p/channel_slack", Path: "daemons/slack/daemon.py", Stamp: "1.0.0"}
	e := newEnv(t, discord, slack)
	e.s.Tick()
	if len(e.procs) != 2 {
		t.Fatalf("started %v", e.started)
	}
	first := e.procs["discord"]

	updated := discord
	updated.Stamp = "1.1.0"
	e.defs = []packages.DaemonDef{updated, slack}
	e.at(CheckEvery, "discord")
	if !first.stopped || e.procs["slack"].stopped {
		t.Fatalf("after an update: discord stopped %v, slack stopped %v", first.stopped, e.procs["slack"].stopped)
	}

	e.defs = []packages.DaemonDef{updated}
	e.at(CheckEvery)
	if !e.procs["slack"].stopped {
		t.Fatal("a removed package's daemon still runs")
	}

	e.procs["discord"].die("exit status 1")
	e.at(0)
	e.s.Restart()
	before := e.procs["discord"]
	e.at(0, "discord")
	if e.procs["discord"] == before || len(e.s.Failing()) != 0 {
		t.Fatal("changed secrets did not start the daemon afresh")
	}
	e.s.Restart()
	running := e.procs["discord"]
	e.at(0, "discord")
	if !running.stopped {
		t.Fatal("changed secrets left the running daemon on its old ones")
	}
}
