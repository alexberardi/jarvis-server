package maintenance

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"
)

// clock is a settable time.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func at(hhmm string, day int) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04:05", fmt.Sprintf("2026-10-%02d %s", day, hhmm), time.Local)
	if err != nil {
		panic(err)
	}
	return t
}

type rig struct {
	r     *Restarter
	clk   *clock
	set   Settings
	rss   int
	exits []string
}

func newRig(start time.Time) *rig {
	g := &rig{clk: &clock{start}, set: Settings{Enabled: true, At: "03:00", CeilingMB: -1}, rss: 100}
	g.r = &Restarter{Settings: func() Settings { return g.set }, Exit: func(why string) { g.exits = append(g.exits, why) },
		Log: slog.New(slog.DiscardHandler), Now: g.clk.now,
		RSSMB: func() (int, bool) { return g.rss, true }, TotalMB: func() (int, bool) { return 4096, true }}
	g.r.start()
	return g
}

func (g *rig) check(t *testing.T, when time.Time, want string) {
	t.Helper()
	g.clk.t = when
	if got := g.r.Check(); got != want {
		t.Fatalf("at %s: Check %q, want %q", when.Format("Jan 2 15:04:05"), got, want)
	}
}

func TestScheduledRestartFiresOnceADayAtHHMM(t *testing.T) {
	g := newRig(at("12:00:00", 20))
	g.check(t, at("02:59:30", 21), "")
	g.check(t, at("03:00:10", 21), Scheduled)
	g.check(t, at("03:00:50", 21), "") // same day: once
	g.check(t, at("03:01:10", 21), "")
	g.check(t, at("03:00:20", 22), Scheduled) // the next day (this process lived on)
	if len(g.exits) != 2 || g.exits[0] != Scheduled {
		t.Fatalf("exits %v", g.exits)
	}
}

// systemd brings the node back within the same minute (RestartSec=5): the new process must not
// restart again for the same 03:00.
func TestAProcessStartedAfterTheTimeDoesNotRestartThatDay(t *testing.T) {
	g := newRig(at("03:00:07", 21))
	g.check(t, at("03:00:07", 21), "")
	g.check(t, at("03:00:59", 21), "")
	g.check(t, at("03:00:30", 22), Scheduled)
}

func TestConfiguredTimeAndMalformedFallsBackToTheDefault(t *testing.T) {
	g := newRig(at("12:00:00", 20))
	g.set.At = "04:30"
	g.check(t, at("03:00:00", 21), "")
	g.check(t, at("04:30:00", 21), Scheduled)
	for _, bad := range []string{"24:00", "4", "aa:bb", "-1:30", ""} {
		g := newRig(at("12:00:00", 20))
		g.set.At = bad
		g.check(t, at("03:00:00", 21), Scheduled)
	}
}

func TestParseHHMM(t *testing.T) {
	for in, ok := range map[string]bool{"00:00": true, "23:59": true, " 03:00 ": true, "3:5": true, "24:00": false, "12:60": false, "12": false, "a:b": false, "1:2:3": false} {
		if _, _, got := ParseHHMM(in); got != ok {
			t.Errorf("ParseHHMM(%q) ok %v, want %v", in, got, ok)
		}
	}
}

func TestRSSCeiling(t *testing.T) {
	g := newRig(at("12:00:00", 20))
	g.set.CeilingMB = 300
	g.rss = 299
	g.check(t, at("14:00:00", 20), "")
	g.rss = 300
	g.check(t, at("14:01:00", 20), RSSCeiling)

	// The default is max(320, 35% of RAM): 1433 MB on 4 GB.
	g = newRig(at("12:00:00", 20))
	g.rss = 1432
	g.check(t, at("14:00:00", 20), "")
	g.rss = 1433
	g.check(t, at("14:01:00", 20), RSSCeiling)
	if got := DefaultCeilingMB(400); got != 320 {
		t.Fatalf("a Zero 2 W keeps the 320 MB floor: %d", got)
	}
	if got := DefaultCeilingMB(0); got != 320 {
		t.Fatalf("unknown RAM: the floor, got %d", got)
	}

	// 0 turns the ceiling off.
	g = newRig(at("12:00:00", 20))
	g.set.CeilingMB = 0
	g.rss = 5000
	g.check(t, at("14:00:00", 20), "")
}

// A ceiling under the process's own baseline would exit every minute and trip systemd's start
// limit (the Python node's 80-second loop, 2026-08-03): the first hour up never fires it.
func TestRSSCeilingWaitsForAnHourUp(t *testing.T) {
	g := newRig(at("12:00:00", 20))
	g.set.CeilingMB = 100
	g.rss = 500
	g.check(t, at("12:01:00", 20), "")
	g.check(t, at("12:59:00", 20), "")
	g.check(t, at("13:00:00", 20), RSSCeiling)
}

func TestDisabledDoesNothing(t *testing.T) {
	g := newRig(at("12:00:00", 20))
	g.set.Enabled = false
	g.set.CeilingMB = 100
	g.rss = 5000
	g.check(t, at("03:00:00", 21), "")
	g.check(t, at("14:00:00", 21), "")
}

func TestRunPollsUntilExit(t *testing.T) {
	g := newRig(at("12:00:00", 20))
	g.clk.t = at("03:00:00", 21)
	g.r.Poll = time.Millisecond
	done := make(chan struct{})
	go func() { g.r.Run(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after it called Exit")
	}
	if len(g.exits) != 1 || g.exits[0] != Scheduled {
		t.Fatalf("exits %v", g.exits)
	}

	ctx, cancel := context.WithCancel(context.Background())
	g = newRig(at("12:00:00", 20))
	g.r.Poll = time.Millisecond
	done = make(chan struct{})
	go func() { g.r.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return when ctx ended")
	}
}
