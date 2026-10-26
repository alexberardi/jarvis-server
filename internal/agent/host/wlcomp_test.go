package host

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestCompositorListsMostRecentFirstAndMarksFocus(t *testing.T) {
	for name, s := range map[string]*fakeSys{"hyprland": linuxHyprland(), "sway": linuxSway()} {
		t.Run(name, func(t *testing.T) {
			ws, err := New(NewLinux(s)).ListWindows(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, w := range ws {
				f := ""
				if w.Focused {
					f = "*"
				}
				got = append(got, w.App+f+"@"+w.Workspace)
			}
			want := map[string][]string{"hyprland": {"foot*@1", "firefox@2"}, "sway": {"firefox*@1", "foot@1"}}[name]
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("windows = %v, want %v (unmapped clients dropped, newest first, focus marked)", got, want)
			}
		})
	}
}

// A Lua-configured Hyprland rejects the classic dispatcher; the Lua form is sent instead.
func TestHyprlandFallsBackToLuaDispatch(t *testing.T) {
	for _, tc := range []struct {
		a    WindowAction
		want string
	}{
		{ActionFocus, `hl.dsp.focus({ window = "address:` + hyprWinID + `" })`},
		{ActionClose, `hl.dsp.window.close({ window = "address:` + hyprWinID + `" })`},
	} {
		s := hyprSys(true)
		if err := New(NewLinux(s)).WindowDo(ctx, tc.a, hyprWinID); err != nil {
			t.Fatalf("%s: %v", tc.a, err)
		}
		if !strings.Contains(s.allCommands(), tc.want) {
			t.Fatalf("%s: no Lua dispatch %q in\n%s", tc.a, tc.want, s.allCommands())
		}
	}
}

func TestHyprlandDispatchFailureIsReported(t *testing.T) {
	s := hyprSys(true)
	base := s.handler
	s.handler = func(name string, args []string) (string, error) {
		if name == "hyprctl" && args[0] == "dispatch" {
			return "", &RunError{Name: "hyprctl", Msg: "no such window"}
		}
		return base(name, args)
	}
	err := New(NewLinux(s)).WindowDo(ctx, ActionFocus, hyprWinID)
	if !errors.Is(err, ErrOperationFailed) {
		t.Fatalf("err = %v, want ErrOperationFailed", err)
	}
}

// XWayland's DISPLAY on a Hyprland session doesn't make the X11 path (and its minimise) appear.
func TestCompositorWinsOverXWayland(t *testing.T) {
	s := linuxHyprland()
	s.env["DISPLAY"] = ":1"
	s.bins["wmctrl"], s.bins["xdotool"] = true, true
	if New(NewLinux(s)).Has(ctx, CapWindowsMinimize) {
		t.Fatal("minimise advertised on Hyprland")
	}
}

// The Lua form each arranging action sends on a Lua-configured Hyprland (checked on 0.56.2).
func TestHyprlandArrangeSendsLuaDispatchers(t *testing.T) {
	sel := `window = "address:` + hyprWinID + `"`
	for _, tc := range []struct {
		a      WindowAction
		target string
		want   string
	}{
		{ActionMaximize, "", `hl.dsp.window.fullscreen({ mode = "maximized", action = "set", ` + sel + ` })`},
		{ActionFullscreen, "", `hl.dsp.window.fullscreen({ mode = "fullscreen", action = "set", ` + sel + ` })`},
		// hyprWinID is maximized in the fake, so unmaximize unsets that mode.
		{ActionUnmaximize, "", `hl.dsp.window.fullscreen({ mode = "maximized", action = "unset", ` + sel + ` })`},
		{ActionSnapLeft, "", `hl.dsp.window.move({ direction = "l", ` + sel + ` })`},
		{ActionSnapRight, "", `hl.dsp.window.move({ direction = "r", ` + sel + ` })`},
		{ActionMoveToWorkspace, "3", `hl.dsp.window.move({ workspace = 3, follow = false, ` + sel + ` })`},
		{ActionMoveToMonitor, "next", `hl.dsp.window.move({ monitor = "+1", ` + sel + ` })`},
		{ActionMoveToMonitor, "2", `hl.dsp.window.move({ monitor = "HDMI-A-1", ` + sel + ` })`},
		{ActionMoveToMonitor, "dp-1", `hl.dsp.window.move({ monitor = "DP-1", ` + sel + ` })`},
	} {
		s := hyprSys(true)
		h := New(NewLinux(s))
		var err error
		if NeedsTarget(tc.a) {
			err = h.WindowMove(ctx, tc.a, hyprWinID, tc.target)
		} else {
			err = h.WindowDo(ctx, tc.a, hyprWinID)
		}
		if err != nil {
			t.Fatalf("%s %s: %v", tc.a, tc.target, err)
		}
		if !strings.Contains(s.allCommands(), tc.want) {
			t.Fatalf("%s %s: no %q in\n%s", tc.a, tc.target, tc.want, s.allCommands())
		}
	}
}

// A classic Hyprland gets the classic forms; the ones that act on the focused window focus it
// first, in one batch.
func TestHyprlandArrangeClassicForms(t *testing.T) {
	for a, want := range map[WindowAction]string{
		ActionMaximize:  "hyprctl --batch dispatch focuswindow address:" + hyprWinID + " ; dispatch fullscreenstate 1 -1",
		ActionSnapRight: "hyprctl --batch dispatch focuswindow address:" + hyprWinID + " ; dispatch movewindow r",
	} {
		s := linuxHyprland()
		if err := New(NewLinux(s)).WindowDo(ctx, a, hyprWinID); err != nil {
			t.Fatalf("%s: %v", a, err)
		}
		if !strings.Contains(s.allCommands(), want) || strings.Contains(s.allCommands(), "hl.dsp") {
			t.Fatalf("%s: want only %q in\n%s", a, want, s.allCommands())
		}
	}
}

// Unmaximize on a window that is neither maximized nor fullscreen does nothing and succeeds.
func TestUnmaximizeANormalWindowIsANoOp(t *testing.T) {
	s := linuxHyprland()
	if err := New(NewLinux(s)).WindowDo(ctx, ActionUnmaximize, "0x55aa02"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(s.allCommands(), "fullscreen") {
		t.Fatalf("dispatched for a normal window:\n%s", s.allCommands())
	}
}

func TestSwayArrangeCommands(t *testing.T) {
	con := "swaymsg [con_id=" + swayWinID + "] "
	for _, tc := range []struct {
		a      WindowAction
		target string
		want   string
	}{
		{ActionFullscreen, "", con + "fullscreen enable"},
		{ActionUnmaximize, "", con + "fullscreen disable"},
		{ActionSnapLeft, "", con + "move left"},
		{ActionMoveToWorkspace, "4", con + "move container to workspace number 4"},
		{ActionMoveToMonitor, "previous", con + "move container to output left"},
		{ActionMoveToMonitor, "2", con + "move container to output HDMI-A-1"},
	} {
		s := linuxSway()
		h := New(NewLinux(s))
		var err error
		if NeedsTarget(tc.a) {
			err = h.WindowMove(ctx, tc.a, swayWinID, tc.target)
		} else {
			err = h.WindowDo(ctx, tc.a, swayWinID)
		}
		if err != nil {
			t.Fatalf("%s: %v", tc.a, err)
		}
		if !strings.Contains(s.allCommands(), tc.want) {
			t.Fatalf("%s: no %q in\n%s", tc.a, tc.want, s.allCommands())
		}
	}
}

// Targets end up inside Lua source and swaymsg commands: anything but a plain workspace number or
// monitor name is refused before a backend sees it.
func TestMoveTargetsAreValidatedBeforeAnyCommand(t *testing.T) {
	for _, tc := range []struct {
		a      WindowAction
		target string
	}{
		{ActionMoveToWorkspace, `1, follow = true }) os.execute("reboot") --`},
		{ActionMoveToWorkspace, "0"},
		{ActionMoveToWorkspace, "two"},
		{ActionMoveToMonitor, `DP-1" }) os.execute("x`},
		{ActionMoveToMonitor, "DP 1; exec reboot"},
		{ActionMoveToMonitor, ""},
	} {
		s := hyprSys(true)
		err := New(NewLinux(s)).WindowMove(ctx, tc.a, hyprWinID, tc.target)
		if !errors.Is(err, ErrInvalidTarget) {
			t.Fatalf("%s %q: err = %v, want ErrInvalidTarget", tc.a, tc.target, err)
		}
		if strings.Contains(s.allCommands(), "dispatch") {
			t.Fatalf("%s %q reached hyprctl:\n%s", tc.a, tc.target, s.allCommands())
		}
	}
	err := New(NewLinux(hyprSys(true))).WindowMove(ctx, ActionMoveToMonitor, hyprWinID, "7")
	if !errors.Is(err, ErrInvalidTarget) {
		t.Fatalf("monitor 7 of 2: err = %v, want ErrInvalidTarget", err)
	}
}

func TestMoveToMonitorNeedsASecondMonitor(t *testing.T) {
	s := linuxHyprland()
	base := s.handler
	s.handler = func(name string, args []string) (string, error) {
		if name == "hyprctl" && len(args) > 1 && args[1] == "monitors" {
			return `[{"name":"HDMI-A-1"}]`, nil
		}
		return base(name, args)
	}
	h := New(NewLinux(s))
	if h.Has(ctx, CapWindowsMonitor) {
		t.Fatal("move to monitor offered with one monitor")
	}
	if !h.Has(ctx, CapWindowsWorkspace) {
		t.Fatal("workspaces lost")
	}
}
