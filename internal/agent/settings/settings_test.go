package settings

import (
	"flag"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// runFlags mirrors the `run` flags the settings stand for.
func runFlags() (*flag.FlagSet, map[string]any) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	v := map[string]any{
		"voice":             fs.Bool("voice", false, ""),
		"no-overlay":        fs.Bool("no-overlay", false, ""),
		"confirm-dictation": fs.String("confirm-dictation", "off", ""),
		"no-browser":        fs.Bool("no-browser", false, ""),
		"no-chimes":         fs.Bool("no-chimes", false, ""),
		"no-tray":           fs.Bool("no-tray", false, ""),
		"voice-input":       fs.String("voice-input", "default", ""),
		"voice-output":      fs.String("voice-output", "default", ""),
		"wake-threshold":    fs.Float64("wake-threshold", 0.4, ""),
	}
	return fs, v
}

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestEveryKeyIsARunFlag(t *testing.T) {
	fs, _ := runFlags()
	for _, k := range Keys {
		if fs.Lookup(k.Flag) == nil {
			t.Errorf("%s: no flag %s", k.Name, k.Flag)
		}
	}
}

func TestMissingFileIsEmpty(t *testing.T) {
	s, unknown, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil || unknown != nil || s != (Settings{}) {
		t.Fatal(s, unknown, err)
	}
}

func TestApplyFileThenFlagsWin(t *testing.T) {
	p := write(t, `{"voice": true, "island": false, "confirm_dictation": "submit", "browser": false,
		"chimes": false, "voice_input": "dsnoopmic", "wake_threshold": 0.6}`)
	s, unknown, err := Load(p)
	if err != nil || len(unknown) != 0 {
		t.Fatal(unknown, err)
	}
	fs, v := runFlags()
	// The command line says --voice=false and --confirm-dictation all: those win.
	if err := fs.Parse([]string{"--voice=false", "--confirm-dictation", "all"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(fs); err != nil {
		t.Fatal(err)
	}
	got := map[string]any{}
	for k, p := range v {
		switch x := p.(type) {
		case *bool:
			got[k] = *x
		case *string:
			got[k] = *x
		case *float64:
			got[k] = *x
		}
	}
	want := map[string]any{
		"voice": false, "confirm-dictation": "all", // flags
		"no-overlay": true, "no-browser": true, "no-chimes": true, "voice-input": "dsnoopmic", "wake-threshold": 0.6, // file
		"voice-output": "default", // neither
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %v, want %v", k, got[k], w)
		}
	}
}

func TestApplySkipsFlagsACommandLacks(t *testing.T) {
	v := true
	fs := flag.NewFlagSet("listen", flag.ContinueOnError)
	if err := (Settings{Voice: &v}).Apply(fs); err != nil {
		t.Fatal(err)
	}
}

func TestUnknownKeysAreReportedNotRefused(t *testing.T) {
	s, unknown, err := Load(write(t, `{"voice": true, "zz_future": 1, "aa_other": "x"}`))
	if err != nil || s.Voice == nil || !*s.Voice || !slices.Equal(unknown, []string{"aa_other", "zz_future"}) {
		t.Fatal(s, unknown, err)
	}
}

func TestBadValuesAreRefused(t *testing.T) {
	for _, body := range []string{
		`{"confirm_dictation": "sometimes"}`,
		`{"wake_threshold": 1.5}`,
		`{"wake_threshold": 0}`,
		`{"voice": "yes"}`,
		`{"voice_input": " "}`,
		`[1, 2]`,
		`{"voice": tru`,
	} {
		if _, _, err := Load(write(t, body)); err == nil {
			t.Errorf("%s: accepted", body)
		}
	}
}

func TestSetGetUnsetSaveRoundTrip(t *testing.T) {
	var s Settings
	for name, val := range map[string]string{"voice": "true", "island": "false", "confirm_dictation": "all", "wake_threshold": "0.55", "voice_output": "wav:out.wav"} {
		k, ok := Lookup(name)
		if !ok {
			t.Fatal(name)
		}
		if err := s.Set(k, val); err != nil {
			t.Fatal(name, err)
		}
		if got, ok := s.Get(k); !ok || got != val {
			t.Fatalf("%s: got %q", name, got)
		}
	}
	for name, bad := range map[string]string{"voice": "maybe", "confirm_dictation": "never", "wake_threshold": "high"} {
		k, _ := Lookup(name)
		c := s
		if err := c.Set(k, bad); err == nil {
			t.Errorf("%s=%s accepted", name, bad)
		}
	}
	k, _ := Lookup("voice_output")
	s.Unset(k)
	if _, ok := s.Get(k); ok {
		t.Fatal("unset kept the value")
	}
	p := filepath.Join(t.TempDir(), "sub", FileName)
	if err := Save(p, s); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(p); st.Mode().Perm() != 0o600 {
			t.Fatalf("mode %o", st.Mode().Perm())
		}
	}
	b, _ := os.ReadFile(p)
	if strings.Contains(string(b), "voice_output") {
		t.Fatalf("unset key written:\n%s", b)
	}
	back, _, err := Load(p)
	if err != nil || *back.Voice != true || *back.Island != false || *back.ConfirmDictation != "all" || *back.WakeThreshold != 0.55 {
		t.Fatal(back, err)
	}
}

func TestSaveRefusesInvalid(t *testing.T) {
	bad := "sometimes"
	if err := Save(filepath.Join(t.TempDir(), FileName), Settings{ConfirmDictation: &bad}); err == nil {
		t.Fatal("saved an invalid file")
	}
}

func TestConfirmModesMatchTheFlag(t *testing.T) {
	// dictate.ParseConfirmMode accepts exactly these; cmd/jarvis-agent's test checks the link.
	if !slices.Equal(ConfirmModes, []string{"off", "submit", "all"}) {
		t.Fatal(ConfirmModes)
	}
}
