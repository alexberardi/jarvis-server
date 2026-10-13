package main

import (
	"bytes"
	"flag"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/dictate"
	"github.com/alexberardi/jarvis-server/internal/agent/settings"
)

func TestEverySettingIsARunFlag(t *testing.T) {
	fs, _ := newRunFlags(flag.ContinueOnError)
	for _, k := range settings.Keys {
		if fs.Lookup(k.Flag) == nil {
			t.Errorf("setting %s names flag --%s, which run lacks", k.Name, k.Flag)
		}
	}
	for _, m := range settings.ConfirmModes {
		if _, err := dictate.ParseConfirmMode(m); err != nil {
			t.Errorf("confirm mode %q: %v", m, err)
		}
	}
}

func TestRunSettingsFileAndFlags(t *testing.T) {
	p := filepath.Join(t.TempDir(), "agent.json")
	if err := os.WriteFile(p, []byte(`{"voice": true, "island": false, "confirm_dictation": "submit", "wake_threshold": 0.5}`), 0o600); err != nil {
		t.Fatal(err)
	}
	fs, f := newRunFlags(flag.ContinueOnError)
	if err := fs.Parse([]string{"--config", p, "--confirm-dictation", "all"}); err != nil {
		t.Fatal(err)
	}
	if err := applySettings(fs, *f.config, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	if !*f.voiceOn || !*f.noOverlay || *f.tf.confirm != "all" || f.vf.threshold != 0.5 {
		t.Fatalf("voice=%v no-overlay=%v confirm=%q threshold=%v", *f.voiceOn, *f.noOverlay, *f.tf.confirm, f.vf.threshold)
	}
}

func TestRunRefusesABrokenSettingsFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "agent.json")
	if err := os.WriteFile(p, []byte(`{"confirm_dictation": "sometimes"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	fs, _ := newRunFlags(flag.ContinueOnError)
	if err := applySettings(fs, p, slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
		t.Fatal("started with a broken settings file")
	}
}

func TestConfigCommand(t *testing.T) {
	p := filepath.Join(t.TempDir(), "agent.json")
	var out bytes.Buffer
	cfg := func(args ...string) error {
		out.Reset()
		return configCmd(append([]string{"--file", p}, args...), &out)
	}
	if err := cfg("set", "voice", "true"); err != nil {
		t.Fatal(err)
	}
	if err := cfg("set", "confirm_dictation", "never"); err == nil {
		t.Fatal("saved a bad value")
	}
	if err := cfg("set", "nope", "1"); err == nil {
		t.Fatal("saved an unknown key")
	}
	if err := cfg("show"); err != nil || !strings.Contains(out.String(), "voice              true") ||
		!strings.Contains(out.String(), "island             (unset)") {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if err := cfg("unset", "voice"); err != nil {
		t.Fatal(err)
	}
	if s, _, _ := settings.Load(p); s.Voice != nil {
		t.Fatal("unset kept voice")
	}
	if err := cfg("path"); err != nil || strings.TrimSpace(out.String()) != p {
		t.Fatal(out.String())
	}
	if err := cfg("frob"); err == nil {
		t.Fatal("unknown verb accepted")
	}
}
