package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/control"
	"github.com/alexberardi/jarvis-server/internal/agent/install"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// fakeStart records what a start did.
type fakeStart struct {
	running   bool // an agent answers the control socket
	comesUp   bool // a started agent answers
	setUp     bool // enrolled
	installed bool
	log       []string
}

func (f *fakeStart) starter(e install.Env) starter {
	return starter{
		env: e,
		sys: install.Sys{
			Run: func(_ context.Context, args ...string) error {
				f.log = append(f.log, "run "+strings.Join(args, " "))
				return errors.New("no systemd here")
			},
			Spawn: func(args ...string) error { f.log = append(f.log, "spawn "+strings.Join(args, " ")); return nil },
		},
		send: func(cmd string) error {
			f.log = append(f.log, "send "+cmd)
			if !f.running {
				return control.ErrNotRunning
			}
			return nil
		},
		waitSend: func(cmd string) error {
			f.log = append(f.log, "wait "+cmd)
			if !f.comesUp {
				return control.ErrNotRunning
			}
			return nil
		},
		enrolled: func() bool { return f.setUp },
		install:  func() error { f.log = append(f.log, "install"); f.installed = true; return nil },
		firstRun: func(context.Context) error { f.log = append(f.log, "first-run"); return nil },
		window:   func() { f.log = append(f.log, "window") },
	}
}

func linuxEnv(t *testing.T, exe string) install.Env {
	home := t.TempDir()
	if exe == "" {
		exe = filepath.Join(home, "src", "jarvis-agent")
	}
	return install.Env{GOOS: "linux", Home: home, Exe: exe, Getenv: func(string) string { return "" }}
}

// Opening Jarvis while it runs flashes the pill and opens the setup page, which says it is
// running: the pill alone was easy to miss (and absent without the island).
func TestStartWhenRunningShowsThePillAndTheSetupPage(t *testing.T) {
	f := &fakeStart{running: true, setUp: true}
	if err := f.starter(linuxEnv(t, "")).start(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.log, []string{"send show-running", "window"}) {
		t.Fatalf("did %q", f.log)
	}
	// At login: a ping, no pill.
	f.log = nil
	_ = f.starter(linuxEnv(t, "")).start(context.Background(), true)
	if !slices.Equal(f.log, []string{"send ping"}) {
		t.Fatalf("did %q", f.log)
	}
}

func TestStartStartsThenShowsThePill(t *testing.T) {
	f := &fakeStart{setUp: true, comesUp: true}
	e := linuxEnv(t, "")
	if err := f.starter(e).start(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	// A dev copy in home: no install; no unit: spawned from this session; then the pill.
	want := []string{"send show-running", "spawn " + e.Exe + " run", "wait show-running"}
	if !slices.Equal(f.log, want) {
		t.Fatalf("did %q\nwant %q", f.log, want)
	}
	f = &fakeStart{setUp: true}
	if err := f.starter(e).start(context.Background(), false); err == nil {
		t.Fatal("an agent that never answered counted as started")
	}
}

func TestStartNotSetUpGoesToTheFirstRunPage(t *testing.T) {
	f := &fakeStart{}
	if err := f.starter(linuxEnv(t, "")).start(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.log, "first-run") || slices.ContainsFunc(f.log, func(s string) bool { return strings.HasPrefix(s, "spawn") }) {
		t.Fatalf("did %q", f.log)
	}
	// At login, no page: the login item's `run` waits for setup in its status icon
	// (setupwait.go); without systemd starting it, the autostart entry spawns it.
	f.log = nil
	e := linuxEnv(t, "")
	if err := f.starter(e).start(context.Background(), true); err != nil || slices.Contains(f.log, "first-run") ||
		!slices.Contains(f.log, "spawn "+e.Exe+" run") {
		t.Fatal(err, f.log)
	}
}

func TestInstallOnAComputerNotSetUpOpensTheSetupPage(t *testing.T) {
	for _, c := range []struct {
		noStart, filesOnly, enrolled bool
		start, setup                 bool
	}{
		{enrolled: true, start: true},                   // set up: start it, as before
		{enrolled: false, setup: true},                  // not set up: the page, which starts it after
		{noStart: true, enrolled: false},                // --no-start: neither
		{noStart: true, enrolled: true},                 //
		{filesOnly: true, enrolled: false, start: true}, // --files-only: Plan starts nothing anyway
	} {
		start, setup := installStarts(c.noStart, c.filesOnly, c.enrolled)
		if start != c.start || setup != c.setup {
			t.Errorf("%+v: start %v setup %v", c, start, setup)
		}
	}
}

// The .deb's system copy, first launched by this user, sets the user up first (Q5).
func TestFirstLaunchOfAPackagedCopyInstalls(t *testing.T) {
	f := &fakeStart{setUp: true, comesUp: true}
	e := linuxEnv(t, "/usr/lib/jarvis-agent/jarvis-agent")
	if err := f.starter(e).start(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.log, "install") {
		t.Fatalf("no install: %q", f.log)
	}
	// Not set up either: install, then the first-run page.
	f = &fakeStart{}
	_ = f.starter(e).start(context.Background(), false)
	if i, j := slices.Index(f.log, "install"), slices.Index(f.log, "first-run"); i < 0 || j < i {
		t.Fatalf("did %q", f.log)
	}
}

func TestExitCodes(t *testing.T) {
	ns := notSetUp(fmt.Errorf("load: %w", node.ErrNotEnrolled))
	t.Setenv("XPC_SERVICE_NAME", "")
	if exitCode(ns) != exitNotSetUp || exitCode(errors.New("boom")) != 1 {
		t.Fatal(exitCode(ns))
	}
	t.Setenv("XPC_SERVICE_NAME", "net.jarvisautomation.agent")
	if exitCode(ns) != 0 {
		t.Fatal("under its LaunchAgent, not set up must be a clean exit (KeepAlive)")
	}
	if notSetUp(errors.New("other")).Error() != "other" {
		t.Fatal("other errors pass through")
	}
}

func TestStopSaysWhatHappened(t *testing.T) {
	var out strings.Builder
	stopped := 0
	stop := func() error { stopped++; return nil }
	if err := stopAgent(&out, func() bool { return true }, stop); err != nil {
		t.Fatal(err)
	}
	if stopped != 1 || !strings.Contains(out.String(), "Stopped Jarvis Automation Agent") ||
		!strings.Contains(out.String(), "next login") {
		t.Fatalf("stopped %d, said %q", stopped, out.String())
	}
	// Not running: still stops (a restart systemd scheduled after a crash), and says so.
	out.Reset()
	if err := stopAgent(&out, func() bool { return false }, stop); err != nil {
		t.Fatal(err)
	}
	if stopped != 2 || !strings.Contains(out.String(), "isn't running") {
		t.Fatalf("stopped %d, said %q", stopped, out.String())
	}
	stuck := errors.New("the running agent didn't stop")
	if err := stopAgent(&out, func() bool { return true }, func() error { return stuck }); !errors.Is(err, stuck) {
		t.Fatal(err)
	}
}
