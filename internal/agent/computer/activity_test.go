package computer

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

// recorder is an indicator (the island, the tray) that keeps what it was shown.
type recorder struct {
	mu    sync.Mutex
	shown []string
}

func (r *recorder) show(k Kind, text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := string(k)
	if k == Idle {
		name = "idle"
	}
	r.shown = append(r.shown, fmt.Sprintf("%s %q", name, text))
}

func (r *recorder) all() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.shown, ", ")
}

func TestSetDrivesEveryIndicator(t *testing.T) {
	var a Activity
	island, tray := &recorder{}, &recorder{}
	a.Show(island.show)
	a.Set(Controlling, "Ordering the blue mug")
	a.Show(tray.show) // a late indicator starts with what is going on
	a.Pause()
	a.Set(Recording, "")
	a.Set(Idle, "")
	if got, want := island.all(), `idle "", controlling "Ordering the blue mug", paused "Ordering the blue mug", recording "", idle ""`; got != want {
		t.Fatalf("island:\n got %s\nwant %s", got, want)
	}
	if got, want := tray.all(), `controlling "Ordering the blue mug", paused "Ordering the blue mug", recording "", idle ""`; got != want {
		t.Fatalf("tray:\n got %s\nwant %s", got, want)
	}
}

func TestStatus(t *testing.T) {
	var a Activity
	if a.String() != "idle" {
		t.Fatalf("idle: %q", a.String())
	}
	a.Set(Controlling, "Ordering the blue mug")
	if k, text := a.Status(); k != Controlling || text != "Ordering the blue mug" || a.String() != "controlling: Ordering the blue mug" {
		t.Fatalf("controlling: %v %q %q", k, text, a.String())
	}
	a.Set(Recording, "")
	if a.String() != "recording" {
		t.Fatalf("recording: %q", a.String())
	}
	// Pause only pauses something going on.
	a.Set(Idle, "")
	a.Pause()
	if k, _ := a.Status(); k != Idle {
		t.Fatalf("paused nothing: %v", k)
	}
}

// TestStopFansOut: Stop ends every run (the executor's and recorder's Runs.Stop), clears the
// indicators, and says whether anything was going on.
func TestStopFansOut(t *testing.T) {
	var a Activity
	ind := &recorder{}
	a.Show(ind.show)
	var mu sync.Mutex
	var stopped []string
	rec := func(s string) func() { return func() { mu.Lock(); stopped = append(stopped, s); mu.Unlock() } }
	a.OnStop(rec("executor"))
	remove := a.OnStop(rec("recorder"))
	// A stopper may set the activity itself (a run winding down says so): no deadlock.
	a.OnStop(func() { a.Set(Idle, "") })

	a.Set(Controlling, "Ordering the blue mug")
	if !a.Stop() {
		t.Fatal("Stop while controlling said nothing was going on")
	}
	if k, _ := a.Status(); k != Idle {
		t.Fatalf("after Stop: %v", k)
	}
	if !strings.Contains(ind.all(), `controlling "Ordering the blue mug", idle ""`) {
		t.Fatalf("indicators: %s", ind.all())
	}
	mu.Lock()
	if strings.Join(stopped, ",") != "executor,recorder" {
		t.Fatalf("stopped %q", stopped)
	}
	stopped = nil
	mu.Unlock()

	remove()
	if a.Stop() {
		t.Fatal("Stop while idle said something was going on")
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(stopped, ",") != "executor" {
		t.Fatalf("after remove, stopped %q", stopped)
	}
}

func TestNilActivity(t *testing.T) {
	var a *Activity
	a.Set(Controlling, "x")
	a.Pause()
	if a.Stop() || a.String() != "idle" {
		t.Fatal("a nil Activity does nothing")
	}
}
