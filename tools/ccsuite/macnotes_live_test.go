//go:build darwin

package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/macax"
)

// TestMacSessionLive runs, on this Mac, the checks a desktop-mac case's bring-up makes before
// it starts anything, and prints what it found:
//
//	JARVIS_CCSUITE_MAC_LIVE=1 go test ./tools/ccsuite/ -run MacSessionLive -v
//
// It starts no session, no agent and no app, and reads no note; the probes never prompt. On a
// Mac without the grants it must say which to turn on, which is the point: a case that cannot
// run says so in one line instead of failing opaquely ten minutes later.
func TestMacSessionLive(t *testing.T) {
	if os.Getenv("JARVIS_CCSUITE_MAC_LIVE") != "1" {
		t.Skip("set JARVIS_CCSUITE_MAC_LIVE=1 to probe this Mac's session and grants")
	}
	t.Logf("Accessibility %v, Screen Recording %v, Automation for Notes %s",
		macax.Trusted(), macax.ScreenTrusted(), macax.AutomationStatus("com.apple.Notes"))
	var log bytes.Buffer
	e := &Env{Dir: t.TempDir(), Log: &log}
	d := &desktop{kind: "mac", rt: t.TempDir()}
	err := e.macSession(context.Background(), d)
	t.Logf("macSession: %v\n%s", err, log.String())
	if err == nil {
		if !d.sessionAlive() {
			t.Error("the Mac's own session should always count as alive")
		}
		if len(d.env) == 0 {
			t.Error("the session left the agent no environment")
		}
		return
	}
	// The one thing to assert off a granted host: the failure names what to do about it.
	if !strings.Contains(err.Error(), "System Settings") && !strings.Contains(err.Error(), "run ccsuite from a terminal") {
		t.Errorf("bring-up failed without saying what to turn on: %v", err)
	}
}
