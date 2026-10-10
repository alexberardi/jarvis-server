package host

import (
	"context"
	"os"
	"testing"
	"time"
)

// Native tests drive the real host. Opt in with JARVIS_HOST_NATIVE=1 (CI does); they skip what
// this host can't do, which is itself the behaviour under test. They never close or minimise
// anything, and restore the volume they touch. JARVIS_HOST_NATIVE_OPEN=1 also opens a URL.

func native(t *testing.T) *Host {
	if os.Getenv("JARVIS_HOST_NATIVE") != "1" {
		t.Skip("set JARVIS_HOST_NATIVE=1 to drive the real host")
	}
	b, err := Native()
	if err != nil {
		t.Fatal(err)
	}
	return New(b)
}

func TestNativeCapabilities(t *testing.T) {
	h := native(t)
	caps, err := h.Capabilities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s capabilities: %v", h.Platform(), caps)
}

func TestNativeVolumeRoundTrip(t *testing.T) {
	h := native(t)
	ctx := context.Background()
	if !h.Has(ctx, CapVolumeGet) || !h.Has(ctx, CapVolumeSet) {
		t.Skip("no volume control here")
	}
	orig, err := h.GetVolume(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer h.SetVolume(ctx, orig)
	target := 23
	if orig == target {
		target = 24
	}
	if applied, err := h.SetVolume(ctx, target); err != nil || applied != target {
		t.Fatalf("SetVolume(%d) = %d, %v", target, applied, err)
	}
	got, err := h.GetVolume(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Some mixers quantise (macOS steps of ~6%), so allow a little slack.
	if got < target-4 || got > target+4 {
		t.Fatalf("set %d, read back %d", target, got)
	}
	t.Logf("volume %d -> %d -> restored %d", orig, got, orig)
}

func TestNativeListWindows(t *testing.T) {
	h := native(t)
	ctx := context.Background()
	if !h.Has(ctx, CapWindowsList) {
		t.Skip("no window control here")
	}
	ws, err := h.ListWindows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range ws {
		if w.ID == "" {
			t.Fatalf("window without an id: %+v", w)
		}
	}
	t.Logf("%d windows", len(ws))
}

func TestNativeOpen(t *testing.T) {
	h := native(t)
	if os.Getenv("JARVIS_HOST_NATIVE_OPEN") != "1" {
		t.Skip("set JARVIS_HOST_NATIVE_OPEN=1 to open a browser tab")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if !h.Has(ctx, CapOpen) {
		t.Skip("no open here")
	}
	if err := h.Open(ctx, "https://example.com/?from=jarvis-agent-native-test"); err != nil {
		t.Fatal(err)
	}
}
