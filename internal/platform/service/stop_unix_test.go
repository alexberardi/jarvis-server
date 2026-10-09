//go:build unix

package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// AD8b: a unit or LaunchDaemon an older version wrote restarts jarvisd on any exit, so the
// admin Stop button is refused (with the reinstall command) and status calls it stale until
// `service install` rewrites it.
func TestSystemdStopBlocker(t *testing.T) {
	for _, userMode := range []bool{false, true} {
		euid := 0
		if userMode {
			euid = 1000
		}
		s, _, _, root := testSystemd(t, userMode, euid)
		ctx := context.Background()
		if reason, _ := s.stopBlocker(); !strings.Contains(reason, "can't read") {
			t.Fatalf("user=%v no unit: %q", userMode, reason)
		}
		if err := s.Install(ctx, InstallOptions{Binary: "/opt/jarvisd/jarvisd", Home: filepath.Join(root, "data")}); err != nil {
			t.Fatal(err)
		}
		if reason, cmd := s.stopBlocker(); reason != "" || cmd != "" {
			t.Fatalf("user=%v fresh unit blocks stop: %q %q", userMode, reason, cmd)
		}
		unit, _ := os.ReadFile(s.unitPath)
		var old []string
		for _, line := range strings.Split(string(unit), "\n") {
			if !strings.HasPrefix(line, "RestartPreventExitStatus=") && !strings.HasPrefix(line, "SuccessExitStatus=") &&
				!strings.Contains(line, "admin Stop button") {
				old = append(old, line)
			}
		}
		if err := os.WriteFile(s.unitPath, []byte(strings.Join(old, "\n")), 0o644); err != nil {
			t.Fatal(err)
		}
		reason, cmd := s.stopBlocker()
		if !strings.Contains(reason, "older version") || cmd != InstallCommand(Systemd, userMode) {
			t.Fatalf("user=%v old unit: %q %q", userMode, reason, cmd)
		}
		if st, _ := s.Status(ctx); !st.Stale || !strings.Contains(st.StaleReason, "after an admin stop") {
			t.Fatalf("user=%v old unit status: %+v", userMode, st)
		}
		if err := s.Install(ctx, InstallOptions{Binary: "/opt/jarvisd/jarvisd", Home: s.InstalledHome()}); err != nil {
			t.Fatal(err)
		}
		if reason, _ := s.stopBlocker(); reason != "" {
			t.Fatalf("user=%v after reinstall: %q", userMode, reason)
		}
		if st, _ := s.Status(ctx); st.Stale {
			t.Fatalf("user=%v after reinstall: %+v", userMode, st)
		}
	}
}

func TestLaunchdStopBlocker(t *testing.T) {
	l, f, _, _ := testLaunchd(t, 0, "alex")
	f.fail["launchctl print "+target] = errors.New("Could not find service")
	ctx := context.Background()
	if reason, _ := l.stopBlocker(); !strings.Contains(reason, "can't read") {
		t.Fatalf("no plist: %q", reason)
	}
	if err := l.Install(ctx, InstallOptions{Binary: "/usr/local/bin/jarvisd", NoStart: true}); err != nil {
		t.Fatal(err)
	}
	if reason, cmd := l.stopBlocker(); reason != "" || cmd != "" {
		t.Fatalf("fresh plist blocks stop: %q %q", reason, cmd)
	}
	b, _ := os.ReadFile(l.plistPath)
	old := strings.Replace(string(b), "<dict>\n\t\t<key>SuccessfulExit</key>\n\t\t<false/>\n\t</dict>", "<true/>", 1)
	if old == string(b) {
		t.Fatal("no KeepAlive dict to replace")
	}
	if err := os.WriteFile(l.plistPath, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	if reason, cmd := l.stopBlocker(); !strings.Contains(reason, "older version") || cmd != "sudo jarvisd service install" {
		t.Fatalf("KeepAlive true: %q %q", reason, cmd)
	}
	if st, _ := l.Status(ctx); !st.Stale || !strings.Contains(st.StaleReason, "after an admin stop") {
		t.Fatalf("KeepAlive true status: %+v", st)
	}
}

// After an admin stop the job is still loaded (launchd only declined to restart it), so
// `jarvisd service start` must kickstart it rather than rely on bootstrap.
func TestLaunchdStartAfterAdminStop(t *testing.T) {
	l, f, _, _ := testLaunchd(t, 0, "alex")
	f.fail["launchctl print "+target] = errors.New("Could not find service")
	ctx := context.Background()
	if err := l.Install(ctx, InstallOptions{Binary: "/usr/local/bin/jarvisd", NoStart: true}); err != nil {
		t.Fatal(err)
	}
	// Loaded, not running, last exit 0.
	delete(f.fail, "launchctl print "+target)
	delete(f.fail, "launchctl print "+helperTarget)
	f.out["launchctl print "+target] = "system/net.jarvisautomation.jarvisd = {\n\tstate = not running\n\tlast exit code = 0\n}\n"
	f.calls = nil
	if err := l.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(f.calls, "launchctl bootstrap system "+l.plistPath) || !slices.Contains(f.calls, "launchctl kickstart "+target) {
		t.Fatalf("start of a loaded, stopped job: %q", f.calls)
	}
}

// systemd: the unit ends inactive after StopExitCode (SuccessExitStatus=), not failed, so a
// plain `systemctl start` (what `jarvisd service start` runs) brings it back; no reset-failed.
func TestSystemdStartAfterAdminStop(t *testing.T) {
	s, f, _, root := testSystemd(t, false, 0)
	ctx := context.Background()
	if err := s.Install(ctx, InstallOptions{Binary: "/opt/jarvisd/jarvisd", Home: filepath.Join(root, "data"), NoStart: true}); err != nil {
		t.Fatal(err)
	}
	unit, _ := os.ReadFile(s.unitPath)
	if !strings.Contains(string(unit), "\nSuccessExitStatus=98\n") || !strings.Contains(string(unit), "\nRestartPreventExitStatus=98\n") {
		t.Fatalf("unit:\n%s", unit)
	}
	f.calls = nil
	if err := s.Start(ctx); err != nil || !slices.Equal(f.calls, []string{"systemctl start " + unitName}) {
		t.Fatalf("start %v %q", err, f.calls)
	}
}
