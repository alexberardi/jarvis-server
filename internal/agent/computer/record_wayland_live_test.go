//go:build linux

package computer

import (
	"context"
	"encoding/json"
	"fmt"
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
	src := NewDesktopSource(e.r.Dir, screen.Native(), nil)
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

// What a recording reads besides the input, on this desktop, read-only (no input, no capture):
// the open windows and the front window's tree, as the start event and the first result read
// them. It logs counts and timings, never a title or a name.
func TestCaptureReadLive(t *testing.T) {
	if os.Getenv("JARVIS_RECORD_CAPTURE_LIVE") != "1" {
		t.Skip("JARVIS_RECORD_CAPTURE_LIVE=1 reads this desktop's window list and the front window's accessibility tree")
	}
	src, ok := NewDesktopSource(t.TempDir(), nil, nil).(*DesktopSource)
	if !ok {
		t.Fatal("no desktop source here")
	}
	var raw [][]byte
	d := &deskRecorder{id: "rec_live", p: src.Platform, now: time.Now, log: slogDiscard(),
		emit: func(b json.RawMessage) { raw = append(raw, b) }}
	began := time.Now()
	d.begin()
	if len(raw) != 1 {
		t.Fatalf("no start event (%d events)", len(raw))
	}
	var e DesktopEvent
	if err := json.Unmarshal(raw[0], &e); err != nil {
		t.Fatal(err)
	}
	t.Logf("start: %d windows, %d bytes; front window tree: %d named elements; %v", len(e.Windows), len(raw[0]), len(d.seen), time.Since(began).Round(time.Millisecond))
	if len(e.Windows) == 0 || !e.Windows[0].Focused {
		t.Fatalf("start names no front window")
	}
	d.owed = 1
	again := time.Now()
	d.result()
	if len(raw) != 2 {
		t.Fatal("no result")
	}
	t.Logf("result: %d bytes; %v", len(raw[1]), time.Since(again).Round(time.Millisecond))
}
