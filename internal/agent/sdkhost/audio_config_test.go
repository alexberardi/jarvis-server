package sdkhost

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"
)

func TestAudioConfigReportsSaveAndMixerFailures(t *testing.T) {
	a := &Audio{Path: filepath.Join(t.TempDir(), "missing", "volume.json"), Run: func(context.Context, string, ...string) (string, error) { return "", nil }}
	if err := a.Configure(context.Background(), map[string]any{"volume_percent": 30.0}); err == nil {
		t.Fatal("unpersisted volume confirmed")
	}
	a.Path = filepath.Join(t.TempDir(), "volume.json")
	a.Run = func(context.Context, string, ...string) (string, error) { return "", errors.New("unavailable") }
	if err := a.Configure(context.Background(), map[string]any{"volume_percent": 30.0}); err == nil {
		t.Fatal("failed mixer confirmed")
	}
	a.Run = func(context.Context, string, ...string) (string, error) { return "", nil }
	if err := a.Configure(context.Background(), map[string]any{"volume_percent": 40.0, "is_muted": true}); err != nil {
		t.Fatal(err)
	}
	if v, _ := a.Volume(context.Background()); v != 40 || !a.Muted() {
		t.Fatal(v, a.Muted())
	}
}

func TestAudioConfigPositiveVolumeUnmutesUnlessExplicitlyMuted(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "volume only", true: "explicit mute"}[explicit], func(t *testing.T) {
			var calls [][]string
			a := &Audio{Path: filepath.Join(t.TempDir(), "volume.json"), Run: func(_ context.Context, command string, args ...string) (string, error) {
				calls = append(calls, append([]string{command}, args...))
				return "", nil
			}}
			ctx := context.Background()
			if !a.SetMuted(ctx, true) {
				t.Fatal("could not seed muted state")
			}
			calls = nil
			patch := map[string]any{"volume_percent": 40.0}
			if explicit {
				patch["is_muted"] = true
			}
			if err := a.Configure(ctx, patch); err != nil {
				t.Fatal(err)
			}
			if a.Muted() != explicit {
				t.Fatalf("muted = %v, want %v", a.Muted(), explicit)
			}
			flag := "0"
			if explicit {
				flag = "1"
			}
			if !slices.ContainsFunc(calls, func(args []string) bool {
				return slices.Equal(args, []string{"pactl", "set-sink-mute", "@DEFAULT_SINK@", flag})
			}) {
				t.Fatal("mute preference was not applied to the mixer")
			}
		})
	}
}

func TestAudioConfigRetriesUnmuteAfterMixerFailure(t *testing.T) {
	a := &Audio{Path: filepath.Join(t.TempDir(), "volume.json"), Run: func(context.Context, string, ...string) (string, error) { return "", nil }}
	ctx := context.Background()
	if !a.SetMuted(ctx, true) {
		t.Fatal("could not seed muted state")
	}
	a.Run = func(context.Context, string, ...string) (string, error) { return "", errors.New("mixer unavailable") }
	patch := map[string]any{"volume_percent": 40.0}
	if err := a.Configure(ctx, patch); err == nil {
		t.Fatal("confirmed an unavailable mixer")
	}
	unmuted := false
	a.Run = func(_ context.Context, cmd string, args ...string) (string, error) {
		if cmd == "pactl" && slices.Equal(args, []string{"set-sink-mute", "@DEFAULT_SINK@", "0"}) {
			unmuted = true
		}
		return "", nil
	}
	if err := a.Configure(ctx, patch); err != nil || !unmuted {
		t.Fatalf("retry did not unmute the speaker: unmuted=%v err=%v", unmuted, err)
	}
}
