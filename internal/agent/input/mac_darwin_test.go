//go:build darwin

package input

import (
	"context"
	"os"
	"testing"
)

// TestMacLiveCursor moves the real pointer, so it runs only on the Mac test host:
// JARVIS_INPUT_MAC_LIVE=1 go test ./internal/agent/input/ -run Live -v (needs Accessibility for
// the terminal running it).
func TestMacLiveCursor(t *testing.T) {
	if os.Getenv("JARVIS_INPUT_MAC_LIVE") != "1" {
		t.Skip("set JARVIS_INPUT_MAC_LIVE=1 to move the real pointer")
	}
	ctx := context.Background()
	in := Native()
	if ok, why := in.Available(ctx); !ok {
		t.Skip(why)
	}
	x0, y0, err := in.Cursor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Move(ctx, x0, y0)
	for _, p := range [][2]int{{200, 150}, {321, 243}} {
		if err := in.Move(ctx, p[0], p[1]); err != nil {
			t.Fatal(err)
		}
		x, y, err := in.Cursor(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if abs(x-p[0]) > 1 || abs(y-p[1]) > 1 {
			t.Errorf("moved to %v, cursor reads %d,%d", p, x, y)
		}
	}
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// TestMacEventLocation builds a mouse event (never posted) and reads its point back: CGPoint
// passes by value through purego both ways.
func TestMacEventLocation(t *testing.T) {
	if err := loadCG(); err != nil {
		t.Skip(err)
	}
	ev := cgEventCreateMouseEvent(cgSource, evMoved, cgPoint{123.5, 45}, 0)
	if ev == 0 {
		t.Skip("CGEventCreateMouseEvent failed (no window server?)")
	}
	defer cfRelease(ev)
	if p := cgEventGetLocation(ev); p != (cgPoint{123.5, 45}) {
		t.Fatalf("location = %+v, want {123.5 45}", p)
	}
	if _, _, err := (cgPoster{}).Location(); err != nil {
		t.Fatal(err)
	}
}

// TestMacPrivateSource: our events post from a private source, not the default one, so the HID
// system state (NativeMonitor) doesn't count them as the user's. Both need a GUI session (a
// sandbox or ssh gets no source, and NativeMonitor blocks), so it runs on the Mac test host
// with JARVIS_INPUT_MAC_LIVE=1; the computer package's TestGuardMacLive runs NativeMonitor.
func TestMacLivePrivateSource(t *testing.T) {
	if os.Getenv("JARVIS_INPUT_MAC_LIVE") != "1" {
		t.Skip("set JARVIS_INPUT_MAC_LIVE=1 on the Mac test host")
	}
	if err := loadCG(); err != nil {
		t.Fatal(err)
	}
	if cgSource == 0 {
		t.Error("no private event source: our events would count as the user's")
	}
}
