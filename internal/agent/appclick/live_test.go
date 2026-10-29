//go:build linux

package appclick

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/atspi"
	"github.com/alexberardi/jarvis-server/internal/agent/computer"
	"github.com/alexberardi/jarvis-server/internal/computeruse"
)

// TestLiveLibreOffice drives a real LibreOffice Writer's accessibility tree: what its snapshot
// holds, how long it takes, and a voice "click Bold" through Source. It only runs inside
// scripts/appclick-live's isolated session (its own headless sway, session and accessibility
// buses, HOME and LibreOffice profile), never on a desktop someone is using (agent invariant 27).
func TestLiveLibreOffice(t *testing.T) {
	if os.Getenv("JARVIS_APPCLICK_LIVE") != "1" {
		t.Skip("set by scripts/appclick-live, inside its isolated session only")
	}
	rt := os.Getenv("XDG_RUNTIME_DIR")
	if !strings.HasPrefix(rt, os.TempDir()) || os.Getenv("JARVIS_APPCLICK_ISOLATED") != rt {
		t.Fatalf("not the isolated session (XDG_RUNTIME_DIR=%q): refusing to touch a real desktop", rt)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if st := atspi.Enable(ctx); !st.Enabled || st.Err != nil {
		t.Fatalf("accessibility bus: %+v", st)
	}
	ws := computer.NativeWindows()
	var win computer.DesktopWindow
	for deadline := time.Now().Add(90 * time.Second); time.Now().Before(deadline); time.Sleep(time.Second) {
		list, err := ws.List(ctx)
		if err == nil {
			for _, w := range list {
				if w.Focused && w.PID > 0 && strings.Contains(w.Title, "Writer") {
					win = w
				}
			}
		}
		if win.PID > 0 {
			if s, err := atspi.SnapshotShowing(ctx, win.PID, snapshotMax); err == nil && len(s.Elements) > 10 {
				break
			}
		}
	}
	if win.PID == 0 {
		t.Fatal("no focused Writer window appeared")
	}
	t.Logf("window: %+v", win)

	if full, err := atspi.Snapshot(ctx, win.PID, 1000); err == nil {
		t.Logf("full snapshot (computer tasks): %d elements", len(full.Elements))
	}
	start := time.Now()
	s, err := atspi.SnapshotShowing(ctx, win.PID, snapshotMax)
	took := time.Since(start)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	t.Logf("showing snapshot (voice): %d elements in %s", len(s.Elements), took.Round(time.Millisecond))
	var positioned, withContext int
	for _, e := range s.Elements {
		if e.Position != "" {
			positioned++
		}
		if e.Context != "" {
			withContext++
		}
		t.Logf("  %s %-14s %-32q pos=%-13s near=%q bounds=%v state=%v", e.Ref, e.Role, e.Name, e.Position, e.Context, e.Bounds, e.State)
	}
	t.Logf("with a position: %d/%d; with context: %d/%d", positioned, len(s.Elements), withContext, len(s.Elements))

	bold := find(s, "Bold")
	if bold == nil {
		t.Fatal(`no control named "Bold" in Writer's snapshot`)
	}
	src := &Source{Windows: ws, Snapshot: atspi.SnapshotShowing, Press: atspi.Press, Allowed: func() bool { return true }}
	out, ok := src.Direct(ctx, "click Bold")
	t.Logf(`"click Bold": %v %v`, out, ok)
	if !ok || out["success"] != true {
		t.Fatalf(`"click Bold" didn't press: %v %v`, out, ok)
	}
	after, err := atspi.SnapshotShowing(ctx, win.PID, snapshotMax)
	if err != nil {
		t.Fatal(err)
	}
	if b := find(after, "Bold"); b == nil || checked(b) == checked(bold) {
		t.Errorf("Bold's state didn't change: before %v, after %v", bold.State, stateOf(b))
	}
}

func find(s computeruse.Snapshot, name string) *computeruse.SnapElement {
	for i := range s.Elements {
		if strings.EqualFold(s.Elements[i].Name, name) {
			return &s.Elements[i]
		}
	}
	return nil
}

func checked(e *computeruse.SnapElement) bool {
	for _, st := range e.State {
		if st == "checked" {
			return true
		}
	}
	return false
}

func stateOf(e *computeruse.SnapElement) []string {
	if e == nil {
		return nil
	}
	return e.State
}
