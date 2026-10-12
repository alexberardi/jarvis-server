package island

import (
	"flag"
	"image"
	"image/png"
	"io"
	"log/slog"
	"os"
	"path/filepath"
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

type fakeBackend struct {
	mu                   sync.Mutex
	shows, frames, hides int
	closed               bool
	showErr              error
}

func (b *fakeBackend) show() (float64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.shows++
	return 1, b.showErr
}
func (b *fakeBackend) present(*image.RGBA) error { b.mu.Lock(); b.frames++; b.mu.Unlock(); return nil }
func (b *fakeBackend) hide()                     { b.mu.Lock(); b.hides++; b.mu.Unlock() }
func (b *fakeBackend) close()                    { b.mu.Lock(); b.closed = true; b.mu.Unlock() }
func (b *fakeBackend) counts() (int, int, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.shows, b.frames, b.hides
}

var quietLog = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestIslandRendersOnlyWhileVisible(t *testing.T) {
	be := &fakeBackend{}
	i := newIsland(be, quietLog)
	time.Sleep(100 * time.Millisecond)
	if s, f, _ := be.counts(); s != 0 || f != 0 {
		t.Fatalf("idle island did work: %d shows, %d frames", s, f)
	}
	i.Set(Listening, 0)
	time.Sleep(300 * time.Millisecond)
	s, f, _ := be.counts()
	if s != 1 || f < 3 {
		t.Fatalf("listening: %d shows, %d frames", s, f)
	}
	i.Set(Hidden, 0)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, _, h := be.counts(); h == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("never hid")
		}
		time.Sleep(20 * time.Millisecond)
	}
	_, f1, _ := be.counts()
	time.Sleep(200 * time.Millisecond)
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
	be := &fakeBackend{showErr: io.ErrUnexpectedEOF}
	i := newIsland(be, quietLog)
	defer i.Close()
	i.Set(Listening, 0)
	time.Sleep(150 * time.Millisecond)
	i.Set(Speaking, 0)
	time.Sleep(150 * time.Millisecond)
	if s, f, _ := be.counts(); s != 1 || f != 0 {
		t.Fatalf("after a failed show: %d shows, %d frames (want one try, then off)", s, f)
	}
}

func TestNilIslandIsANoOp(t *testing.T) {
	var i *Island
	i.Set(Listening, time.Second)
	i.Level(0.5)
	i.Close()
}
