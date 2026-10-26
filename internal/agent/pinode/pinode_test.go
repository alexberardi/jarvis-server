package pinode

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/maintenance"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/settings"
	"github.com/alexberardi/jarvis-server/internal/agent/voice"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type fakeLEDs struct {
	mu       sync.Mutex
	previews []string
	enabled  []bool
	idleDark []bool
	bright   []int
}

func (l *fakeLEDs) Preview(name string, d time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.previews = append(l.previews, name+" "+d.String())
}
func (l *fakeLEDs) SetEnabled(on bool) { l.mu.Lock(); l.enabled = append(l.enabled, on); l.mu.Unlock() }
func (l *fakeLEDs) SetIdleDark(d bool) {
	l.mu.Lock()
	l.idleDark = append(l.idleDark, d)
	l.mu.Unlock()
}
func (l *fakeLEDs) SetBrightness(p int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.bright = append(l.bright, p)
}

type fakeVoice struct {
	cfg     voice.Config
	tunes   int
	rms     []float64
	err     error
	measure []time.Duration
}

func (v *fakeVoice) Tune(f func(*voice.Config)) { f(&v.cfg); v.tunes++ }
func (v *fakeVoice) Measure(ctx context.Context, d time.Duration) ([]float64, error) {
	v.measure = append(v.measure, d)
	if v.err == context.DeadlineExceeded {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return v.rms, v.err
}

type fakeAudio struct {
	mu     sync.Mutex
	volume []int
	muted  []bool
}

func (a *fakeAudio) SetVolume(_ context.Context, p int) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.volume = append(a.volume, p)
	return true
}
func (a *fakeAudio) SetMuted(_ context.Context, m bool) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.muted = append(a.muted, m)
	return true
}

type fakeCC struct {
	valid    map[string]bool
	verified []string
	ambient  map[string]map[string]any
}

func newFakeCC() *fakeCC {
	return &fakeCC{valid: map[string]bool{}, ambient: map[string]map[string]any{}}
}
func (f *fakeCC) Verify(_ context.Context, rid string) (bool, error) {
	f.verified = append(f.verified, rid)
	ok := f.valid[rid]
	delete(f.valid, rid)
	return ok, nil
}
func (f *fakeCC) PostToolReport(context.Context, string, any) error { return errors.New("unexpected") }
func (f *fakeCC) PostToolResult(context.Context, string, map[string]any) error {
	return errors.New("unexpected")
}
func (f *fakeCC) PostAmbientResult(_ context.Context, rid string, p map[string]any) error {
	f.ambient[rid] = p
	return nil
}

func initial() State {
	return State{WakeThreshold: 0.4, WakeAck: true, FollowUpListen: 4, FollowUpSilence: 1, LEDEnabled: true, LEDIdle: true, LEDBrightness: 100,
		SilenceThreshold: 300, SilenceSecs: 0.8, FollowUpMinAfterOnset: 0.7, FollowUpMinSpeech: 0.3,
		Maintenance: maintenance.Settings{Enabled: true, At: "03:00", CeilingMB: -1}}
}

func newNode(t *testing.T) (*Node, *fakeLEDs, *fakeVoice, string) {
	t.Helper()
	leds, v := &fakeLEDs{}, &fakeVoice{cfg: voice.DefaultConfig()}
	path := filepath.Join(t.TempDir(), settings.FileName)
	n := New(initial(), path, quiet)
	n.LEDs, n.Voice, n.Audio = leds, v, &fakeAudio{}
	return n, leds, v, path
}

// run dispatches one published command through the node dispatcher, as MQTT would.
func run(t *testing.T, n *Node, cc node.Backend, verb string, det map[string]any) {
	t.Helper()
	b, err := json.Marshal([]map[string]any{{"command": verb, "details": det}})
	if err != nil {
		t.Fatal(err)
	}
	d := &node.Dispatcher{CC: cc, Log: quiet, Verbs: map[string]func(context.Context, map[string]any){}}
	for name, v := range n.Verbs() {
		d.Verbs[name] = func(ctx context.Context, det map[string]any) { v(ctx, cc, det) }
	}
	d.Handle(context.Background(), b)
}

// --- update_node_config ---

func TestTrustAnchorKeysAreNeverAppliedOrSaved(t *testing.T) {
	safe, rejected := FilterTrustAnchors(map[string]any{
		"node_id": "evil", "api_key": "k", "command_center_url": "http://evil", "jarvis_tts_url": "x",
		"mqtt_broker": "evil", "mqtt_password": "p", "_k2_watermark": 9, "allow_updates": true,
		"wake_word_model_autodownload_enabled": true, "release_track": "dev",
		"led_brightness_percent": 50,
	})
	want := []string{"_k2_watermark", "allow_updates", "api_key", "command_center_url", "jarvis_tts_url",
		"mqtt_broker", "mqtt_password", "node_id", "release_track", "wake_word_model_autodownload_enabled"}
	if !slices.Equal(rejected, want) {
		t.Fatalf("rejected %v", rejected)
	}
	if len(safe) != 1 || safe["led_brightness_percent"] != 50 {
		t.Fatalf("safe %v", safe)
	}

	n, leds, v, path := newNode(t)
	run(t, n, newFakeCC(), "update_node_config", map[string]any{"settings": map[string]any{
		"command_center_url": "http://evil", "node_id": "evil", "allow_updates": true}})
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a rejected-only update wrote agent.json")
	}
	if len(leds.enabled)+len(leds.bright)+v.tunes != 0 {
		t.Fatal("a rejected-only update applied something")
	}
}

func TestUpdateAppliesLiveAndPersists(t *testing.T) {
	n, leds, v, path := newNode(t)
	on := true
	if err := settings.Save(path, settings.Settings{Voice: &on}); err != nil { // another key survives
		t.Fatal(err)
	}
	run(t, n, newFakeCC(), "update_node_config", map[string]any{"settings": map[string]any{
		"led_enabled":                "false",
		"led_idle_enabled":           "false",
		"led_brightness_percent":     40.0,
		"wake_word_threshold":        0.55,
		"wake_word_threshold_auto":   "true",
		"wake_ack_audio_enabled":     false,
		"follow_up_listen_seconds":   0.0,
		"follow_up_silence_duration": 0.7,
		// #295: not the Go defaults (300, 0.8, 0.7, 0.3), so a key left unapplied shows.
		"silence_threshold":                     1000.9, // Python's int()
		"silence_duration":                      0.5,
		"follow_up_min_record_after_onset_secs": 0.9,
		"follow_up_min_speech_secs":             "0.45",
		"volume_percent":                        30.0, // the mixer (sdkhost.Audio), which persists it in volume.json
		"is_muted":                              true,
		"room":                                  "kitchen",
		"api_key":                               "nope",
	}})
	a := n.Audio.(*fakeAudio)
	if !slices.Equal(a.volume, []int{30}) || !slices.Equal(a.muted, []bool{true}) {
		t.Fatalf("audio %v %v", a.volume, a.muted)
	}
	if e, f := v.cfg.Endpointing, v.cfg.FollowUp; e.SilenceThreshold != 1000 || e.SilenceSecs != 0.5 ||
		f.MinAfterOnsetSecs != 0.9 || f.MinSpeechSecs != 0.45 {
		t.Fatalf("endpointing %+v, follow-up %+v", e, f)
	}
	if !slices.Equal(leds.enabled, []bool{false}) || !slices.Equal(leds.bright, []int{40}) || !slices.Equal(leds.idleDark, []bool{true}) {
		t.Fatalf("leds %v %v %v", leds.enabled, leds.bright, leds.idleDark)
	}
	if v.cfg.WakeThreshold != 0.55 || !v.cfg.WakeThresholdAuto || v.cfg.Chimes || v.cfg.FollowUp.ListenSecs != 0 || v.cfg.FollowUp.SilenceSecs != 0.7 {
		t.Fatalf("voice cfg %+v", v.cfg)
	}
	want := State{WakeThreshold: 0.55, WakeAuto: true, WakeAck: false, FollowUpListen: 0, FollowUpSilence: 0.7, LEDEnabled: false, LEDIdle: false, LEDBrightness: 40,
		SilenceThreshold: 1000, SilenceSecs: 0.5, FollowUpMinAfterOnset: 0.9, FollowUpMinSpeech: 0.45, Maintenance: initial().Maintenance}
	if got := n.State(); got != want {
		t.Fatalf("state %+v", got)
	}
	s, unknown, err := settings.Load(path)
	if err != nil || len(unknown) != 0 {
		t.Fatal(err, unknown)
	}
	if *s.Voice != true || *s.LEDs != false || *s.LEDIdle != false || *s.LEDBrightness != 40 || *s.WakeThreshold != 0.55 || !*s.WakeAuto ||
		*s.Chimes != false || *s.FollowUp != 0 || *s.FollowUpSilence != 0.7 ||
		*s.SilenceThreshold != 1000 || *s.ListenSilence != 0.5 || *s.FollowUpMinAfterOnset != 0.9 || *s.FollowUpMinSpeech != 0.45 {
		t.Fatalf("saved %+v", s)
	}
	b, _ := os.ReadFile(path)
	for _, k := range []string{"volume", "muted", "room", "api_key"} {
		if strings.Contains(string(b), k) {
			t.Errorf("%s persisted:\n%s", k, b)
		}
	}
}

// The maintenance restart reads State at every poll: no pipeline or LED change, saved to
// agent.json.
func TestUpdateMaintenanceRestart(t *testing.T) {
	n, leds, v, path := newNode(t)
	run(t, n, newFakeCC(), "update_node_config", map[string]any{"settings": map[string]any{
		"maintenance_restart_enabled":        "false",
		"maintenance_restart_at_time":        "04:30",
		"maintenance_restart_rss_ceiling_mb": 512.0,
	}})
	want := initial()
	want.Maintenance = maintenance.Settings{Enabled: false, At: "04:30", CeilingMB: 512}
	if got := n.State(); got != want {
		t.Fatalf("state %+v", got.Maintenance)
	}
	if v.tunes != 0 || len(leds.enabled) != 0 {
		t.Fatalf("tunes %d, leds %v", v.tunes, leds.enabled)
	}
	s, _, err := settings.Load(path)
	if err != nil || s.MaintenanceRestart == nil || *s.MaintenanceRestart || s.MaintenanceRestartAt == nil || *s.MaintenanceRestartAt != "04:30" ||
		s.MaintenanceRSSCeilingMB == nil || *s.MaintenanceRSSCeilingMB != 512 {
		t.Fatalf("saved %+v %v", s, err)
	}

	// 0 turns the ceiling off (Python's ceiling_mb > 0 check); bad values are skipped.
	run(t, n, newFakeCC(), "update_node_config", map[string]any{"settings": map[string]any{"maintenance_restart_rss_ceiling_mb": 0.0}})
	run(t, n, newFakeCC(), "update_node_config", map[string]any{"settings": map[string]any{
		"maintenance_restart_at_time": "25:00", "maintenance_restart_rss_ceiling_mb": -5.0}})
	run(t, n, newFakeCC(), "update_node_config", map[string]any{"settings": map[string]any{"maintenance_restart_at_time": 3.0}})
	want.Maintenance.CeilingMB = 0
	if got := n.State(); got != want {
		t.Fatalf("state after bad values %+v", got.Maintenance)
	}
}

func TestUpdateSkipsBadValues(t *testing.T) {
	n, leds, v, path := newNode(t)
	run(t, n, newFakeCC(), "update_node_config", map[string]any{"settings": map[string]any{
		"wake_word_threshold":                   1.5,
		"led_brightness_percent":                "bright",
		"follow_up_silence_duration":            0.0,
		"led_enabled":                           1.0, // Python: str(1) == "1" is true
		"silence_threshold":                     0.5, // int() is 0
		"silence_duration":                      -1.0,
		"follow_up_min_record_after_onset_secs": 0.0,
		"follow_up_min_speech_secs":             -0.1,
		"volume_percent":                        "loud",
	}})
	if a := n.Audio.(*fakeAudio); len(a.volume) != 0 {
		t.Fatalf("volume %v", a.volume)
	}
	if v.tunes != 0 || len(leds.bright) != 0 || !slices.Equal(leds.enabled, []bool{true}) {
		t.Fatalf("tunes %d, leds %v %v", v.tunes, leds.bright, leds.enabled)
	}
	if got := n.State(); got != initial() {
		t.Fatalf("state %+v", got)
	}
	s, _, err := settings.Load(path)
	if err != nil || s.WakeThreshold != nil || s.LEDBrightness != nil || s.LEDs == nil || !*s.LEDs {
		t.Fatalf("saved %+v %v", s, err)
	}
}

func TestUpdateWithoutAHatOrVoiceStillPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), settings.FileName)
	n := New(initial(), path, quiet)
	run(t, n, newFakeCC(), "update_node_config", map[string]any{"settings": map[string]any{
		"led_brightness_percent": 120.0, "wake_word_threshold": 0.6}, "restart": true})
	s, _, err := settings.Load(path)
	if err != nil || *s.LEDBrightness != 100 || *s.WakeThreshold != 0.6 {
		t.Fatalf("saved %+v %v", s, err)
	}
	if n.State().LEDBrightness != 100 {
		t.Fatal(n.State())
	}
}

func TestVolumeIsClampedAndLeftOutOfAgentJSON(t *testing.T) {
	n, _, _, path := newNode(t)
	run(t, n, newFakeCC(), "update_node_config", map[string]any{"settings": map[string]any{
		"volume_percent": 140.0, "is_muted": "false"}})
	a := n.Audio.(*fakeAudio)
	if !slices.Equal(a.volume, []int{100}) || !slices.Equal(a.muted, []bool{false}) {
		t.Fatalf("audio %v %v", a.volume, a.muted)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a volume-only update wrote agent.json (volume.json is its file)")
	}

	// No mixer on this build: skipped, not an error.
	n = New(initial(), "", quiet)
	run(t, n, newFakeCC(), "update_node_config", map[string]any{"settings": map[string]any{"volume_percent": 50.0}})
}

func TestUpdateWithNoSettingsDoesNothing(t *testing.T) {
	n, _, v, path := newNode(t)
	run(t, n, newFakeCC(), "update_node_config", map[string]any{"restart": true})
	run(t, n, newFakeCC(), "update_node_config", map[string]any{"settings": "junk"})
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) || v.tunes != 0 {
		t.Fatal("wrote or tuned on an empty update")
	}
}

// --- preview_led_pattern ---

func TestPreviewParsesAndClampsTheDuration(t *testing.T) {
	n, leds, _, _ := newNode(t)
	for _, det := range []map[string]any{
		{"pattern": "thinking"},                                 // 3 s by default
		{"pattern": " alert ", "duration_seconds": 0.1},         // clamped up
		{"pattern": "error", "duration_seconds": 3600.0},        // clamped down
		{"pattern": "speaking", "duration_seconds": nil},        // null: the default
		{"pattern": "listening", "duration_seconds": "2"},       // Python's float("2")
		{"pattern": "muted", "duration_seconds": "long"},        // bad: the default
		{"pattern": "no_such_pattern", "duration_seconds": 1.5}, // LEDs render it off
		{"pattern": ""},
		{},
	} {
		run(t, n, newFakeCC(), "preview_led_pattern", det)
	}
	want := []string{"thinking 3s", "alert 500ms", "error 30s", "speaking 3s", "listening 2s", "muted 3s", "no_such_pattern 1.5s"}
	if !slices.Equal(leds.previews, want) {
		t.Fatalf("previews %v", leds.previews)
	}
}

func TestPreviewWithoutAHatIsANoOp(t *testing.T) {
	n := New(initial(), "", quiet)
	run(t, n, newFakeCC(), "preview_led_pattern", map[string]any{"pattern": "thinking"})
}

// --- measure_ambient_noise ---

func TestAmbientMatchesThePythonNode(t *testing.T) {
	b, err := os.ReadFile("testdata/ambient_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		RMS                []float64
		P50, P75, P95, Max float64
		Suggested          int
	}
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	near := func(a, b float64) bool { return math.Abs(a-b) <= 1e-3*max(1, math.Abs(b)) }
	for i, c := range cases {
		got := Ambient(c.RMS)
		if !near(got.P50, c.P50) || !near(got.P75, c.P75) || !near(got.P95, c.P95) || got.Max != c.Max || got.Suggested != c.Suggested {
			t.Errorf("case %d: got %+v, want p50 %v p75 %v p95 %v max %v suggested %d", i, got, c.P50, c.P75, c.P95, c.Max, c.Suggested)
		}
	}
}

func TestMeasureVerifiesThenPostsTheStats(t *testing.T) {
	n, _, v, _ := newNode(t)
	v.rms = []float64{2500, 2500, 2500, 2500}
	cc := newFakeCC()
	cc.valid["rid-1"] = true
	run(t, n, cc, "measure_ambient_noise", map[string]any{"request_id": "rid-1", "duration_seconds": 30.0})
	got := cc.ambient["rid-1"]
	want := map[string]any{"success": true, "duration_seconds": 10.0, "chunks": 4, "p50_rms": 2500.0,
		"p75_rms": 2500.0, "p95_rms": 2500.0, "max_rms": 2500.0, "suggested_silence_threshold": 3200}
	if len(got) != len(want) {
		t.Fatalf("payload %v", got)
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %v (%T), want %v", k, got[k], got[k], w)
		}
	}
	if !slices.Equal(v.measure, []time.Duration{10 * time.Second}) {
		t.Fatal(v.measure)
	}
	run(t, n, cc, "measure_ambient_noise", map[string]any{"request_id": "rid-2"})
	if _, ok := cc.ambient["rid-2"]; ok || len(v.measure) != 1 {
		t.Fatal("measured without verification")
	}
	run(t, n, cc, "measure_ambient_noise", map[string]any{})
	if len(cc.verified) != 2 {
		t.Fatal("verified a command without request_id", cc.verified)
	}
}

func TestMeasureDurationDefaultsAndClamps(t *testing.T) {
	n, _, v, _ := newNode(t)
	v.rms = []float64{100}
	cc := newFakeCC()
	for i, d := range []any{nil, 0.0, 0.2, 4.5} {
		rid := string(rune('a' + i))
		cc.valid[rid] = true
		run(t, n, cc, "measure_ambient_noise", map[string]any{"request_id": rid, "duration_seconds": d})
	}
	want := []time.Duration{3 * time.Second, 3 * time.Second, time.Second, 4500 * time.Millisecond}
	if !slices.Equal(v.measure, want) {
		t.Fatal(v.measure)
	}
}

func TestMeasureFailures(t *testing.T) {
	for _, c := range []struct {
		name  string
		voice *fakeVoice
		want  string
	}{
		{"voice off", &fakeVoice{err: voice.ErrNotListening}, "audio_bus_unavailable"},
		{"no pipeline at all", nil, "audio_bus_unavailable"},
		{"silent mic", &fakeVoice{}, "no_audio_captured"},
		{"busy", &fakeVoice{err: context.DeadlineExceeded}, "capture_failed: the voice pipeline stayed busy"},
		{"mic error", &fakeVoice{err: errors.New("microphone: gone")}, "capture_failed: microphone: gone"},
	} {
		t.Run(c.name, func(t *testing.T) {
			n := New(initial(), "", quiet)
			n.MeasureWait = 10 * time.Millisecond
			if c.voice != nil {
				n.Voice = c.voice
			}
			cc := newFakeCC()
			cc.valid["r"] = true
			run(t, n, cc, "measure_ambient_noise", map[string]any{"request_id": "r", "duration_seconds": 1.0})
			got := cc.ambient["r"]
			if got["success"] != false || got["error"] != c.want || len(got) != 2 {
				t.Fatalf("payload %v", got)
			}
		})
	}
}

// The app may send a longer follow-up window than the node allows (#296): it applies and
// saves the cap, 3 s, and says so in the log.
func TestUpdateClampsTheFollowUpWindow(t *testing.T) {
	for _, c := range []struct {
		in, want float64
		clamped  bool
	}{{8, 3, true}, {3.5, 3, true}, {2.5, 2.5, false}, {0, 0, false}} {
		var logs strings.Builder
		n, _, v, path := newNode(t)
		n.log = slog.New(slog.NewTextHandler(&logs, nil))
		run(t, n, newFakeCC(), "update_node_config", map[string]any{"settings": map[string]any{
			"follow_up_listen_seconds": c.in,
		}})
		if v.cfg.FollowUp.ListenSecs != c.want || n.State().FollowUpListen != c.want {
			t.Errorf("%v: voice %v, state %v, want %v", c.in, v.cfg.FollowUp.ListenSecs, n.State().FollowUpListen, c.want)
		}
		s, _, err := settings.Load(path)
		if err != nil || s.FollowUp == nil || *s.FollowUp != c.want {
			t.Errorf("%v: saved %v (%v), want %v", c.in, s.FollowUp, err, c.want)
		}
		if got := strings.Contains(logs.String(), "follow_up_listen_seconds clamped"); got != c.clamped {
			t.Errorf("%v: clamp logged %v, want %v:\n%s", c.in, got, c.clamped, logs.String())
		}
	}
}

func TestUpdateRejectsANegativeFollowUpWindow(t *testing.T) {
	n, _, v, _ := newNode(t)
	run(t, n, newFakeCC(), "update_node_config", map[string]any{"settings": map[string]any{
		"follow_up_listen_seconds": -1.0,
	}})
	if v.tunes != 0 || n.State() != initial() {
		t.Fatalf("tunes %d, state %+v", v.tunes, n.State())
	}
}

func TestLEDMode(t *testing.T) {
	for _, c := range []struct {
		enabled, idle bool
		want          string
	}{{true, true, "always"}, {true, false, "responding"}, {false, true, "off"}, {false, false, "off"}} {
		if got := (State{LEDEnabled: c.enabled, LEDIdle: c.idle}).LEDMode(); got != c.want {
			t.Fatalf("enabled %v idle %v: %q, want %q", c.enabled, c.idle, got, c.want)
		}
	}
}
