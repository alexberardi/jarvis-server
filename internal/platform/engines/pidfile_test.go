//go:build darwin || linux

package engines

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// TestSupervisorHelper is a stand-in jarvisd: it supervises one fake engine with a pid dir
// and runs until killed. The engine ignores SIGPIPE, as llama-server does, so it survives
// writing to the dead supervisor's pipes.
func TestSupervisorHelper(t *testing.T) {
	dir := os.Getenv("ENGINES_SUPERVISOR_PIDDIR")
	if dir == "" {
		t.Skip("helper process")
	}
	s, err := New(Spec{
		Name: "orphan",
		Path: os.Args[0],
		Args: []string{"-test.run=^TestHelperProcess$"},
		Env: map[string]string{"ENGINES_HELPER": "1", "HELPER_IGNORE_PIPE": "1",
			"HELPER_STARTS": filepath.Join(dir, "..", "starts"), "ENGINES_SUPERVISOR_PIDDIR": ""},
		PIDDir: dir,
	}, quietLog())
	if err != nil {
		panic(err)
	}
	if err := s.Start(); err != nil {
		panic(err)
	}
	for {
		time.Sleep(time.Hour)
	}
}

// alive reports whether pid is running. A zombie counts as gone: an orphan's new parent (init
// in a container) may never reap it.
func alive(pid int) bool {
	_, _, err := procIdentity(pid)
	return err == nil
}

func readPIDFile(t *testing.T, path string) (pidFile, bool) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return pidFile{}, false
	}
	var pf pidFile
	if err := json.Unmarshal(b, &pf); err != nil {
		t.Fatalf("pid file %s: %v", b, err)
	}
	return pf, true
}

func TestPIDFileFollowsTheEngine(t *testing.T) {
	t.Parallel()
	spec := Spec{
		Name:   "fake",
		Path:   os.Args[0],
		Args:   []string{"-test.run=^TestHelperProcess$"},
		Env:    map[string]string{"ENGINES_HELPER": "1"},
		PIDDir: filepath.Join(t.TempDir(), "run"),
	}
	s := start(t, spec)
	pf, ok := readPIDFile(t, pidPath(spec.PIDDir, spec.Name))
	if !ok || pf.PID != s.Status().PID || pf.Exe == "" || pf.Start == "" {
		t.Fatalf("pid file = %+v, %v; engine pid %d", pf, ok, s.Status().PID)
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pidPath(spec.PIDDir, spec.Name)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pid file left after Stop: %v", err)
	}
}

// The bug this guards: jarvisd SIGKILLed on macOS left llama-server running. Sweep at the next
// start must kill it. On Linux Pdeathsig may already have, and Sweep just tidies up.
func TestSweepKillsEngineOfKilledSupervisor(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "run")
	jarvisd := exec.Command(os.Args[0], "-test.run=^TestSupervisorHelper$")
	jarvisd.Env = append(os.Environ(), "ENGINES_SUPERVISOR_PIDDIR="+dir)
	if err := jarvisd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = jarvisd.Process.Kill(); _ = jarvisd.Wait() })
	var pf pidFile
	eventually(t, "engine pid file", func() bool {
		var ok bool
		pf, ok = readPIDFile(t, pidPath(dir, "orphan"))
		return ok
	})
	t.Cleanup(func() { _ = syscall.Kill(-pf.PID, syscall.SIGKILL) })
	eventually(t, "engine running", func() bool {
		_, err := os.Stat(filepath.Join(dir, "..", "starts"))
		return err == nil
	})
	_ = jarvisd.Process.Kill()
	_ = jarvisd.Wait()
	if runtime.GOOS == "darwin" {
		time.Sleep(100 * time.Millisecond)
		if !alive(pf.PID) {
			t.Fatal("engine died with its supervisor; this test no longer shows the leak")
		}
	}

	got := Sweep(dir, quietLog())
	if runtime.GOOS == "darwin" && got != 1 {
		t.Fatalf("Sweep killed %d engines, want 1", got)
	}
	eventually(t, "orphaned engine gone", func() bool { return !alive(pf.PID) })
	if _, err := os.Stat(pidPath(dir, "orphan")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pid file left after Sweep: %v", err)
	}
}

// A pid file must not get an unrelated process killed: the PID may have been reused.
func TestSweepSparesOtherProcesses(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	other := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
	other.Env = []string{"ENGINES_HELPER=1", "HELPER_SLEEP=1"}
	other.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := other.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Process.Kill(); _ = other.Wait() })
	var exe, start string
	eventually(t, "process identity", func() bool {
		var err error
		exe, start, err = procIdentity(other.Process.Pid)
		return err == nil
	})
	gone := exec.Command(os.Args[0], "-test.run=^$")
	if err := gone.Run(); err != nil {
		t.Fatal(err)
	}
	files := map[string]pidFile{
		"other-exe":   {PID: other.Process.Pid, Exe: "/not/the/engine", Start: start},
		"other-start": {PID: other.Process.Pid, Exe: exe, Start: start + "1"},
		"dead":        {PID: gone.Process.Pid, Exe: exe, Start: start},
	}
	for name, pf := range files {
		if err := writePIDFile(dir, name, pf); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(pidPath(dir, "garbage"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := Sweep(dir, quietLog()); got != 0 {
		t.Fatalf("Sweep killed %d, want 0", got)
	}
	if !alive(other.Process.Pid) {
		t.Fatal("Sweep killed a process whose identity did not match")
	}
	left, _ := filepath.Glob(filepath.Join(dir, "*"))
	if len(left) != 0 {
		t.Fatalf("pid files left: %v", left)
	}
}
