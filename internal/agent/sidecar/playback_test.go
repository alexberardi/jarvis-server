package sidecar

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// musicPython is python(t) with an SDK that has IJarvisMusicCommand (0.16.0).
func musicPython(t *testing.T) string {
	py := python(t)
	if err := exec.Command(py, "-c", "import jarvis_command_sdk.music").Run(); err != nil {
		t.Skipf("%s has no jarvis_command_sdk.music (SDK < 0.16.0): %v", py, err)
	}
	return py
}

func state(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("state %s: %v", raw, err)
	}
	return m
}

func TestSidecarPlaybackSource(t *testing.T) {
	ctx := context.Background()
	sc, err := Start(ctx, musicPython(t), "testdata/music", slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()

	if got := sc.MusicCommands(); len(got) != 1 || got[0] != (MusicCommand{Name: "radio", Binary: "mpv"}) {
		t.Fatalf("MusicCommands = %+v", got)
	}
	// takes_over_playback_binary follows playback_binary, so takeover keeps working.
	if sc.player["radio"] != "mpv" {
		t.Fatalf("player = %v", sc.player)
	}

	// Nothing plays yet: a null state, no artwork, no reports.
	if st, err := sc.PlaybackStatus(ctx, "radio"); err != nil || st != nil {
		t.Fatalf("idle status = %s, %v", st, err)
	}
	if b, mime, err := sc.PlaybackArtwork(ctx, "radio"); err != nil || b != nil || mime != "" {
		t.Fatalf("idle artwork = %q %q %v", b, mime, err)
	}
	if r, err := sc.PlaybackReports(ctx); err != nil || len(r) != 0 {
		t.Fatalf("reports = %+v, %v", r, err)
	}

	// run() starts playing and calls report(): queued until drained, then gone.
	if out, ok := sc.Call(ctx, "radio", map[string]any{}, node.Request{}); !ok || out["message"] != "Playing Jazz FM" {
		t.Fatalf("call = %v, %v", out, ok)
	}
	reports, err := sc.PlaybackReports(ctx)
	if err != nil || len(reports) != 1 || reports[0].Command != "radio" {
		t.Fatalf("reports = %+v, %v", reports, err)
	}
	if m := state(t, reports[0].State); m["state"] != "playing" || m["media_title"] != "Jazz FM" {
		t.Fatalf("reported state = %v", m)
	}
	if r, _ := sc.PlaybackReports(ctx); len(r) != 0 {
		t.Fatalf("reports after drain = %+v", r)
	}

	st, err := sc.PlaybackStatus(ctx, "radio")
	if err != nil {
		t.Fatal(err)
	}
	if m := state(t, st); m["state"] != "playing" || m["volume"] != 50.0 {
		t.Fatalf("status = %v", m)
	}
	if b, mime, err := sc.PlaybackArtwork(ctx, "radio"); err != nil || string(b) != "\xff\xd8\xffjpeg" || mime != "image/jpeg" {
		t.Fatalf("artwork = %q %q %v", b, mime, err)
	}

	// A control returns the state read back afterwards; its arguments reach the command.
	st, err = sc.PlaybackControl(ctx, "radio", "volume", map[string]any{"volume": 80})
	if err != nil || state(t, st)["volume"] != 80.0 {
		t.Fatalf("volume = %s, %v", st, err)
	}
	st, err = sc.PlaybackControl(ctx, "radio", "pause", nil)
	if err != nil || state(t, st)["state"] != "paused" {
		t.Fatalf("pause = %s, %v", st, err)
	}

	// The command raising, and a command that isn't a music command, are errors, not a dead sidecar.
	if _, err := sc.PlaybackControl(ctx, "radio", "next", nil); err == nil || !strings.Contains(err.Error(), "radio can't next") {
		t.Fatalf("next = %v", err)
	}
	if _, err := sc.PlaybackStatus(ctx, "nope"); err == nil || !strings.Contains(err.Error(), "not a music command") {
		t.Fatalf("unknown command = %v", err)
	}
	if st, err := sc.PlaybackStatus(ctx, "radio"); err != nil || state(t, st)["state"] != "paused" {
		t.Fatalf("status after errors = %s, %v", st, err)
	}
}

func TestLiveWithoutSidecarHasNoPlayback(t *testing.T) {
	var l Live
	ctx := context.Background()
	if l.MusicCommands() != nil {
		t.Fatal("music commands without a sidecar")
	}
	if r, err := l.PlaybackReports(ctx); r != nil || err != nil {
		t.Fatalf("reports = %v, %v", r, err)
	}
	if _, err := l.PlaybackStatus(ctx, "radio"); err != ErrNoPackages {
		t.Fatalf("status = %v", err)
	}
}
