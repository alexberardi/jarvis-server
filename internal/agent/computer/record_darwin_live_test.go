//go:build darwin

package computer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/input"
	"github.com/alexberardi/jarvis-server/internal/agent/macax"
)

// TestLiveDesktopRecording records this Mac's desktop through the real event tap:
//
//	JARVIS_RECORD_MAC_LIVE=1 go test -run Live -v ./internal/agent/computer/
//
// It scrolls the wheel once where the pointer is (a posted event the tap sees as the user's) and
// checks that it reaches the trace. With JARVIS_RECORD_MAC_LIVE_SECONDS=30 it records you for
// that long instead (type a list in Notes, then a password somewhere) and prints the trace.
// It needs Input Monitoring and Accessibility for the test binary or the terminal running it.
func TestLiveDesktopRecording(t *testing.T) {
	if os.Getenv("JARVIS_RECORD_MAC_LIVE") != "1" {
		t.Skip("set JARVIS_RECORD_MAC_LIVE=1 to record this desktop")
	}
	if !macax.ListenTrusted() {
		macax.ListenPrompt()
		t.Skip("not allowed to monitor input (System Settings → Privacy & Security → Input Monitoring); asked, rerun once granted")
	}
	if !macax.Trusted() {
		t.Skip("not trusted for Accessibility (System Settings → Privacy & Security → Accessibility)")
	}
	dir := t.TempDir()
	activity := &Activity{}
	r := &Recorder{Dir: filepath.Join(dir, "recordings"), Activity: activity,
		Sources: map[string]Source{ModeDesktop: NewDesktopSource(filepath.Join(dir, "recordings"), nil, nil)}}
	ctx := context.Background()
	id, err := r.Start(ctx, ModeDesktop)
	if err != nil {
		t.Fatal(err)
	}
	secs, _ := strconv.Atoi(os.Getenv("JARVIS_RECORD_MAC_LIVE_SECONDS"))
	if secs > 0 {
		t.Logf("recording for %d s: go ahead", secs)
		time.Sleep(time.Duration(secs) * time.Second)
	} else {
		in := input.Native()
		if ok, why := in.Available(ctx); !ok {
			_, _ = r.Stop(id)
			t.Skip(why)
		}
		m, ok := in.(*input.Mac)
		if !ok {
			_, _ = r.Stop(id)
			t.Skip("no CGEvent input")
		}
		x, y, err := m.Post.Location()
		if err != nil {
			_, _ = r.Stop(id)
			t.Skip(err)
		}
		if err := in.Scroll(ctx, int(x), int(y), 0, 1); err != nil {
			t.Error(err)
		}
		time.Sleep(time.Second) // for the tap to see it
	}
	tr, err := r.Stop(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range tr.Events {
		t.Logf("%s", e)
	}
	if secs > 0 {
		return
	}
	var scrolled bool
	for _, raw := range tr.Events {
		var e DesktopEvent
		if json.Unmarshal(raw, &e) == nil && e.Kind == InScroll {
			scrolled = true
		}
	}
	if !scrolled {
		t.Fatalf("the posted scroll isn't in the trace (%d events)", len(tr.Events))
	}
}
