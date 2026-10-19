package main

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/browser"
	"github.com/alexberardi/jarvis-server/internal/agent/computer"
	"github.com/alexberardi/jarvis-server/internal/agent/tray"
)

// `jarvis-agent computer status|stop` reach the running agent's Activity over the control
// socket: status says what Jarvis is doing, stop ends it.
func TestComputerStopAndStatus(t *testing.T) {
	dir, err := os.MkdirTemp("", "jcs")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "c.sock")
	ln, err := browser.Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	a := &computer.Activity{}
	stopped := 0
	a.OnStop(func() { stopped++ })
	ctx, cancel := context.WithCancel(context.Background())
	wait := serveControl(ctx, ln, slog.New(slog.DiscardHandler), &runningPill{}, func() {}, func() *voiceSwitch { return nil },
		func() error { return nil }, a)
	defer func() { cancel(); wait() }()
	dial := func() (net.Conn, error) { return browser.Dial(path) }

	run := func(args ...string) string {
		t.Helper()
		var b bytes.Buffer
		if err := computerCmd(args, &b, dial); err != nil {
			t.Fatalf("computer %v: %v", args, err)
		}
		return strings.TrimSpace(b.String())
	}
	if got := run("status"); got != "Jarvis isn't using this computer." {
		t.Fatalf("idle status: %q", got)
	}
	a.Set(computer.Controlling, "Ordering the blue mug")
	if got := run("status"); got != "Jarvis is controlling this computer: Ordering the blue mug" {
		t.Fatalf("controlling status: %q", got)
	}
	if got := run("stop"); got != "Stopped." || stopped != 1 {
		t.Fatalf("stop: %q (%d stops)", got, stopped)
	}
	if k, _ := a.Status(); k != computer.Idle {
		t.Fatalf("after stop: %v", k)
	}
	if got := run("stop"); got != "Jarvis wasn't using this computer." {
		t.Fatalf("stop while idle: %q", got)
	}
	a.Set(computer.Recording, "")
	if got := run("status"); got != "Jarvis is recording" {
		t.Fatalf("recording status: %q", got)
	}
	var b bytes.Buffer
	if err := computerCmd([]string{"dance"}, &b, dial); err == nil {
		t.Fatal("computer dance accepted")
	}
	noAgent := func() (net.Conn, error) { return nil, os.ErrNotExist }
	if err := computerCmd([]string{"status"}, &b, noAgent); err == nil || !strings.Contains(err.Error(), "isn't running") {
		t.Fatalf("no agent: %v", err)
	}
}

// The status icon follows the Activity, and its Stop item stops it.
func TestTrayFollowsTheActivity(t *testing.T) {
	a := &computer.Activity{}
	ic := &trayIcon{log: slog.New(slog.DiscardHandler), sw: newFakeVoice().sw(false), activity: a}
	if s := ic.state(); s.Activity != "" {
		t.Fatalf("idle: %+v", s)
	}
	a.Set(computer.Paused, "Ordering the blue mug")
	if s := ic.state(); s.Activity != tray.ActivityPaused {
		t.Fatalf("paused: %+v", s)
	}
	stopped := false
	a.OnStop(func() { stopped = true })
	ic.stopTaskFromMenu()
	if k, _ := a.Status(); k != computer.Idle || !stopped {
		t.Fatalf("Stop Jarvis: %v, stopped %v", k, stopped)
	}
}

func TestFakeActivity(t *testing.T) {
	a, err := fakeActivity("controlling:Ordering the blue mug")
	if err != nil || a.String() != "controlling: Ordering the blue mug" {
		t.Fatalf("got %v, %v", a, err)
	}
	if a, err := fakeActivity(""); a != nil || err != nil {
		t.Fatalf("none: got %v, %v", a, err)
	}
	if _, err := fakeActivity("idle:x"); err == nil {
		t.Fatal("idle: want an error")
	}
}
