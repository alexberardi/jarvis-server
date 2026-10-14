package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/settings"
	"github.com/alexberardi/jarvis-server/internal/agent/voice"
)

// fakeVoice scripts a voiceSwitch: what loading the stack does, and each pipeline run, which
// lasts until its context ends (as voice.Serve does).
type fakeVoice struct {
	mu        sync.Mutex
	loads     int
	loadErr   error
	serving   int // pipelines running now
	runs      int
	saved     []bool
	shown     []bool
	idle      int // indicator told CueIdle
	started   chan struct{}
	stoppedCh chan struct{}
}

func newFakeVoice() *fakeVoice {
	return &fakeVoice{started: make(chan struct{}, 8), stoppedCh: make(chan struct{}, 8)}
}

func (f *fakeVoice) SetState(c voice.Cue, _ time.Duration) {
	if c == voice.CueIdle {
		f.mu.Lock()
		f.idle++
		f.mu.Unlock()
	}
}
func (f *fakeVoice) Level(float32) {}

func (f *fakeVoice) sw(on bool) *voiceSwitch {
	return &voiceSwitch{
		log:       slog.New(slog.DiscardHandler),
		on:        on,
		connected: make(chan struct{}),
		load: func(ctx context.Context) (*voiceStack, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.loads++
			if f.loadErr != nil {
				return nil, f.loadErr
			}
			return &voiceStack{close: func() {}}, nil
		},
		serve: func(ctx context.Context, vs *voiceStack, cc *node.CC) {
			f.mu.Lock()
			f.serving++
			f.runs++
			if vs.indicator == nil {
				panic("indicators not attached")
			}
			f.mu.Unlock()
			f.started <- struct{}{}
			<-ctx.Done()
			f.mu.Lock()
			f.serving--
			f.mu.Unlock()
			f.stoppedCh <- struct{}{}
		},
		persist: func(on bool) error { f.mu.Lock(); f.saved = append(f.saved, on); f.mu.Unlock(); return nil },
		changed: func(on bool) { f.mu.Lock(); f.shown = append(f.shown, on); f.mu.Unlock() },
	}
}

func (f *fakeVoice) state() (loads, serving, runs, idle int, saved, shown []bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.loads, f.serving, f.runs, f.idle, append([]bool(nil), f.saved...), append([]bool(nil), f.shown...)
}

func recv(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(20 * time.Second): // a hang guard; returns at once on a value
		t.Fatalf("timed out waiting for %s", what)
	}
}

// The toggle starts and stops the pipeline in the running agent, loads the wake model once, and
// saves each choice; nothing listens before the agent has connected to jarvisd.
func TestVoiceSwitchLive(t *testing.T) {
	f := newFakeVoice()
	sw := f.sw(false)
	sw.addIndicator(f)
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan struct{})
	go func() { sw.Serve(ctx, &node.CC{}); close(served) }()

	sw.Set(true)
	recv(t, f.started, "the pipeline to start")
	if !sw.On() {
		t.Fatal("not on after Set(true)")
	}
	sw.Set(false) // returns once the pipeline has stopped
	if _, serving, _, idle, _, _ := f.state(); serving != 0 || idle != 1 {
		t.Fatalf("after Set(false): %d pipelines running, island hidden %d times", serving, idle)
	}
	sw.Set(true)
	recv(t, f.started, "the pipeline to start again")
	sw.Set(true) // already on: no second pipeline
	cancel()
	<-served
	loads, serving, runs, _, saved, shown := f.state()
	if loads != 1 || serving != 0 || runs != 2 {
		t.Fatalf("loads %d, running %d, runs %d", loads, serving, runs)
	}
	if !equalBools(saved, []bool{true, false, true, true}) || !equalBools(shown, []bool{true, false, true, true}) {
		t.Fatalf("saved %v, shown %v", saved, shown)
	}
}

// On at start: listening begins once connected; before that, Set only records the choice.
func TestVoiceSwitchStartsOnConnect(t *testing.T) {
	f := newFakeVoice()
	sw := f.sw(true)
	sw.addIndicator(f)
	sw.Set(false)
	sw.Set(true) // not connected: nothing runs yet
	if _, _, runs, _, _, _ := f.state(); runs != 0 {
		t.Fatalf("%d runs before connecting", runs)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan struct{})
	go func() { sw.Serve(ctx, &node.CC{}); close(served) }()
	recv(t, f.started, "the pipeline to start on connect")
	cancel()
	<-served
	if _, serving, _, _, _, _ := f.state(); serving != 0 {
		t.Fatal("pipeline still running after the agent stopped")
	}
}

// A stack that can't load (no microphone, models unreachable) turns the toggle back off for this
// run, keeps the user's saved choice, and the next Set tries again.
func TestVoiceSwitchLoadFailure(t *testing.T) {
	f := newFakeVoice()
	f.loadErr = errors.New("no microphone")
	sw := f.sw(false)
	sw.addIndicator(f)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sw.Set(true) // recorded; Serve starts it
	go sw.Serve(ctx, &node.CC{})
	<-sw.connected
	sw.wait()
	if sw.On() {
		t.Fatal("still on after the stack failed to load")
	}
	loads, _, runs, _, saved, shown := f.state()
	if loads != 1 || runs != 0 || !equalBools(saved, []bool{true}) || !equalBools(shown, []bool{true, false}) {
		t.Fatalf("loads %d runs %d saved %v shown %v", loads, runs, saved, shown)
	}
	if !strings.Contains(sw.lastErr(), "no microphone") {
		t.Fatalf("lastErr %q", sw.lastErr())
	}
}

func equalBools(a, b []bool) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestWebUIURL(t *testing.T) {
	for in, want := range map[string]string{
		"http://192.0.2.10:7710":     "http://192.0.2.10:7710",
		"https://jarvis.example/":    "https://jarvis.example/",
		"":                           "",
		"file:///etc/passwd":         "",
		"javascript:alert(1)":        "",
		"http://user:pw@192.0.2.10/": "",
		"http:///no-host":            "",
	} {
		if got := webUIURL(node.Services{svcWebUI: in}); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

// The toggle saves agent.json's voice and leaves the other choices alone.
func TestSaveVoiceKeepsOtherSettings(t *testing.T) {
	p := filepath.Join(t.TempDir(), "agent.json")
	if err := os.WriteFile(p, []byte(`{"island": false, "confirm_dictation": "submit"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := saveVoice(p, false); err != nil {
		t.Fatal(err)
	}
	s, _, err := settings.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if s.Voice == nil || *s.Voice || s.Island == nil || *s.Island || s.ConfirmDictation == nil || *s.ConfirmDictation != "submit" {
		t.Fatalf("after saving voice off: %+v", s)
	}
	if !voiceByDefault {
		t.Fatal("desktop installs listen by default (user, 2026-10-13)")
	}
	if got := listeningState(false); !strings.Contains(got, "Jarvis Automation menu") {
		t.Fatalf("off points at %q, not the menu", got)
	}
}
