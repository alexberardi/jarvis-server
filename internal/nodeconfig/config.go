// Package nodeconfig defines the non-secret node settings contract. It deliberately
// knows nothing about K2, encrypted snapshots, credentials or package settings.
package nodeconfig

import (
	"fmt"
	"math"
)

type Field struct {
	Key   string  `json:"key"`
	Label string  `json:"label"`
	Help  string  `json:"help,omitempty"`
	Group string  `json:"group"`
	Type  string  `json:"type"`
	Min   float64 `json:"min"`
	Max   float64 `json:"max"`
	Step  float64 `json:"step,omitempty"`
}

type View struct {
	ReadOnly        []string       `json:"read_only,omitempty"`
	Values          map[string]any `json:"values"`
	Fields          []Field        `json:"fields"`
	RestartRequired bool           `json:"restart_required,omitempty"`
}

// Fields is a fresh copy; node-supplied labels, schemas and unknown keys are never
// forwarded to a browser. "legacy" permits the Python node's longer follow-up.
func Fields(profile string) []Field {
	f := []Field{
		{"wake_ack_audio_enabled", "Wake acknowledgment audio", "Play wake and error chimes on the Go agent.", "Voice", "boolean", 0, 0, 0},
		{"wake_word_threshold", "Wake word threshold", "Lower is more sensitive; higher reduces accidental wakes.", "Voice", "number", .01, .99, .01},
		{"wake_word_threshold_auto", "Automatic wake sensitivity", "Calibrate from recent wakes; the threshold above is the starting value.", "Voice", "boolean", 0, 0, 0},
		{"follow_up_listen_seconds", "Follow-up duration", "Seconds to wait for another reply. Zero turns follow-ups off.", "Voice", "number", 0, 3, .1},
		{"silence_duration", "End-of-speech pause", "Seconds of silence that end your initial command.", "Voice", "number", .05, 10, .05},
		{"follow_up_silence_duration", "Follow-up pause", "Seconds of silence that end a follow-up reply.", "Voice", "number", .05, 10, .05},
		{"silence_threshold", "Silence threshold (RMS)", "Used when the room's noise level is not known.", "Advanced voice", "number", 1, 32767, 1},
		{"follow_up_min_record_after_onset_secs", "Minimum follow-up recording", "Seconds to record after speech starts.", "Advanced voice", "number", .05, 10, .05},
		{"follow_up_min_speech_secs", "Minimum follow-up speech", "Shorter speech is treated as noise, in seconds.", "Advanced voice", "number", 0, 10, .05},
	}
	if profile == "legacy" {
		f[3].Max = 15
	}
	if profile != "desktop" {
		f = append(f,
			Field{"led_enabled", "LEDs", "", "Lights", "boolean", 0, 0, 0},
			Field{"led_brightness_percent", "LED brightness", "Percent.", "Lights", "number", 0, 100, 1},
			Field{"volume_percent", "Speaker volume", "Percent.", "Audio", "number", 0, 100, 1},
			Field{"is_muted", "Mute speaker", "", "Audio", "boolean", 0, 0, 0},
			Field{"maintenance_restart_enabled", "Maintenance restart", "Restart this Pi at the scheduled time.", "Maintenance", "boolean", 0, 0, 0},
			Field{"maintenance_restart_at_time", "Restart time", "Local time, HH:MM.", "Maintenance", "time", 0, 0, 0},
			Field{"maintenance_restart_rss_ceiling_mb", "Memory restart threshold", "MB; zero disables memory-triggered restarts.", "Maintenance", "number", 0, 1048576, 1},
		)
	}
	return f
}

func valid(v any, f Field) bool {
	switch f.Type {
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "number":
		n, ok := v.(float64)
		return ok && !math.IsNaN(n) && !math.IsInf(n, 0) && n >= f.Min && n <= f.Max && (f.Step != 1 || math.Trunc(n) == n)
	case "time":
		s, ok := v.(string)
		if !ok || len(s) != 5 || s[2] != ':' {
			return false
		}
		for _, i := range []int{0, 1, 3, 4} {
			if s[i] < '0' || s[i] > '9' {
				return false
			}
		}
		return s[:2] <= "23" && s[3:] <= "59"
	}
	return false
}

func Validate(values map[string]any, fields []Field) error {
	if len(values) == 0 {
		return fmt.Errorf("choose at least one setting")
	}
	for key, v := range values {
		found := false
		for _, f := range fields {
			if f.Key == key {
				found = true
				if !valid(v, f) {
					return fmt.Errorf("invalid value for %s", f.Label)
				}
				break
			}
		}
		if !found {
			return fmt.Errorf("unsupported setting")
		}
	}
	return nil
}

func ViewOf(values map[string]any, profile string) View {
	v := View{Values: map[string]any{}, Fields: []Field{}}
	for _, f := range Fields(profile) {
		readField := f
		if f.Type == "number" {
			readField.Min, readField.Max, readField.Step = -math.MaxFloat64, math.MaxFloat64, 0
		}
		if x, ok := values[f.Key]; ok && valid(x, readField) {
			v.Values[f.Key] = x
			v.Fields = append(v.Fields, f)
		}
	}
	return v
}
