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
				got = append(got, w.App+f)
			}
			want := map[string][]string{"hyprland": {"foot*", "firefox"}, "sway": {"firefox*", "foot"}}[name]
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
