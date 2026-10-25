package host

import (
	"errors"
	"strings"
	"testing"
)

func TestWindowsArranging(t *testing.T) {
	const id = winWinID
	for _, tc := range []struct {
		name   string
		setup  func(*fakeWin32)
		a      WindowAction
		target string
		want   string
	}{
		{"maximize", nil, ActionMaximize, "", "maximize 1311"},
		{"unmaximize a maximized window", func(f *fakeWin32) { f.zoomed = true }, ActionUnmaximize, "", "restore 1311"},
		{"unmaximize a normal one is a no-op", nil, ActionUnmaximize, "", ""},
		{"unminimize", func(f *fakeWin32) { f.iconic = true }, ActionUnminimize, "", "restore 1311"},
		{"unminimize a shown window doesn't unmaximize it", func(f *fakeWin32) { f.zoomed = true }, ActionUnminimize, "", ""},
		{"snap left", nil, ActionSnapLeft, "", "setrect 1311 0,0 960x1040"},
		{"snap right", nil, ActionSnapRight, "", "setrect 1311 960,0 960x1040"},
		{"snap a maximized window restores it first", func(f *fakeWin32) { f.zoomed = true }, ActionSnapRight, "", "restore 1311\nsetrect 1311 960,0 960x1040"},
		{"snap on the second monitor", func(f *fakeWin32) { f.rect = Rect{2000, 100, 2800, 700} }, ActionSnapLeft, "", "setrect 1311 1920,0 960x1040"},
		{"next monitor keeps the place", nil, ActionMoveToMonitor, "next", "setrect 1311 2020,100 800x600"},
		{"previous wraps", nil, ActionMoveToMonitor, "previous", "setrect 1311 2020,100 800x600"},
		{"monitor 1 from 2", func(f *fakeWin32) { f.rect = Rect{2000, 100, 2800, 700} }, ActionMoveToMonitor, "1", "setrect 1311 80,100 800x600"},
		{"shrinks to fit a smaller monitor", func(f *fakeWin32) {
			f.areas = []Rect{{0, 0, 1920, 1040}, {1920, 0, 2944, 728}}
			f.rect = Rect{0, 0, 1900, 1000}
		}, ActionMoveToMonitor, "2", "setrect 1311 1920,0 1024x728"},
	} {
		f := winFull()
		if tc.setup != nil {
			tc.setup(f)
		}
		h := New(NewWindows(f))
		var err error
		if NeedsTarget(tc.a) {
			err = h.WindowMove(ctx, tc.a, id, tc.target)
		} else {
			err = h.WindowDo(ctx, tc.a, id)
		}
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		var acts []string
		for _, c := range f.calls {
			if strings.HasPrefix(c, "maximize") || strings.HasPrefix(c, "restore") || strings.HasPrefix(c, "setrect") {
				acts = append(acts, c)
			}
		}
		if got := strings.Join(acts, "\n"); got != tc.want {
			t.Errorf("%s: did %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestWindowsMonitorTargets(t *testing.T) {
	h := New(NewWindows(winFull()))
	for _, target := range []string{"3", "DP-1"} {
		if err := h.WindowMove(ctx, ActionMoveToMonitor, winWinID, target); !errors.Is(err, ErrInvalidTarget) {
			t.Fatalf("%q: %v", target, err)
		}
	}
	one := winFull()
	one.areas = []Rect{{0, 0, 1920, 1040}}
	if New(NewWindows(one)).Has(ctx, CapWindowsMonitor) {
		t.Fatal("move to monitor offered with one monitor")
	}
}
