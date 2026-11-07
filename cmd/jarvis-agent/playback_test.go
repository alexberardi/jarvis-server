package main

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/playback"
)

func TestDisplayState(t *testing.T) {
	if displayState(map[string]any{"active": false}) != nil {
		t.Fatal("inactive display counted as playing")
	}
	if displayState(map[string]any{"active": true, "controllable": false}) != nil {
		t.Fatal("a player it can't control counted as playing")
	}
	st := displayState(map[string]any{"active": true, "controllable": true, "paused": true, "position": 12.5, "duration": 90.0})
	if st == nil || st.State != "paused" || *st.Position != 12.5 || *st.Duration != 90 || len(st.Actions) != 3 {
		t.Fatalf("state = %+v", st)
	}
	if st := displayState(map[string]any{"active": true, "controllable": true, "paused": false}); st.State != "playing" {
		t.Fatalf("state = %+v", st)
	}
}

func TestPlaybackReporterTiming(t *testing.T) {
	clock := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	p := newPlaybackReporter(func() *node.CC { return nil }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	p.now = func() time.Time { return clock }
	if _, ok := p.due(); ok {
		t.Fatal("sent with nothing to say")
	}
	playing := &playback.Now{Source: "pkg:radio", State: playback.State{State: "playing"}}
	p.update(playing)
	if n, ok := p.due(); !ok || n != playing {
		t.Fatalf("first change not sent: %v %v", n, ok)
	}
	// A burst of changes inside a second: held, then the latest goes.
	p.update(&playback.Now{Source: "pkg:radio", State: playback.State{State: "paused"}})
	if _, ok := p.due(); ok {
		t.Fatal("sent twice within a second")
	}
	clock = clock.Add(time.Second)
	if n, ok := p.due(); !ok || n.State.State != "paused" {
		t.Fatalf("latest change not sent: %v %v", n, ok)
	}
	// No change: resent every 10 s while something plays, so jarvisd keeps it (30 s TTL).
	clock = clock.Add(9 * time.Second)
	if _, ok := p.due(); ok {
		t.Fatal("resent too soon")
	}
	clock = clock.Add(time.Second)
	if _, ok := p.due(); !ok {
		t.Fatal("not resent after 10 s")
	}
	// Stopped: nil goes once, then nothing more.
	clock = clock.Add(time.Second)
	p.update(nil)
	if n, ok := p.due(); !ok || n != nil {
		t.Fatalf("stop not sent: %v %v", n, ok)
	}
	clock = clock.Add(time.Minute)
	if _, ok := p.due(); ok {
		t.Fatal("kept sending after the stop")
	}
}

type countingSource struct{ controls []map[string]any }

func (*countingSource) ID() string   { return "pkg:radio" }
func (*countingSource) Name() string { return "Radio" }
func (*countingSource) Status(context.Context) (*playback.State, error) {
	return &playback.State{State: "playing", Actions: []string{"volume"}}, nil
}
func (c *countingSource) Control(_ context.Context, _ string, args map[string]any) (*playback.State, error) {
	c.controls = append(c.controls, args)
	return &playback.State{State: "playing", Actions: []string{"volume"}}, nil
}

func TestPlaybackVerbDropsExpiredCommands(t *testing.T) {
	clock := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	src := &countingSource{}
	hub := &playback.Hub{Sources: func() []playback.Source { return []playback.Source{src} }, Now: func() time.Time { return clock }}
	hub.Tick(context.Background())
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	rep := newPlaybackReporter(func() *node.CC { return nil }, log)
	verb := playbackVerb(hub, rep, func() time.Time { return clock }, log)

	verb(context.Background(), nil, map[string]any{"action": "volume", "volume": 150.0, "expires_at": clock.Add(time.Second).Format(time.RFC3339)})
	if len(src.controls) != 1 || src.controls[0]["volume"] != 100 {
		t.Fatalf("controls = %v", src.controls)
	}
	verb(context.Background(), nil, map[string]any{"action": "volume", "volume": 10.0, "expires_at": clock.Add(-time.Second).Format(time.RFC3339)})
	if len(src.controls) != 1 {
		t.Fatalf("an expired control ran: %v", src.controls)
	}
}
