package main

import (
	"io"
	"log/slog"
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
