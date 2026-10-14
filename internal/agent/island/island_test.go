package island

import (
	"flag"
	"image"
	"image/png"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite testdata/*.png")

// The renderer's states, as small golden PNGs (look at them: go test -run Golden -update, then
// open testdata/). Compared with a tolerance: arm64 fuses multiply-adds, amd64 doesn't.
func TestRenderGolden(t *testing.T) {
	bars := [nBars]float64{0.4, 0.75, 0.95, 0.7, 0.45}
	for _, tc := range []struct {
		name string
		f    Frame
	}{
		{"listening", Frame{State: Listening, Appear: 1, Bars: bars, Glow: 0.7}},
		{"thinking", Frame{State: Thinking, Appear: 1, Bars: [nBars]float64{0.4, 0.3, 0.22, 0.2, 0.25}}},
		{"speaking", Frame{State: Speaking, Appear: 1, Bars: bars, Glow: 0.5}},
		{"follow_up", Frame{State: FollowUp, Appear: 1, Bars: [nBars]float64{0.1, 0.1, 0.1, 0.1, 0.1}, Ring: 0.62}},
		{"error", Frame{State: Error, Appear: 1, Bars: [nBars]float64{0.08, 0.08, 0.08, 0.08, 0.08}}},
		{"sliding_in", Frame{State: Listening, Appear: 0.45, Bars: bars}},
		{"note", Frame{State: Note, Appear: 1, Text: "Typing into Claude: test def"}},
		{"running", Frame{State: Note, Appear: 1, Text: RunningNote}},
		{"note_long", Frame{State: Note, Appear: 1, Text: "Send \"please run the whole test suite again and tell me which ones fail\" to Claude? Say yes or cancel."}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			img := image.NewRGBA(image.Rect(0, 0, CanvasW*2, CanvasH*2))
			Render(img, tc.f, 2)
			path := filepath.Join("testdata", tc.name+".png")
			if *update {
				writePNG(t, path, img)
				return
			}
			f, err := os.Open(path)
			if err != nil {
				t.Fatalf("%v (run with -update to create it)", err)
			}
			defer f.Close()
			want, err := png.Decode(f)
			if err != nil {
				t.Fatal(err)
			}
			if bad := differing(img, want); bad > len(img.Pix)/4/200 {
				t.Fatalf("%d pixels differ from %s", bad, path)
			}
		})
	}
}

func TestRenderHiddenIsTransparent(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, CanvasW, CanvasH))
	Render(img, Frame{State: Listening, Appear: 1}, 1)
	Render(img, Frame{}, 1)
	for _, b := range img.Pix {
		if b != 0 {
			t.Fatal("a hidden frame left pixels behind")
		}
	}
}

func writePNG(t *testing.T, path string, img image.Image) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

func differing(got *image.RGBA, want image.Image) int {
	if got.Bounds() != want.Bounds() {
		return got.Bounds().Dx() * got.Bounds().Dy()
	}
	n := 0
	b := got.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r1, g1, b1, a1 := got.At(x, y).RGBA()
			r2, g2, b2, a2 := want.At(x, y).RGBA()
			if far(r1, r2) || far(g1, g2) || far(b1, b2) || far(a1, a2) {
				n++
			}
		}
	}
	return n
}

func far(a, b uint32) bool { return max(a, b)-min(a, b) > 4*257 }

// --- the animation model ---

type clock struct{ t time.Time }

func (c *clock) now() time.Time                { return c.t }
func (c *clock) add(d time.Duration) time.Time { c.t = c.t.Add(d); return c.t }

// run steps m for d at 30 fps and returns the last frame.
func run(m *model, c *clock, d time.Duration) (Frame, bool) {
	var f Frame
	var vis bool
	for e := time.Duration(0); e < d; e += frameEvery {
		f, vis = m.step(c.add(frameEvery))
	}
	return f, vis
}

func TestModelSlidesInAndOut(t *testing.T) {
	c := &clock{t: time.Unix(1_700_000_000, 0)}
	var m model
	if _, vis := m.step(c.now()); vis {
		t.Fatal("visible before anything happened")
	}
	m.set(Listening, 0, c.now())
	f, vis := run(&m, c, 100*time.Millisecond)
	if !vis || f.Appear <= 0 || f.Appear >= 1 {
		t.Fatalf("mid slide-in: %+v %v", f, vis)
	}
	f, _ = run(&m, c, 300*time.Millisecond)
	if f.Appear != 1 || f.State != Listening {
		t.Fatalf("after the slide-in: %+v", f)
	}
	m.set(Hidden, 0, c.now())
	f, vis = run(&m, c, 100*time.Millisecond)
	if !vis || f.State != Listening {
		t.Fatalf("sliding out keeps its colours: %+v %v", f, vis)
	}
	if _, vis = run(&m, c, 300*time.Millisecond); vis {
		t.Fatal("still visible after sliding out")
	}
}

func TestModelBarsFollowTheLevel(t *testing.T) {
	c := &clock{t: time.Unix(1_700_000_000, 0)}
	var m model
	m.set(Listening, 0, c.now())
	quiet, _ := run(&m, c, time.Second)
	for range 15 {
		m.level(0.2) // about -14 dBFS: speech
		run(&m, c, frameEvery)
	}
	loud, _ := m.step(c.add(frameEvery))
	if sum(loud.Bars) < 2*sum(quiet.Bars) || loud.Glow < 0.5 {
		t.Fatalf("bars %v (quiet %v), glow %.2f", loud.Bars, quiet.Bars, loud.Glow)
	}
	// Silence lets them fall back.
	after, _ := run(&m, c, time.Second)
	if sum(after.Bars) > sum(loud.Bars)/2 {
		t.Fatalf("bars stayed up after the voice stopped: %v", after.Bars)
	}
}

func TestModelFollowUpRingCountsDown(t *testing.T) {
	c := &clock{t: time.Unix(1_700_000_000, 0)}
	var m model
	m.set(FollowUp, 8*time.Second, c.now())
	f, _ := run(&m, c, 2*time.Second)
	if f.Ring < 0.70 || f.Ring > 0.78 {
		t.Fatalf("ring after 2 of 8 s: %.3f", f.Ring)
	}
	// An ignored answer: the window carries on with what is left, so the ring restarts full
	// against the shorter window rather than a fresh 8 s.
	m.set(FollowUp, 3*time.Second, c.now())
	f, _ = run(&m, c, 1500*time.Millisecond)
	if f.Ring < 0.45 || f.Ring > 0.55 {
		t.Fatalf("ring 1.5 s into a 3 s remainder: %.3f", f.Ring)
	}
}

func TestModelErrorFlashesThenHides(t *testing.T) {
	c := &clock{t: time.Unix(1_700_000_000, 0)}
	var m model
	m.set(Thinking, 0, c.now())
	run(&m, c, 500*time.Millisecond)
	m.set(Error, 0, c.now())
	if f, vis := run(&m, c, time.Second); !vis || f.State != Error {
		t.Fatalf("error not shown: %+v", f)
	}
	if _, vis := run(&m, c, time.Second); vis {
		t.Fatal("error flash never went away")
	}
}

func sum(b [nBars]float64) float64 {
	s := 0.0
	for _, v := range b {
		s += v
	}
	return s
}

// --- the island's loop over a fake backend ---

// The loop runs on a fake clock and fake frame ticks the test delivers, so nothing here
// depends on how fast the machine is: wall-clock timeouts are only hang guards.

const hangGuard = 10 * time.Second

type fakeBackend struct {
	mu                   sync.Mutex
	shows, frames, hides int
	closed               bool
	showErr              error
	hid                  chan struct{} // one send per hide
}

func newFakeBackend() *fakeBackend { return &fakeBackend{hid: make(chan struct{}, 16)} }

func (b *fakeBackend) show() (float64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.shows++
	return 1, b.showErr
}
func (b *fakeBackend) present(*image.RGBA) error { b.mu.Lock(); b.frames++; b.mu.Unlock(); return nil }
func (b *fakeBackend) hide()                     { b.mu.Lock(); b.hides++; b.mu.Unlock(); b.hid <- struct{}{} }
func (b *fakeBackend) close()                    { b.mu.Lock(); b.closed = true; b.mu.Unlock() }
func (b *fakeBackend) counts() (int, int, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.shows, b.frames, b.hides
}

var quietLog = slog.New(slog.NewTextHandler(io.Discard, nil))

// frames drives an island: its clock moves only when a frame tick is delivered.
type frames struct {
	t    *testing.T
	mu   sync.Mutex
	at   time.Time
	tick chan time.Time
}

func newDriven(t *testing.T, be backend) (*Island, *frames) {
	d := &frames{t: t, at: time.Unix(1_700_000_000, 0), tick: make(chan time.Time)}
	i := (&Island{be: be, log: quietLog, now: d.now, suspendWait: time.Minute,
		ticker: func(time.Duration) (<-chan time.Time, func()) { return d.tick, func() {} }}).start()
	return i, d
}

func (d *frames) now() time.Time { d.mu.Lock(); defer d.mu.Unlock(); return d.at }

// next delivers one frame tick. The loop takes it only once it has presented the frame
// before, so when next returns that frame is on the backend.
func (d *frames) next() {
	d.t.Helper()
	d.mu.Lock()
	d.at = d.at.Add(frameEvery)
	at := d.at
	d.mu.Unlock()
	select {
	case d.tick <- at:
	case <-time.After(hangGuard):
		d.t.Fatal("the island stopped taking frames")
	}
}

// untilHidden delivers frames until the backend hides.
func (d *frames) untilHidden(be *fakeBackend) {
	d.t.Helper()
	for {
		d.mu.Lock()
		d.at = d.at.Add(frameEvery)
		at := d.at
		d.mu.Unlock()
		select {
		case d.tick <- at:
		case <-be.hid:
			return
		case <-time.After(hangGuard):
			d.t.Fatal("never hid")
		}
	}
}

// waitFor polls a condition the island reaches without frame ticks.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(hangGuard)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestIslandRendersOnlyWhileVisible(t *testing.T) {
	be := newFakeBackend()
	i, d := newDriven(t, be)
	if s, f, _ := be.counts(); s != 0 || f != 0 {
		t.Fatalf("idle island did work: %d shows, %d frames", s, f)
	}
	i.Set(Listening, 0)
	for range 3 {
		d.next()
	}
	s, f, _ := be.counts()
	if s != 1 || f < 3 {
		t.Fatalf("listening: %d shows, %d frames", s, f)
	}
	i.Set(Hidden, 0)
	d.untilHidden(be)
	_, f1, h := be.counts()
	if h != 1 {
		t.Fatalf("%d hides", h)
	}
	// Hidden, the loop waits for a Set: it takes no frame and draws nothing.
	select {
	case d.tick <- d.now():
		t.Fatal("still rendering while hidden")
	case <-time.After(50 * time.Millisecond):
	}
	if _, f2, _ := be.counts(); f2 != f1 {
		t.Fatalf("still rendering while hidden: %d → %d frames", f1, f2)
	}
	i.Close()
	if !be.closed {
		t.Fatal("backend not closed")
	}
	i.Set(Listening, 0) // after Close: a no-op, not a panic
}

func TestIslandTurnsOffWhenTheBackendFails(t *testing.T) {
	be := newFakeBackend()
	be.showErr = io.ErrUnexpectedEOF
	i, _ := newDriven(t, be)
	defer i.Close()
	i.Set(Listening, 0)
	waitFor(t, "never tried to show", func() bool { i.mu.Lock(); defer i.mu.Unlock(); return i.off })
	i.Set(Speaking, 0)
	if s, f, _ := be.counts(); s != 1 || f != 0 {
		t.Fatalf("after a failed show: %d shows, %d frames (want one try, then off)", s, f)
	}
}

func TestNilIslandIsANoOp(t *testing.T) {
	var i *Island
	i.Set(Listening, time.Second)
	i.Level(0.5)
	i.Suspend()()
	i.Close()
}

// TestSuspendHidesAtOnceAndResumes: a screenshot (desktop_read_screen) must not show the
// island: Suspend returns once it is off the screen, and resume brings the state back.
func TestSuspendHidesAtOnceAndResumes(t *testing.T) {
	be := newFakeBackend()
	i, d := newDriven(t, be)
	defer i.Close()
	i.Set(Thinking, 0)
	d.next() // shown, one frame up

	// Suspend waits for the loop's next frame, which hides instead of drawing. Its own
	// timeout is a minute here, so returning at all means the hide was acknowledged.
	resumed := make(chan func(), 1)
	go func() { resumed <- i.Suspend() }()
	d.untilHidden(be)
	var resume func()
	select {
	case resume = <-resumed:
	case <-time.After(hangGuard):
		t.Fatal("Suspend did not return once the island was hidden")
	}
	if _, _, h := be.counts(); h != 1 {
		t.Fatalf("Suspend returned with the island still up (%d hides)", h)
	}
	_, f1, _ := be.counts()

	// A second capture meanwhile keeps it hidden until both are done. The hidden loop needs
	// no frame to acknowledge, so this Suspend returns by itself.
	resume2 := i.Suspend()
	resume()
	resume() // idempotent
	if s, f, _ := be.counts(); s != 1 || f != f1 {
		t.Fatalf("shown again while a capture was still running (%d shows, %d frames)", s, f)
	}
	resume2()
	d.next() // back up, one frame drawn
	if s, f, _ := be.counts(); s != 2 || f <= f1 {
		t.Fatalf("not back after resume: %d shows, %d frames", s, f)
	}
	// Suspending a hidden island returns at once, too: no frame is delivered meanwhile.
	i.Set(Hidden, 0)
	d.untilHidden(be)
	i.Suspend()()
}

func TestNoteOverStates(t *testing.T) {
	t0 := time.Unix(100, 0)
	var m model
	m.set(Thinking, 0, t0)
	m.setNote("Typing: hello", 2*time.Second, t0)
	if f, _ := m.step(t0.Add(100 * time.Millisecond)); f.State != Note || f.Text != "Typing: hello" {
		t.Fatalf("note: %+v", f)
	}
	if f, _ := m.step(t0.Add(2100 * time.Millisecond)); f.State != Thinking || f.Text != "" {
		t.Fatalf("after the note: %+v", f)
	}
	// Over a hidden island: it slides in with the text, and out with it.
	var h model
	h.setNote("Pressing Enter", time.Second, t0)
	var f Frame
	for d := time.Duration(0); d <= 500*time.Millisecond; d += 50 * time.Millisecond {
		f, _ = h.step(t0.Add(d))
	}
	if f.State != Note || f.Appear != 1 {
		t.Fatalf("note over hidden: %+v", f)
	}
	f, visible := h.step(t0.Add(1100 * time.Millisecond))
	if !visible || f.State != Note || f.Text != "Pressing Enter" || f.Appear >= 1 {
		t.Fatalf("sliding out: %+v %v", f, visible)
	}
	h.setNote("x", time.Minute, t0)
	h.setNote("", 0, t0)
	if h.shown(t0) != Hidden {
		t.Fatal("an empty note doesn't clear")
	}
}

func TestShowRunning(t *testing.T) {
	var i *Island
	i.ShowRunning() // nil: no island, no panic
	var m model
	now := time.Unix(1000, 0)
	m.setNote(RunningNote, RunningFor, now)
	if m.shown(now) != Note || m.shown(now.Add(RunningFor+time.Millisecond)) != Hidden {
		t.Fatal("the running pill should show, then hide")
	}
}

func TestFitNote(t *testing.T) {
	face := noteFace(2)
	if face == nil {
		t.Fatal("no font")
	}
	short, w := fitNote(face, "hi", 2)
	if short != "hi" || w != pillW {
		t.Errorf("short note %q %v", short, w)
	}
	long, w := fitNote(face, strings.Repeat("dictation ", 40), 2)
	if !strings.HasSuffix(long, "…") || w < noteMaxW-8 || w > noteMaxW || len(long) > 200 {
		t.Errorf("long note %q %v", long, w)
	}
}
