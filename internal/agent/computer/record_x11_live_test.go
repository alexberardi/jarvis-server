//go:build linux

package computer

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/x11"
)

// The real RECORD path on $DISPLAY: XTEST clicks and keys come back through the data
// connection as a recording's events. It presses keys and clicks, so it is for a throwaway
// headless X server only (Xvfb, a rootful Xwayland under headless sway), never a desktop
// someone is using.
func TestX11RecordLive(t *testing.T) {
	if os.Getenv("JARVIS_X11_RECORD_LIVE") != "1" {
		t.Skip("JARVIS_X11_RECORD_LIVE=1 records $DISPLAY while it clicks and types there (a headless X server only)")
	}
	d := &x11.Display{}
	e := newRecEnv(t)
	e.r.Sources[ModeDesktop] = newX11Source(e.r.Dir, d, x11Access{}, nil, nil)
	id, err := e.r.Start(context.Background(), ModeDesktop)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	time.Sleep(200 * time.Millisecond) // RECORD's StartOfData
	if err := d.Click(ctx, 100, 120, 1, 1); err != nil {
		t.Fatal(err)
	}
	for _, k := range [][]string{{"h"}, {"i"}, {"shift", "h"}, {"enter"}, {"ctrl", "s"}} {
		if err := d.Keys(ctx, k); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.Scroll(ctx, 200, 210, 0, 2); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	tr, err := e.r.Stop(id)
	if err != nil {
		t.Fatal(err)
	}
	evs := traceEvents(t, tr)
	got := summary(evs)
	t.Logf("trace: %s", got)
	if want := `click; input "hiH"; key [Enter]; key [ctrl+s]; scroll`; got != want {
		t.Fatalf("trace:\n got %s\nwant %s", got, want)
	}
	if c, s := evs[0], evs[4]; c.X != 100 || c.Y != 120 || s.X != 200 || s.Y != 210 || s.DY != 2 {
		t.Fatalf("click %+v, scroll %+v", c, s)
	}
}
