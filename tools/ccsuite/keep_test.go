//go:build !windows

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDown: `ccsuite down` stops every kept group whose leader is still the program it started,
// leaves a group whose id now belongs to something else alone, and removes kept.json.
func TestDown(t *testing.T) {
	start := func() *exec.Cmd {
		cmd := exec.Command("sleep", "60")
		ownGroup(cmd)
		if err := cmd.Start(); err != nil {
			t.Skip("no sleep:", err)
		}
		go cmd.Wait()
		t.Cleanup(func() { killGroup(cmd.Process.Pid, true) })
		return cmd
	}
	ours, other := start(), start()
	dir, rt := t.TempDir(), t.TempDir()
	k := kept{Root: dir, RuntimeDir: rt, Groups: []keptGroup{
		{"ccsites", ours.Process.Pid, ours.Args[0]},
		{"browser", other.Process.Pid, "/opt/chromium/chrome"}, // the id was reused by a sleep
	}}
	b, _ := json.Marshal(k)
	os.WriteFile(filepath.Join(dir, "kept.json"), b, 0o644)
	if err := downCmd([]string{"--run", dir}, io.Discard); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond) // the reaper
	if groupAlive(ours.Process.Pid) {
		t.Error("the kept group still runs")
	}
	if !groupAlive(other.Process.Pid) {
		t.Error("a group that was not ours was stopped")
	}
	for _, p := range []string{filepath.Join(dir, "kept.json"), rt} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s is still there", p)
		}
	}
	if downCmd([]string{"--run", dir}, io.Discard) == nil {
		t.Error("down without kept.json succeeded")
	}
}

// TestLeaderIs: a live leader is matched by its program's name, on Linux and macOS alike, and a
// leader that is gone (only its group left) counts as ours.
func TestLeaderIs(t *testing.T) {
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Skip("no sleep:", err)
	}
	defer cmd.Wait()
	defer cmd.Process.Kill()
	// Until the child has exec'd, its command line is empty (taken as gone): wait for it.
	for i := 0; i < 200 && !execd(cmd.Process.Pid); i++ {
		time.Sleep(5 * time.Millisecond)
	}
	if !leaderIs(cmd.Process.Pid, cmd.Args[0]) {
		t.Error("a sleep is not taken for a sleep")
	}
	if leaderIs(cmd.Process.Pid, "/opt/chromium/chrome") {
		t.Error("a sleep is taken for chrome")
	}
	cmd.Process.Kill()
	cmd.Wait()
	if !leaderIs(cmd.Process.Pid, "/opt/chromium/chrome") {
		t.Error("a leader that is gone is not taken for ours")
	}
}

// execd reports whether pid runs a program already (on macOS, always).
func execd(pid int) bool {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	return err != nil || strings.Contains(string(b), "sleep")
}
