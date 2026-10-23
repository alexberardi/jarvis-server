package main

import (
	"context"
	"errors"
	"fmt"
	"os"
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

func darwinEnv(t *testing.T, exe string) install.Env {
	return install.Env{GOOS: "darwin", Home: t.TempDir(), UID: 501, Exe: exe,
		SMAppService: true, Getenv: func(string) string { return "" }}
}

// #483: the old Downloads copy kept launching after an update was installed in /Applications.
// Started while an equal-or-newer copy is installed there, it hands the start to that copy and
// does nothing else here (no install, no setup page, no kickstart of the stale login item).
func TestStartHandsOverToTheInstalledCopy(t *testing.T) {
	const installed = "/Applications/Jarvis Automation Agent.app/Contents/MacOS/jarvis-agent"
	t.Setenv(handedOverEnv, "") // the handover sets it for the spawned copy
	t.Setenv("XPC_SERVICE_NAME", "")
	f := &fakeStart{setUp: true}
	st := f.starter(darwinEnv(t, "/Users/u/Downloads/old/Jarvis Automation Agent.app/Contents/MacOS/jarvis-agent"))
	st.handover = func() (install.Handover, string) { return install.HandOverToInstalled, installed }
	st.adopt = func() error { t.Fatal("adopted the login item from the stale copy"); return nil }
	if err := st.start(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	want := []string{"send show-running", "spawn " + installed + " start"}
	if !slices.Equal(f.log, want) {
		t.Fatalf("did %q\nwant %q", f.log, want)
	}
	// At login the spawned copy is told it is a login start.
	f.log = nil
	if err := st.start(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	want = []string{"send ping", "spawn " + installed + " start --login"}
	if !slices.Equal(f.log, want) {
		t.Fatalf("did %q\nwant %q", f.log, want)
	}
}

// The copy a handover started never hands over again, so two copies taken for each other (a
// path through a symlink) can't hand the start around forever. But only the second handover is
// suppressed: the installed copy a stale one spawns still adopts the login item, or the stale
// bundle would own it forever (#483). The dirs and version func are fakes: the test never reads
// the real /Applications or execs a binary.
func TestHandoverDecisionAfterAHandover(t *testing.T) {
	old := version
	version = "v1.0.0"
	t.Cleanup(func() { version = old })
	apps := t.TempDir()
	installedExe := filepath.Join(apps, "Jarvis Automation Agent.app", "Contents", "MacOS", "jarvis-agent")
	if err := os.MkdirAll(filepath.Dir(installedExe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(installedExe, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(t.TempDir(), "Downloads", "old", "Jarvis Automation Agent.app", "Contents", "MacOS", "jarvis-agent")
	for _, c := range []struct {
		name    string
		exe     string
		want    install.Handover
		wantExe string
	}{
		// The spawned installed copy, not started by launchd, re-points the login item.
		{"the installed copy still adopts the login item", installedExe, install.AdoptLoginItem, installedExe},
		// A stale copy that would hand over (a newer copy is installed) runs here instead.
		{"a stale copy never hands over twice", stale, install.RunHere, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(handedOverEnv, "1")
			t.Setenv("XPC_SERVICE_NAME", "")
			d, exe := handoverDecision(darwinEnv(t, c.exe), []string{apps}, func(string) string { return "v2.0.0" })
			if d != c.want || exe != c.wantExe {
				t.Fatalf("decided %v %q, want %v %q", d, exe, c.want, c.wantExe)
			}
			if v, ok := os.LookupEnv(handedOverEnv); ok {
				t.Fatalf("%s=%q is left for this copy's own children", handedOverEnv, v)
			}
		})
	}
}

// The installed copy opened by hand takes the login item over from whatever bundle holds it,
// then starts as usual.
func TestStartFromApplicationsAdoptsTheLoginItem(t *testing.T) {
	f := &fakeStart{setUp: true, comesUp: true}
	e := darwinEnv(t, "/Applications/Jarvis Automation Agent.app/Contents/MacOS/jarvis-agent")
	// Installed for this user: the LaunchAgent plist exists.
	plist := filepath.Join(e.Home, "Library", "LaunchAgents", "net.jarvisautomation.agent.plist")
	if err := os.MkdirAll(filepath.Dir(plist), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plist, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	st := f.starter(e)
	st.handover = func() (install.Handover, string) { return install.AdoptLoginItem, e.Exe }
	st.adopt = func() error { f.log = append(f.log, "adopt"); return nil }
	if err := st.start(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	want := []string{"send show-running", "adopt",
		"run launchctl kickstart gui/501/net.jarvisautomation.agent", "spawn " + e.Exe + " run", "wait show-running"}
	if !slices.Equal(f.log, want) {
		t.Fatalf("did %q\nwant %q", f.log, want)
	}
}

// A copy that would adopt but was never installed for this user installs instead (the
// first-launch flow registers the login item from this bundle anyway).
func TestStartNotInstalledInstallsInsteadOfAdopting(t *testing.T) {
	f := &fakeStart{setUp: true, comesUp: true}
	e := darwinEnv(t, "/Applications/Jarvis Automation Agent.app/Contents/MacOS/jarvis-agent")
	st := f.starter(e)
	st.handover = func() (install.Handover, string) { return install.AdoptLoginItem, e.Exe }
	st.adopt = func() error { f.log = append(f.log, "adopt"); return nil }
	if err := st.start(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(f.log, "adopt") || !f.installed {
		t.Fatalf("did %q, want install and no adopt", f.log)
	}
}
