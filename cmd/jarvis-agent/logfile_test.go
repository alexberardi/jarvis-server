package main

import (
	"path/filepath"
	"testing"
)

func TestLaunchdLogTarget(t *testing.T) {
	home := filepath.Join("Users", "u")
	want := filepath.Join(home, "Library", "Logs", "jarvis-agent.log")
	if got := launchdLogTarget("net.jarvisautomation.agent", true, home); got != want {
		t.Fatalf("SMAppService job: %q, want %q", got, want)
	}
	for _, c := range []struct {
		service string
		null    bool
	}{
		{"net.jarvisautomation.agent", false},       // the plain LaunchAgent gave it the file
		{"net.jarvisautomation.agent.dev", true},    // agent-dev-run.sh's foreground job has its own log
		{"", true},                                  // not launchd's
		{"application.com.apple.Terminal.1", false}, // a terminal
	} {
		if got := launchdLogTarget(c.service, c.null, home); got != "" {
			t.Errorf("%q null=%v: redirected to %q", c.service, c.null, got)
		}
	}
}
