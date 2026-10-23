// Package settings is the desktop agent's config file (installers plan item 1.6): the choices a
// user makes once (voice on or off, the island, dictation confirmation, …) so they aren't
// command-line flags anyone must know. `jarvis-agent run` reads it; a flag given on the command
// line still wins. The first-run page (item 1.5) and `jarvis-agent config set` write it.
//
// The file is <user config dir>/jarvis-agent/agent.json, next to node.json and apps.json:
//
//	{"voice": true, "island": true, "confirm_dictation": "submit"}
//
// Every key is optional: a missing key leaves the flag's default. Each key is a `run` flag
// under another name (Keys), and is applied by setting that flag, so a value is validated
// exactly as the flag would be.
package settings

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// FileName is the config file's name in the agent's config dir.
const FileName = "agent.json"

// Settings are the file's keys. A nil field is unset.
type Settings struct {
	Voice            *bool    `json:"voice,omitempty"`
	Island           *bool    `json:"island,omitempty"`
	Tray             *bool    `json:"tray,omitempty"`
	ConfirmDictation *string  `json:"confirm_dictation,omitempty"`
	Browser          *bool    `json:"browser,omitempty"`
	Chimes           *bool    `json:"chimes,omitempty"`
	VoiceInput       *string  `json:"voice_input,omitempty"`
	VoiceOutput      *string  `json:"voice_output,omitempty"`
	WakeThreshold    *float64 `json:"wake_threshold,omitempty"`
	WakeAuto         *bool    `json:"wake_threshold_auto,omitempty"`
	AutoUpdate       *bool    `json:"auto_update,omitempty"`
	// A Pi node's settings, also written by the app's node settings (update_node_config).
	FollowUp        *float64 `json:"follow_up,omitempty"`
	FollowUpSilence *float64 `json:"follow_up_silence,omitempty"`
	LEDs            *bool    `json:"leds,omitempty"`
	LEDIdle         *bool    `json:"led_idle,omitempty"`
	LEDBrightness   *int     `json:"led_brightness,omitempty"`
	// The voice pipeline's endpointing (#295).
	SilenceThreshold      *float64 `json:"silence_threshold,omitempty"`
	ListenSilence         *float64 `json:"listen_silence,omitempty"`
	FollowUpMinAfterOnset *float64 `json:"follow_up_min_after_onset,omitempty"`
	FollowUpMinSpeech     *float64 `json:"follow_up_min_speech,omitempty"`
	// The Pi's maintenance restart (internal/agent/maintenance).
	MaintenanceRestart      *bool   `json:"maintenance_restart,omitempty"`
	MaintenanceRestartAt    *string `json:"maintenance_restart_at,omitempty"`
	MaintenanceRSSCeilingMB *int    `json:"maintenance_rss_ceiling_mb,omitempty"`
	// ComputerControl: Jarvis may use this computer (COMPUTER-CONTROL.md C11). Unset is off:
	// a new install and an upgrade both start off; the menu's toggle sets it.
	ComputerControl *bool `json:"computer_control,omitempty"`
}

// Key describes one setting: its name in the file, the `run` flag it stands for, and whether
// the flag says the opposite (island ⇔ --no-overlay).
type Key struct {
	Name   string
	Flag   string
	Invert bool
	Help   string
	field  func(*Settings) any // pointer to the field: **bool, **string, **float64 or **int
}

// Keys are every setting, in the order `config show` lists them.
var Keys = []Key{
	{"voice", "voice", false, `listen for "Hey Jarvis" (Linux, macOS; the menu's toggle sets it)`, func(s *Settings) any { return &s.Voice }},
	{"island", "no-overlay", true, "show the on-screen indicator (voice states, the \"Jarvis is running\" pill)", func(s *Settings) any { return &s.Island }},
	{"tray", "no-tray", true, "show the status icon and its menu (menu bar, tray, notification area)", func(s *Settings) any { return &s.Tray }},
	{"confirm_dictation", "confirm-dictation", false, "ask before dictation acts: off, submit (the default) or all", func(s *Settings) any { return &s.ConfirmDictation }},
	{"browser", "no-browser", true, "serve the browser extension (browser_* tools)", func(s *Settings) any { return &s.Browser }},
	{"chimes", "no-chimes", true, "play the wake and error chimes", func(s *Settings) any { return &s.Chimes }},
	{"voice_input", "voice-input", false, `microphone: "default", an ALSA PCM name (Linux), or wav:FILE`, func(s *Settings) any { return &s.VoiceInput }},
	{"voice_output", "voice-output", false, `speaker: "default", an ALSA PCM name (Linux), or wav:FILE`, func(s *Settings) any { return &s.VoiceOutput }},
	{"wake_threshold", "wake-threshold", false, "wake word fires above this score (0 to 1)", func(s *Settings) any { return &s.WakeThreshold }},
	{"wake_threshold_auto", "wake-threshold-auto", false, "calibrate the wake threshold from recent wakes (wake_threshold until there are 5)",
		func(s *Settings) any { return &s.WakeAuto }},
	{"auto_update", "no-auto-update", true, "update to the server's version by itself, and accept updates sent from Jarvis Automation", func(s *Settings) any { return &s.AutoUpdate }},
	{"follow_up", "follow-up", false, "after a reply, listen this many seconds for a follow-up without the wake word (0: off; at most 3)", func(s *Settings) any { return &s.FollowUp }},
	{"follow_up_silence", "follow-up-silence", false, "seconds of silence that end a follow-up answer", func(s *Settings) any { return &s.FollowUpSilence }},
	{"leds", "no-leds", true, "light the ReSpeaker HAT's LEDs (a Pi)", func(s *Settings) any { return &s.LEDs }},
	{"led_idle", "no-led-idle", true, "light the HAT's LEDs while idle (off: only while the node responds)", func(s *Settings) any { return &s.LEDIdle }},
	{"led_brightness", "led-brightness", false, "HAT LED brightness, percent (0 to 100)", func(s *Settings) any { return &s.LEDBrightness }},
	{"silence_threshold", "silence-threshold", false, "RMS below which a command counts as silence, when the room's level before the wake word isn't known",
		func(s *Settings) any { return &s.SilenceThreshold }},
	{"listen_silence", "listen-silence", false, "seconds of silence that end a command", func(s *Settings) any { return &s.ListenSilence }},
	{"follow_up_min_after_onset", "follow-up-min-after-onset", false, "a follow-up answer records at least this many seconds after speech starts",
		func(s *Settings) any { return &s.FollowUpMinAfterOnset }},
	{"follow_up_min_speech", "follow-up-min-speech", false, "a follow-up answer shorter than this many seconds of speech is noise",
		func(s *Settings) any { return &s.FollowUpMinSpeech }},
	{"maintenance_restart", "no-maintenance-restart", true, "a Pi restarts itself once a day and when its memory passes the ceiling",
		func(s *Settings) any { return &s.MaintenanceRestart }},
	{"maintenance_restart_at", "maintenance-restart-at", false, `a Pi's daily restart, "HH:MM" local time`,
		func(s *Settings) any { return &s.MaintenanceRestartAt }},
	{"maintenance_rss_ceiling_mb", "maintenance-rss-ceiling-mb", false, "a Pi restarts when its resident memory reaches this many MB (0: never)",
		func(s *Settings) any { return &s.MaintenanceRSSCeilingMB }},
	{"computer_control", "computer-control", false, `let Jarvis use this computer (off by default; the menu's "Computer control" sets it)`,
		func(s *Settings) any { return &s.ComputerControl }},
}

// Lookup finds a key by its name in the file.
func Lookup(name string) (Key, bool) {
	i := slices.IndexFunc(Keys, func(k Key) bool { return k.Name == name })
	if i < 0 {
		return Key{}, false
	}
	return Keys[i], true
}

// KeyNames are the valid names, for messages.
func KeyNames() string {
	names := make([]string, len(Keys))
	for i, k := range Keys {
		names[i] = k.Name
	}
	return strings.Join(names, ", ")
}

// ConfirmModes are confirm_dictation's values (dictate.Confirm*; repeated here so this package
// stays a leaf).
var ConfirmModes = []string{"off", "submit", "all"}

// DefaultPath is <user config dir>/jarvis-agent/agent.json.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "jarvis-agent", FileName), nil
}

// Load reads the file. A missing file is empty settings. Keys this version doesn't know are
// returned, not refused: a newer agent's file must not stop an older one from starting. A value
// of the wrong type or out of range is an error: run would otherwise silently drop a choice
// like confirm_dictation.
func Load(path string) (Settings, []string, error) {
	var s Settings
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil, nil
	}
	if err != nil {
		return s, nil, err
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return s, nil, nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return s, nil, fmt.Errorf("%s: %w", path, err)
	}
	var unknown []string
	for name := range raw {
		if _, ok := Lookup(name); !ok {
			unknown = append(unknown, name)
		}
	}
	slices.Sort(unknown)
	if err := json.Unmarshal(b, &s); err != nil {
		return Settings{}, nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := s.Validate(); err != nil {
		return Settings{}, nil, fmt.Errorf("%s: %w", path, err)
	}
	return s, unknown, nil
}

// Validate checks the values a flag wouldn't catch until later (or at all).
func (s Settings) Validate() error {
	if s.ConfirmDictation != nil && !slices.Contains(ConfirmModes, *s.ConfirmDictation) {
		return fmt.Errorf("confirm_dictation is %q; use off, submit or all", *s.ConfirmDictation)
	}
	if s.WakeThreshold != nil && (*s.WakeThreshold <= 0 || *s.WakeThreshold >= 1) {
		return fmt.Errorf("wake_threshold is %v; use a number between 0 and 1", *s.WakeThreshold)
	}
	if s.FollowUp != nil && *s.FollowUp < 0 {
		return fmt.Errorf("follow_up is %v; use 0 (off) or more seconds", *s.FollowUp)
	}
	if s.FollowUpSilence != nil && *s.FollowUpSilence <= 0 {
		return fmt.Errorf("follow_up_silence is %v; use a number of seconds above 0", *s.FollowUpSilence)
	}
	if s.SilenceThreshold != nil && *s.SilenceThreshold <= 0 {
		return fmt.Errorf("silence_threshold is %v; use a number above 0", *s.SilenceThreshold)
	}
	if s.ListenSilence != nil && *s.ListenSilence <= 0 {
		return fmt.Errorf("listen_silence is %v; use a number of seconds above 0", *s.ListenSilence)
	}
	if s.FollowUpMinAfterOnset != nil && *s.FollowUpMinAfterOnset <= 0 {
		return fmt.Errorf("follow_up_min_after_onset is %v; use a number of seconds above 0", *s.FollowUpMinAfterOnset)
	}
	if s.FollowUpMinSpeech != nil && *s.FollowUpMinSpeech < 0 {
		return fmt.Errorf("follow_up_min_speech is %v; use 0 or more seconds", *s.FollowUpMinSpeech)
	}
	if s.LEDBrightness != nil && (*s.LEDBrightness < 0 || *s.LEDBrightness > 100) {
		return fmt.Errorf("led_brightness is %d; use 0 to 100", *s.LEDBrightness)
	}
	if s.MaintenanceRestartAt != nil && !validHHMM(*s.MaintenanceRestartAt) {
		return fmt.Errorf("maintenance_restart_at is %q; use HH:MM, 00:00 to 23:59", *s.MaintenanceRestartAt)
	}
	if s.MaintenanceRSSCeilingMB != nil && *s.MaintenanceRSSCeilingMB < 0 {
		return fmt.Errorf("maintenance_rss_ceiling_mb is %d; use 0 (never) or more MB", *s.MaintenanceRSSCeilingMB)
	}
	for _, p := range []*string{s.VoiceInput, s.VoiceOutput} {
		if p != nil && strings.TrimSpace(*p) == "" {
			return errors.New("voice_input and voice_output can't be empty (unset them for the default device)")
		}
	}
	return nil
}

// validHHMM is maintenance.ParseHHMM's check (repeated here so this package stays a leaf).
func validHHMM(v string) bool {
	h, m, ok := strings.Cut(strings.TrimSpace(v), ":")
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	return ok && err1 == nil && err2 == nil && hh >= 0 && hh <= 23 && mm >= 0 && mm <= 59
}

// Save writes the settings atomically, 0600 in a 0700 directory (it sits next to node.json).
func Save(path string, s Settings) error {
	if err := s.Validate(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".agent-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		f.Close()
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// updateMu serializes this process's read-modify-writes of the file: the voice toggle and
// the app's node settings may write it at the same time.
var updateMu sync.Mutex

// Update loads the file, lets change edit it, and saves it (Validate still applies), one
// caller at a time.
func Update(path string, change func(*Settings) error) error {
	updateMu.Lock()
	defer updateMu.Unlock()
	s, _, err := Load(path)
	if err != nil {
		return err
	}
	if err := change(&s); err != nil {
		return err
	}
	return Save(path, s)
}

// Get is the value of key as text ("" when unset).
func (s *Settings) Get(k Key) (string, bool) {
	switch p := k.field(s).(type) {
	case **bool:
		if *p != nil {
			return strconv.FormatBool(**p), true
		}
	case **string:
		if *p != nil {
			return **p, true
		}
	case **float64:
		if *p != nil {
			return strconv.FormatFloat(**p, 'g', -1, 64), true
		}
	case **int:
		if *p != nil {
			return strconv.Itoa(**p), true
		}
	}
	return "", false
}

// Set parses value for key ("true", "off", "0.5") and stores it; Validate still applies.
func (s *Settings) Set(k Key, value string) error {
	switch p := k.field(s).(type) {
	case **bool:
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("%s takes true or false, not %q", k.Name, value)
		}
		*p = &b
	case **string:
		*p = &value
	case **float64:
		f, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return fmt.Errorf("%s takes a number, not %q", k.Name, value)
		}
		*p = &f
	case **int:
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("%s takes a whole number, not %q", k.Name, value)
		}
		*p = &n
	}
	return s.Validate()
}

// Unset removes key, so the flag's default applies again.
func (s *Settings) Unset(k Key) {
	switch p := k.field(s).(type) {
	case **bool:
		*p = nil
	case **string:
		*p = nil
	case **float64:
		*p = nil
	case **int:
		*p = nil
	}
}

// Apply sets each configured key's flag in fs, unless the command line already set that flag:
// flags win. Call it after fs.Parse. A key whose flag fs doesn't define is skipped (a command
// that doesn't take it).
func (s Settings) Apply(fs *flag.FlagSet) error {
	explicit := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	for _, k := range Keys {
		v, ok := s.Get(k)
		if !ok || explicit[k.Flag] || fs.Lookup(k.Flag) == nil {
			continue
		}
		if k.Invert {
			b, _ := strconv.ParseBool(v)
			v = strconv.FormatBool(!b)
		}
		if err := fs.Set(k.Flag, v); err != nil {
			return fmt.Errorf("setting %s: %w", k.Name, err)
		}
	}
	return nil
}
