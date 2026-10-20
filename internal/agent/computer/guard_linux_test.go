//go:build linux

package computer

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/input"
	"github.com/alexberardi/jarvis-server/internal/agent/vkbd"
	"github.com/alexberardi/jarvis-server/internal/agent/x11"
	"github.com/alexberardi/jarvis-server/internal/agent/xkb"
)

// TestGuardLinuxLive runs the guard with the real input backend and NativeMonitor of this
// session (wlroots idle-notify, or X11's screensaver idle time): our own moves and keys must not
// pause the task; then a second client, standing in for the user, presses Shift once, which
// must. A key leaves the pointer where it was, so only the monitor can see it. Run it only in a disposable session, never the user's (it moves the pointer):
// JARVIS_GUARD_LINUX_LIVE=1 with WAYLAND_DISPLAY of a headless sway, or DISPLAY of an Xvfb or
// Xwayland, and nothing else on it.
func TestGuardLinuxLive(t *testing.T) {
	if os.Getenv("JARVIS_GUARD_LINUX_LIVE") != "1" {
		t.Skip("set JARVIS_GUARD_LINUX_LIVE=1 in a disposable sway or X session")
	}
	ctx := context.Background()
	in := input.Native()
	if ok, why := in.Available(ctx); !ok {
		t.Fatal(why)
	}
	mon := input.NativeMonitor()
	defer func() {
		if c, ok := mon.(interface{ Close() }); ok {
			c.Close()
		}
	}()
	if _, err := mon.Since(ctx); err != nil {
		t.Fatalf("monitor: %v", err)
	}
	act := &Activity{}
	act.Set(Controlling, "live guard test")
	g := &Guard{Input: in, Monitor: mon, Activity: act}
	g.Begin(ctx)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go g.Run(runCtx)

	// Ours: moves, a click and a chord, with pauses longer than the idle timeout between some
	// of them, so the compositor's idled/resumed come inside our windows.
	steps := []func() error{
		func() error { return g.Guarded().Move(ctx, 200, 150) },
		func() error { return g.Guarded().Move(ctx, 321, 243) },
		func() error { return g.Guarded().Keys(ctx, []string{"shift"}) },
		func() error { return g.Guarded().Move(ctx, 260, 300) },
	}
	for i, step := range steps {
		if err := step(); err != nil {
			t.Fatal(err)
		}
		last, _ := mon.Since(ctx)
		x, y, _ := in.Cursor(ctx)
		start, end := in.Injecting()
		t.Logf("step %d: ours %s–%s, last activity %s, pointer %d,%d", i, start.Format(time.StampMilli),
			end.Format(time.StampMilli), last.Format(time.StampMilli), x, y)
		wait := 150 * time.Millisecond
		if i%2 == 1 {
			wait = 1600 * time.Millisecond
		}
		time.Sleep(wait)
	}
	time.Sleep(1600 * time.Millisecond)
	if err := g.Check(); err != nil {
		t.Fatalf("our own input paused the task: %v", err)
	}
	ours, _ := mon.Since(ctx)
	t.Logf("our input: last activity %s", ours.Format(time.StampMilli))

	// The user: another client's key press.
	userAt := time.Now()
	if err := userKey(ctx); err != nil {
		t.Fatalf("the second client's key: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for g.Check() == nil && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if g.Check() == nil {
		t.Fatal("the second client's key press didn't pause the task")
	}
	if k, _ := act.Status(); k != Paused {
		t.Fatalf("activity is %q, want paused", k)
	}
	last, err := mon.Since(ctx)
	if err != nil || last.Before(userAt.Add(-50*time.Millisecond)) {
		t.Fatalf("monitor after the user's move: %v, %v; want at or after %s", last, err, userAt.Format(time.StampMilli))
	}
	t.Logf("user input at %s: paused %s later; monitor says %s",
		userAt.Format(time.StampMilli), time.Since(userAt).Round(time.Millisecond), last.Format(time.StampMilli))

	// The user again, within a second of our own input: idle-notify sends no resumed for it (the
	// seat never went idle), only the idled a second after it, which dates it.
	g.Resume(ctx)
	time.Sleep(1600 * time.Millisecond)
	if err := g.Check(); err != nil {
		last, _ := mon.Since(ctx)
		t.Fatalf("paused again before any input: last activity %s, resumed at %s", last.Format(time.StampMilli), userAt.Format(time.StampMilli))
	}
	if err := g.Guarded().Move(ctx, 200, 150); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	if err := g.Check(); err != nil {
		last, _ := mon.Since(ctx)
		x, y, _ := in.Cursor(ctx)
		start, end := in.Injecting()
		t.Fatalf("our own move after resuming paused the task: %v (ours %s–%s, last activity %s, pointer %d,%d)", err,
			start.Format(time.StampMilli), end.Format(time.StampMilli), last.Format(time.StampMilli), x, y)
	}
	userAt = time.Now()
	if err := userKey(ctx); err != nil {
		t.Fatalf("the second client's key: %v", err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for g.Check() == nil && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if g.Check() == nil {
		t.Fatal("a key press 400 ms after our own move didn't pause the task")
	}
	t.Logf("user input 400 ms after ours: paused %s later", time.Since(userAt).Round(time.Millisecond))
}

// userKey presses Shift from a connection of its own, like another program (or a real
// keyboard) would: a second virtual keyboard on Wayland, a second XTEST client on X11.
func userKey(ctx context.Context) error {
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		k := &vkbd.Keyboard{}
		return k.Send(ctx, []xkb.Stroke{{Sym: xkb.KeyShiftL}})
	}
	d := &x11.Display{}
	return d.Keys(ctx, []string{"shift"})
}
