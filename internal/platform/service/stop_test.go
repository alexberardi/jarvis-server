package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// AD8b: a stop request ends serve with an exit status the supervisor leaves alone.
func TestRestarterStop(t *testing.T) {
	for _, tc := range []struct {
		kind Kind
		code int
	}{
		{Systemd, StopExitCode}, // RestartPreventExitStatus= in the unit
		{Launchd, 0},            // KeepAlive SuccessfulExit=false
		{SCM, 0},                // recovery actions fire on a non-zero exit only
		{None, 0},               // nothing restarts it anyway
	} {
		ctx, cancel := context.WithCancel(context.Background())
		r := NewRestarter(tc.kind, cancel)
		if err := r.RequestStop(); err != nil {
			t.Fatalf("%s: %v", tc.kind, err)
		}
		if ctx.Err() == nil || !r.StopRequested() || r.Requested() {
			t.Fatalf("%s: stop did not end serve (or counted as a restart)", tc.kind)
		}
		// A restart asked for after the stop doesn't turn it into one: the first request wins.
		_ = r.Request()
		err := r.Err(nil)
		if !errors.Is(err, ErrStop) || errors.Is(err, ErrRestart) || !Requested(err) || ExitCode(err) != tc.code {
			t.Fatalf("%s: err %v code %d, want %d", tc.kind, err, ExitCode(err), tc.code)
		}
		if ExitCode(fmt.Errorf("wrapped: %w", err)) != tc.code {
			t.Fatalf("%s: wrapped stop", tc.kind)
		}
	}
	// A restart already under way stays a restart.
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := NewRestarter(Systemd, cancel)
	_ = r.Request()
	_ = r.RequestStop()
	if err := r.Err(nil); !errors.Is(err, ErrRestart) || r.StopRequested() {
		t.Fatalf("restart then stop: %v", err)
	}
	if Requested(errors.New("boom")) || Requested(nil) {
		t.Fatal("a failure is not a request")
	}
}

// The code must not collide with what the supervisors and jarvisd already use.
func TestStopExitCode(t *testing.T) {
	if StopExitCode == RestartExitCode || StopExitCode <= 2 || (StopExitCode >= 64 && StopExitCode <= 78) ||
		StopExitCode >= 126 {
		t.Fatalf("StopExitCode %d collides", StopExitCode)
	}
}

// What the Windows service handler reports (OS-independent, so it is tested everywhere): a
// stop is a clean SERVICE_STOPPED with exit 0, which the recovery actions ignore; a restart or a
// failure is a service-specific code, which they act on.
func TestSCMExit(t *testing.T) {
	stop := NewRestarter(SCM, func() {})
	_ = stop.RequestStop()
	restart := NewRestarter(SCM, func() {})
	_ = restart.Request()
	for _, tc := range []struct {
		name     string
		err      error
		specific bool
		code     uint32
	}{
		{"clean stop through the SCM", nil, false, 0},
		{"admin stop", stop.Err(nil), false, 0},
		{"restart", restart.Err(nil), true, RestartExitCode},
		{"failure", errors.New("boom"), true, 1},
	} {
		specific, code := scmExit(tc.err)
		if specific != tc.specific || code != tc.code {
			t.Errorf("%s: (%v, %d), want (%v, %d)", tc.name, specific, code, tc.specific, tc.code)
		}
	}
}

func TestUnitStopReady(t *testing.T) {
	unit, err := RenderSystemd(Unit{Binary: "/usr/local/bin/jarvisd", Home: "/var/lib/jarvisd"})
	if err != nil {
		t.Fatal(err)
	}
	if !unitStopReady(unit) {
		t.Fatalf("the rendered unit does not keep jarvisd stopped:\n%s", unit)
	}
	user, _ := RenderSystemd(Unit{Binary: "/usr/local/bin/jarvisd", Home: "/home/a/.jarvisd", User: true})
	if !unitStopReady(user) {
		t.Fatal("the --user unit does not keep jarvisd stopped")
	}
	for name, u := range map[string]string{
		"before AD8b":       "[Service]\nRestart=always\n",
		"other codes":       "[Service]\nRestartPreventExitStatus=97 SIGKILL\n",
		"reset by override": "[Service]\nRestartPreventExitStatus=98\nRestartPreventExitStatus=\n",
	} {
		if unitStopReady([]byte(u)) {
			t.Errorf("%s: ready", name)
		}
	}
	if !unitStopReady([]byte("RestartPreventExitStatus=SIGKILL\nRestartPreventExitStatus= 1 98\n")) {
		t.Error("accumulated list")
	}
}

func TestPlistStopReady(t *testing.T) {
	p, err := RenderLaunchd(Plist{Binary: "/usr/local/bin/jarvisd", Home: "/Users/alex/.jarvisd", UserName: "alex", UserHome: "/Users/alex"})
	if err != nil {
		t.Fatal(err)
	}
	if !plistStopReady(p) || !strings.Contains(string(p), "<key>RunAtLoad</key>\n\t<true/>") {
		t.Fatalf("the rendered plist does not keep jarvisd stopped or start at boot:\n%s", p)
	}
	old := strings.Replace(string(p), "<dict>\n\t\t<key>SuccessfulExit</key>\n\t\t<false/>\n\t</dict>", "<true/>", 1)
	if old == string(p) || plistStopReady([]byte(old)) {
		t.Fatal("KeepAlive true counted as ready")
	}
	if plistStopReady([]byte(strings.Replace(string(p), "<false/>\n\t</dict>", "<true/>\n\t</dict>", 1))) {
		t.Fatal("SuccessfulExit true counted as ready")
	}
}

func TestInstallAndStopCommands(t *testing.T) {
	for _, tc := range []struct {
		kind          Kind
		user          bool
		install, stop string
	}{
		{Systemd, false, "sudo jarvisd service install", "sudo jarvisd service stop"},
		{Systemd, true, "jarvisd service install --user", "jarvisd service stop --user"},
		{Launchd, false, "sudo jarvisd service install", "sudo jarvisd service stop"},
		{SCM, false, "jarvisd service install", "jarvisd service stop"},
	} {
		if got := InstallCommand(tc.kind, tc.user); got != tc.install {
			t.Errorf("%s user=%v install %q", tc.kind, tc.user, got)
		}
		if got := stopCommand(tc.kind, tc.user); got != tc.stop {
			t.Errorf("%s user=%v stop %q", tc.kind, tc.user, got)
		}
	}
}
