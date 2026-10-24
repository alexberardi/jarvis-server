package host

import (
	"errors"
	"strings"
	"testing"
)

func ewmhLinux(f *fakeX) (*Host, *fakeSys) {
	s, x := linuxEWMH()
	if f != nil {
		f.sys, f.wm, f.windows = s, true, x.windows
		x = f
	}
	return New(&Linux{Sys: s, X: x}), s
}

// Each arranging action is offered only when the window manager advertises its EWMH part.
func TestX11ArrangingFollowsTheWindowManager(t *testing.T) {
	h, _ := ewmhLinux(&fakeX{features: map[string]bool{"_NET_WM_STATE": true, "_NET_WM_STATE_FULLSCREEN": true,
		"_NET_WM_DESKTOP": true}, desktops: 1})
	for c, want := range map[string]bool{CapWindowsUnminimize: true, CapWindowsFullscreen: true, CapWindowsUnmaximize: true,
		CapWindowsMaximize: false, CapWindowsSnap: false, CapWindowsWorkspace: false, CapWindowsMonitor: false} {
		if got := h.Has(ctx, c); got != want {
			t.Errorf("%s: %v, want %v", c, got, want)
		}
	}
}

func TestX11ArrangingSendsEWMHRequests(t *testing.T) {
	const id = "0x03a00007"
	for _, tc := range []struct {
		a      WindowAction
		target string
		want   string
	}{
		{ActionUnminimize, "", "x11 activate " + id},
		{ActionMaximize, "", "x11 state " + id + " add=true _NET_WM_STATE_MAXIMIZED_VERT,_NET_WM_STATE_MAXIMIZED_HORZ"},
		{ActionFullscreen, "", "x11 state " + id + " add=true _NET_WM_STATE_FULLSCREEN"},
		{ActionUnmaximize, "", "x11 state " + id + " add=false _NET_WM_STATE_FULLSCREEN\nx11 state " + id + " add=false _NET_WM_STATE_MAXIMIZED_VERT,_NET_WM_STATE_MAXIMIZED_HORZ"},
		{ActionSnapLeft, "", "x11 snap " + id + " left=true"},
		{ActionSnapRight, "", "x11 snap " + id + " left=false"},
		{ActionMoveToWorkspace, "2", "x11 desktop " + id + " 1"}, // EWMH counts from 0
	} {
		h, s := ewmhLinux(nil)
		var err error
		if NeedsTarget(tc.a) {
			err = h.WindowMove(ctx, tc.a, id, tc.target)
		} else {
			err = h.WindowDo(ctx, tc.a, id)
		}
		if err != nil {
			t.Fatalf("%s: %v", tc.a, err)
		}
		if !strings.Contains(s.allCommands(), tc.want) {
			t.Fatalf("%s: no %q in\n%s", tc.a, tc.want, s.allCommands())
		}
	}
	h, s := ewmhLinux(nil)
	if err := h.WindowMove(ctx, ActionMoveToWorkspace, id, "7"); !errors.Is(err, ErrInvalidTarget) {
		t.Fatalf("workspace 7 of 4: %v", err)
	}
	if strings.Contains(s.allCommands(), "desktop") {
		t.Fatalf("moved to a workspace that doesn't exist:\n%s", s.allCommands())
	}
}

// The focused window is the one the WM says is active.
func TestX11ListsTheActiveWindowAsFocused(t *testing.T) {
	s, x := linuxEWMH()
	x.windows[0].Active = true
	ws, err := New(&Linux{Sys: s, X: x}).ListWindows(ctx)
	if err != nil || !ws[0].Focused || ws[1].Focused {
		t.Fatalf("%+v %v", ws, err)
	}
}
