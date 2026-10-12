package screen

import (
	"context"
	"os"
	"runtime"
	"testing"
)

// TestNativeCapture drives this desktop's real capture (it takes a screenshot; nothing is
// saved). Opt-in: JARVIS_SCREEN_NATIVE=1. On macOS the capture only works as Jarvis Agent.app
// with the Screen Recording grant; the front-window lookup works from any process.
func TestNativeCapture(t *testing.T) {
	if os.Getenv("JARVIS_SCREEN_NATIVE") == "" {
		t.Skip("JARVIS_SCREEN_NATIVE not set")
	}
	if runtime.GOOS == "darwin" {
		w, err := frontWindow()
		t.Logf("front window: id=%d app=%q title set=%v err=%v", w.ID, w.App, w.Title != "", err)
		if err != nil || w.ID == 0 {
			t.Errorf("no front window: %v", err)
		}
	}
	c := Native()
	if c == nil {
		t.Skip("no capturer on " + runtime.GOOS)
	}
	ok, why := c.Available(context.Background())
	t.Logf("available=%v %s", ok, why)
	if !ok {
		return
	}
	for _, scope := range []string{ScopeWindow, ScopeScreen} {
		shot, err := c.Capture(context.Background(), scope)
		if err != nil {
			t.Errorf("%s: %v", scope, err)
			continue
		}
		data, w, h, err := Encode(shot.Image)
		t.Logf("%s: %v -> %dx%d, %d bytes jpeg, app %q, title set %v (err %v)", scope, shot.Image.Bounds().Size(), w, h, len(data), shot.App, shot.Title != "", err)
	}
}
