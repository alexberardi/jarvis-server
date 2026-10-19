package computer

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/input"
)

// ErrPaused is what an action gets while the task is paused: the user took over the mouse or
// keyboard, and the task waits until it is resumed (or stopped).
var ErrPaused = errors.New("paused: the user is using the computer")

// InputMonitor tells when the user last used the mouse or keyboard: the last HID activity
// (input.NativeMonitor).
type InputMonitor interface {
	Since(ctx context.Context) (time.Time, error)
}

const (
	guardEvery = 100 * time.Millisecond
	// guardSettle is how long after one of our injections its events may still show as HID
	// activity.
	guardSettle = 250 * time.Millisecond
	// guardDrift is how far (px) the pointer may be from where we left it before it is the
	// user's hand.
	guardDrift = 3
	// guardWindows is how many of our recent injection windows are remembered.
	guardWindows = 16
)

type window struct{ start, end time.Time } // end zero: still posting

// Guard pauses a computer task the moment the user moves the mouse or types. While Jarvis
// controls the computer, Run polls every 100 ms: HID activity outside every one of our own
// injection windows ([start, end+250ms]), or the pointer more than 3 px from where our last
// action left it, is the user's. Then the task pauses: Activity shows Paused, and every action
// through Guarded (or after Check) gets ErrPaused until Resume.
//
// The executor calls Begin when a task starts, runs Run on a goroutine, sends its actions
// through Guarded and calls Resume when told to (computer_act {resume: true}, or the server's
// Paused card).
type Guard struct {
	Input    input.Input
	Monitor  InputMonitor // nil: the pointer check only
	Activity *Activity
	Now      func() time.Time // nil: time.Now
	Every    time.Duration    // 0: 100 ms

	mu      sync.Mutex
	paused  bool
	base    time.Time // activity at or before it is not the user's (Begin, Resume)
	windows []window  // our recent injections, newest last
	x, y    int       // where our last action left the pointer
	known   bool      // x, y are known
	lastEnd time.Time // end of the injection x, y were read after
}

func (g *Guard) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

// Begin takes now and the pointer where it is as the baseline: a task starts (or resumes).
func (g *Guard) Begin(ctx context.Context) {
	x, y, err := g.Input.Cursor(ctx)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.paused = false
	g.base = g.now()
	g.windows = g.windows[:0]
	g.x, g.y, g.known = x, y, err == nil
	_, g.lastEnd = g.Input.Injecting()
}

// Run polls until ctx ends.
func (g *Guard) Run(ctx context.Context) {
	every := g.Every
	if every <= 0 {
		every = guardEvery
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			g.poll(ctx)
		}
	}
}

// Check is ErrPaused while the task is paused.
func (g *Guard) Check() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.paused {
		return ErrPaused
	}
	return nil
}

// Resume continues a paused task: what the user did meanwhile is the new baseline, and the
// activity shows controlling again, with the task's intent.
func (g *Guard) Resume(ctx context.Context) {
	g.Begin(ctx)
	if k, text := g.Activity.Status(); k == Paused {
		g.Activity.Set(Controlling, text)
	}
}

// poll looks once for the user's hand, and pauses the task when it finds it.
func (g *Guard) poll(ctx context.Context) {
	if g.Check() != nil {
		return
	}
	if g.look(ctx) {
		g.Activity.Pause() // not under g.mu: its indicators run in it
	}
}

// look reports whether the user's hand is on the mouse or keyboard; then the guard is paused.
func (g *Guard) look(ctx context.Context) (paused bool) {
	start, end := g.Input.Injecting()
	var last time.Time
	var monErr error
	if g.Monitor != nil {
		last, monErr = g.Monitor.Since(ctx)
	}
	injecting := !start.IsZero() && end.IsZero()
	var x, y int
	var curErr error
	if !injecting {
		x, y, curErr = g.Input.Cursor(ctx)
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	if g.paused {
		return false
	}
	if !start.IsZero() {
		g.saw(window{start, end})
	}
	if g.Monitor != nil && monErr == nil && last.After(g.base) && !g.ours(last) {
		g.paused = true
		return true
	}
	if injecting || curErr != nil {
		return false
	}
	if end.After(g.lastEnd) || !g.known {
		// An injection ended that we didn't see the pointer after: it is where it left it.
		g.x, g.y, g.known, g.lastEnd = x, y, true, end
		return false
	}
	if dx, dy := x-g.x, y-g.y; dx*dx+dy*dy > guardDrift*guardDrift {
		g.paused = true
		return true
	}
	return false
}

// saw remembers one of our injection windows (once). g.mu is held.
func (g *Guard) saw(w window) {
	for i := range g.windows {
		if g.windows[i].start.Equal(w.start) {
			g.windows[i].end = w.end
			return
		}
	}
	if len(g.windows) == guardWindows {
		g.windows = append(g.windows[:0], g.windows[1:]...)
	}
	g.windows = append(g.windows, w)
}

// ours says whether HID activity at t falls in one of our injection windows. g.mu is held.
func (g *Guard) ours(t time.Time) bool {
	for _, w := range g.windows {
		if t.Before(w.start) {
			continue
		}
		if w.end.IsZero() || !t.After(w.end.Add(guardSettle)) {
			return true
		}
	}
	return false
}

// injected records an action's window and where it left the pointer.
func (g *Guard) injected(ctx context.Context) {
	start, end := g.Input.Injecting()
	x, y, err := g.Input.Cursor(ctx)
	g.mu.Lock()
	defer g.mu.Unlock()
	if !start.IsZero() {
		g.saw(window{start, end})
	}
	if err == nil && !end.IsZero() {
		g.x, g.y, g.known, g.lastEnd = x, y, true, end
	}
}

// Guarded is g.Input with the guard in front: every action is refused with ErrPaused while the
// task is paused, and its injection window and where it left the pointer are recorded (polls
// may miss a short action between two others).
func (g *Guard) Guarded() input.Input { return guarded{g} }

type guarded struct{ g *Guard }

func (in guarded) do(ctx context.Context, f func() error) error {
	if err := in.g.Check(); err != nil {
		return err
	}
	err := f()
	in.g.injected(ctx)
	return err
}

func (in guarded) Available(ctx context.Context) (bool, string) {
	return in.g.Input.Available(ctx)
}

func (in guarded) Move(ctx context.Context, x, y int) error {
	return in.do(ctx, func() error { return in.g.Input.Move(ctx, x, y) })
}

func (in guarded) Button(ctx context.Context, b string, down bool) error {
	return in.do(ctx, func() error { return in.g.Input.Button(ctx, b, down) })
}

func (in guarded) Click(ctx context.Context, x, y int, b string, n int) error {
	return in.do(ctx, func() error { return in.g.Input.Click(ctx, x, y, b, n) })
}

func (in guarded) Drag(ctx context.Context, path [][2]int) error {
	return in.do(ctx, func() error { return in.g.Input.Drag(ctx, path) })
}

func (in guarded) Scroll(ctx context.Context, x, y, dx, dy int) error {
	return in.do(ctx, func() error { return in.g.Input.Scroll(ctx, x, y, dx, dy) })
}

func (in guarded) Keys(ctx context.Context, chord []string) error {
	return in.do(ctx, func() error { return in.g.Input.Keys(ctx, chord) })
}

func (in guarded) Type(ctx context.Context, text string) error {
	return in.do(ctx, func() error { return in.g.Input.Type(ctx, text) })
}

func (in guarded) Cursor(ctx context.Context) (int, int, error) { return in.g.Input.Cursor(ctx) }

func (in guarded) Injecting() (time.Time, time.Time) { return in.g.Input.Injecting() }
