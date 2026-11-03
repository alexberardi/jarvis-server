package main

import (
	"context"
	"encoding/json"
	"github.com/alexberardi/jarvis-server/internal/agent/browser"
	"github.com/alexberardi/jarvis-server/internal/agent/control"
	"github.com/alexberardi/jarvis-server/internal/agent/settings"
	"github.com/alexberardi/jarvis-server/internal/agent/voice"
	"github.com/alexberardi/jarvis-server/internal/nodeconfig"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestLocalSettingsSaveAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	d, err := stoppedSettings(path, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	patch := map[string]any{"island": false}
	if voice.Supported {
		patch["wake_ack_audio_enabled"], patch["follow_up_listen_seconds"] = false, 1.5
	}
	view, err := d.edit(context.Background(), patch)
	if err != nil || view.Values["island"] != false {
		t.Fatalf("%+v %v", view, err)
	}
	d, err = stoppedSettings(path, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	view, err = d.edit(context.Background(), nil)
	if err != nil || view.Values["island"] != false || (voice.Supported && view.Values["follow_up_listen_seconds"] != 1.5) {
		t.Fatalf("%+v %v", view, err)
	}
	if _, err := d.edit(context.Background(), map[string]any{"voice_output": "wav:/tmp/private", "island": true}); err == nil {
		t.Fatal("accepted file output")
	}
	s, _, _ := settings.Load(path)
	if s.Island == nil || *s.Island {
		t.Fatal("invalid patch partly saved")
	}
	if _, err := d.edit(context.Background(), map[string]any{"k2": "secret"}); err == nil {
		t.Fatal("accepted secret")
	}
	if !voice.Supported {
		if _, ok := view.Values["wake_ack_audio_enabled"]; ok {
			t.Fatal("offered unsupported voice")
		}
		if _, err := d.edit(context.Background(), map[string]any{"wake_ack_audio_enabled": true}); err == nil {
			t.Fatal("accepted unsupported voice")
		}
	}
}

func TestLocalSettingsWaitsForStartup(t *testing.T) {
	dir, err := os.MkdirTemp("", "settings-start-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	t.Setenv(browser.ControlSocketEnv, filepath.Join(dir, "control.sock"))
	unlock, err := lockSettingsStartup()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	result := make(chan nodeconfig.View, 1)
	failures := make(chan error, 1)
	go func() {
		v, err := localSettings(context.Background(), nil)
		result <- v
		failures <- err
	}()
	select {
	case <-result:
		t.Fatal("stopped read bypassed startup lock")
	case <-time.After(50 * time.Millisecond):
	}
	ln, err := listenControl()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = control.ServeRequests(ctx, ln, func(cmd string, _ map[string]any) (string, error) {
			v := nodeconfig.View{Values: map[string]any{"from_running": true}}
			b, _ := json.Marshal(v)
			return string(b), nil
		}, "")
	}()
	defer func() { cancel(); <-done }()
	unlock()
	if v := <-result; v.Values["from_running"] != true {
		t.Fatalf("did not use newly running agent: %+v", v)
	}
	if err := <-failures; err != nil {
		t.Fatal(err)
	}
}

func TestNodeSettingsRespectExplicitFlags(t *testing.T) {
	overrides := overriddenNodeConfig(map[string]bool{"no-chimes": true, "no-leds": true, "maintenance-restart-at": true})
	for _, k := range []string{"wake_ack_audio_enabled", "led_enabled", "maintenance_restart_at_time"} {
		if !slices.Contains(overrides, k) {
			t.Fatalf("missing override %s", k)
		}
	}
	d, err := stoppedSettings(filepath.Join(t.TempDir(), "agent.json"), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	d.locked = map[string]bool{"island": true}
	if _, err := d.edit(context.Background(), map[string]any{"island": false}); err == nil {
		t.Fatal("changed command-line setting")
	}
}

func TestDesktopAudioFieldsMatchBackendCapabilities(t *testing.T) {
	for _, goos := range []string{"linux", "darwin", "windows"} {
		for _, supported := range []bool{false, true} {
			fields := desktopFieldsFor(goos, supported)
			for _, key := range []string{"voice_input", "voice_output"} {
				found := slices.ContainsFunc(fields, func(f nodeconfig.Field) bool { return f.Key == key })
				if found != (goos == "linux" && supported) {
					t.Fatalf("%s voice=%v exposed %s: %v", goos, supported, key, found)
				}
			}
		}
	}
}
