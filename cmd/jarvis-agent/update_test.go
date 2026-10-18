package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/selfupdate"
)

// handOver on the Pi node: `update --finish` goes to a transient unit of the node user's
// manager (systemd-run --user). When that fails it is an error, never a detached process: the
// system unit's restart stops everything in its cgroup, so the installer would die with the
// agent it restarts.
func TestHandOverOnThePi(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("systemd")
	}
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	spawned := filepath.Join(dir, "spawned")
	exe := filepath.Join(dir, "jarvis-agent")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\necho \"$@\" >"+spawned+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	sr := filepath.Join(dir, "systemd-run")
	t.Setenv("PATH", dir+":/usr/bin:/bin")
	t.Setenv("INVOCATION_ID", "")
	u := &selfupdate.Updater{Paths: selfupdate.Paths{Dir: filepath.Join(dir, "updates")},
		Target: selfupdate.Target{GOOS: "linux", Exe: exe, Pi: true}}
	log := slog.New(slog.DiscardHandler)

	if err := os.WriteFile(sr, []byte("#!/bin/sh\necho \"$@\" >"+calls+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := handOver(u, log); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(calls)
	if got := string(b); !strings.HasPrefix(got, "--user ") || !strings.Contains(got, " -- "+exe+" update --finish --state-dir "+u.Paths.Dir) {
		t.Fatalf("systemd-run %q", got)
	}

	if err := os.WriteFile(sr, []byte("#!/bin/sh\necho 'Failed to connect to bus' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := handOver(u, log); err == nil {
		t.Fatal("handed over with systemd-run failing")
	}
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(spawned); err == nil {
		t.Fatal("the installer was started detached")
	}
}
