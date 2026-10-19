package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/voice"
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

// Music ducking runs only on a Pi install: a dev box forced into --profile pi (node-sim, the
// voice lane) gets none, so the user's own players are never stopped. The mic gain follows the
// music only with the HAT.
func TestPiMusicOnlyOnAPi(t *testing.T) {
	yes, no := func() bool { return true }, func() bool { return false }
	marker := func(p string) bool { return p == piMarker }
	none := func(string) bool { return false }
	if d := newPiMusic(no, none, nil); d != nil {
		t.Fatal("ducking on a box that is no Pi")
	}
	if d := newPiMusic(no, marker, nil); d == nil || d.Gain != nil {
		t.Fatalf("Pi install without the HAT: %+v, want ducking without a gain", d)
	}
	if d := newPiMusic(yes, none, nil); d == nil || d.Gain == nil {
		t.Fatalf("Pi with the HAT: %+v, want ducking with its gain", d)
	}
}
