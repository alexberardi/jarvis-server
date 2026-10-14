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
}

// Key describes one setting: its name in the file, the `run` flag it stands for, and whether
// the flag says the opposite (island ⇔ --no-overlay).
type Key struct {
	Name   string
	Flag   string
	Invert bool
	Help   string
	field  func(*Settings) any // pointer to the field: **bool, **string or **float64
}

// Keys are every setting, in the order `config show` lists them.
var Keys = []Key{
	{"voice", "voice", false, `listen for "Hey Jarvis" (Linux, macOS; the menu's toggle sets it)`, func(s *Settings) any { return &s.Voice }},
	{"island", "no-overlay", true, "show the on-screen indicator (voice states, the \"Jarvis is running\" pill)", func(s *Settings) any { return &s.Island }},
	{"tray", "no-tray", true, "show the status icon and its menu (menu bar, tray, notification area)", func(s *Settings) any { return &s.Tray }},
	{"confirm_dictation", "confirm-dictation", false, "ask before dictation acts: off, submit or all", func(s *Settings) any { return &s.ConfirmDictation }},
	{"browser", "no-browser", true, "serve the browser extension (browser_* tools)", func(s *Settings) any { return &s.Browser }},
	{"chimes", "no-chimes", true, "play the wake and error chimes", func(s *Settings) any { return &s.Chimes }},
	{"voice_input", "voice-input", false, `microphone: "default", an ALSA PCM name (Linux), or wav:FILE`, func(s *Settings) any { return &s.VoiceInput }},
	{"voice_output", "voice-output", false, `speaker: "default", an ALSA PCM name (Linux), or wav:FILE`, func(s *Settings) any { return &s.VoiceOutput }},
	{"wake_threshold", "wake-threshold", false, "wake word fires above this score (0 to 1)", func(s *Settings) any { return &s.WakeThreshold }},
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
	for _, p := range []*string{s.VoiceInput, s.VoiceOutput} {
		if p != nil && strings.TrimSpace(*p) == "" {
			return errors.New("voice_input and voice_output can't be empty (unset them for the default device)")
		}
	}
	return nil
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
