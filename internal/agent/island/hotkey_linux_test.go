package island

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// fakeStopKeys records which way the stop hotkey was asked for and keeps its press func.
type fakeStopKeys struct {
	env      map[string]string
	x11Err   error
	label    string
	portErr  error
	asked    []string
	press    func()
	pressCtx context.Context
}

func (f *fakeStopKeys) keys() stopKeys {
	return stopKeys{
		getenv: func(k string) string { return f.env[k] },
		x11: func(ctx context.Context, on func()) error {
			f.asked = append(f.asked, "x11")
			f.press, f.pressCtx = on, ctx
			return f.x11Err
		},
		portal: func(ctx context.Context, on func()) (string, error) {
			f.asked = append(f.asked, "portal")
			f.press, f.pressCtx = on, ctx
			return f.label, f.portErr
		},
	}
}

func startKeys(t *testing.T, f *fakeStopKeys, ev AppEvents) string {
	t.Helper()
	defer stopHotkeyAs.Store(stopHotkeyAs.Load())
	stopHotkeyAs.Store(nil)
	var buf bytes.Buffer
	f.keys().start(context.Background(), slog.New(slog.NewTextHandler(&buf, nil)), ev)
	return StopHotkey() + "|" + buf.String()
}

func TestStopHotkeyX11(t *testing.T) {
	f := &fakeStopKeys{env: map[string]string{"DISPLAY": ":0"}}
	stopped := make(chan struct{}, 1)
	got := startKeys(t, f, AppEvents{StopHotkey: func() { stopped <- struct{}{} }})
	if label, _, _ := strings.Cut(got, "|"); label != "Ctrl+Alt+Esc" || strings.Join(f.asked, ",") != "x11" {
		t.Fatalf("label %q, asked %v; want Ctrl+Alt+Esc through the X11 grab", label, f.asked)
	}
	f.press()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("a press didn't call StopHotkey")
	}
}

func TestStopHotkeyX11Held(t *testing.T) {
	f := &fakeStopKeys{env: map[string]string{"DISPLAY": ":0"}, x11Err: errors.New("another program holds Ctrl+Alt+Esc")}
	got := startKeys(t, f, AppEvents{StopHotkey: func() {}})
	if label, log, _ := strings.Cut(got, "|"); label != "" || !strings.Contains(log, "another program holds") {
		t.Fatalf("label %q, log %q; want none and the reason logged", label, log)
	}
}

func TestStopHotkeyWayland(t *testing.T) {
	f := &fakeStopKeys{env: map[string]string{"WAYLAND_DISPLAY": "wayland-1", "DISPLAY": ":1"}, label: "Ctrl+Alt+Escape"}
	got := startKeys(t, f, AppEvents{StopHotkey: func() {}})
	if label, _, _ := strings.Cut(got, "|"); label != "Ctrl+Alt+Escape" || strings.Join(f.asked, ",") != "portal" {
		t.Fatalf("label %q, asked %v; want the portal's label (never XWayland's grab)", label, f.asked)
	}
}

func TestStopHotkeyWaylandWithoutKey(t *testing.T) {
	// No portal, or one that binds no key (Hyprland): no label, and the log says how to bind it.
	for _, f := range []*fakeStopKeys{
		{env: map[string]string{"WAYLAND_DISPLAY": "wayland-1"}, portErr: errors.New("this desktop has no GlobalShortcuts portal")},
		{env: map[string]string{"WAYLAND_DISPLAY": "wayland-1"}},
	} {
		got := startKeys(t, f, AppEvents{StopHotkey: func() {}})
		if label, log, _ := strings.Cut(got, "|"); label != "" || !strings.Contains(log, "jarvis-agent computer stop") {
			t.Fatalf("label %q, log %q; want none and the Hyprland bind line", label, log)
		}
	}
}

func TestStopHotkeyNotAskedFor(t *testing.T) {
	for _, tc := range []struct {
		env map[string]string
		ev  AppEvents
	}{
		{map[string]string{"DISPLAY": ":0"}, AppEvents{}},       // no StopHotkey: nothing to call
		{map[string]string{}, AppEvents{StopHotkey: func() {}}}, // no desktop (a Pi)
	} {
		f := &fakeStopKeys{env: tc.env}
		if got := startKeys(t, f, tc.ev); got != "|" || len(f.asked) != 0 {
			t.Fatalf("got %q, asked %v; want nothing", got, f.asked)
		}
	}
}
