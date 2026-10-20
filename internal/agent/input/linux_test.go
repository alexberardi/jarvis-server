//go:build linux

package input

import "testing"

// TestNativePicksBackend: a Wayland session gets the wlroots backend, anything else X11.
func TestNativePicksBackend(t *testing.T) {
	t.Setenv("WAYLAND_DISPLAY", "wayland-1")
	if _, ok := Native().(*Wlroots); !ok {
		t.Errorf("WAYLAND_DISPLAY set: Native() = %T, want *Wlroots", Native())
	}
	t.Setenv("WAYLAND_DISPLAY", "")
	if _, ok := Native().(*X11); !ok {
		t.Errorf("WAYLAND_DISPLAY empty: Native() = %T, want *X11", Native())
	}
}
