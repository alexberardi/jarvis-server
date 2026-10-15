package main

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// The bug behind "no pill on start": install and start ask for the pill the moment the control
// socket answers, before run has its island. It must show once the island attaches.
func TestRunningPillAskedBeforeTheIsland(t *testing.T) {
	var p runningPill
	p.Show()
	p.Show() // twice before: still one pill
	n := 0
	p.Attach(func() { n++ })
	if n != 1 {
		t.Fatalf("pill shown %d times on attach, want 1", n)
	}
	p.Show()
	if n != 2 {
		t.Fatalf("after attach: shown %d times, want 2", n)
	}
}

func TestRunningPillNotAskedOrNoIsland(t *testing.T) {
	var p runningPill
	n := 0
	p.Attach(func() { n++ })
	if n != 0 {
		t.Fatal("shown on attach without being asked")
	}
	var none runningPill
	none.Show()
	none.Attach(nil) // --no-overlay, or no display: nothing to show, no panic
	none.Show()
}

func TestAppEventsShowThePill(t *testing.T) {
	got := make(chan struct{}, 4)
	var p runningPill
	p.Attach(func() { got <- struct{}{} })
	ev := appEvents(&p, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ev.Reopen()
	<-got
	for _, u := range []string{"jarvis-agent://start", "jarvis-agent:start", "jarvis-agent://start/"} {
		ev.OpenURL(u)
		select {
		case <-got:
		default:
			t.Fatalf("%s: no pill", u)
		}
	}
	for _, u := range []string{"jarvis-agent://start?x=1", "jarvis-agent://quit", "https://example.com"} {
		ev.OpenURL(u)
		select {
		case <-got:
			t.Fatalf("%s: showed the pill", u)
		default:
		}
	}
}

// Signed out: only node.json goes; agent.json and apps.json stay; the setup page opens; and the
// exit status is "not set up", which no supervisor restarts (decision 2026-10-14).
func TestSignedOutKeepsSettingsAndOpensSetup(t *testing.T) {
	t.Setenv("XPC_SERVICE_NAME", "")
	dir := t.TempDir()
	for _, f := range []string{"node.json", "agent.json", "apps.json"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	opened := 0
	err := signedOutToSetup(fmt.Errorf("run: %w", node.ErrSignedOut), filepath.Join(dir, "node.json"), "n1", slog.New(slog.DiscardHandler), func() { opened++ })
	if code := exitCode(err); code != exitNotSetUp {
		t.Fatalf("exit %d (%v), want %d", code, err, exitNotSetUp)
	}
	if opened != 1 {
		t.Fatalf("setup page opened %d times", opened)
	}
	if _, err := os.Stat(filepath.Join(dir, "node.json")); !os.IsNotExist(err) {
		t.Fatalf("node.json still there: %v", err)
	}
	for _, f := range []string{"agent.json", "apps.json"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatalf("%s gone: %v", f, err)
		}
	}
	// Already gone: still fine.
	if err := signedOutToSetup(node.ErrSignedOut, filepath.Join(dir, "node.json"), "n1", slog.New(slog.DiscardHandler), nil); exitCode(err) != exitNotSetUp {
		t.Fatalf("second sign-out: %v", err)
	}
	// Anything else passes through untouched.
	other := errors.New("boom")
	if err := signedOutToSetup(other, filepath.Join(dir, "agent.json"), "n1", slog.New(slog.DiscardHandler), func() { opened++ }); err != other || opened != 1 {
		t.Fatalf("other error: %v, opened %d", err, opened)
	}
	if err := signedOutToSetup(nil, filepath.Join(dir, "agent.json"), "n1", slog.New(slog.DiscardHandler), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "agent.json")); err != nil {
		t.Fatal("agent.json removed on a clean exit")
	}
}

// Signed out because this computer was enrolled again (jarvisd retired the old node): the new
// node's credentials stay, no setup page opens, and the exit is an ordinary failure, so the
// supervisor restarts the agent on them.
func TestSignedOutByANewerEnrolmentKeepsItsCredentials(t *testing.T) {
	t.Setenv("XPC_SERVICE_NAME", "")
	path := filepath.Join(t.TempDir(), "node.json")
	if err := node.SaveCreds(path, node.Creds{ConfigURL: "http://cfg", NodeID: "new", NodeKey: "k"}); err != nil {
		t.Fatal(err)
	}
	opened := 0
	err := signedOutToSetup(fmt.Errorf("run: %w", node.ErrSignedOut), path, "old", slog.New(slog.DiscardHandler), func() { opened++ })
	if err == nil || exitCode(err) == exitNotSetUp || exitCode(err) == 0 || opened != 0 {
		t.Fatalf("err=%v exit=%d opened=%d", err, exitCode(err), opened)
	}
	if c, err := node.LoadCreds(path); err != nil || c.NodeID != "new" {
		t.Fatalf("the new credentials were touched: %+v %v", c, err)
	}
	// The file still holds this run's node: the ordinary sign-out.
	if err := signedOutToSetup(node.ErrSignedOut, path, "new", slog.New(slog.DiscardHandler), func() { opened++ }); exitCode(err) != exitNotSetUp || opened != 1 {
		t.Fatalf("own sign-out: %v opened=%d", err, opened)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("node.json still there: %v", err)
	}
}
