package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// userSerial is the user's own dev emulator. The harness never sends it a command.
const userSerial = "emulator-5554"

// Runner runs adb with args and returns stdout. Swapped out in tests.
type Runner func(ctx context.Context, args ...string) ([]byte, error)

func execRunner(adb string) Runner {
	return func(ctx context.Context, args ...string) ([]byte, error) {
		var out, errb bytes.Buffer
		cmd := exec.CommandContext(ctx, adb, args...)
		cmd.Stdout, cmd.Stderr = &out, &errb
		if err := cmd.Run(); err != nil {
			return out.Bytes(), fmt.Errorf("adb %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
		}
		return out.Bytes(), nil
	}
}

// Device is one emulator, addressed by serial on every call (`adb -s`), so nothing else
// attached to the user's adb server is ever touched.
type Device struct {
	Serial string
	run    Runner
}

func NewDevice(serial string, run Runner) (*Device, error) {
	if serial == "" {
		return nil, errors.New("no serial")
	}
	if serial == userSerial {
		return nil, fmt.Errorf("refusing %s: that is the user's own emulator", serial)
	}
	return &Device{Serial: serial, run: run}, nil
}

func (d *Device) adb(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	return d.run(ctx, append([]string{"-s", d.Serial}, args...)...)
}

// Shell runs a device command line (already quoted for the device's sh).
func (d *Device) Shell(ctx context.Context, cmdline string) (string, error) {
	out, err := d.adb(ctx, "shell", cmdline)
	return strings.TrimSpace(string(out)), err
}

// Dump returns the current UI hierarchy. uiautomator gives up ("could not get idle state")
// while something animates; retried a few times.
func (d *Device) Dump(ctx context.Context) (*Screen, []byte, error) {
	var last error
	for i := 0; i < 4; i++ {
		if _, err := d.adb(ctx, "shell", "uiautomator dump /data/local/tmp/mobileshot.xml >/dev/null"); err != nil {
			last = err
			time.Sleep(500 * time.Millisecond)
			continue
		}
		raw, err := d.adb(ctx, "exec-out", "cat /data/local/tmp/mobileshot.xml")
		if err != nil {
			last = err
			continue
		}
		sc, err := ParseDump(raw)
		if err != nil {
			last = err
			time.Sleep(500 * time.Millisecond)
			continue
		}
		return sc, raw, nil
	}
	return nil, nil, last
}

// Screenshot writes a PNG of the display to path.
func (d *Device) Screenshot(ctx context.Context, path string) error {
	png, err := d.adb(ctx, "exec-out", "screencap -p")
	if err != nil {
		return err
	}
	if !bytes.HasPrefix(png, []byte("\x89PNG")) {
		return fmt.Errorf("screencap: not a PNG (%d bytes)", len(png))
	}
	return os.WriteFile(path, png, 0o644)
}

func (d *Device) Tap(ctx context.Context, n Node) error {
	x, y := n.Bounds.Center()
	_, err := d.Shell(ctx, fmt.Sprintf("input tap %d %d", x, y))
	return err
}

// Type types into the focused field.
func (d *Device) Type(ctx context.Context, s string) error {
	_, err := d.Shell(ctx, "input text "+inputText(s))
	return err
}

func (d *Device) Key(ctx context.Context, key string) error {
	_, err := d.Shell(ctx, "input keyevent "+key)
	return err
}

// Swipe scrolls: a drag from (x,y1) to (x,y2) over ms milliseconds.
func (d *Device) Swipe(ctx context.Context, x, y1, y2, ms int) error {
	_, err := d.Shell(ctx, fmt.Sprintf("input swipe %d %d %d %d %d", x, y1, x, y2, ms))
	return err
}

// Look sets the system theme (the app follows it) and font scale.
func (d *Device) Look(ctx context.Context, theme string, fontScale string) error {
	night := "no"
	if theme == "dark" {
		night = "yes"
	}
	if _, err := d.Shell(ctx, "cmd uimode night "+night); err != nil {
		return err
	}
	if _, err := d.Shell(ctx, "settings put system font_scale "+shellQuote(fontScale)); err != nil {
		return err
	}
	d.FreezeStatusBar(ctx)
	return nil
}

// Launch cold-starts the app (a theme or font change applies cleanly; the session survives in
// SecureStore).
func (d *Device) Launch(ctx context.Context, pkg string) error {
	if _, err := d.Shell(ctx, "am force-stop "+shellQuote(pkg)); err != nil {
		return err
	}
	_, err := d.Shell(ctx, "monkey -p "+shellQuote(pkg)+" -c android.intent.category.LAUNCHER 1 >/dev/null 2>&1")
	d.FreezeStatusBar(ctx)
	return err
}

// FreezeStatusBar puts SystemUI in demo mode (clock 09:41, no notification icons) so shots
// differ only by the app. A night-mode switch restarts SystemUI, which drops demo mode, so it
// is reapplied after every theme change and launch. Best effort.
func (d *Device) FreezeStatusBar(ctx context.Context) {
	_, _ = d.Shell(ctx, "settings put global sysui_demo_allowed 1; "+
		"am broadcast -a com.android.systemui.demo -e command enter >/dev/null; "+
		"am broadcast -a com.android.systemui.demo -e command clock -e hhmm 0941 >/dev/null; "+
		"am broadcast -a com.android.systemui.demo -e command notifications -e visible false >/dev/null")
}

// Logcat returns the app's recent log lines (React Native's console goes to ReactNativeJS).
func (d *Device) Logcat(ctx context.Context) string {
	out, _ := d.adb(ctx, "logcat", "-d", "-t", "400", "-s", "ReactNativeJS:V", "AndroidRuntime:E")
	return string(out)
}
