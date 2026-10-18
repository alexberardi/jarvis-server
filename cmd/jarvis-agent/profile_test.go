package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/hat"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/voice"
)

func TestResolveProfile(t *testing.T) {
	for _, tc := range []struct {
		flag     string
		detected bool
		marker   bool
		want     string
	}{
		{"auto", false, false, "desktop"},
		{"", false, false, "desktop"},
		{"auto", true, false, "pi"},
		{"auto", false, true, "pi"},
		{"desktop", true, true, "desktop"},
		{"pi", false, false, "pi"},
	} {
		exists := func(p string) bool { return tc.marker && p == piMarker }
		p, err := resolveProfile(tc.flag, func() bool { return tc.detected }, exists)
		if err != nil || p.name != tc.want {
			t.Errorf("%q detected=%v marker=%v: %q, %v; want %q", tc.flag, tc.detected, tc.marker, p.name, err, tc.want)
		}
	}
	if _, err := resolveProfile("laptop", func() bool { return false }, func(string) bool { return false }); err == nil {
		t.Error("an unknown profile was accepted")
	}
}

// The heartbeat body each profile sends (§8 Q6): a Pi says install_mode "tarball", runtime "go"
// and no node_kind, so jarvisd doesn't treat it as a computer (cc/signout.go desktop()).
func TestProfileHeartbeatBody(t *testing.T) {
	beat := func(p profile) map[string]any {
		var body map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			_, _ = io.WriteString(w, `{}`)
		}))
		defer srv.Close()
		b := node.Beat{Version: "v1", InstallMode: p.installMode, NodeKind: p.nodeKind, Busy: true}
		if err := node.NewCC(srv.URL, node.Creds{NodeID: "n1", NodeKey: "k"}).Heartbeat(context.Background(), b); err != nil {
			t.Fatal(err)
		}
		return body
	}
	pi := beat(piProfile)
	vi := pi["version_info"].(map[string]any)
	if _, ok := pi["node_kind"]; ok || vi["install_mode"] != "tarball" || vi["runtime"] != "go" {
		t.Errorf("pi heartbeat: %v", pi)
	}
	desk := beat(desktopProfile)
	vi = desk["version_info"].(map[string]any)
	if desk["node_kind"] != "desktop" || vi["install_mode"] != "desktop-agent" || vi["runtime"] != "go" {
		t.Errorf("desktop heartbeat: %v", desk)
	}
}

// The Pi profile's toolset: no desktop, screen, dictation or browser tools; the clock stays.
func TestPiToolset(t *testing.T) {
	tf := addToolFlags(flag.NewFlagSet("run", flag.ContinueOnError))
	tf.pi = true
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	_, tools, closeTools, err := tf.build(context.Background(), log, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTools()
	var names []string
	for _, d := range tools.Available(context.Background()) {
		names = append(names, d.Name)
	}
	for _, n := range names {
		for _, prefix := range []string{"desktop_", "screen_", "dictat", "browser_"} {
			if strings.HasPrefix(n, prefix) {
				t.Errorf("pi has %s: %v", n, names)
			}
		}
	}
	if !slices.Contains(names, "get_current_time") {
		t.Errorf("no clock: %v", names)
	}
	if tf.origins == nil {
		t.Error("origins not made (learnServer and listen fill them)")
	}
}

// The Pi's node_config: the HAT as opened, and the voice and LED settings, under the Python
// node's keys.
func TestPiNodeConfig(t *testing.T) {
	vf := &voiceFlags{threshold: 0.6, noChimes: true, followUp: 4, followSilence: 0.5, bargeIn: true}
	hf := &hatFlags{mode: "auto", brightness: 40}
	got := piNodeConfig(vf, hf, nil, false)()
	want := map[string]any{
		"wake_word_threshold": 0.6, "wake_ack_audio_enabled": false,
		"follow_up_listen_seconds": 4.0, "follow_up_silence_duration": 0.5, "barge_in_enabled": true,
		"led_enabled": true, "led_brightness_percent": 40,
		"hardware": map[string]any{"hat_detected": false, "led_chain_available": false, "button_available": false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("no HAT:\n got %v\nwant %v", got, want)
	}
	// Detected, but its LEDs couldn't be opened (or --hat off).
	hw := piNodeConfig(vf, &hatFlags{mode: "off"}, nil, true)()
	if h := hw["hardware"].(map[string]any); h["hat_detected"] != true || h["led_chain_available"] != false || hw["led_enabled"] != false {
		t.Errorf("detected, off: %v", hw)
	}
	// LEDs open and the button line held.
	hd := &hatDevices{leds: hat.NewLEDs(nopStrip{}, slog.New(slog.NewTextHandler(io.Discard, nil))), closeLine: func() error { return nil }}
	defer hd.leds.Close()
	on := piNodeConfig(vf, hf, hd, true)()["hardware"]
	if !reflect.DeepEqual(on, map[string]any{"hat_detected": true, "led_chain_available": true, "button_available": true}) {
		t.Errorf("HAT open: %v", on)
	}
	// The button line busy (the Python node holds it).
	hd.closeLine = nil
	if h := piNodeConfig(vf, hf, hd, true)()["hardware"].(map[string]any); h["button_available"] != false || h["led_chain_available"] != true {
		t.Errorf("button busy: %v", h)
	}
}

func TestOnlyAPiSpeaksTheVoiceProfileVerbs(t *testing.T) {
	if v := voiceProfileVerbs(desktopProfile, voice.NewMic(), slog.Default()); v != nil {
		t.Fatalf("desktop registers %v: its speaker is the signed-in user (#143)", slices.Collect(maps.Keys(v)))
	}
	v := voiceProfileVerbs(piProfile, voice.NewMic(), slog.Default())
	if got := slices.Sorted(maps.Keys(v)); !slices.Equal(got, []string{"enroll_voice", "verify_voice"}) {
		t.Fatalf("pi verbs = %v", got)
	}
}
