package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/hat"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/pinode"
	"github.com/alexberardi/jarvis-server/internal/agent/sdkhost"
	"github.com/alexberardi/jarvis-server/internal/agent/settings"
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
	vf := &voiceFlags{threshold: 0.6, wakeAuto: true, noChimes: true, followUp: 4, followSilence: 0.5, bargeIn: true,
		silenceThreshold: 1000, listenSilence: 0.5, followMinAfterOnset: 0.9, followMinSpeech: 0.45}
	hf := &hatFlags{mode: "auto", brightness: 40}
	st := piState(vf, hf)
	st.Maintenance = (&maintFlags{at: "03:00", ceilingMB: -1}).settings()
	state := func() pinode.State { return st }
	defer func(f func() (int, bool)) { memTotalMB = f }(memTotalMB)
	memTotalMB = func() (int, bool) { return 4096, true }
	got := piNodeConfig(state, true, nil, nil, false)()
	want := map[string]any{
		"wake_word_threshold": 0.6, "wake_word_threshold_auto": true, "wake_ack_audio_enabled": false,
		"follow_up_listen_seconds": 4.0, "follow_up_silence_duration": 0.5, "barge_in_enabled": true,
		"silence_threshold": 1000, "silence_duration": 0.5,
		"follow_up_min_record_after_onset_secs": 0.9, "follow_up_min_speech_secs": 0.45,
		"led_enabled":      true,
		"led_idle_enabled": true, "led_brightness_percent": 40,
		// The ceiling in effect: max(320 MB, 35% of 4 GB).
		"maintenance_restart_enabled": true, "maintenance_restart_at_time": "03:00", "maintenance_restart_rss_ceiling_mb": 1433,
		"hardware": map[string]any{"hat_detected": false, "led_chain_available": false, "button_available": false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("no HAT:\n got %v\nwant %v", got, want)
	}
	// The speaker's volume and mute, as the mixer has them (#295).
	withAudio := piNodeConfig(state, true, nil, snapAudio{pct: 35, muted: true}, false)()
	if withAudio["volume_percent"] != 35 || withAudio["is_muted"] != true {
		t.Errorf("volume: %v", withAudio)
	}
	if v, ok := piNodeConfig(state, true, nil, snapAudio{err: true}, false)()["volume_percent"]; ok {
		t.Errorf("an unreadable volume reported as %v", v)
	}
	// Detected, but its LEDs couldn't be opened (or --hat off).
	off := piState(vf, &hatFlags{mode: "off"})
	hw := piNodeConfig(func() pinode.State { return off }, true, nil, nil, true)()
	if h := hw["hardware"].(map[string]any); h["hat_detected"] != true || h["led_chain_available"] != false || hw["led_enabled"] != false {
		t.Errorf("detected, off: %v", hw)
	}
	// LEDs open and the button line held.
	hd := &hatDevices{leds: hat.NewLEDs(nopStrip{}, slog.New(slog.NewTextHandler(io.Discard, nil))), closeLine: func() error { return nil }}
	defer hd.leds.Close()
	on := piNodeConfig(state, true, hd, nil, true)()["hardware"]
	if !reflect.DeepEqual(on, map[string]any{"hat_detected": true, "led_chain_available": true, "button_available": true}) {
		t.Errorf("HAT open: %v", on)
	}
	// The button line busy (the Python node holds it).
	hd.closeLine = nil
	if h := piNodeConfig(state, true, hd, nil, true)()["hardware"].(map[string]any); h["button_available"] != false || h["led_chain_available"] != true {
		t.Errorf("button busy: %v", h)
	}
	// No HAT: the ACT LED is no chain and has no button.
	act := &hatDevices{leds: hd.leds, act: true}
	if h := piNodeConfig(state, true, act, nil, false)()["hardware"]; !reflect.DeepEqual(h, map[string]any{"hat_detected": false, "led_chain_available": false, "button_available": false}) {
		t.Errorf("ACT LED: %v", h)
	}
}

type snapAudio struct {
	pct   int
	muted bool
	err   bool
}

func (snapAudio) SetVolume(context.Context, int) bool { return true }
func (snapAudio) SetMuted(context.Context, bool) bool { return true }
func (a snapAudio) Volume(context.Context) (int, error) {
	if a.err {
		return 0, errors.New("no mixer")
	}
	return a.pct, nil
}
func (a snapAudio) Muted() bool { return a.muted }

// The app's settings change what node_config reports, and --no-leds starts the LEDs dark.
func TestPiNodeReportsUpdates(t *testing.T) {
	vf := &voiceFlags{threshold: 0.4, followUp: 4, followSilence: 0.5}
	hf := &hatFlags{mode: "auto", brightness: 100, noLEDs: true}
	if piState(vf, hf).LEDEnabled {
		t.Fatal("--no-leds reported as led_enabled")
	}
	r := &node.Runner{}
	live := voice.NewLive()
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.json")
	var mixer []string
	audio := &sdkhost.Audio{Path: filepath.Join(dir, "volume.json"), Run: func(_ context.Context, name string, args ...string) (string, error) {
		mixer = append(mixer, name+" "+strings.Join(args, " "))
		return "", nil
	}}
	cfg, state := wirePiNode(r, vf, hf, &maintFlags{at: "03:00", ceilingMB: -1}, nil, live, audio, path, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if len(r.Verbs) != 3 {
		t.Fatalf("verbs %v", r.Verbs)
	}
	b, _ := json.Marshal([]map[string]any{{"command": "update_node_config", "details": map[string]any{
		"settings": map[string]any{"led_brightness_percent": 30, "wake_word_threshold": 0.7, "wake_word_threshold_auto": true,
			"silence_threshold": 1000, "silence_duration": 0.5, "volume_percent": 65,
			"maintenance_restart_at_time": "04:30", "maintenance_restart_rss_ceiling_mb": 600}}}})
	d := &node.Dispatcher{Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Verbs: map[string]func(context.Context, map[string]any){}}
	for name, v := range r.Verbs {
		d.Verbs[name] = func(ctx context.Context, det map[string]any) { v(ctx, nil, det) }
	}
	d.Handle(context.Background(), b)
	got := cfg()
	if got["led_brightness_percent"] != 30 || got["wake_word_threshold"] != 0.7 || got["wake_word_threshold_auto"] != true || got["silence_threshold"] != 1000 ||
		got["silence_duration"] != 0.5 || got["volume_percent"] != 65 || got["is_muted"] != false ||
		got["maintenance_restart_at_time"] != "04:30" || got["maintenance_restart_rss_ceiling_mb"] != 600 {
		t.Fatalf("node_config %v", got)
	}
	// The maintenance restart reads the same settings.
	if m := state().Maintenance; !m.Enabled || m.At != "04:30" || m.CeilingMB != 600 {
		t.Fatalf("maintenance %+v", m)
	}
	s, _, err := settings.Load(path)
	if err != nil || *s.LEDBrightness != 30 || *s.WakeThreshold != 0.7 || s.WakeAuto == nil || !*s.WakeAuto || *s.SilenceThreshold != 1000 || *s.ListenSilence != 0.5 {
		t.Fatalf("saved %+v %v", s, err)
	}
	// The volume reached the speaker and volume.json (applied again at start), not agent.json.
	if !slices.Contains(mixer, "pactl set-sink-volume @DEFAULT_SINK@ 65%") {
		t.Fatalf("mixer %v", mixer)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "volume.json")); err != nil || !strings.Contains(string(b), "65") {
		t.Fatalf("volume.json %s %v", b, err)
	}
}

// A Pi speaks its settings verbs and its voice-profile verbs: neither wiring drops the other's.
func TestAPiKeepsAllItsVerbs(t *testing.T) {
	r := &node.Runner{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	wirePiNode(r, &voiceFlags{threshold: 0.4}, &hatFlags{mode: "auto", brightness: 100}, &maintFlags{}, nil, voice.NewLive(), nil, "", log)
	addVerbs(r, voiceProfileVerbs(piProfile, voice.NewMic(), log))
	want := []string{"enroll_voice", "measure_ambient_noise", "preview_led_pattern", "update_node_config", "verify_voice"}
	if got := slices.Sorted(maps.Keys(r.Verbs)); !slices.Equal(got, want) {
		t.Fatalf("verbs = %v", got)
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

// What the app saved in agent.json is the pipeline's tuning at the next start (#295).
func TestVoiceSettingsReachThePipelineAtStart(t *testing.T) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	vf := addVoiceFlags(fs)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	if c, d := voiceConfig(vf), voice.DefaultConfig(); c.Endpointing.SilenceThreshold != d.Endpointing.SilenceThreshold ||
		c.Endpointing.SilenceSecs != d.Endpointing.SilenceSecs || c.FollowUp.MinAfterOnsetSecs != d.FollowUp.MinAfterOnsetSecs ||
		c.FollowUp.MinSpeechSecs != d.FollowUp.MinSpeechSecs {
		t.Fatalf("defaults changed: %+v %+v", c.Endpointing, c.FollowUp)
	}
	thr, secs, onset, speech := 1000.0, 0.5, 0.9, 0.45
	s := settings.Settings{SilenceThreshold: &thr, ListenSilence: &secs, FollowUpMinAfterOnset: &onset, FollowUpMinSpeech: &speech}
	if err := s.Apply(fs); err != nil {
		t.Fatal(err)
	}
	c := voiceConfig(vf)
	if c.Endpointing.SilenceThreshold != 1000 || c.Endpointing.SilenceSecs != 0.5 || c.FollowUp.MinAfterOnsetSecs != 0.9 || c.FollowUp.MinSpeechSecs != 0.45 {
		t.Fatalf("endpointing %+v, follow-up %+v", c.Endpointing, c.FollowUp)
	}
	if st := piState(vf, &hatFlags{}); st.SilenceThreshold != 1000 || st.SilenceSecs != 0.5 || st.FollowUpMinAfterOnset != 0.9 || st.FollowUpMinSpeech != 0.45 {
		t.Fatalf("state %+v", st)
	}
}
