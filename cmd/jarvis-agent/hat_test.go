package main

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/hat"
	"github.com/alexberardi/jarvis-server/internal/agent/voice"
)

type nopStrip struct{}

func (nopStrip) Write(b []byte) (int, error) { return len(b), nil }
func (nopStrip) Close() error                { return nil }

// TestLEDCuesFollowThePythonNode: a turn shows wake_detected, then listening, thinking, speaking,
// listening for the follow-up window, and the idle pattern after; a failure flashes red. The
// wake flash's timer fires when the test says so.
func TestLEDCuesFollowThePythonNode(t *testing.T) {
	l := hat.NewLEDs(nopStrip{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer l.Close()
	var flashEnd []func()
	c := &ledCue{leds: l, errorFor: time.Hour, after: func(d time.Duration, f func()) *time.Timer {
		if d != wakeFlash {
			t.Errorf("timer for %v, want the %v wake flash", d, wakeFlash)
		}
		flashEnd = append(flashEnd, f)
		return time.AfterFunc(time.Hour, func() {}) // never fires; the test runs f
	}}
	want := func(p string) {
		t.Helper()
		if got := l.Current(); got != p {
			t.Fatalf("showing %s, want %s", got, p)
		}
	}
	endFlash := func() {
		t.Helper()
		if len(flashEnd) == 0 {
			t.Fatal("no wake flash scheduled")
		}
		f := flashEnd[0]
		flashEnd = flashEnd[1:]
		f()
	}
	c.SetState(voice.CueListening, 0)
	want(hat.WakeDetected)
	endFlash()
	want(hat.Listening)
	c.SetState(voice.CueThinking, 0)
	want(hat.Thinking)
	c.SetState(voice.CueSpeaking, 0)
	want(hat.Speaking)
	c.SetState(voice.CueFollowUp, 8*time.Second)
	want(hat.Listening)
	c.SetState(voice.CueListening, 0) // an answer starting: no wake flash mid-turn
	want(hat.Listening)
	c.SetState(voice.CueIdle, 0)
	want(hat.Normal)

	// A thinking state set during the wake flash wins over the flash's timer.
	c.SetState(voice.CueListening, 0)
	c.SetState(voice.CueThinking, 0)
	endFlash()
	want(hat.Thinking)

	c.SetState(voice.CueError, 0)
	want(hat.Error)
	// The red clears by itself (the LEDs' preview timer, tested in package hat).
	c.errorFor = time.Millisecond
	c.SetState(voice.CueError, 0)
	deadline := time.Now().Add(10 * time.Second) // a hang guard
	for l.Current() != hat.Normal {
		if time.Now().After(deadline) {
			t.Fatalf("error never cleared: %s", l.Current())
		}
		time.Sleep(time.Millisecond)
	}
	if len(flashEnd) != 0 {
		t.Fatalf("%d unexpected wake flashes", len(flashEnd))
	}
}

func TestIndicatorsFanOut(t *testing.T) {
	vs := &voiceStack{}
	a, b := &recCue{}, &recCue{}
	vs.addIndicator(a)
	vs.addIndicator(b)
	vs.indicator.SetState(voice.CueSpeaking, 0)
	vs.indicator.Level(0.5)
	if a.cue != voice.CueSpeaking || b.cue != voice.CueSpeaking || a.level != 0.5 || b.level != 0.5 {
		t.Fatalf("not fanned out: %+v %+v", a, b)
	}
}

type recCue struct {
	cue   voice.Cue
	level float32
}

func (r *recCue) SetState(c voice.Cue, _ time.Duration) { r.cue = c }
func (r *recCue) Level(v float32)                       { r.level = v }

// fakeACT is a sysfs LED directory, as internal/agent/hat's tests build it.
func fakeACT(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, v := range map[string]string{"trigger": "none timer heartbeat [mmc0] default-on\n", "brightness": "0\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// A Pi without the HAT (or with its LEDs unusable) shows the voice state on the ACT LED: no
// button, and Close gives the LED back to its trigger. --hat off keeps it dark.
func TestOpenHatFallsBackToTheACTLED(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	oldDetected, oldStrip, oldDirs := hatDetected, openStrip, actDirs
	t.Cleanup(func() { hatDetected, openStrip, actDirs = oldDetected, oldStrip, oldDirs })
	hatDetected = func() bool { return false }
	openStrip = func(string) (hat.Strip, error) { return nil, errors.New("no SPI") }
	var dir string
	for _, mode := range []string{"auto", "on"} {
		dir = fakeACT(t) // Close leaves "mmc0" in trigger, not the kernel's bracketed list
		actDirs = []string{filepath.Join(dir, "missing"), dir}
		hd := openHat(&hatFlags{mode: mode}, nil, log)
		if hd == nil || !hd.act || hd.leds == nil || hd.button != nil {
			t.Fatalf("--hat %s without the HAT: %+v, want the ACT LED alone", mode, hd)
		}
		if err := hd.Close(); err != nil {
			t.Fatalf("--hat %s: close: %v", mode, err)
		}
		b, _ := os.ReadFile(filepath.Join(dir, "trigger"))
		if strings.TrimSpace(string(b)) != "mmc0" {
			t.Errorf("--hat %s: trigger after Close = %q, want mmc0 back", mode, b)
		}
	}
	if hd := openHat(&hatFlags{mode: "off"}, nil, log); hd != nil {
		t.Errorf("--hat off opened %+v", hd)
	}
	if hd := openHat(&hatFlags{mode: "bogus"}, nil, log); hd != nil {
		t.Errorf("--hat bogus opened %+v", hd)
	}
	actDirs = []string{filepath.Join(dir, "missing")}
	if hd := openHat(&hatFlags{mode: "auto"}, nil, log); hd != nil {
		t.Errorf("no HAT, no ACT LED: %+v", hd)
	}
}
