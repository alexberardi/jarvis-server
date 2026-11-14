//go:build linux

package computer

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/screen"
)

// The real Wayland path on this desktop (Hyprland or sway, the user in the input group): the
// keyboard and mouse devices, the compositor, AT-SPI and grim. Nothing can be faked into evdev
// without root, so the person at the desktop does the input: click anywhere, type "hi" into a
// text field and press Enter within JARVIS_RECORD_WAYLAND_SECONDS (15). Keyframes stay in the
// test's temp dir and go with it.
func TestWaylandRecordLive(t *testing.T) {
	if os.Getenv("JARVIS_RECORD_WAYLAND_LIVE") != "1" {
		t.Skip("JARVIS_RECORD_WAYLAND_LIVE=1 records this Wayland desktop while you click and type (needs the input group)")
	}
	secs, _ := strconv.Atoi(os.Getenv("JARVIS_RECORD_WAYLAND_SECONDS"))
	if secs <= 0 {
		secs = 15
	}
	e := newRecEnv(t)
	// The recorder's own debug lines (why a keyframe or an element is missing) go to the test log.
	log := slog.New(slog.NewTextHandler(testLog{t}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	src := NewDesktopSource(e.r.Dir, screen.Native(), log)
	e.r.Sources[ModeDesktop] = src
	id, err := e.r.Start(context.Background(), ModeDesktop)
	if err != nil {
		t.Fatal(err)
	}
	if w := src.(Warner).Warning(); w != "" {
		t.Logf("start warning: %s", w)
	}
	fmt.Fprintf(os.Stderr, "\nRecording for %d s: click anywhere, then type hi into a text field and press Enter.\n", secs)
	time.Sleep(time.Duration(secs) * time.Second)
	tr, err := e.r.Stop(id)
	if err != nil {
		t.Fatal(err)
	}
	evs := traceEvents(t, tr)
	t.Logf("trace: %s", summary(evs))
	for _, k := range tr.Keyframes {
		st, err := os.Stat(k.Path)
		if err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("keyframe %s: %v %v", k.Path, err, st)
		}
		t.Logf("keyframe %d: %s crop %q", k.Seq, k.Path, k.Crop)
	}
	var click, typed, enter, start, result bool
	for _, ev := range evs {
		switch {
		case ev.Kind == KindStart:
			start = len(ev.Windows) > 0
		case ev.Kind == KindResult:
			result = result || (ev.Of > 0 && ev.Window != nil)
		case ev.Kind == InClick:
			click = true
			if ev.Window == nil || ev.App == "" {
				t.Errorf("a click without its window: %+v", ev)
			}
			if os.Getenv("HYPRLAND_INSTANCE_SIGNATURE") != "" && ev.X == 0 && ev.Y == 0 {
				t.Errorf("a click on Hyprland without the cursor's point: %+v", ev)
			}
		case ev.Kind == "input" && (strings.Contains(ev.Value, "hi") || ev.Masked):
			typed = true
		case ev.Kind == InKey && ev.Key == KeyEnter:
			enter = true
		}
	}
	if !start || !result {
		t.Errorf("want the open windows at the start and a result after an action; got start %v result %v", start, result)
	}
	if !click || !typed || !enter || len(tr.Keyframes) == 0 {
		t.Fatalf("want a click, the typed hi, Enter and a keyframe; got click %v typed %v enter %v keyframes %d",
			click, typed, enter, len(tr.Keyframes))
	}
}

// testLog writes a logger's lines to the test's log.
type testLog struct{ t *testing.T }

func (l testLog) Write(b []byte) (int, error) {
	l.t.Log(strings.TrimSpace(string(b)))
	return len(b), nil
}
