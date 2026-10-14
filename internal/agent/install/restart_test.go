package install

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// restartSys is fakeSys plus a control socket: quit records itself and can fail; launchd's
// kickstart -k answers as loaded or not.
func restartSys(f *fakeSys, loaded bool, quitErr error, log *[]string) Sys {
	s := f.sys("")
	run := s.Run
	s.Run = func(ctx context.Context, args ...string) error {
		*log = append(*log, "run")
		if len(args) > 2 && args[0] == "launchctl" && args[1] == "kickstart" && !loaded {
			f.cmds = append(f.cmds, strings.Join(args, " "))
			return errors.New("no such job")
		}
		return run(ctx, args...)
	}
	s.Quit = func() error { *log = append(*log, "quit"); return quitErr }
	return s
}

func TestRestartLinuxThroughSystemd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix paths")
	}
	home := t.TempDir()
	e := Env{GOOS: "linux", Home: home, Exe: filepath.Join(home, "jarvis-agent")}
	if err := writeFile(e.linux().unit, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := fakeSys{active: true}
	var log []string
	if err := Restart(context.Background(), e, restartSys(&f, true, nil, &log)); err != nil {
		t.Fatal(err)
	}
	if log[0] != "quit" || !slices.Contains(f.cmds, "systemctl --user restart jarvis-agent.service") || len(f.spawned) != 0 {
		t.Fatalf("log %q cmds %q spawned %q", log, f.cmds, f.spawned)
	}
	// No graphical session (a plain compositor): quit, then a detached run.
	f = fakeSys{}
	log = nil
	if err := Restart(context.Background(), e, restartSys(&f, true, nil, &log)); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(f.cmds, "systemctl --user restart jarvis-agent.service") || len(f.spawned) != 1 {
		t.Fatalf("cmds %q spawned %q", f.cmds, f.spawned)
	}
}

func TestRestartDarwinThroughLaunchd(t *testing.T) {
	e := Env{GOOS: "darwin", Home: t.TempDir(), UID: 501, Exe: "/Applications/Jarvis Automation Agent.app/Contents/MacOS/jarvis-agent"}
	var f fakeSys
	var log []string
	if err := Restart(context.Background(), e, restartSys(&f, true, nil, &log)); err != nil {
		t.Fatal(err)
	}
	if log[0] != "quit" || !slices.Equal(f.cmds, []string{"launchctl kickstart -k gui/501/net.jarvisautomation.agent"}) || len(f.spawned) != 0 {
		t.Fatalf("log %q cmds %q spawned %q", log, f.cmds, f.spawned)
	}
	// Not under launchd: a detached run of this copy.
	f = fakeSys{}
	log = nil
	if err := Restart(context.Background(), e, restartSys(&f, false, nil, &log)); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.spawned, []string{e.Exe + " run"}) {
		t.Fatalf("spawned %q", f.spawned)
	}
}

func TestRestartWindowsQuitsThenRuns(t *testing.T) {
	e := Env{GOOS: "windows", Home: t.TempDir(), Exe: `C:\Program Files\Jarvis Automation Agent\jarvis-agent.exe`}
	var f fakeSys
	var log []string
	if err := Restart(context.Background(), e, restartSys(&f, false, nil, &log)); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(log, []string{"quit"}) || !slices.Equal(f.spawned, []string{e.Exe + " run"}) {
		t.Fatalf("log %q spawned %q", log, f.spawned)
	}
	// An agent that won't quit, with nothing to kill it: say so, start nothing.
	f = fakeSys{}
	log = nil
	stuck := errors.New("the running agent didn't stop")
	if err := Restart(context.Background(), e, restartSys(&f, false, stuck, &log)); !errors.Is(err, stuck) || len(f.spawned) != 0 {
		t.Fatalf("err %v spawned %q", err, f.spawned)
	}
}
