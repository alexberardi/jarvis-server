//go:build linux

package input

import (
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/idlenotify"
	"github.com/alexberardi/jarvis-server/internal/agent/x11"
)

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

// TestNativeMonitorPicksBackend: idle-notify in a Wayland session, the X server's idle time
// otherwise.
func TestNativeMonitorPicksBackend(t *testing.T) {
	t.Setenv("WAYLAND_DISPLAY", "wayland-1")
	if _, ok := NativeMonitor().(*idlenotify.Monitor); !ok {
		t.Errorf("WAYLAND_DISPLAY set: NativeMonitor() = %T, want *idlenotify.Monitor", NativeMonitor())
	}
	t.Setenv("WAYLAND_DISPLAY", "")
	if _, ok := NativeMonitor().(*x11.Idle); !ok {
		t.Errorf("WAYLAND_DISPLAY empty: NativeMonitor() = %T, want *x11.Idle", NativeMonitor())
	}
}
