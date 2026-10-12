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
		ws, err := windowList()
		w, ok := frontOf(ws)
		t.Logf("windows: %d, front: id=%d app=%q title set=%v err=%v", len(ws), w.ID, w.App, w.Title != "", err)
		if err != nil || !ok {
			t.Errorf("no front window: %v", err)
		}
		if q := os.Getenv("JARVIS_SCREEN_WINDOW"); q != "" {
			nw, err := namedOf(ws, q)
			t.Logf("named %q: id=%d app=%q err=%v", q, nw.ID, nw.App, err)
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
		shot, err := c.Capture(context.Background(), scope, "")
		if err != nil {
			t.Errorf("%s: %v", scope, err)
			continue
		}
		data, w, h, err := Encode(shot.Image)
		t.Logf("%s: %v -> %dx%d, %d bytes jpeg, app %q, title set %v (err %v)", scope, shot.Image.Bounds().Size(), w, h, len(data), shot.App, shot.Title != "", err)
	}
	if q := os.Getenv("JARVIS_SCREEN_WINDOW"); q != "" {
		shot, err := c.Capture(context.Background(), ScopeWindow, q)
		if err != nil {
			t.Logf("named %q: %v", q, err)
		} else {
			t.Logf("named %q: %v, app %q", q, shot.Image.Bounds().Size(), shot.App)
		}
	}
}
