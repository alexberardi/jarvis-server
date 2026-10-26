package hat

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"slices"
	"sync"
	"testing"
	"time"
)

// golden is tools/golden/export_hat.py's output: the Python node's patterns (scaled as its
// _render does) and apa102_pi's own frames.
type golden struct {
	Patterns []struct {
		Name   string     `json:"name"`
		T      float64    `json:"t"`
		Scale  int        `json:"scale"`
		Pixels [][4]uint8 `json:"pixels"`
	} `json:"patterns"`
	Frames []struct {
		Pixels [][4]uint8 `json:"pixels"`
		Bytes  []byte     `json:"bytes"`
	} `json:"frames"`
}

func loadGolden(t *testing.T) golden {
	t.Helper()
	b, err := os.ReadFile("testdata/hat_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var g golden
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	return g
}

func pixels(in [][4]uint8) []Pixel {
	out := make([]Pixel, len(in))
	for i, p := range in {
		out[i] = Pixel{p[0], p[1], p[2], p[3]}
	}
	return out
}

func TestPatternsMatchPython(t *testing.T) {
	g := loadGolden(t)
	names := map[string]bool{}
	for _, c := range g.Patterns {
		names[c.Name] = true
		if got, want := Render(c.Name, c.T, c.Scale), pixels(c.Pixels); !slices.Equal(got, want) {
			t.Errorf("%s at t=%v scale=%d: got %v, want %v", c.Name, c.T, c.Scale, got, want)
		}
	}
	for n := range patterns {
		if !names[n] && !goOnly[n] {
			t.Errorf("pattern %q has no Python golden", n)
		}
	}
	if len(names) != len(patterns)-len(goOnly) {
		t.Errorf("Python has %d patterns, Go %d besides its own", len(names), len(patterns)-len(goOnly))
	}
}

// goOnly are the Go node's patterns Python has no counterpart for.
var goOnly = map[string]bool{Setup: true}

// Setup mode's pattern (#292): a blue breath between 10% and 40%, 3 s a cycle.
func TestSetupPattern(t *testing.T) {
	for _, c := range []struct {
		t      float64
		bright uint8
	}{{0, 10}, {0.75, 25}, {1.5, 40}, {3, 10}} {
		want := Pixel{0, 60, 255, c.bright}
		if got := Render(Setup, c.t, 100); !slices.Equal(got, []Pixel{want, want, want}) {
			t.Errorf("t=%v: %v, want %v", c.t, got, want)
		}
	}
}

func TestFramesMatchAPA102Pi(t *testing.T) {
	for _, c := range loadGolden(t).Frames {
		if got := Frame(pixels(c.Pixels)); !slices.Equal(got, c.Bytes) {
			t.Errorf("%v: got % x, want % x", c.Pixels, got, c.Bytes)
		}
	}
}

// fakeStrip records frames.
type fakeStrip struct {
	mu     sync.Mutex
	frames [][]byte
	closed bool
}

func (f *fakeStrip) Write(b []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.frames = append(f.frames, slices.Clone(b))
	return len(b), nil
}
func (f *fakeStrip) Close() error { f.mu.Lock(); f.closed = true; f.mu.Unlock(); return nil }
func (f *fakeStrip) last() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.frames) == 0 {
		return nil
	}
	return f.frames[len(f.frames)-1]
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// hangGuard bounds every wait: what is waited for never depends on how long it takes.
const hangGuard = 10 * time.Second

// waitFor polls cond (the renderer runs on its own goroutine).
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(hangGuard)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// timers stands in for time.AfterFunc: nothing fires until the test says so.
type timers struct {
	mu  sync.Mutex
	fns map[time.Duration]func()
}

func newTimers() *timers { return &timers{fns: map[time.Duration]func(){}} }

func (ts *timers) after(d time.Duration, f func()) *time.Timer {
	ts.mu.Lock()
	ts.fns[d] = f
	ts.mu.Unlock()
	return time.AfterFunc(time.Hour, func() {}) // only for Stop; never fires in a test
}

// fire runs the callback last scheduled for d, as its timer would.
func (ts *timers) fire(t *testing.T, d time.Duration) {
	t.Helper()
	ts.mu.Lock()
	f := ts.fns[d]
	ts.mu.Unlock()
	if f == nil {
		t.Fatalf("nothing scheduled for %v", d)
	}
	f()
}

func shows(s *fakeStrip, name string, scale int) func() bool {
	want := Frame(Render(name, 0, scale))
	return func() bool { return slices.Equal(s.last(), want) }
}

func TestLEDsTwoLayers(t *testing.T) {
	s := &fakeStrip{}
	l := NewLEDs(s, quiet)
	waitFor(t, "normal at start", shows(s, Normal, 100))

	l.SetPattern(Alert)
	waitFor(t, "alert", shows(s, Alert, 100))
	l.SetTransient(Speaking)
	waitFor(t, "speaking over alert", shows(s, Speaking, 100))
	l.SetTransient("")
	waitFor(t, "back to alert", shows(s, Alert, 100))

	l.SetBrightness(50)
	waitFor(t, "alert at 50%", shows(s, Alert, 50))
	l.SetEnabled(false)
	waitFor(t, "disabled: dark", shows(s, Off, 0))
	l.SetEnabled(true)

	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(s.last(), Frame(Render(Off, 0, 0))) || !s.closed {
		t.Fatal("Close must leave the LEDs off and close the strip")
	}
}

func TestPreviewAutoClearsUnlessSuperseded(t *testing.T) {
	s := &fakeStrip{}
	l := NewLEDs(s, quiet)
	defer l.Close()
	ts := newTimers()
	l.afterFunc = ts.after
	l.Preview(Error, 80*time.Millisecond)
	if l.Current() != Error {
		t.Fatalf("preview not shown: %s", l.Current())
	}
	ts.fire(t, 80*time.Millisecond)
	if l.Current() != Normal {
		t.Fatalf("preview not cleared: %s", l.Current())
	}

	// A real state set during a preview must survive the preview's timer (Python's bug fix).
	l.Preview(WakeDetected, 80*time.Millisecond)
	l.SetTransient(WakeDetected)
	ts.fire(t, 80*time.Millisecond) // a timer Stop came too late for
	if l.Current() != WakeDetected {
		t.Fatalf("the preview timer cleared a real state: %s", l.Current())
	}
}

func TestUnchangedFramesAreNotRewritten(t *testing.T) {
	s := &fakeStrip{}
	l := NewLEDs(s, quiet)
	time.Sleep(300 * time.Millisecond) // ~6 ticks of a steady pattern
	l.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	if n := len(s.frames); n != 2 { // normal once, then off at Close
		t.Fatalf("%d writes for a steady pattern, want 2", n)
	}
}

// newTestButton is a button on a fake clock whose warning and hold fire only when the test
// fires them (warnAt and holdAt).
func newTestButton(l *LEDs) (*Button, chan string, *timers, *time.Time) {
	ev := make(chan string, 4)
	ts := newTimers()
	now := time.Unix(0, 0)
	b := &Button{LEDs: l, Log: quiet, WarnAfter: warnAt, HoldAfter: holdAt,
		OnShortPress: func() { ev <- "short" }, OnHold: func() { ev <- "hold" },
		now: func() time.Time { return now }, after: ts.after}
	return b, ev, ts, &now
}

const warnAt, holdAt = 40 * time.Millisecond, 120 * time.Millisecond

func expect(t *testing.T, ev chan string, want string) {
	t.Helper()
	select {
	case got := <-ev:
		if got != want {
			t.Fatalf("got %s, want %s", got, want)
		}
	case <-time.After(hangGuard):
		t.Fatalf("no %s", want)
	}
}

func noEvent(t *testing.T, ev chan string) {
	t.Helper()
	select {
	case got := <-ev:
		t.Fatalf("unexpected %s", got)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestButtonShortPress(t *testing.T) {
	l := NewLEDs(&fakeStrip{}, quiet)
	defer l.Close()
	b, ev, _, now := newTestButton(l)
	b.Edge(true)
	*now = now.Add(Debounce + 5*time.Millisecond)
	b.Edge(false)
	expect(t, ev, "short")
	noEvent(t, ev) // no hold after release
}

func TestButtonHoldWarnsThenHolds(t *testing.T) {
	l := NewLEDs(&fakeStrip{}, quiet)
	defer l.Close()
	b, ev, ts, now := newTestButton(l)
	b.Edge(true)
	ts.fire(t, warnAt)
	if l.Current() != ShutdownWarning {
		t.Fatalf("no shutdown warning: %s", l.Current())
	}
	ts.fire(t, holdAt)
	expect(t, ev, "hold")
	*now = now.Add(holdAt)
	b.Edge(false)
	noEvent(t, ev) // a hold is not also a short press
	if l.Current() != Normal {
		t.Fatalf("release must clear the overlay, showing %s", l.Current())
	}
}

func TestButtonReleaseBeforeHoldCancelsWarning(t *testing.T) {
	l := NewLEDs(&fakeStrip{}, quiet)
	defer l.Close()
	b, ev, ts, now := newTestButton(l)
	b.Edge(true)
	ts.fire(t, warnAt)
	if l.Current() != ShutdownWarning {
		t.Fatalf("no shutdown warning: %s", l.Current())
	}
	*now = now.Add(warnAt + Debounce)
	b.Edge(false)
	expect(t, ev, "short") // Python: released before the hold time is a short press
	noEvent(t, ev)
	if l.Current() != Normal {
		t.Fatalf("overlay left on: %s", l.Current())
	}
}

func TestButtonDebounce(t *testing.T) {
	b, ev, _, now := newTestButton(nil)
	b.Edge(true)
	*now = now.Add(5 * time.Millisecond)
	b.Edge(false) // bounce: ignored
	*now = now.Add(5 * time.Millisecond)
	b.Edge(true) // same state: ignored
	noEvent(t, ev)
	*now = now.Add(100 * time.Millisecond)
	b.Edge(false)
	expect(t, ev, "short")
	b.Close()
}

func TestAnimatedPatternsKeepRendering(t *testing.T) {
	s := &fakeStrip{}
	l := NewLEDs(s, quiet)
	defer l.Close()
	l.SetTransient(Thinking)
	waitFor(t, "the pinwheel to advance through its 3 steps", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		seen := map[string]bool{}
		for _, f := range s.frames {
			seen[string(f)] = true
		}
		return len(seen) >= 3 // 1, 2 and 3 LEDs lit
	})
}

func TestIdleDarkLightsOnlyWhileResponding(t *testing.T) {
	s := &fakeStrip{}
	l := NewLEDs(s, quiet)
	defer l.Close()
	waitFor(t, "normal at start", shows(s, Normal, 100))
	l.SetIdleDark(true)
	waitFor(t, "idle is dark", shows(s, Off, 0))
	l.SetTransient(Listening)
	waitFor(t, "listening still lights", shows(s, Listening, 100))
	l.SetTransient("")
	waitFor(t, "dark again after the turn", shows(s, Off, 0))
	l.SetIdleDark(false)
	waitFor(t, "idle glow back", shows(s, Normal, 100))
}
