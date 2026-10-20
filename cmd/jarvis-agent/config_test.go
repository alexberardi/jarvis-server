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

// Computer control is off unless turned on: a new install (no agent.json), an upgrade (an
// agent.json without the key), and on again after a restart once the menu saved it.
func TestComputerControlDefaultsOffAndSurvivesRestart(t *testing.T) {
	p := filepath.Join(t.TempDir(), "agent.json")
	runWith := func() bool {
		t.Helper()
		fs, f := newRunFlags(flag.ContinueOnError)
		if err := fs.Parse([]string{"--config", p}); err != nil {
			t.Fatal(err)
		}
		if err := applySettings(fs, *f.config, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
			t.Fatal(err)
		}
		return *f.computerOn
	}
	if runWith() {
		t.Fatal("a new install starts with computer control on")
	}
	if err := os.WriteFile(p, []byte(`{"voice": true, "island": true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if runWith() {
		t.Fatal("an upgrade (agent.json without computer_control) starts with it on")
	}
	if err := saveComputer(p, true); err != nil {
		t.Fatal(err)
	}
	if !runWith() {
		t.Fatal("turned on in the menu, then restarted: off")
	}
	if err := saveComputer(p, false); err != nil {
		t.Fatal(err)
	}
	if runWith() {
		t.Fatal("turned off in the menu, then restarted: on")
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), `"voice": true`) {
		t.Fatalf("saving the switch lost the other settings: %s", b)
	}
}

func TestRunConfirmsSubmitByDefault(t *testing.T) {
	fs, f := newRunFlags(flag.ContinueOnError)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	if err := applySettings(fs, filepath.Join(t.TempDir(), "none.json"), slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	if *f.tf.confirm != dictate.ConfirmSubmit {
		t.Fatalf("confirm=%q, want submit", *f.tf.confirm)
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

// An agent.json (or --follow-up) from before the 3 s cap still starts the agent, with the
// window clamped to 3 s (#296).
func TestRunClampsTheFollowUpWindow(t *testing.T) {
	for _, c := range []struct {
		file string
		args []string
		want float64
	}{
		{`{}`, nil, 3},
		{`{"follow_up": 8}`, nil, 3},
		{`{"follow_up": 2}`, nil, 2},
		{`{"follow_up": 0}`, nil, 0},
		{`{}`, []string{"--follow-up", "6"}, 3},
	} {
		p := filepath.Join(t.TempDir(), "agent.json")
		if err := os.WriteFile(p, []byte(c.file), 0o600); err != nil {
			t.Fatal(err)
		}
		fs, f := newRunFlags(flag.ContinueOnError)
		if err := fs.Parse(append([]string{"--config", p}, c.args...)); err != nil {
			t.Fatal(err)
		}
		log := slog.New(slog.NewTextHandler(io.Discard, nil))
		if err := applySettings(fs, *f.config, log); err != nil {
			t.Fatalf("%s %v: %v", c.file, c.args, err)
		}
		f.vf.clampFollowUp(log)
		if f.vf.followUp != c.want {
			t.Errorf("%s %v: follow-up %v, want %v", c.file, c.args, f.vf.followUp, c.want)
		}
	}
}
