package pinode

import (
	"context"
	"fmt"
	"slices"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/settings"
	"github.com/alexberardi/jarvis-server/internal/nodeconfig"
)

// Config is a view constructed only from ordinary settings, never the K2 store.
func (n *Node) Config(ctx context.Context) nodeconfig.View {
	n.mu.Lock()
	s := n.state
	n.mu.Unlock()
	values := map[string]any{
		"wake_word_threshold": s.WakeThreshold, "wake_word_threshold_auto": s.WakeAuto,
		"wake_ack_audio_enabled": s.WakeAck, "follow_up_listen_seconds": s.FollowUpListen,
		"follow_up_silence_duration": s.FollowUpSilence, "silence_threshold": s.SilenceThreshold,
		"silence_duration": s.SilenceSecs, "follow_up_min_record_after_onset_secs": s.FollowUpMinAfterOnset,
		"follow_up_min_speech_secs": s.FollowUpMinSpeech,
	}
	if n.VoiceUnsupported {
		values = map[string]any{}
	}
	profile := "desktop"
	if !n.Desktop {
		profile = "pi"
		if n.LEDs != nil {
			values["led_enabled"], values["led_brightness_percent"] = s.LEDEnabled, float64(s.LEDBrightness)
		}
		values["maintenance_restart_enabled"] = s.Maintenance.Enabled
		values["maintenance_restart_at_time"] = s.Maintenance.At
		values["maintenance_restart_rss_ceiling_mb"] = float64(s.Maintenance.CeilingMB)
		if a, ok := n.Audio.(interface {
			Volume(context.Context) (int, error)
			Muted() bool
		}); ok {
			if volume, err := a.Volume(ctx); err == nil {
				values["volume_percent"] = float64(volume)
			}
			values["is_muted"] = a.Muted()
		}
	}
	v := nodeconfig.ViewOf(values, profile)
	for _, f := range v.Fields {
		if slices.Contains(n.ReadOnly, f.Key) {
			v.ReadOnly = append(v.ReadOnly, f.Key)
		}
	}
	return v
}

// ApplyConfig validates the whole patch, persists before tuning, and serializes
// save + state + tune with legacy MQTT updates. Tune queues changes until idle.
func (n *Node) ApplyConfig(ctx context.Context, values map[string]any, local ...func(*settings.Settings) error) (nodeconfig.View, error) {
	n.configMu.Lock()
	defer n.configMu.Unlock()
	view := n.Config(ctx)
	if err := nodeconfig.Validate(values, view.Fields); err != nil && (len(values) > 0 || len(local) == 0) {
		return view, err
	}
	var changes []change
	for k, v := range values {
		if slices.Contains(n.ReadOnly, k) {
			return view, fmt.Errorf("setting is controlled by a command-line flag")
		}
		c, why := parse(k, v)
		if why != "" {
			return view, fmt.Errorf("invalid setting: %s", k)
		}
		changes = append(changes, c)
	}
	if n.path != "" {
		if err := settings.Update(n.path, func(s *settings.Settings) error {
			for _, c := range changes {
				if c.save != nil {
					c.save(s)
				}
			}
			for _, f := range local {
				if err := f(s); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return view, fmt.Errorf("settings could not be saved")
		}
	}
	n.mu.Lock()
	for _, c := range changes {
		if c.state != nil {
			c.state(&n.state)
		}
		if c.tune != nil && n.Voice != nil {
			n.Voice.Tune(c.tune)
		}
		if c.leds != nil && n.LEDs != nil {
			c.leds(n.LEDs)
		}
	}
	n.mu.Unlock()
	if a, ok := n.Audio.(interface {
		Configure(context.Context, map[string]any) error
	}); ok {
		if err := a.Configure(ctx, values); err != nil {
			return n.Config(ctx), err
		}
	} else {
		for _, c := range changes {
			if c.audio != nil {
				return n.Config(ctx), fmt.Errorf("speaker settings could not be confirmed")
			}
		}
	}
	return n.Config(ctx), nil
}

func (n *Node) configCommand(ctx context.Context, cc node.Backend, det map[string]any, write bool) {
	rid, _ := det["request_id"].(string)
	if rid == "" {
		return
	}
	if ok, err := cc.Verify(ctx, rid); err != nil || !ok {
		return
	}
	v := n.Config(ctx)
	var err error
	if write {
		values, _ := det["settings"].(map[string]any)
		v, err = n.ApplyConfig(ctx, values)
	}
	out := map[string]any{"success": err == nil, "values": v.Values, "read_only": v.ReadOnly}
	if err != nil {
		out = map[string]any{"success": false}
	}
	if err := cc.PostToolResult(ctx, rid, out); err != nil {
		n.log.Warn("node config reply failed", "err", err)
	}
}
