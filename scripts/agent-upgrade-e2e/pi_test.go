//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The -pi lane's fake systemd, without an agent: the unit is a shell script that logs each
// start, sudo allows only the sudoers lines, systemctl alone is refused, a restart stops the old
// process, Restart=always restarts a crash until the start limit, which only reset-failed
// clears, and systemd-run --user runs with only --setenv.
func TestFakeSystemd(t *testing.T) {
	work := t.TempDir()
	starts := filepath.Join(work, "starts")
	crash := filepath.Join(work, "crash")
	e := &e2e{work: work, exe: filepath.Join(work, "jarvis-agent")}
	loop := filepath.Join(work, "loop")
	script := "#!/bin/sh\necho \"$$ $*\" >>" + starts + "\n[ -e " + crash + " ] && { rm -f " + crash + "; exit 3; }\n[ -e " + loop + " ] && exit 3\nexec sleep 60\n"
	if err := os.WriteFile(e.exe, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	s := newFakeSystemd(e)
	defer s.Close()
	call := func(tool string, args ...string) toolReply {
		t.Helper()
		b, _ := json.Marshal(toolCall{Tool: tool, Args: args})
		resp, err := http.Post(s.url+"/call", "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var r toolReply
		if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	lines := func() []string {
		b, _ := os.ReadFile(starts)
		return strings.Fields(strings.ReplaceAll(string(b), " run --profile pi", ""))
	}
	alive := func(pid string) bool {
		_, err := os.Stat("/proc/" + pid)
		return err == nil
	}
	waitStarts := func(n int) {
		t.Helper()
		for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
			if len(lines()) >= n {
				return
			}
		}
		t.Fatalf("starts %q, want %d", lines(), n)
	}

	s.Start()
	waitStarts(1)
	first := lines()[0]
	for _, refused := range [][]string{
		{"sudo", "/usr/bin/systemctl", "restart", "jarvis-agent.service"}, // no -n
		{"sudo", "-n", "systemctl", "restart", "jarvis-agent.service"},    // not the sudoers path
		{"sudo", "-n", "/usr/bin/systemctl", "stop", "jarvis-agent.service"},
		{"systemctl", "restart", "jarvis-agent.service"},
		{"systemd-run", "--", "/bin/true"},
	} {
		if r := call(refused[0], refused[1:]...); r.Code == 0 {
			t.Fatalf("%q was allowed", refused)
		}
	}
	if r := call("sudo", "-n", "/usr/bin/systemctl", "restart", "jarvis-agent.service"); r.Code != 0 {
		t.Fatalf("the sudoers line was refused: %+v", r)
	}
	waitStarts(2)
	if got := lines(); len(got) != 2 || alive(first) {
		t.Fatalf("after the restart: starts %q, first alive %v", got, alive(first))
	}

	// A crash: Restart=always starts it again after RestartSec.
	if err := os.WriteFile(crash, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	call("sudo", sudoersLine...)
	waitStarts(4)

	// A crash loop: the start limit (here 6 starts in the interval, 4 of them so far) fails the
	// unit, Restart=always stops, and a restart is refused until reset-failed.
	s.mu.Lock()
	s.burst = 6
	s.mu.Unlock()
	if err := os.WriteFile(loop, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	call("sudo", sudoersLine...)
	waitStarts(6)
	for end := time.Now().Add(5 * time.Second); !s.Failed() && time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
	}
	if !s.Failed() || s.LimitHits() != 1 {
		t.Fatalf("after %d starts: failed %v, limit hits %d", len(lines()), s.Failed(), s.LimitHits())
	}
	time.Sleep(1500 * time.Millisecond) // past RestartSec: nothing restarts a failed unit
	if got := lines(); len(got) != 6 {
		t.Fatalf("a failed unit restarted: starts %q", got)
	}
	if r := call("sudo", sudoersLine...); r.Code == 0 || !strings.Contains(r.Out, "failed") {
		t.Fatalf("restarting a failed unit: %+v", r)
	}
	if err := os.Remove(loop); err != nil {
		t.Fatal(err)
	}
	if r := call("sudo", resetFailedLine...); r.Code != 0 || s.Failed() {
		t.Fatalf("reset-failed: %+v, failed %v", r, s.Failed())
	}
	if r := call("sudo", sudoersLine...); r.Code != 0 {
		t.Fatalf("restart after reset-failed: %+v", r)
	}
	waitStarts(7)

	out := filepath.Join(work, "env")
	if r := call("systemd-run", "--user", "--setenv=FOO=bar", "--", "/bin/sh", "-c", "env >"+out); r.Code != 0 {
		t.Fatalf("systemd-run --user: %+v", r)
	}
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		if b, _ := os.ReadFile(out); bytes.Contains(b, []byte("FOO=bar")) {
			if bytes.Contains(b, []byte("HOME=")) {
				t.Fatalf("the transient unit got more than --setenv:\n%s", b)
			}
			return
		}
	}
	t.Fatal("the transient unit didn't run")
}
