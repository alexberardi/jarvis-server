package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/alexberardi/jarvis-server/internal/agent/browser"
	"github.com/alexberardi/jarvis-server/internal/agent/control"
	"github.com/alexberardi/jarvis-server/internal/agent/pinode"
	"github.com/alexberardi/jarvis-server/internal/agent/settings"
	"github.com/alexberardi/jarvis-server/internal/agent/voice"
	"github.com/alexberardi/jarvis-server/internal/nodeconfig"
)

var desktopFields = []nodeconfig.Field{
	{Key: "island", Label: "On-screen indicator", Group: "Desktop", Type: "boolean", Help: "Restart Jarvis to apply."},
	{Key: "tray", Label: "Status icon", Group: "Desktop", Type: "boolean", Help: "Restart Jarvis to apply. Settings remain available by opening Jarvis."},
	{Key: "browser", Label: "Browser integration", Group: "Desktop", Type: "boolean", Help: "Restart Jarvis to apply."},
	{Key: "confirm_dictation", Label: "Dictation confirmation", Group: "Desktop", Type: "dictation", Help: "Off, before submitting, or before every action. Restart Jarvis to apply."},
	{Key: "choice_listen_seconds", Label: "Time to answer on-screen choices", Group: "Desktop", Type: "number", Min: .1, Max: 30, Step: .1, Help: "Seconds. Restart Jarvis to apply."},
	{Key: "auto_update", Label: "Automatic updates", Group: "Desktop", Type: "boolean", Help: "Follow the server's version. Restart Jarvis to apply."},
	{Key: "voice_input", Label: "Microphone device", Group: "Audio devices", Type: "text", Help: "Use default, or an audio device name. Restart Jarvis to apply."},
	{Key: "voice_output", Label: "Speaker device", Group: "Audio devices", Type: "text", Help: "Use default, or an audio device name. Restart Jarvis to apply."},
}

type desktopSettings struct {
	mu      sync.Mutex
	node    *pinode.Node
	path    string
	startup map[string]any
	running bool
	locked  map[string]bool
}

func desktopValues(fs *flag.FlagSet) map[string]any {
	v := map[string]any{}
	for _, field := range supportedDesktopFields() {
		key, _ := settings.Lookup(field.Key)
		text := fs.Lookup(key.Flag).Value.String()
		switch field.Type {
		case "boolean":
			b, _ := strconv.ParseBool(text)
			if key.Invert {
				b = !b
			}
			v[field.Key] = b
		case "number":
			n, _ := strconv.ParseFloat(text, 64)
			v[field.Key] = n
		default:
			v[field.Key] = text
		}
	}
	return v
}

func stoppedSettings(path string, log *slog.Logger) (*desktopSettings, error) {
	fs, f := newRunFlags(flag.ContinueOnError)
	s, _, err := settings.Load(path)
	if err != nil {
		return nil, errors.New("could not read agent settings")
	}
	if err := s.Apply(fs); err != nil {
		return nil, errors.New("invalid agent settings")
	}
	f.vf.clampFollowUp(log)
	n := pinode.New(piState(f.vf, f.hf), path, log)
	n.Desktop = true
	n.VoiceUnsupported = !voice.Supported
	return &desktopSettings{node: n, path: path, startup: desktopValues(fs)}, nil
}

func (d *desktopSettings) edit(ctx context.Context, patch map[string]any) (nodeconfig.View, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	common, local := map[string]any{}, map[string]any{}
	for k, v := range patch {
		if d.locked[k] {
			return nodeconfig.View{}, errors.New("setting is controlled by a command-line flag")
		}
		field := nodeconfig.Field{}
		for _, f := range supportedDesktopFields() {
			if f.Key == k {
				field = f
				break
			}
		}
		if field.Key == "" {
			common[k] = v
			continue
		}
		switch field.Type {
		case "text":
			s, ok := v.(string)
			if !ok || s == "" || len(s) > 128 || strings.ContainsAny(s, "/\\\x00\r\n") || strings.HasPrefix(strings.ToLower(s), "wav:") {
				return nodeconfig.View{}, fmt.Errorf("invalid audio device name")
			}
		case "dictation":
			if v != "off" && v != "submit" && v != "all" {
				return nodeconfig.View{}, errors.New("choose a dictation confirmation mode")
			}
		default:
			if err := nodeconfig.Validate(map[string]any{k: v}, []nodeconfig.Field{field}); err != nil {
				return nodeconfig.View{}, err
			}
		}
		local[k] = v
	}
	if patch != nil {
		if len(patch) == 0 {
			return nodeconfig.View{}, errors.New("choose at least one setting")
		}
		if _, err := d.node.ApplyConfig(ctx, common, func(s *settings.Settings) error {
			for k, v := range local {
				key, _ := settings.Lookup(k)
				if err := s.Set(key, fmt.Sprint(v)); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return nodeconfig.View{}, err
		}
	}
	view := d.node.Config(ctx)
	s, _, err := settings.Load(d.path)
	if err != nil {
		return nodeconfig.View{}, errors.New("could not read saved settings")
	}
	for _, f := range supportedDesktopFields() {
		value := d.startup[f.Key]
		k, _ := settings.Lookup(f.Key)
		if text, ok := s.Get(k); ok && !d.locked[f.Key] {
			switch f.Type {
			case "boolean":
				value, _ = strconv.ParseBool(text)
			case "number":
				value, _ = strconv.ParseFloat(text, 64)
			default:
				value = text
			}
		}
		view.Values[f.Key] = value
		view.Fields = append(view.Fields, f)
		if d.locked[f.Key] {
			view.ReadOnly = append(view.ReadOnly, f.Key)
		}
		if d.running && value != d.startup[f.Key] {
			view.RestartRequired = true
		}
	}
	return view, nil
}

func localSettings(ctx context.Context, patch map[string]any) (nodeconfig.View, error) {
	unlock, err := lockSettingsStartup()
	if err != nil {
		return nodeconfig.View{}, errors.New("Jarvis is starting or settings are busy; try again")
	}
	defer unlock()
	reply, err := control.AskSettings(controlDial, patch)
	if err == nil {
		var v nodeconfig.View
		if err := json.Unmarshal([]byte(reply.Answer), &v); err != nil {
			return v, errors.New("invalid settings response")
		}
		return v, nil
	}
	if !errors.Is(err, control.ErrNotRunning) {
		var rejected *control.RejectedError
		if errors.As(err, &rejected) {
			return nodeconfig.View{}, rejected
		}
		return nodeconfig.View{}, errors.New("could not reach agent settings; reload or restart Jarvis")
	}
	path, err := settings.DefaultPath()
	if err != nil {
		return nodeconfig.View{}, err
	}
	d, err := stoppedSettings(path, logger())
	if err != nil {
		return nodeconfig.View{}, err
	}
	return d.edit(ctx, patch)
}

// Cover the gap between startup reading agent.json and binding its control socket.
// A stopped editor either saves before that read, or finds the new socket and uses
// the running agent. Its lock is separate from the settings file's update lock.
func lockSettingsStartup() (func(), error) {
	p, err := browser.ControlSocketPath()
	if err != nil {
		return nil, err
	}
	return settings.Lock(p + ".settings-startup")
}

func supportedDesktopFields() []nodeconfig.Field {
	return desktopFieldsFor(runtime.GOOS, voice.Supported)
}

func desktopFieldsFor(goos string, voiceSupported bool) []nodeconfig.Field {
	var fields []nodeconfig.Field
	for _, f := range desktopFields {
		// AudioQueue and WinMM always use the OS default device. Only ALSA
		// supports names; a saved arbitrary name would prevent voice starting.
		if f.Group == "Audio devices" && (!voiceSupported || goos != "linux") {
			continue
		}
		if f.Key == "choice_listen_seconds" && !voiceSupported {
			continue
		}
		fields = append(fields, f)
	}
	return fields
}
func overriddenDesktop(flags map[string]bool) map[string]bool {
	out := map[string]bool{}
	for _, f := range desktopFields {
		k, _ := settings.Lookup(f.Key)
		if flags[k.Flag] {
			out[f.Key] = true
		}
	}
	return out
}
func overriddenNodeConfig(flags map[string]bool) []string {
	keys := map[string]string{
		"wake_word_threshold": "wake-threshold", "wake_word_threshold_auto": "wake-threshold-auto", "wake_ack_audio_enabled": "no-chimes",
		"follow_up_listen_seconds": "follow-up", "follow_up_silence_duration": "follow-up-silence", "silence_duration": "listen-silence",
		"silence_threshold": "silence-threshold", "follow_up_min_record_after_onset_secs": "follow-up-min-after-onset", "follow_up_min_speech_secs": "follow-up-min-speech",
		"led_enabled": "no-leds", "led_brightness_percent": "led-brightness", "maintenance_restart_enabled": "no-maintenance-restart",
		"maintenance_restart_at_time": "maintenance-restart-at", "maintenance_restart_rss_ceiling_mb": "maintenance-rss-ceiling-mb",
	}
	var out []string
	for k, f := range keys {
		if flags[f] {
			out = append(out, k)
		}
	}
	return out
}
