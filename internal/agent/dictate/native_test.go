package dictate

import (
	"context"
	"os"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/host"
)

// TestNativeWindowsReadOnly lists this desktop's windows and the active one through the real
// backend, read-only: it never focuses, types or presses anything, and logs only counts (no
// titles). Opt-in: JARVIS_DICTATE_NATIVE_LIST=1.
func TestNativeWindowsReadOnly(t *testing.T) {
	if os.Getenv("JARVIS_DICTATE_NATIVE_LIST") != "1" {
		t.Skip("set JARVIS_DICTATE_NATIVE_LIST=1 to list this desktop's windows (read-only)")
	}
	be, err := host.Native()
	if err != nil {
		t.Fatal(err)
	}
	kb, win, err := Native(host.New(be), func() bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ok, why := kb.Available(ctx)
	t.Logf("keyboard %s available=%v %s", kb.Name(), ok, why)
	if ok, why := win.Available(ctx); !ok {
		t.Skipf("no window backend: %s", why)
	}
	ws, err := win.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	active, err := win.Active(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found, visible := 0, 0
	for _, w := range ws {
		if win.Same(w.ID, active) {
			found++
		}
		if w.Visible {
			visible++
		}
	}
	t.Logf("%d windows (%d visible); the active one is in the list %d time(s)", len(ws), visible, found)
	if len(ws) > 0 && active != "" && found != 1 {
		t.Errorf("the active window should be exactly one listed window, found %d", found)
	}
}
