package host

import (
	"errors"
	"strings"
	"testing"
)

// Platform-specific behaviour the shared contract can't express.

func TestLinuxWaylandHasOpenAndVolumeButNoWindows(t *testing.T) {
	s := linuxFull()
	s.env = map[string]string{"WAYLAND_DISPLAY": "wayland-1", "DISPLAY": ":1"} // XWayland too
	caps, _ := New(NewLinux(s)).Capabilities(ctx)
	if strings.Join(caps, ",") != "open,volume.get,volume.set" {
		t.Fatalf("caps = %v", caps)
	}
}

func TestLinuxHeadlessHasVolumeOnly(t *testing.T) {
	s := linuxFull()
	s.env = map[string]string{}
	caps, _ := New(NewLinux(s)).Capabilities(ctx)
	if strings.Join(caps, ",") != "volume.get,volume.set" {
		t.Fatalf("caps = %v", caps)
	}
}

func TestLinuxMinimizeNeedsXdotool(t *testing.T) {
	s := linuxFull()
	delete(s.bins, "xdotool")
	h := New(NewLinux(s))
	if h.Has(ctx, CapWindowsMinimize) {
		t.Fatal("minimise advertised without xdotool")
	}
	if !h.Has(ctx, CapWindowsClose) {
		t.Fatal("close should not need xdotool")
	}
}

func TestLinuxWpctlAndAmixer(t *testing.T) {
	for _, tc := range []struct {
		bin, out string
		want     int
	}{
		{"wpctl", "Volume: 0.37", 37},
		{"wpctl", "Volume: 0.50 [MUTED]", 50},
		{"amixer", "Simple mixer control 'Master',0\n  Mono: Playback 42 [66%] [-21.00dB] [on]", 66},
	} {
		s := &fakeSys{bins: map[string]bool{tc.bin: true}, env: map[string]string{},
			handler: func(string, []string) (string, error) { return tc.out, nil }}
		v, err := New(NewLinux(s)).GetVolume(ctx)
		if err != nil || v != tc.want {
			t.Fatalf("%s %q: got %d, %v; want %d", tc.bin, tc.out, v, err, tc.want)
		}
		if _, err := New(NewLinux(s)).SetVolume(ctx, 120); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(s.allCommands(), " 100%") {
			t.Fatalf("%s set did not receive 100%%:\n%s", tc.bin, s.allCommands())
		}
	}
}

func TestLinuxUnparseableVolumeIsOperationFailed(t *testing.T) {
	s := &fakeSys{bins: map[string]bool{"pactl": true}, env: map[string]string{},
		handler: func(string, []string) (string, error) { return "garbage", nil }}
	if _, err := New(NewLinux(s)).GetVolume(ctx); !errors.Is(err, ErrOperationFailed) {
		t.Fatalf("err = %v", err)
	}
}

func TestLinuxWmctrlTitlesKeepSpacing(t *testing.T) {
	ws, err := New(NewLinux(linuxFull())).ListWindows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ws[0].Title != "Mozilla Firefox — a  title" || ws[0].App != "firefox.Firefox" {
		t.Fatalf("parsed %+v", ws[0])
	}
}

func TestDarwinWithoutAccessibilityHasNoWindowControl(t *testing.T) {
	s := macFull()
	inner := s.handler
	s.handler = func(name string, args []string) (string, error) {
		if strings.Contains(args[len(args)-1], "System Events") {
			return "", &RunError{Name: "osascript", Msg: "execution error: System Events got an error: osascript is not allowed assistive access. (-25211)"}
		}
		return inner(name, args)
	}
	caps, _ := New(NewDarwin(s)).Capabilities(ctx)
	if strings.Join(caps, ",") != "open,volume.get,volume.set" {
		t.Fatalf("caps = %v", caps)
	}
	// And a direct call maps the TCC denial to NotSupported, not a generic failure.
	if _, err := NewDarwin(s).ListWindows(ctx); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("err = %v", err)
	}
}

func TestDarwinMissingVolumeIsOperationFailed(t *testing.T) {
	s := &fakeSys{bins: map[string]bool{"osascript": true}, env: map[string]string{},
		handler: func(string, []string) (string, error) { return "missing value", nil }}
	if _, err := NewDarwin(s).GetVolume(ctx); !errors.Is(err, ErrOperationFailed) {
		t.Fatalf("err = %v", err)
	}
}

func TestWindowsWithoutDesktopKeepsVolume(t *testing.T) {
	w := winFull()
	w.desktop = false // Session 0: a service or a SYSTEM task
	caps, _ := New(NewWindows(w)).Capabilities(ctx)
	if strings.Join(caps, ",") != "volume.get,volume.set" {
		t.Fatalf("caps = %v", caps)
	}
}

func TestLinuxLaunchFallsBackToGtkLaunch(t *testing.T) {
	s := linuxFull()
	delete(s.bins, "gio")
	s.bins["gtk-launch"] = true
	if err := New(NewLinux(s)).LaunchApp(ctx, "/usr/share/applications/org.gnome.Calculator.desktop"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(s.starts, ";") != "gtk-launch org.gnome.Calculator" {
		t.Fatalf("starts = %v", s.starts)
	}
	delete(s.bins, "gtk-launch")
	if err := New(NewLinux(s)).LaunchApp(ctx, "/x/y.desktop"); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("no launcher: err = %v", err)
	}
}

func TestAbsoluteByBackendPlatformNotRunner(t *testing.T) {
	for _, tc := range []struct {
		platform, path string
		want           bool
	}{
		{"linux", "/usr/share/applications/x.desktop", true},
		{"darwin", "/Applications/Steam.app", true},
		{"darwin", "Steam.app", false},
		{"darwin", `C:\Steam.app`, false},
		{"windows", `C:\ProgramData\Steam.lnk`, true},
		{"windows", `c:/Users/x/Steam.lnk`, true},
		{"windows", `\\server\share\Steam.lnk`, true},
		{"windows", "/Applications/Steam.app", false},
		{"windows", `Steam.lnk`, false},
		{"windows", `1:\x`, false},
	} {
		if got := absFor(tc.platform, tc.path); got != tc.want {
			t.Errorf("absFor(%s, %q) = %v", tc.platform, tc.path, got)
		}
	}
}
