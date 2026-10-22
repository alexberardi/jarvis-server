package hat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeACT is a sysfs LED directory: trigger lists the kernel's triggers with the current one in
// brackets, as /sys/class/leds/ACT/trigger does.
func fakeACT(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, v := range map[string]string{"trigger": "none timer heartbeat [mmc0] default-on\n", "brightness": "0\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func sysfs(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

// The monochrome remap is led_service.py's _effective_pattern: the command-flow overlays show
// the stable pattern; error and shutdown_warning show themselves.
func TestACTPicksTheStablePatternUnderCommandFlowOverlays(t *testing.T) {
	var a act
	for _, tc := range []struct{ stable, transient, want string }{
		{Normal, "", Normal},
		{Alert, "", Alert},
		{Alert, Listening, Alert},
		{Normal, WakeDetected, Normal},
		{Normal, Thinking, Normal},
		{Alert, Speaking, Alert},
		{Normal, NotForMe, Normal},
		{Normal, Muted, Normal},
		{Normal, Error, Error},
		{Alert, ShutdownWarning, ShutdownWarning},
		{Normal, Off, Off},
		{Normal, Setup, Setup},
	} {
		if got := a.pick(tc.stable, tc.transient); got != tc.want {
			t.Errorf("pick(%q, %q) = %q, want %q", tc.stable, tc.transient, got, tc.want)
		}
	}
}

func TestACTStates(t *testing.T) {
	for _, tc := range []struct {
		name            string
		t               float64
		trigger, bright string
	}{
		{Off, 0, "none", "0"},
		{Normal, 0, "default-on", ""},
		{Listening, 0, "default-on", ""}, // a preview of an overlay: on, as Python's
		{Alert, 0.1, "none", "1"},        // 1 Hz: 0.5 s on, 0.5 s off
		{Alert, 0.6, "none", "0"},
		{Error, 0.1, "none", "1"}, // ~3 Hz: 0.15 s on, 0.15 s off
		{Error, 0.2, "none", "0"},
		{ShutdownWarning, 0.05, "none", "1"}, // 5 Hz
		{ShutdownWarning, 0.15, "none", "0"},
		{Setup, 1, "none", "1"}, // the Go node's setup mode: a slow 1.5 s blink
		{Setup, 2, "none", "0"},
	} {
		trig, br := actState(tc.name, tc.t)
		if trig != tc.trigger || br != tc.bright {
			t.Errorf("%s at %.2f s: trigger %q brightness %q, want %q %q", tc.name, tc.t, trig, br, tc.trigger, tc.bright)
		}
	}
	for _, n := range []string{Alert, Error, ShutdownWarning, Setup} {
		if !(act{}).animated(n) {
			t.Errorf("%s must blink", n)
		}
	}
	if (act{}).animated(Normal) || (act{}).animated(Thinking) {
		t.Error("steady patterns are drawn once")
	}
}

func TestACTLEDsDriveSysfsAndRestoreTheTrigger(t *testing.T) {
	dir := fakeACT(t)
	l, err := NewACTLEDs(dir, quiet)
	if err != nil {
		t.Fatal(err)
	}
	trigger := func(want string) func() bool { return func() bool { return sysfs(t, dir, "trigger") == want } }
	bright := func(want string) func() bool { return func() bool { return sysfs(t, dir, "brightness") == want } }
	waitFor(t, "normal: on, by the kernel", trigger("default-on"))

	l.SetPattern(Alert)
	waitFor(t, "alert: trigger none", trigger("none"))
	waitFor(t, "alert blinks on", bright("1"))
	waitFor(t, "alert blinks off", bright("0"))
	waitFor(t, "alert blinks on again", bright("1"))

	l.SetTransient(Listening) // no colours: the alert keeps blinking
	waitFor(t, "alert under listening blinks off", bright("0"))
	waitFor(t, "alert under listening blinks on", bright("1"))

	l.SetPattern(Normal)
	waitFor(t, "normal under listening", trigger("default-on"))

	l.SetEnabled(false)
	waitFor(t, "disabled: off", func() bool { return sysfs(t, dir, "trigger") == "none" && sysfs(t, dir, "brightness") == "0" })
	l.SetEnabled(true)
	waitFor(t, "enabled again", trigger("default-on"))

	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if got := sysfs(t, dir, "trigger"); got != "mmc0" {
		t.Fatalf("Close left trigger %q, want the one found at start (mmc0)", got)
	}
}

func TestNewACTLEDsNeedsTheLED(t *testing.T) {
	if _, err := NewACTLEDs(t.TempDir(), quiet); err == nil {
		t.Fatal("a directory without trigger and brightness is not an LED")
	}
}

func TestFindACT(t *testing.T) {
	root := t.TempDir()
	act, led0, none := filepath.Join(root, "ACT"), filepath.Join(root, "led0"), filepath.Join(root, "none")
	if got := FindACT(act, led0); got != "" {
		t.Fatalf("no LED: %q", got)
	}
	if err := os.Rename(fakeACT(t), led0); err != nil {
		t.Fatal(err)
	}
	if got := FindACT(act, led0); got != led0 {
		t.Fatalf("older kernels call it led0: got %q", got)
	}
	if err := os.Rename(fakeACT(t), act); err != nil {
		t.Fatal(err)
	}
	if got := FindACT(none, act, led0); got != act {
		t.Fatalf("got %q, want %q", got, act)
	}
}
