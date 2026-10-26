// Package pinode is the Pi node's command verbs beyond the desktop's two (go-pi-node.md §7
// item 5.4): update_node_config, preview_led_pattern and measure_ambient_noise, ported from
// N:scripts/mqtt_tts_listener.py (handle_update_node_config, handle_preview_led_pattern,
// handle_measure_ambient_noise). The Pi profile hands Verbs to the node dispatcher.
package pinode

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/maintenance"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/settings"
	"github.com/alexberardi/jarvis-server/internal/agent/voice"
)

// LEDs is the HAT's LEDs (*hat.LEDs).
type LEDs interface {
	Preview(name string, d time.Duration)
	SetEnabled(on bool)
	SetIdleDark(dark bool)
	SetBrightness(percent int)
}

// Voice is the voice pipeline, running or not (*voice.Live).
type Voice interface {
	Tune(f func(*voice.Config))
	Measure(ctx context.Context, d time.Duration) ([]float64, error)
}

// Audio is the speaker's volume and mute (*sdkhost.Audio), which persists them itself
// (volume.json) and applies them again at start.
type Audio interface {
	SetVolume(ctx context.Context, pct int) bool
	SetMuted(ctx context.Context, muted bool) bool
}

// State is the node settings the agent runs with now, under their meaning in the Python node's
// config.json: what the settings snapshot's node_config reports.
type State struct {
	WakeThreshold   float64 // wake_word_threshold
	WakeAuto        bool    // wake_word_threshold_auto
	WakeAck         bool    // wake_ack_audio_enabled (the Go node's chimes)
	FollowUpListen  float64 // follow_up_listen_seconds
	FollowUpSilence float64 // follow_up_silence_duration
	LEDEnabled      bool    // led_enabled
	LEDIdle         bool    // led_idle_enabled: false lights the LEDs only while responding
	LEDBrightness   int     // led_brightness_percent
	// The voice pipeline's endpointing.
	SilenceThreshold      float64 // silence_threshold (the static one; the room's level wins when known)
	SilenceSecs           float64 // silence_duration: silence that ends a command
	FollowUpMinAfterOnset float64 // follow_up_min_record_after_onset_secs
	FollowUpMinSpeech     float64 // follow_up_min_speech_secs
	// maintenance_restart_enabled, _at_time and _rss_ceiling_mb: read at every poll.
	Maintenance maintenance.Settings
}

// LEDMode is the LEDs as one setting for the admin page: "off", "responding" (lit only while the
// node responds) or "always" (the dim idle glow too).
func (s State) LEDMode() string {
	switch {
	case !s.LEDEnabled:
		return "off"
	case !s.LEDIdle:
		return "responding"
	}
	return "always"
}

// Node handles the verbs. Set LEDs, Voice and Audio after New when the Pi has them.
type Node struct {
	LEDs  LEDs  // nil: no HAT
	Voice Voice // nil: voice can't run on this build
	Audio Audio // nil: no mixer to set
	// MeasureWait is how long a measurement may wait for a turn in progress to end (30 s).
	MeasureWait time.Duration

	path string // agent.json
	log  *slog.Logger

	mu    sync.Mutex
	state State
}

// New is a Node running with state, persisting to the settings file at path ("" doesn't save).
func New(state State, path string, log *slog.Logger) *Node {
	return &Node{state: state, path: path, log: log, MeasureWait: 30 * time.Second}
}

// State is a copy of the settings in effect.
func (n *Node) State() State {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.state
}

// Verb handles one command verb; it verifies and replies itself, through cc.
type Verb func(ctx context.Context, cc node.Backend, details map[string]any)

// Verbs are the node's handlers (node.Runner.Verbs, given the connected command-center).
func (n *Node) Verbs() map[string]Verb {
	return map[string]Verb{
		"update_node_config":    n.updateNodeConfig,
		"preview_led_pattern":   n.previewLEDPattern,
		"measure_ambient_noise": n.measureAmbientNoise,
	}
}

// --- update_node_config ---

// forbiddenKeys are identity and credentials, and the update/egress consent gates
// (N:constants/config_policy.py UPDATE_POLICY_KEYS): writable only through the K2 config push.
var forbiddenKeys = []string{"node_id", "api_key", "allow_updates", "wake_word_model_autodownload_enabled", "release_track"}

// FilterTrustAnchors drops what update_node_config may never set (N: _reject_trust_anchor_keys):
// the node's identity and credentials, every endpoint (*_url) and broker (mqtt_*) key, the
// config file's internals (_*) and the update/egress consent gates. This channel is plaintext
// MQTT relayed by command-center: a repointed CC URL would be node RCE.
func FilterTrustAnchors(in map[string]any) (safe map[string]any, rejected []string) {
	safe = map[string]any{}
	for k, v := range in {
		if slices.Contains(forbiddenKeys, k) || strings.HasSuffix(k, "_url") || strings.HasPrefix(k, "mqtt_") || strings.HasPrefix(k, "_") {
			rejected = append(rejected, k)
			continue
		}
		safe[k] = v
	}
	sort.Strings(rejected)
	return safe, rejected
}

// change is one accepted key: its effect on State, agent.json, the running pipeline or the
// speaker.
type change struct {
	state func(*State)
	save  func(*settings.Settings)
	tune  func(*voice.Config)          // nil: not a voice setting
	leds  func(LEDs)                   // nil: not an LED setting
	audio func(context.Context, Audio) // set instead of state and save: Audio persists it
	note  string                       // logged when applied: what was changed on the way in
}

// parse turns one key into a change, or says why it is skipped.
func parse(key string, v any) (change, string) {
	switch key {
	case "wake_word_threshold":
		f, ok := number(v)
		if !ok || f <= 0 || f >= 1 {
			return change{}, "use a number between 0 and 1"
		}
		return change{state: func(s *State) { s.WakeThreshold = f }, save: func(s *settings.Settings) { s.WakeThreshold = &f },
			tune: func(c *voice.Config) { c.WakeThreshold = f }}, ""
	case "wake_word_threshold_auto":
		b := truthy(v)
		return change{state: func(s *State) { s.WakeAuto = b }, save: func(s *settings.Settings) { s.WakeAuto = &b },
			tune: func(c *voice.Config) { c.WakeThresholdAuto = b }}, ""
	case "wake_ack_audio_enabled":
		b := truthy(v)
		return change{state: func(s *State) { s.WakeAck = b }, save: func(s *settings.Settings) { s.Chimes = &b },
			tune: func(c *voice.Config) { c.Chimes = b }}, ""
	case "follow_up_listen_seconds":
		f, ok := number(v)
		if !ok || f < 0 {
			return change{}, "use 0 (off) or more seconds"
		}
		note := ""
		if c := voice.ClampFollowUpListen(f); c != f {
			note = fmt.Sprintf("follow_up_listen_seconds clamped from %v to %v s", f, c)
			f = c
		}
		return change{note: note, state: func(s *State) { s.FollowUpListen = f }, save: func(s *settings.Settings) { s.FollowUp = &f },
			tune: func(c *voice.Config) { c.FollowUp.ListenSecs = f }}, ""
	case "follow_up_silence_duration":
		f, ok := number(v)
		if !ok || f <= 0 {
			return change{}, "use a number of seconds above 0"
		}
		return change{state: func(s *State) { s.FollowUpSilence = f }, save: func(s *settings.Settings) { s.FollowUpSilence = &f },
			tune: func(c *voice.Config) { c.FollowUp.SilenceSecs = f }}, ""
	case "led_enabled":
		b := truthy(v)
		return change{state: func(s *State) { s.LEDEnabled = b }, save: func(s *settings.Settings) { s.LEDs = &b },
			leds: func(l LEDs) { l.SetEnabled(b) }}, ""
	case "led_idle_enabled":
		b := truthy(v)
		return change{state: func(s *State) { s.LEDIdle = b }, save: func(s *settings.Settings) { s.LEDIdle = &b },
			leds: func(l LEDs) { l.SetIdleDark(!b) }}, ""
	case "led_brightness_percent":
		f, ok := number(v)
		if !ok {
			return change{}, "use a percentage"
		}
		p := max(0, min(100, int(f))) // Python's int(); the LED service clamps
		return change{state: func(s *State) { s.LEDBrightness = p }, save: func(s *settings.Settings) { s.LEDBrightness = &p },
			leds: func(l LEDs) { l.SetBrightness(p) }}, ""
	case "silence_threshold":
		f, ok := number(v)
		f = math.Trunc(f) // Python's int()
		if !ok || f <= 0 {
			return change{}, "use a number above 0"
		}
		return change{state: func(s *State) { s.SilenceThreshold = f }, save: func(s *settings.Settings) { s.SilenceThreshold = &f },
			tune: func(c *voice.Config) { c.Endpointing.SilenceThreshold = f }}, ""
	case "silence_duration":
		f, ok := number(v)
		if !ok || f <= 0 {
			return change{}, "use a number of seconds above 0"
		}
		return change{state: func(s *State) { s.SilenceSecs = f }, save: func(s *settings.Settings) { s.ListenSilence = &f },
			tune: func(c *voice.Config) { c.Endpointing.SilenceSecs = f }}, ""
	case "follow_up_min_record_after_onset_secs":
		f, ok := number(v)
		if !ok || f <= 0 {
			return change{}, "use a number of seconds above 0"
		}
		return change{state: func(s *State) { s.FollowUpMinAfterOnset = f }, save: func(s *settings.Settings) { s.FollowUpMinAfterOnset = &f },
			tune: func(c *voice.Config) { c.FollowUp.MinAfterOnsetSecs = f }}, ""
	case "follow_up_min_speech_secs":
		f, ok := number(v)
		if !ok || f < 0 {
			return change{}, "use 0 or more seconds"
		}
		return change{state: func(s *State) { s.FollowUpMinSpeech = f }, save: func(s *settings.Settings) { s.FollowUpMinSpeech = &f },
			tune: func(c *voice.Config) { c.FollowUp.MinSpeechSecs = f }}, ""
	case "maintenance_restart_enabled":
		b := truthy(v)
		return change{state: func(s *State) { s.Maintenance.Enabled = b }, save: func(s *settings.Settings) { s.MaintenanceRestart = &b }}, ""
	case "maintenance_restart_at_time":
		at, ok := v.(string)
		if _, _, valid := maintenance.ParseHHMM(at); !ok || !valid {
			return change{}, "use HH:MM, 00:00 to 23:59"
		}
		at = strings.TrimSpace(at)
		return change{state: func(s *State) { s.Maintenance.At = at }, save: func(s *settings.Settings) { s.MaintenanceRestartAt = &at }}, ""
	case "maintenance_restart_rss_ceiling_mb":
		f, ok := number(v)
		if !ok || f < 0 {
			return change{}, "use 0 (never) or more MB"
		}
		mb := int(f) // Python's int()
		return change{state: func(s *State) { s.Maintenance.CeilingMB = mb }, save: func(s *settings.Settings) { s.MaintenanceRSSCeilingMB = &mb }}, ""
	case "volume_percent":
		f, ok := number(v)
		if !ok {
			return change{}, "use a percentage"
		}
		p := max(0, min(100, int(f))) // Python's int(); set_volume_percent clamps
		return change{audio: func(ctx context.Context, a Audio) { a.SetVolume(ctx, p) }}, ""
	case "is_muted":
		b := truthy(v)
		return change{audio: func(ctx context.Context, a Audio) { a.SetMuted(ctx, b) }}, ""
	case "wake_word_model":
		return change{}, "the Go node has one wake word (hey_jarvis); not applied or saved"
	}
	return change{}, "not a Go node setting; not applied or saved"
}

func (n *Node) updateNodeConfig(ctx context.Context, _ node.Backend, det map[string]any) {
	in, _ := det["settings"].(map[string]any)
	if len(in) == 0 {
		n.log.Warn("update_node_config: no settings provided")
		return
	}
	in, rejected := FilterTrustAnchors(in)
	if len(rejected) > 0 {
		n.log.Warn("update_node_config: rejected trust-anchor/policy keys: a config push may not repoint "+
			"the CC URL, node identity, or broker, nor flip update/egress consent gates", "rejected", rejected)
	}
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var changes []change
	var applied []string
	for _, k := range keys {
		c, why := parse(k, in[k])
		if why == "" && c.audio != nil && n.Audio == nil {
			why = "no mixer on this node; not applied or saved"
		}
		if why != "" {
			n.log.Warn("update_node_config: key ignored", "key", k, "value", in[k], "why", why)
			continue
		}
		if c.note != "" {
			n.log.Info("update_node_config: " + c.note)
		}
		changes = append(changes, c)
		applied = append(applied, k)
	}
	if len(changes) == 0 {
		return
	}
	// Live first, as Python: the user sees the effect even if the file can't be written.
	n.mu.Lock()
	save := false
	for _, c := range changes {
		if c.audio != nil {
			continue
		}
		save = true
		c.state(&n.state)
		if c.leds != nil && n.LEDs != nil {
			c.leds(n.LEDs)
		}
		if c.tune != nil && n.Voice != nil {
			n.Voice.Tune(c.tune)
		}
	}
	n.mu.Unlock()
	// The mixer outside the lock: pactl may take its time.
	for _, c := range changes {
		if c.audio != nil {
			c.audio(ctx, n.Audio)
		}
	}
	if n.path != "" && save {
		err := settings.Update(n.path, func(s *settings.Settings) error {
			for _, c := range changes {
				if c.save != nil {
					c.save(s)
				}
			}
			return nil
		})
		if err != nil {
			n.log.Error("update_node_config: not saved; it lasts until the agent restarts", "err", err)
		}
	}
	n.log.Info("node config updated via MQTT", "keys", applied)
	// Python restarts only for wake_word_model, which the Go node doesn't take.
	if restart, _ := det["restart"].(bool); restart {
		n.log.Info("update_node_config: no restart needed; every setting the Go node takes applies live")
	}
}

// truthy is Python's `bool(v) if isinstance(v, bool) else str(v).lower() in ("true", "1", "yes")`.
func truthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		s := strings.ToLower(t)
		return s == "true" || s == "1" || s == "yes"
	case float64:
		return t == 1
	}
	return false
}

// number is Python's float(v) for JSON values: a number or numeric text; NaN and ±Inf are not.
func number(v any) (float64, bool) {
	var f float64
	switch t := v.(type) {
	case float64:
		f = t
	case string:
		var err error
		if f, err = strconv.ParseFloat(strings.TrimSpace(t), 64); err != nil {
			return 0, false
		}
	default:
		return 0, false
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}

// --- preview_led_pattern ---

// previewDuration is preview's duration_seconds: 3 s by default (also for null, NaN or a value
// that isn't a number), clamped to [0.5, 30] so a bad message can't park a pattern for hours.
func previewDuration(v any, log *slog.Logger) time.Duration {
	secs := 3.0
	if v != nil {
		if f, ok := number(v); ok {
			secs = f
		} else {
			log.Warn("preview_led_pattern: bad duration_seconds", "value", v)
		}
	}
	if c := max(0.5, min(30, secs)); c != secs {
		log.Warn("preview_led_pattern: duration clamped", "requested", secs, "used", c)
		secs = c
	}
	return time.Duration(secs * float64(time.Second))
}

func (n *Node) previewLEDPattern(_ context.Context, _ node.Backend, det map[string]any) {
	pattern := ""
	if v, ok := det["pattern"]; ok && v != nil {
		pattern = strings.TrimSpace(fmt.Sprint(v)) // Python's str()
	}
	if pattern == "" {
		n.log.Warn("preview_led_pattern: no pattern provided")
		return
	}
	d := previewDuration(det["duration_seconds"], n.log)
	if n.LEDs == nil {
		n.log.Warn("preview_led_pattern: no HAT LEDs on this node", "pattern", pattern)
		return
	}
	// An unknown pattern renders off, as Python's.
	n.LEDs.Preview(pattern, d)
	n.log.Info("LED preview", "pattern", pattern, "duration_seconds", d.Seconds())
}

// --- measure_ambient_noise ---

// AmbientStats is a measurement's summary (the result payload's numbers).
type AmbientStats struct {
	P50, P75, P95, Max float64
	Suggested          int // suggested_silence_threshold
}

// Ambient summarises the room's chunk RMS values as the Python node does: numpy's default
// (linear) percentiles over float32, and a suggested silence threshold of P75 × 1.3 rounded to
// the nearest 100 (Python's round(): halves to even) within the app's slider range,
// 1000–10000. P75 ignores rare peaks (a chair scrape); 1.3× leaves headroom over the floor
// while staying under conversational speech. rms must not be empty.
func Ambient(rms []float64) AmbientStats {
	v := make([]float32, len(rms))
	for i, r := range rms {
		v[i] = float32(r)
	}
	slices.Sort(v)
	st := AmbientStats{P50: percentile(v, 50), P75: percentile(v, 75), P95: percentile(v, 95), Max: float64(v[len(v)-1])}
	st.Suggested = max(1000, min(10000, int(math.RoundToEven(st.P75*1.3/100))*100))
	return st
}

// percentile is numpy.percentile(a, q) with method "linear" on sorted float32 values,
// interpolating as numpy's _lerp does (from the far end when the weight is ≥ 0.5).
func percentile(v []float32, q float64) float64 {
	idx := q / 100 * float64(len(v)-1)
	lo := int(math.Floor(idx))
	hi := min(lo+1, len(v)-1)
	t := float32(idx - float64(lo))
	a, b := v[lo], v[hi]
	d := b - a
	if t >= 0.5 {
		return float64(b - d*(1-t))
	}
	return float64(a + d*t)
}

func (n *Node) measureAmbientNoise(ctx context.Context, cc node.Backend, det map[string]any) {
	rid, _ := det["request_id"].(string)
	if rid == "" {
		n.log.Warn("measure_ambient_noise: missing request_id, ignoring")
		return
	}
	// jarvisd never sends trusted: the command must be one it issued to this node.
	if ok, err := cc.Verify(ctx, rid); err != nil || !ok {
		n.log.Warn("measure_ambient_noise: verification failed", "request_id", short(rid), "err", err)
		return
	}
	secs := 3.0 // Python: float(details.get("duration_seconds") or 3.0)
	if f, ok := number(det["duration_seconds"]); ok && f != 0 {
		secs = f
	}
	secs = max(1, min(10, secs))
	post := func(p map[string]any) {
		if err := cc.PostAmbientResult(ctx, rid, p); err != nil {
			n.log.Error("measure_ambient_noise: result POST failed", "err", err)
		}
	}
	fail := func(msg string) {
		n.log.Warn("measure_ambient_noise: failed", "request_id", short(rid), "error", msg)
		post(map[string]any{"success": false, "error": msg})
	}
	if n.Voice == nil {
		fail("audio_bus_unavailable")
		return
	}
	d := time.Duration(secs * float64(time.Second))
	mctx, cancel := context.WithTimeout(ctx, d+n.MeasureWait)
	rms, err := n.Voice.Measure(mctx, d)
	cancel()
	switch {
	case errors.Is(err, voice.ErrNotListening):
		fail("audio_bus_unavailable")
		return
	case errors.Is(err, context.DeadlineExceeded):
		fail("capture_failed: the voice pipeline stayed busy")
		return
	case err != nil:
		fail("capture_failed: " + err.Error())
		return
	case len(rms) == 0:
		fail("no_audio_captured")
		return
	}
	st := Ambient(rms)
	n.log.Info("ambient noise measurement complete", "request_id", short(rid), "chunks", len(rms),
		"p50", round1(st.P50), "p75", round1(st.P75), "p95", round1(st.P95), "max", round1(st.Max), "suggested", st.Suggested)
	post(map[string]any{
		"success":                     true,
		"duration_seconds":            secs,
		"chunks":                      len(rms),
		"p50_rms":                     st.P50,
		"p75_rms":                     st.P75,
		"p95_rms":                     st.P95,
		"max_rms":                     st.Max,
		"suggested_silence_threshold": st.Suggested,
	})
}

func short(rid string) string { return rid[:min(8, len(rid))] }

func round1(f float64) float64 { return math.Round(f*10) / 10 }
