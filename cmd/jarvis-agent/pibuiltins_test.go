package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/hat"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/sdkhost"
	"github.com/alexberardi/jarvis-server/internal/agent/voice"
	"github.com/alexberardi/jarvis-server/internal/audio/device"
)

// The Pi's host keeps its timers and volume in the state dir, and gets command-center once
// the runner connects, ahead of the voice pipeline.
func TestPiHostWiring(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newPiHost(ctx, dir, voice.NewMic(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := h.Answer(ctx, "timer.set", json.RawMessage(`{"seconds":600,"label":"tea"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "timers.json")); err != nil {
		t.Fatal(err)
	}
	if h.Say("hello") != true { // queued; no pipeline listens, so it is dropped after a while
		t.Fatal("say")
	}

	r := &node.Runner{}
	var order []string
	r.OnConnected = func(context.Context, *node.CC) { order = append(order, "voice") }
	connectHost(r, h)
	r.OnConnected(ctx, node.NewCC("http://127.0.0.1:1", node.Creds{}))
	if _, err := h.Answer(ctx, "inbox.post", json.RawMessage(`{"title":"x"}`)); err != nil || len(order) != 1 {
		t.Fatalf("connected: %v, %v", err, order)
	}
	v, _ := h.Answer(ctx, "inbox.post", json.RawMessage(`{"title":"x"}`))
	if v == "no_cc_url" {
		t.Fatal("the host has no command-center after connecting")
	}
	connectHost(r, nil) // the desktop: unchanged
}

// The Pi's alerts on the HAT: the middle LED purple while an alert would be said (news doesn't
// light it), and the button's short press reads every pending one; nothing heard (no pipeline
// listening) keeps them and flashes red.
func TestAlertsOnTheHat(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	now := time.Now()
	q := &sdkhost.Alerts{}
	leds := hat.NewLEDs(nopStrip{}, log)
	defer leds.Close()
	q.Watch(alertLED(leds))

	q.Add(sdkhost.Alert{ID: "n", Title: "News", Summary: "Headlines", Priority: 1, CreatedAt: now, ExpiresAt: now.Add(time.Hour)})
	if got := leds.Current(); got != hat.Normal {
		t.Errorf("news: LEDs %q", got)
	}
	q.Add(sdkhost.Alert{ID: "r", Title: "Reminder: oven", Summary: "Reminder: oven", Priority: 3, CreatedAt: now, ExpiresAt: now.Add(time.Hour)})
	if got := leds.Current(); got != hat.Alert {
		t.Errorf("a reminder: LEDs %q", got)
	}

	said := make(chan string, 10)
	a := &sdkhost.Announcer{Alerts: q, Log: log, Say: func(_ context.Context, text string) error { said <- text; return nil }}
	pressed := make(chan struct{})
	b := &hat.Button{LEDs: leds, OnShortPress: func() { readAlerts(ctx, a, leds, log); close(pressed) }}
	b.Edge(true)
	time.Sleep(hat.Debounce + 10*time.Millisecond)
	b.Edge(false)
	<-pressed
	if got := []string{<-said, <-said}; got[0] != "Reminder: oven" || got[1] != "Headlines" {
		t.Errorf("the press said %v", got)
	}
	if q.Count() != 0 || leds.Current() != hat.Normal {
		t.Errorf("after the press: %d queued, LEDs %q", q.Count(), leds.Current())
	}

	// No pipeline: sayNow fails at once as unheard, the alert stays, the LEDs flash the error.
	a.Say = sayNow(voice.NewMic())
	q.Add(sdkhost.Alert{ID: "r2", Title: "Reminder: tea", Summary: "Reminder: tea", Priority: 3, CreatedAt: now, ExpiresAt: now.Add(time.Hour)})
	readAlerts(ctx, a, leds, log)
	if q.Count() != 1 || leds.Current() != hat.Error {
		t.Errorf("unheard: %d queued, LEDs %q", q.Count(), leds.Current())
	}
}

// Music ducking runs only on a Pi install: a dev box forced into --profile pi (node-sim, the
// voice lane) gets none, so the user's own players are never stopped. The mic gain follows the
// music, and the echo canceller switches the mic, only with the HAT.
func TestPiMusicOnlyOnAPi(t *testing.T) {
	yes, no := func() bool { return true }, func() bool { return false }
	marker := func(p string) bool { return p == piMarker }
	none := func(string) bool { return false }
	if d := newPiMusic(no, none, nil); d != nil {
		t.Fatal("ducking on a box that is no Pi")
	}
	if d := newPiMusic(no, marker, nil); d == nil || d.Gain != nil || d.EC != nil || micSwitch(d) != nil {
		t.Fatalf("Pi install without the HAT: %+v, want ducking without a gain or echo cancel", d)
	}
	d := newPiMusic(yes, none, nil)
	if d == nil || d.Gain == nil || d.EC == nil || micSwitch(d) == nil {
		t.Fatalf("Pi with the HAT: %+v, want ducking with its gain and echo cancel", d)
	}
	if d.EC.Mic != "dsnoopmic" || d.EC.Sink != "jarvis_output" {
		t.Fatalf("echo cancel on %s → %s, want install-pi.sh's dsnoopmic → jarvis_output", d.EC.Mic, d.EC.Sink)
	}
	if micSwitch(nil) != nil {
		t.Fatal("a switch without music")
	}
	var o voice.Options
	withPiMusic(&o, d)
	if sw, _ := d.EC.Capture.(*device.Switch); o.Music != d || o.Switch == nil || o.Switch != sw {
		t.Fatalf("voice options %+v: want the ducker and the echo canceller's switch as the mic", o)
	}
	o = voice.Options{}
	withPiMusic(&o, nil)
	if o.Music != nil || o.Switch != nil {
		t.Fatalf("no music: options %+v", o)
	}
}
