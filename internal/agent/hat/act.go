package hat

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// The ACT-LED fallback ports N:services/led_service.py: a Pi without the HAT shows the node's
// state on its own green activity LED. One colour shows little: off, normal (on), alert (a 1 Hz
// blink), error (~3 Hz) and shutdown_warning (5 Hz) look different, and the command-flow
// overlays (wake_detected … muted) show the stable pattern instead. The Go node's setup mode
// (#292) blinks slowly. led_brightness_percent has no effect (Python's set_brightness_scale is a
// no-op). Raspberry Pi OS's udev rule (99-com.rules) lets the gpio group write the LED, which the
// node's user is in for the button.

// ACTDirs are the activity LED's sysfs directories: "ACT" on current kernels, "led0" on older
// ones.
var ACTDirs = []string{"/sys/class/leds/ACT", "/sys/class/leds/led0"}

// actBlink is how long a blinking pattern stays on, then off (Python's _blink_period).
var actBlink = map[string]float64{Alert: 0.5, Error: 0.15, ShutdownWarning: 0.1, Setup: 1.5}

// actOn is the kernel trigger that keeps the LED lit (Python's "normal").
const actOn = "default-on"

// actState is the trigger and brightness for pattern name at t seconds ("" brightness: the
// trigger sets it).
func actState(name string, t float64) (trigger, brightness string) {
	if name == Off {
		return "none", "0"
	}
	if half, ok := actBlink[name]; ok {
		if math.Mod(t, 2*half) < half {
			return "none", "1"
		}
		return "none", "0"
	}
	return actOn, ""
}

// act is the ACT LED in dir. Only the renderer goroutine writes it, then Close.
type act struct {
	dir     string
	restore string // the trigger found at start, put back by close
	trigger string // the trigger last written
}

func (act) pick(stable, transient string) string {
	switch transient {
	case "", WakeDetected, Listening, Thinking, Speaking, NotForMe, Muted:
		return stable
	}
	return transient
}

func (act) encode(name string, t float64, _ int) []byte {
	trig, br := actState(name, t)
	return []byte(trig + "\n" + br)
}

func (act) animated(name string) bool { _, ok := actBlink[name]; return ok }

func (a *act) write(b []byte) error {
	trig, br, _ := strings.Cut(string(b), "\n")
	if trig != a.trigger {
		if err := a.put("trigger", trig); err != nil {
			return err
		}
		a.trigger = trig
	}
	if br == "" {
		return nil
	}
	return a.put("brightness", br)
}

func (a *act) put(name, v string) error {
	return os.WriteFile(filepath.Join(a.dir, name), []byte(v), 0)
}

func (a *act) close() error { return a.put("trigger", a.restore) }

// FindACT is the first of dirs (ACTDirs) that holds an LED, or "".
func FindACT(dirs ...string) string {
	for _, d := range dirs {
		if isLED(d) {
			return d
		}
	}
	return ""
}

func isLED(dir string) bool {
	for _, f := range []string{"trigger", "brightness"} {
		if st, err := os.Stat(filepath.Join(dir, f)); err != nil || st.IsDir() {
			return false
		}
	}
	return true
}

// NewACTLEDs starts rendering to the ACT LED in dir: "normal" (lit) until told otherwise. Close
// gives the LED back to the trigger it had (mmc0, actpwr, …).
func NewACTLEDs(dir string, log *slog.Logger) (*LEDs, error) {
	if !isLED(dir) {
		return nil, fmt.Errorf("%s: not an LED", dir)
	}
	b, err := os.ReadFile(filepath.Join(dir, "trigger"))
	if err != nil {
		return nil, err
	}
	restore := currentTrigger(b)
	if restore == "" {
		return nil, fmt.Errorf("%s/trigger: no current trigger in %q", dir, bytes.TrimSpace(b))
	}
	// Writing what is there already tells a read-only LED from one this user may drive.
	if err := os.WriteFile(filepath.Join(dir, "trigger"), []byte(restore), 0); err != nil {
		if errors.Is(err, os.ErrPermission) {
			return nil, fmt.Errorf("%s: not writable by this user (the gpio group, on Raspberry Pi OS): %w", dir, err)
		}
		return nil, err
	}
	return newLEDs(&act{dir: dir, restore: restore, trigger: restore}, log), nil
}

// currentTrigger is the bracketed entry of a sysfs trigger list ("none [mmc0] timer").
func currentTrigger(list []byte) string {
	for _, f := range strings.Fields(string(list)) {
		if len(f) > 2 && f[0] == '[' && f[len(f)-1] == ']' {
			return f[1 : len(f)-1]
		}
	}
	return ""
}
