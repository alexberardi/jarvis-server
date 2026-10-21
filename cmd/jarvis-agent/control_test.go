package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/computer"
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
	activity := &computer.Activity{}
	windows := 0
	ev := appEvents(&p, func() { windows++ }, activity, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ev.Reopen()
	<-got
	// Reopening the app (Dock, Finder, Spotlight) also shows a window: the setup page.
	if windows != 1 {
		t.Fatalf("reopen opened %d windows", windows)
	}
	for _, u := range []string{"jarvis-agent://start", "jarvis-agent:start", "jarvis-agent://start/"} {
		ev.OpenURL(u)
		select {
		case <-got:
		default:
			t.Fatalf("%s: no pill", u)
		}
	}
	if windows != 1 {
		t.Fatalf("a start link opened a window (%d)", windows)
	}
	for _, u := range []string{"jarvis-agent://start?x=1", "jarvis-agent://quit", "https://example.com"} {
		ev.OpenURL(u)
		select {
		case <-got:
			t.Fatalf("%s: showed the pill", u)
		default:
		}
	}

	// The stop hotkey stops what Jarvis is doing with the computer.
	activity.Set(computer.Controlling, "Ordering the blue mug")
	stopped := make(chan struct{})
	activity.OnStop(func() { close(stopped) })
	ev.StopHotkey()
	<-stopped
	if k, _ := activity.Status(); k != computer.Idle {
		t.Fatalf("after the stop hotkey the activity is %q", k)
	}

	// No setup page to open (not a computer): only the pill, no panic.
	appEvents(&p, nil, activity, slog.New(slog.DiscardHandler)).Reopen()
	<-got
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

// #297: whatever ends the node, run's context ends with it, so the control socket closes too
// (it kept a process with no node answering the setup page's start). A computer that was signed
// out waits for the new sign-in (signedOut) instead of exiting; a Pi still exits "not set up".
func TestNodeEndStopsTheRunAndASignedOutComputerWaits(t *testing.T) {
	t.Setenv("XPC_SERVICE_NAME", "")
	path := filepath.Join(t.TempDir(), "node.json")
	save := func() {
		if err := node.SaveCreds(path, node.Creds{ConfigURL: "http://cfg", NodeID: "n1", NodeKey: "k"}); err != nil {
			t.Fatal(err)
		}
	}
	stopped, opened := 0, 0
	end := nodeEnd{credsFile: path, nodeID: "n1", desktop: true, stop: func() { stopped++ }, openSetup: func() { opened++ }, log: slog.New(slog.DiscardHandler)}

	save()
	err := end.of(fmt.Errorf("run: %w", node.ErrSignedOut))
	var so signedOut
	if !errors.As(err, &so) || so.credsFile != path || stopped != 1 || opened != 1 {
		t.Fatalf("computer: err=%v stopped=%d opened=%d", err, stopped, opened)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("node.json still there: %v", err)
	}

	save()
	end.desktop = false
	err = end.of(node.ErrSignedOut)
	if errors.As(err, &so) || exitCode(err) != exitNotSetUp || stopped != 2 {
		t.Fatalf("pi: err=%v exit=%d stopped=%d", err, exitCode(err), stopped)
	}

	boom := errors.New("boom")
	if err := end.of(boom); err != boom || stopped != 3 {
		t.Fatalf("other end: err=%v stopped=%d", err, stopped)
	}
}

// #297: a signed-out computer's run doesn't exit: it waits, without opening the setup page a
// second time, until the page saves new credentials, then runs again in place (reexec).
func TestSignedOutRunRunsAgainOnTheNewCredentials(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "node.json")
	defer func(d time.Duration) { setupPollEvery = d }(setupPollEvery)
	setupPollEvery = 5 * time.Millisecond
	ran := make(chan struct{}, 1)
	defer func(f func(*slog.Logger) error) { reexec = f }(reexec)
	reexec = func(*slog.Logger) error { ran <- struct{}{}; return nil }

	so := signedOut{err: errNotSetUp{node.ErrSignedOut}, credsFile: path, noTray: true, log: slog.New(slog.DiscardHandler)}
	done := make(chan error, 1)
	go func() {
		done <- so.rejoin(context.Background(), func() (net.Listener, error) { return nil, errors.New("no socket in this test") })
	}()
	select {
	case err := <-done:
		t.Fatalf("returned before the sign-in: %v", err)
	case <-ran:
		t.Fatal("ran again before the sign-in")
	case <-time.After(50 * time.Millisecond):
	}
	if err := node.SaveCreds(path, node.Creds{ConfigURL: "http://cfg", NodeID: "n2", NodeKey: "k"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("didn't run again on the new credentials")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, setupOfferedMarker)); !os.IsNotExist(err) {
		t.Fatalf("the wait offered the setup page again: %v", err)
	}
}
