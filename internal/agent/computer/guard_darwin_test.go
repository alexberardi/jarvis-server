//go:build darwin

package computer

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/input"
)

// TestGuardMacLive moves the real pointer through the guard and polls the real HID monitor:
// our own events must not pause the task. It runs only on the Mac test host, hands off the
// mouse and keyboard: JARVIS_GUARD_MAC_LIVE=1 go test ./internal/agent/computer/ -run MacLive -v
// (needs Accessibility for the terminal running it). With JARVIS_GUARD_MAC_LIVE_USER=1 it then
// waits up to 10 s for you to move the mouse, which must pause it.
func TestGuardMacLive(t *testing.T) {
	if os.Getenv("JARVIS_GUARD_MAC_LIVE") != "1" {
		t.Skip("set JARVIS_GUARD_MAC_LIVE=1 to move the real pointer")
	}
	ctx := context.Background()
	in := input.Native()
	if ok, why := in.Available(ctx); !ok {
		t.Skip(why)
	}
	mon := input.NativeMonitor()
	act := &Activity{}
	act.Set(Controlling, "live guard test")
	g := &Guard{Input: in, Monitor: mon, Activity: act}
	x0, y0, err := in.Cursor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Move(ctx, x0, y0)

	before, err := mon.Since(ctx)
	if err != nil || before.IsZero() || before.After(time.Now()) {
		t.Fatalf("last HID activity %v, %v: want a time in the past", before, err)
	}
	g.Begin(ctx)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go g.Run(runCtx)
	for _, p := range [][2]int{{200, 150}, {321, 243}, {260, 300}, {200, 150}} {
		if err := g.Guarded().Move(ctx, p[0], p[1]); err != nil {
			t.Fatal(err)
		}
		time.Sleep(150 * time.Millisecond)
	}
	time.Sleep(400 * time.Millisecond)
	after, _ := mon.Since(ctx)
	t.Logf("HID activity: %s before, %s after our moves (unchanged: the private source keeps ours out)",
		before.Format(time.StampMilli), after.Format(time.StampMilli))
	if err := g.Check(); err != nil {
		t.Fatalf("our own moves paused the task: %v", err)
	}

	if os.Getenv("JARVIS_GUARD_MAC_LIVE_USER") != "1" {
		return
	}
	t.Log("move the mouse now")
	deadline := time.Now().Add(10 * time.Second)
	for g.Check() == nil && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if g.Check() == nil {
		t.Fatal("the user's mouse didn't pause the task")
	}
	if k, _ := act.Status(); k != Paused {
		t.Fatalf("activity is %q, want paused", k)
	}
}
