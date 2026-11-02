package sdkhost

import (
	"context"
	"errors"
	"path/filepath"
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
