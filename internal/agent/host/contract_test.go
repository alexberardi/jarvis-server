package host

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
)

// The contract suite: one set of assertions, every backend, on every OS. It is what catches a
// Windows-only mistake on the Linux runner, so backends must stay constructible everywhere.

type recorder interface{ allCommands() string }

type subject struct {
	name     string
	platform string
	winID    string
	full     func() (Backend, recorder)
	empty    func() (Backend, recorder)
	// lacks: capabilities this platform has no native version of, so even a full host must not
	// advertise them (Hyprland and sway have no minimise).
	lacks []string
}

// arranging is the arranging capabilities, which the X11, macOS and Windows backends don't have
// yet.
var arranging = []string{CapWindowsUnminimize, CapWindowsMaximize, CapWindowsUnmaximize,
	CapWindowsFullscreen, CapWindowsSnap, CapWindowsMonitor, CapWindowsWorkspace}

// do runs any window action, with a target for the moves.
func do(h *Host, a WindowAction, id string) error {
	switch a {
	case ActionMoveToWorkspace:
		return h.WindowMove(ctx, a, id, "2")
	case ActionMoveToMonitor:
		return h.WindowMove(ctx, a, id, "next")
	}
	return h.WindowDo(ctx, a, id)
}

func (s subject) wantCaps() []string {
	var want []string
	for c := range KnownCapabilities {
		if !slices.Contains(s.lacks, c) {
			want = append(want, c)
		}
	}
	sort.Strings(want)
	return want
}

var subjects = []subject{
	{"linux", "linux", linuxWinID,
		func() (Backend, recorder) { s := linuxFull(); return NewLinux(s), s },
		func() (Backend, recorder) { s := linuxEmpty(); return NewLinux(s), s }, arranging},
	{"linux-ewmh", "linux", linuxWinID,
		func() (Backend, recorder) { s, x := linuxEWMH(); return &Linux{Sys: s, X: x}, s },
		func() (Backend, recorder) { s := linuxEmpty(); return &Linux{Sys: s, X: &fakeX{sys: s, wm: true}}, s }, arranging},
	{"linux-hyprland", "linux", hyprWinID,
		func() (Backend, recorder) { s := linuxHyprland(); return NewLinux(s), s },
		func() (Backend, recorder) { s := linuxEmpty(); return NewLinux(s), s },
		[]string{CapWindowsMinimize, CapWindowsUnminimize}},
	{"linux-sway", "linux", swayWinID,
		func() (Backend, recorder) { s := linuxSway(); return NewLinux(s), s },
		func() (Backend, recorder) { s := linuxEmpty(); return NewLinux(s), s },
		[]string{CapWindowsMinimize, CapWindowsUnminimize, CapWindowsMaximize}},
	{"darwin", "darwin", macWinID,
		func() (Backend, recorder) { d, s, _ := macDesktop(); return d, s },
		func() (Backend, recorder) { s := macEmpty(); return NewDarwin(s), s }, arranging},
	{"windows", "windows", winWinID,
		func() (Backend, recorder) { w := winFull(); return NewWindows(w), w },
		func() (Backend, recorder) { w := winEmpty(); return NewWindows(w), w }, arranging},
}

func forEach(t *testing.T, f func(t *testing.T, s subject)) {
	for _, s := range subjects {
		t.Run(s.name, func(t *testing.T) { f(t, s) })
	}
}

var ctx = context.Background()

func TestConstructionTouchesNothing(t *testing.T) {
	s := linuxFull()
	_ = NewLinux(s)
	m := macFull()
	_ = NewDarwin(m)
	w := winFull()
	_ = NewWindows(w)
	if len(s.calls)+len(m.calls)+len(w.calls) != 0 {
		t.Fatalf("construction probed the OS: %v %v %v", s.calls, m.calls, w.calls)
	}
}

func TestPlatformName(t *testing.T) {
	forEach(t, func(t *testing.T, s subject) {
		b, _ := s.full()
		if b.Platform() != s.platform {
			t.Fatalf("Platform() = %q, want %q", b.Platform(), s.platform)
		}
	})
}

func TestCapabilitiesAreFromTheClosedSet(t *testing.T) {
	forEach(t, func(t *testing.T, s subject) {
		for _, mk := range []func() (Backend, recorder){s.full, s.empty} {
			b, _ := mk()
			for _, c := range b.Capabilities(ctx) {
				if !KnownCapabilities[c] {
					t.Fatalf("unknown capability %q", c)
				}
			}
		}
	})
}

func TestFullHostHasEveryCapability(t *testing.T) {
	forEach(t, func(t *testing.T, s subject) {
		b, _ := s.full()
		got, err := New(b).Capabilities(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if want := s.wantCaps(); !reflect.DeepEqual(got, want) {
			t.Fatalf("capabilities = %v, want %v", got, want)
		}
	})
}

func TestEmptyHostRefusesEverythingUniformly(t *testing.T) {
	forEach(t, func(t *testing.T, s subject) {
		b, _ := s.empty()
		h := New(b)
		caps, _ := h.Capabilities(ctx)
		if len(caps) != 0 {
			t.Fatalf("empty host advertises %v", caps)
		}
		_, e1 := h.GetVolume(ctx)
		_, e2 := h.SetVolume(ctx, 50)
		_, e3 := h.ListWindows(ctx)
		e4 := h.WindowDo(ctx, ActionClose, s.winID)
		e5 := h.Open(ctx, "https://example.com")
		for i, err := range []error{e1, e2, e3, e4, e5} {
			if !errors.Is(err, ErrNotSupported) {
				t.Fatalf("op %d: err = %v, want ErrNotSupported", i, err)
			}
		}
	})
}

func TestVolumeClampsNeverErrors(t *testing.T) {
	forEach(t, func(t *testing.T, s subject) {
		for _, tc := range []struct{ in, want int }{{150, 100}, {-5, 0}, {37, 37}, {0, 0}, {100, 100}} {
			b, _ := s.full()
			h := New(b)
			applied, err := h.SetVolume(ctx, tc.in)
			if err != nil {
				t.Fatalf("SetVolume(%d): %v", tc.in, err)
			}
			if applied != tc.want {
				t.Fatalf("SetVolume(%d) applied %d, want %d", tc.in, applied, tc.want)
			}
			// The level that reached the platform is the clamped one, and reads back.
			got, err := h.GetVolume(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("after SetVolume(%d) GetVolume = %d, want %d", tc.in, got, tc.want)
			}
		}
	})
}

func TestListWindowsReturnsOpaqueIDs(t *testing.T) {
	forEach(t, func(t *testing.T, s subject) {
		b, _ := s.full()
		ws, err := New(b).ListWindows(ctx)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, w := range ws {
			if w.ID == "" {
				t.Fatalf("empty window id in %+v", ws)
			}
			if w.ID == s.winID {
				found = true
			}
		}
		if !found {
			t.Fatalf("window %s not listed in %+v", s.winID, ws)
		}
	})
}

// A backend that lists an action does it; one that doesn't answers ErrNotSupported (native
// actions only: no stand-ins).
func TestWindowActionsSucceedOnALiveWindow(t *testing.T) {
	forEach(t, func(t *testing.T, s subject) {
		for _, a := range WindowActions {
			b, _ := s.full()
			err := do(New(b), a, s.winID)
			if slices.Contains(s.lacks, ActionCapability[a]) {
				if !errors.Is(err, ErrNotSupported) {
					t.Fatalf("%s: err = %v, want ErrNotSupported", a, err)
				}
				continue
			}
			if err != nil {
				t.Fatalf("%s: %v", a, err)
			}
		}
	})
}

// Hostile or stale ids are WindowNotFound, and never reach a subprocess or script.
func TestBadWindowIDsAreNotFoundAndNeverExecuted(t *testing.T) {
	bad := []string{
		`1"; do shell script "touch /tmp/pwned`,
		"$(reboot)",
		"0xZZZ",
		"-1",
		"99999/99", // well-formed but gone
		"0x7fffffff",
		"424242",
	}
	forEach(t, func(t *testing.T, s subject) {
		for _, id := range bad {
			b, rec := s.full()
			err := New(b).WindowDo(ctx, ActionClose, id)
			if !errors.Is(err, ErrWindowNotFound) {
				t.Fatalf("id %q: err = %v, want ErrWindowNotFound", id, err)
			}
			if strings.Contains(rec.allCommands(), "pwned") || strings.Contains(rec.allCommands(), "reboot") {
				t.Fatalf("id %q reached a command:\n%s", id, rec.allCommands())
			}
		}
	})
}

// Close is a graceful request on every platform: no kill, no quit, no terminate.
func TestCloseIsGraceful(t *testing.T) {
	forEach(t, func(t *testing.T, s subject) {
		b, rec := s.full()
		if err := New(b).WindowDo(ctx, ActionClose, s.winID); err != nil {
			t.Fatal(err)
		}
		graceful := map[string]string{"linux": "wmctrl -i -c " + s.winID, "linux-ewmh": "x11 close 0x03a00007",
			"linux-hyprland": "hyprctl dispatch closewindow address:" + s.winID,
			// sway's kill is its close command: it sends the app its close event, never a signal.
			"linux-sway": "swaymsg [con_id=" + s.winID + "] kill",
			"darwin":     "ax close 4242/1", "windows": "postclose 1311"}[s.name]
		cmds := strings.ToLower(strings.ReplaceAll(rec.allCommands(), graceful, ""))
		for _, banned := range []string{"kill", "quit", "terminate", "-9", "xdotool windowclose"} {
			if strings.Contains(cmds, banned) {
				t.Fatalf("close used %q:\n%s", banned, cmds)
			}
		}
		if !strings.Contains(rec.allCommands(), graceful) {
			t.Fatalf("close did not issue %q:\n%s", graceful, rec.allCommands())
		}
	})
}

// Open is fire-and-forget: launched, never waited on.
func TestOpenIsFireAndForget(t *testing.T) {
	forEach(t, func(t *testing.T, s subject) {
		b, rec := s.full()
		if err := New(b).Open(ctx, "https://example.com"); err != nil {
			t.Fatal(err)
		}
		cmds := rec.allCommands()
		switch s.platform {
		case "linux", "darwin":
			fs := map[string]*fakeSys{}
			_ = fs
			sys := rec.(*fakeSys)
			if len(sys.starts) != 1 || !strings.HasSuffix(sys.starts[0], " https://example.com") {
				t.Fatalf("open did not Start exactly once: starts=%v", sys.starts)
			}
			for _, r := range sys.runs {
				if strings.Contains(r, "example.com") {
					t.Fatalf("open waited on a Run: %s", r)
				}
			}
		case "windows":
			if !strings.Contains(cmds, "shellopen https://example.com") {
				t.Fatalf("open did not ShellExecute:\n%s", cmds)
			}
		}
	})
}

// A backend exposes exactly the Backend interface: a platform-only public method is how
// callers start depending on one platform.
func TestBackendsExposeNoExtraPublicMethods(t *testing.T) {
	iface := reflect.TypeOf((*Backend)(nil)).Elem()
	want := map[string]bool{}
	for i := 0; i < iface.NumMethod(); i++ {
		want[iface.Method(i).Name] = true
	}
	for _, b := range []Backend{NewLinux(linuxEmpty()), NewDarwin(macEmpty()), NewWindows(winEmpty())} {
		ty := reflect.TypeOf(b)
		for i := 0; i < ty.NumMethod(); i++ {
			if name := ty.Method(i).Name; !want[name] {
				t.Errorf("%s has public method %s outside Backend", ty, name)
			}
		}
	}
}

func TestFactoryRefusesUnknownPlatforms(t *testing.T) {
	if _, err := ForOS("plan9", OSSys{}, NewWin32()); err == nil {
		t.Fatal("ForOS(plan9) did not fail")
	}
	for _, goos := range []string{"linux", "darwin", "windows"} {
		b, err := ForOS(goos, linuxEmpty(), winEmpty())
		if err != nil || b.Platform() != goos {
			t.Fatalf("ForOS(%s) = %v, %v", goos, b, err)
		}
	}
}

type liar struct{ Backend }

func (liar) Capabilities(context.Context) []string { return []string{"volume.gett"} }

func TestHostRejectsUnknownCapabilityNames(t *testing.T) {
	h := New(liar{NewLinux(linuxEmpty())})
	if _, err := h.Capabilities(ctx); err == nil {
		t.Fatal("a typo'd capability escaped")
	}
	if h.Has(ctx, "volume.gett") {
		t.Fatal("Has trusted a typo'd capability")
	}
}

// A gate that is skipped would let the backend's own error leak out instead of the uniform one.
func TestCapabilityGateRunsBeforeTheBackend(t *testing.T) {
	s := linuxFull()
	s.env = map[string]string{"WAYLAND_DISPLAY": "wayland-1"} // session, but no X11
	h := New(NewLinux(s))
	err := h.WindowDo(ctx, ActionFocus, linuxWinID)
	if !errors.Is(err, ErrNotSupported) {
		t.Fatalf("err = %v, want ErrNotSupported", err)
	}
	if strings.Contains(s.allCommands(), "wmctrl") {
		t.Fatalf("backend ran despite the gate:\n%s", s.allCommands())
	}
}

// LaunchApp is fire-and-forget, launches exactly the catalogue path, refuses relative paths,
// and is gated like open.
func TestLaunchAppIsFireAndForget(t *testing.T) {
	forEach(t, func(t *testing.T, s subject) {
		app := map[string]string{"linux": "/usr/share/applications/steam.desktop", "darwin": "/Applications/Steam.app",
			"windows": `C:\ProgramData\Microsoft\Windows\Start Menu\Programs\Steam.lnk`}[s.platform]
		b, rec := s.full()
		if err := New(b).LaunchApp(ctx, app); err != nil {
			t.Fatal(err)
		}
		cmds := rec.allCommands()
		if !strings.Contains(cmds, app) {
			t.Fatalf("launch did not use the catalogue path:\n%s", cmds)
		}
		if sys, ok := rec.(*fakeSys); ok {
			if len(sys.starts) != 1 {
				t.Fatalf("launch did not Start exactly once: %v", sys.starts)
			}
			for _, r := range sys.runs {
				if strings.Contains(r, app) {
					t.Fatalf("launch waited on a Run: %s", r)
				}
			}
		}
		b2, rec2 := s.full()
		for _, rel := range []string{"Steam", `Steam\x.lnk`, "./Steam.app"} {
			if err := New(b2).LaunchApp(ctx, rel); !errors.Is(err, ErrInvalidTarget) {
				t.Fatalf("relative path %q: err = %v", rel, err)
			}
		}
		if err := New(b2).LaunchApp(ctx, "Steam"); !errors.Is(err, ErrInvalidTarget) {
			t.Fatalf("relative path: err = %v", err)
		}
		if strings.Contains(rec2.allCommands(), "Steam") {
			t.Fatalf("a relative path reached the OS:\n%s", rec2.allCommands())
		}
		e, _ := s.empty()
		if err := New(e).LaunchApp(ctx, app); !errors.Is(err, ErrNotSupported) {
			t.Fatalf("empty host: err = %v", err)
		}
	})
}
