package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/browser"
	"github.com/alexberardi/jarvis-server/internal/agent/computer"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
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
		func() error { return nil }, a, computer.NewSwitch(false, a), nil)
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

// `jarvis-agent computer on|off` set the running agent's switch over the control socket; off
// stops a task going on, as "Stop Jarvis" does.
func TestComputerOnOff(t *testing.T) {
	dir, err := os.MkdirTemp("", "jco")
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
	sw := computer.NewSwitch(false, a)
	var saved []bool
	sw.Persist = func(on bool) error { saved = append(saved, on); return nil }
	ctx, cancel := context.WithCancel(context.Background())
	wait := serveControl(ctx, ln, slog.New(slog.DiscardHandler), &runningPill{}, func() {}, func() *voiceSwitch { return nil },
		func() error { return nil }, a, sw, nil)
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
	if got := run("on"); got != "Computer control is on: Jarvis may use this computer." || !sw.On() {
		t.Fatalf("on: %q (switch %v)", got, sw.On())
	}
	stopped := 0
	a.OnStop(func() { stopped++ })
	a.Set(computer.Controlling, "Ordering the blue mug")
	if got := run("off"); got != "Computer control is off: Jarvis won't use this computer." || sw.On() || stopped != 1 {
		t.Fatalf("off: %q (switch %v, %d stops)", got, sw.On(), stopped)
	}
	if k, _ := a.Status(); k != computer.Idle {
		t.Fatalf("after off: %v", k)
	}
	if len(saved) != 2 || !saved[0] || saved[1] {
		t.Fatalf("saved %v", saved)
	}
}

// The status icon's "Computer control" follows the switch; choosing it turns the switch over,
// and off stops the task going on.
func TestTrayComputerControl(t *testing.T) {
	a := &computer.Activity{}
	sw := computer.NewSwitch(false, a)
	ic := &trayIcon{log: slog.New(slog.DiscardHandler), sw: newFakeVoice().sw(false), activity: a, computer: sw}
	if s := ic.state(); !s.ComputerAvailable || s.Computer {
		t.Fatalf("default: %+v", s)
	}
	ic.setComputerFromMenu(true)
	if s := ic.state(); !s.Computer || !sw.On() {
		t.Fatalf("after on: %+v", s)
	}
	stopped := false
	a.OnStop(func() { stopped = true })
	a.Set(computer.Controlling, "Ordering the blue mug")
	ic.setComputerFromMenu(false)
	if s := ic.state(); s.Computer || s.Activity != "" || !stopped {
		t.Fatalf("after off mid-task: %+v, stopped %v", s, stopped)
	}
	// A Pi (no switch) shows no item.
	ic.computer = nil
	if s := ic.state(); s.ComputerAvailable {
		t.Fatalf("no switch: %+v", s)
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

// The recorder's trace says masked, never a value, for a secret field.
func TestPrintRecordEvent(t *testing.T) {
	var b strings.Builder
	printRecordEvent(&b, browser.RecordEvent{Seq: 3, Kind: "input", URL: "https://bank.test/login", Masked: true,
		Element: &browser.RecordElement{Role: "textbox", Name: "Password", Tag: "input", Type: "password", Selector: "#password"}})
	printRecordEvent(&b, browser.RecordEvent{Seq: 4, Kind: "key", Key: "Enter", URL: "https://bank.test/login",
		Element: &browser.RecordElement{Role: "textbox", Name: "Username", Tag: "input", Type: "text", Selector: "#username", Frame: 2}})
	want := "   3 input    textbox \"Password\" type=password sel=#password masked  https://bank.test/login\n" +
		"   4 key      Enter textbox \"Username\" type=text sel=#username frame=2  https://bank.test/login\n"
	if b.String() != want {
		t.Errorf("got\n%s\nwant\n%s", b.String(), want)
	}
}

// fakeRecSource is the browser extension's recorder.
type fakeRecSource struct{ emit func(json.RawMessage) }

func (s *fakeRecSource) Start(_ context.Context, _ string, emit func(json.RawMessage)) error {
	s.emit = emit
	return nil
}
func (s *fakeRecSource) Stop(context.Context, string) error { return nil }

// `jarvis-agent computer record start|stop` and the menu's "Record a macro…" / "Stop recording"
// record through the running agent's recorder; a stop hands the trace on for upload.
func TestComputerRecordStartStop(t *testing.T) {
	dir, err := os.MkdirTemp("", "jcr")
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
	sw := computer.NewSwitch(false, a) // recording isn't gated by computer control
	src := &fakeRecSource{}
	handed := make(chan computer.Trace, 2)
	rec := &computer.Recorder{Dir: filepath.Join(dir, "recordings"), Activity: a,
		Sources: map[string]computer.Source{computer.ModeBrowser: src}, Stopped: func(tr computer.Trace) { handed <- tr }}
	ctx, cancel := context.WithCancel(context.Background())
	wait := serveControl(ctx, ln, slog.New(slog.DiscardHandler), &runningPill{}, func() {}, func() *voiceSwitch { return nil },
		func() error { return nil }, a, sw, func() *computer.Recorder { return rec })
	defer func() { cancel(); wait() }()
	dial := func() (net.Conn, error) { return browser.Dial(path) }
	run := func(args ...string) (string, error) {
		var b bytes.Buffer
		err := computerCmd(args, &b, dial)
		return strings.TrimSpace(b.String()), err
	}
	got, err := run("record", "start")
	if err != nil || !strings.HasPrefix(got, "Recording (rec_") {
		t.Fatalf("record start: %q %v", got, err)
	}
	if k, _ := a.Status(); k != computer.Recording {
		t.Fatalf("activity %v", k)
	}
	src.emit(json.RawMessage(`{"kind":"click"}`))
	got, err = run("record", "stop")
	if err != nil || !strings.HasSuffix(got, ": 1 events, sent to Jarvis.") {
		t.Fatalf("record stop: %q %v", got, err)
	}
	if tr := <-handed; len(tr.Events) != 1 {
		t.Fatalf("handed on %+v", tr)
	}
	if _, err := run("record", "stop"); err == nil || !strings.Contains(err.Error(), "no recording") {
		t.Fatalf("stop with nothing going on: %v", err)
	}

	// The menu.
	ic := &trayIcon{log: slog.New(slog.DiscardHandler), sw: newFakeVoice().sw(false), activity: a, computer: sw, recorder: rec}
	if s := ic.state(); !s.Record {
		t.Fatalf("Record a macro… hidden: %+v", s)
	}
	ic.recordFromMenu(true)
	if rec.Current() == "" {
		t.Fatal("Record a macro… didn't start a recording")
	}
	ic.recordFromMenu(false)
	if tr := <-handed; tr.ID == "" || rec.Current() != "" {
		t.Fatalf("Stop recording: %+v, still %q", tr, rec.Current())
	}
}

// The recorder records the desktop where the OS can (macOS; Linux, where an X11 session can and
// Wayland refuses at start), and says it can't elsewhere.
func TestNewRecorderDesktopSource(t *testing.T) {
	activity := &computer.Activity{}
	rec := newRecorder(t.TempDir(), activity, nil, func() *node.CC { return nil }, slog.New(slog.DiscardHandler))
	_, has := rec.Sources[computer.ModeDesktop]
	if has != (runtime.GOOS == "darwin" || runtime.GOOS == "linux") {
		t.Fatalf("desktop source on %s: %v", runtime.GOOS, has)
	}
}
