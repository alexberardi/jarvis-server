//go:build darwin

package ax

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/macax"
)

// TestLiveFinderSnapshot snapshots Finder's front window on this Mac:
//
//	JARVIS_AX_LIVE=1 go test -run Live -v ./internal/agent/ax/
//
// It needs the Accessibility grant for the test binary or the terminal running it, and a Finder
// window open.
func TestLiveFinderSnapshot(t *testing.T) {
	if os.Getenv("JARVIS_AX_LIVE") != "1" {
		t.Skip("set JARVIS_AX_LIVE=1 to snapshot Finder's front window")
	}
	if !macax.Trusted() {
		t.Skip("this process is not trusted for Accessibility (System Settings → Privacy & Security → Accessibility)")
	}
	out, err := exec.Command("pgrep", "-x", "Finder").Output()
	if err != nil {
		t.Skipf("Finder isn't running: %v", err)
	}
	pid, err := strconv.Atoi(strings.Fields(string(out))[0])
	if err != nil {
		t.Fatal(err)
	}

	s, err := Snapshot(context.Background(), pid, 0)
	if code(err) == CodeNotFound {
		t.Skipf("Finder has no window open: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Elements) == 0 {
		t.Fatal("Finder's front window has no interactive elements")
	}
	for _, e := range s.Elements {
		t.Logf("%s %s %q %q %v %v", e.Ref, e.Role, e.Name, e.Value, e.State, e.Bounds)
	}

	el, err := Element(s.Gen, s.Elements[0].Ref)
	if err != nil || el.Role != s.Elements[0].Role {
		t.Errorf("Element(%s) = %+v, %v", s.Elements[0].Ref, el, err)
	}
	for _, e := range s.Elements {
		if b := e.Bounds; b != nil && b[2] > 0 && b[3] > 0 {
			at, err := ElementAt(float64(b[0]+b[2]/2), float64(b[1]+b[3]/2))
			if err != nil {
				t.Errorf("ElementAt %s's centre: %v", e.Ref, err)
			}
			t.Logf("ElementAt %s's centre: %+v", e.Ref, at)
			break
		}
	}

	if w, err := frontWindow(pid); err == nil {
		names := w.(elem).attrNames()
		w.release()
		if !strings.Contains(strings.Join(names, " "), "AXChildren") {
			t.Errorf("window attributes %v lack AXChildren", names)
		}
	}

	if _, err := Snapshot(context.Background(), pid, 3); err != nil {
		t.Fatal(err)
	}
	if err := Press(s.Gen, s.Elements[0].Ref); code(err) != CodeStaleRef {
		t.Errorf("Press with the old generation = %v, want %s", err, CodeStaleRef)
	}
}

// TestBindings resolves every symbol and makes real AX calls without needing a window: this
// process has none, so a trusted run gets not_found and an untrusted one not_trusted.
func TestBindings(t *testing.T) {
	if err := load(); err != nil {
		t.Fatal(err)
	}
	want := CodeNotFound
	if !macax.Trusted() {
		want = CodeNotTrusted
	}
	if _, err := Snapshot(context.Background(), os.Getpid(), 0); code(err) != want {
		t.Errorf("Snapshot of this process = %v, want %s", err, want)
	}
	if _, err := ElementAt(-10000, -10000); err != nil && code(err) != want {
		t.Errorf("ElementAt off screen = %v, want nil or %s", err, want)
	}
	if got := goString(cfstr("AXRole")); got != "AXRole" {
		t.Errorf("CFString round trip = %q", got)
	}
}
