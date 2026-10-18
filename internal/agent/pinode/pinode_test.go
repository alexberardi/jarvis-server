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

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/settings"
	"github.com/alexberardi/jarvis-server/internal/agent/voice"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type fakeLEDs struct {
	mu       sync.Mutex
	previews []string
	enabled  []bool
	bright   []int
}

func (l *fakeLEDs) Preview(name string, d time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.previews = append(l.previews, name+" "+d.String())
}
func (l *fakeLEDs) SetEnabled(on bool) { l.mu.Lock(); l.enabled = append(l.enabled, on); l.mu.Unlock() }
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
	return State{WakeThreshold: 0.4, WakeAck: true, FollowUpListen: 4, FollowUpSilence: 1, LEDEnabled: true, LEDBrightness: 100}
}

func newNode(t *testing.T) (*Node, *fakeLEDs, *fakeVoice, string) {
	t.Helper()
	leds, v := &fakeLEDs{}, &fakeVoice{cfg: voice.DefaultConfig()}
	path := filepath.Join(t.TempDir(), settings.FileName)
	n := New(initial(), path, quiet)
	n.LEDs, n.Voice = leds, v
	return n, leds, v, path
}

// run dispatches one published command through the node dispatcher, as MQTT would.
func run(t *testing.T, n *Node, cc node.Backend, verb string, det map[string]any) {
	t.Helper()
	b, err := json.Marshal([]map[string]any{{"command": verb, "details": det}})
	if err != nil {
		t.Fatal(err)
	}
	d := &node.Dispatcher{CC: cc, Log: quiet, Verbs: n.Verbs()}
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
		"led_brightness_percent":     40.0,
		"wake_word_threshold":        0.55,
		"wake_ack_audio_enabled":     false,
		"follow_up_listen_seconds":   0.0,
		"follow_up_silence_duration": 0.7,
		"volume_percent":             30.0, // not applied: no Pi volume control yet
		"is_muted":                   true,
		"room":                       "kitchen",
		"api_key":                    "nope",
	}})
	if !slices.Equal(leds.enabled, []bool{false}) || !slices.Equal(leds.bright, []int{40}) {
		t.Fatalf("leds %v %v", leds.enabled, leds.bright)
	}
	if v.cfg.WakeThreshold != 0.55 || v.cfg.Chimes || v.cfg.FollowUp.ListenSecs != 0 || v.cfg.FollowUp.SilenceSecs != 0.7 {
		t.Fatalf("voice cfg %+v", v.cfg)
	}
	want := State{WakeThreshold: 0.55, WakeAck: false, FollowUpListen: 0, FollowUpSilence: 0.7, LEDEnabled: false, LEDBrightness: 40}
	if got := n.State(); got != want {
		t.Fatalf("state %+v", got)
	}
	s, unknown, err := settings.Load(path)
	if err != nil || len(unknown) != 0 {
		t.Fatal(err, unknown)
	}
	if *s.Voice != true || *s.LEDs != false || *s.LEDBrightness != 40 || *s.WakeThreshold != 0.55 ||
		*s.Chimes != false || *s.FollowUp != 0 || *s.FollowUpSilence != 0.7 {
		t.Fatalf("saved %+v", s)
	}
	b, _ := os.ReadFile(path)
	for _, k := range []string{"volume", "muted", "room", "api_key"} {
		if strings.Contains(string(b), k) {
			t.Errorf("%s persisted:\n%s", k, b)
		}
	}
}

func TestUpdateSkipsBadValues(t *testing.T) {
	n, leds, v, path := newNode(t)
	run(t, n, newFakeCC(), "update_node_config", map[string]any{"settings": map[string]any{
		"wake_word_threshold":        1.5,
		"led_brightness_percent":     "bright",
		"follow_up_silence_duration": 0.0,
		"led_enabled":                1.0, // Python: str(1) == "1" is true
	}})
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
