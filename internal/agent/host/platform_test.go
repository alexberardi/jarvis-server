package host

import (
	"errors"
	"slices"
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

// The built-in EWMH client is preferred over wmctrl/xdotool whenever the window manager
// supports it, needs neither, and is never used outside an X11 session.
func TestLinuxPrefersBuiltInEWMH(t *testing.T) {
	s, x := linuxEWMH()
	s.bins["wmctrl"], s.bins["xdotool"] = true, true // installed, and still not used
	h := New(&Linux{Sys: s, X: x})
	caps, _ := h.Capabilities(ctx)
	for _, c := range AllWindowCaps {
		if !slices.Contains(caps, c) {
			t.Fatalf("caps %v lack %s", caps, c)
		}
	}
	ws, err := h.ListWindows(ctx)
	if err != nil || len(ws) != 2 || ws[0] != (Window{ID: "0x03a00007", Title: "Mozilla Firefox — a  title", App: "firefox.Firefox"}) || !ws[1].Minimized {
		t.Fatalf("%+v %v", ws, err)
	}
	for _, a := range []WindowAction{ActionFocus, ActionMinimize, ActionClose} {
		if err := h.WindowDo(ctx, a, "0x3a00007"); err != nil { // unpadded, as wmctrl-era callers may pass
			t.Fatalf("%s: %v", a, err)
		}
	}
	cmds := s.allCommands()
	for _, want := range []string{"x11 activate 0x03a00007", "x11 iconify 0x03a00007", "x11 close 0x03a00007"} {
		if !strings.Contains(cmds, want) {
			t.Fatalf("no %q in\n%s", want, cmds)
		}
	}
	if strings.Contains(cmds, "wmctrl") || strings.Contains(cmds, "xdotool") {
		t.Fatalf("ran a helper:\n%s", cmds)
	}

	// A failure is an operation failure, not a silent fallback to a helper.
	x.err = errors.New("BadWindow")
	if err := h.WindowDo(ctx, ActionFocus, linuxWinID); !errors.Is(err, ErrOperationFailed) {
		t.Fatalf("err %v, want ErrOperationFailed", err)
	}
}

func TestLinuxEWMHFallsBackToTheHelpers(t *testing.T) {
	// No EWMH window manager: wmctrl as before.
	s := linuxFull()
	x := &fakeX{sys: s, why: "no EWMH window manager is running"}
	h := New(&Linux{Sys: s, X: x})
	if err := h.WindowDo(ctx, ActionClose, linuxWinID); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s.allCommands(), "wmctrl -i -c "+linuxWinID) {
		t.Fatalf("no wmctrl:\n%s", s.allCommands())
	}
	// Neither: no window control.
	s2, x2 := linuxEWMH()
	x2.wm = false
	if caps, _ := New(&Linux{Sys: s2, X: x2}).Capabilities(ctx); slices.Contains(caps, CapWindowsList) {
		t.Fatalf("caps %v", caps)
	}
	// Wayland (XWayland's DISPLAY too): the X server is never asked.
	s3, x3 := linuxEWMH()
	s3.env["WAYLAND_DISPLAY"] = "wayland-1"
	if caps, _ := New(&Linux{Sys: s3, X: x3}).Capabilities(ctx); slices.Contains(caps, CapWindowsList) {
		t.Fatalf("caps %v", caps)
	}
	if strings.Contains(s3.allCommands(), "x11 ") {
		t.Fatalf("probed X on Wayland:\n%s", s3.allCommands())
	}
}
