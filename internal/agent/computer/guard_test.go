package computer

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeInput is an input.Input whose pointer and injection window the test sets; every action
// "posts" over step of the fake clock, as an injection window.
type fakeInput struct {
	mu         sync.Mutex
	clock      *fakeClock
	x, y       int
	start, end time.Time
	calls      int
}

func (f *fakeInput) Available(context.Context) (bool, string) { return true, "" }

func (f *fakeInput) inject(x, y int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.start = f.clock.Now()
	f.clock.Advance(10 * time.Millisecond)
	f.x, f.y = x, y
	f.end = f.clock.Now()
	return nil
}

func (f *fakeInput) Move(_ context.Context, x, y int) error { return f.inject(x, y) }
func (f *fakeInput) Button(context.Context, string, bool) error {
	return f.inject(f.x, f.y)
}
func (f *fakeInput) Click(_ context.Context, x, y int, _ string, _ int) error {
	return f.inject(x, y)
}
func (f *fakeInput) Drag(_ context.Context, path [][2]int) error {
	p := path[len(path)-1]
	return f.inject(p[0], p[1])
}
func (f *fakeInput) Scroll(_ context.Context, x, y, _, _ int) error { return f.inject(x, y) }
func (f *fakeInput) Keys(context.Context, []string) error           { return f.inject(f.x, f.y) }
func (f *fakeInput) Type(context.Context, string) error             { return f.inject(f.x, f.y) }

func (f *fakeInput) Cursor(context.Context) (int, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.x, f.y, nil
}

func (f *fakeInput) Injecting() (time.Time, time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.start, f.end
}

// user moves the pointer as the user would: no injection window.
func (f *fakeInput) user(x, y int) {
	f.mu.Lock()
	f.x, f.y = x, y
	f.mu.Unlock()
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// fakeMonitor is the last HID activity, as the test sets it.
type fakeMonitor struct {
	mu   sync.Mutex
	last time.Time
}

func (m *fakeMonitor) Since(context.Context) (time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.last, nil
}

func (m *fakeMonitor) set(t time.Time) {
	m.mu.Lock()
	m.last = t
	m.mu.Unlock()
}

type guardFixture struct {
	clock *fakeClock
	in    *fakeInput
	mon   *fakeMonitor
	act   *Activity
	g     *Guard
}

func newGuard(t *testing.T) *guardFixture {
	t.Helper()
	clock := &fakeClock{now: time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)}
	in := &fakeInput{clock: clock, x: 100, y: 100}
	mon := &fakeMonitor{last: clock.Now().Add(-time.Minute)}
	act := &Activity{}
	act.Set(Controlling, "Ordering the blue mug")
	g := &Guard{Input: in, Monitor: mon, Activity: act, Now: clock.Now}
	g.Begin(context.Background())
	return &guardFixture{clock, in, mon, act, g}
}

// TestOwnInputIgnored: HID activity inside our injection windows (and up to 500 ms after one)
// and the pointer where our last action left it are not the user's.
func TestOwnInputIgnored(t *testing.T) {
	ctx := context.Background()
	f := newGuard(t)
	in := f.g.Guarded()

	if err := in.Move(ctx, 300, 200); err != nil {
		t.Fatal(err)
	}
	f.mon.set(f.clock.Now()) // our move's last event
	f.clock.Advance(100 * time.Millisecond)
	f.g.poll(ctx)

	// Two actions between polls: the earlier one's events are ours too.
	in.Click(ctx, 310, 210, "left", 1)
	f.clock.Advance(30 * time.Millisecond)
	f.mon.set(f.clock.Now()) // 30 ms after the click: within its end+500ms
	f.clock.Advance(10 * time.Millisecond)
	in.Keys(ctx, []string{"cmd", "a"})
	f.clock.Advance(100 * time.Millisecond)
	f.g.poll(ctx)

	// An action left in progress: its window has no end yet (and the earlier ones' 500 ms are
	// long over).
	f.clock.Advance(600 * time.Millisecond)
	f.in.mu.Lock()
	f.in.start, f.in.end = f.clock.Now(), time.Time{}
	f.in.mu.Unlock()
	f.clock.Advance(30 * time.Millisecond)
	f.mon.set(f.clock.Now())
	f.g.poll(ctx)

	if err := f.g.Check(); err != nil {
		t.Fatalf("our own input paused the task: %v", err)
	}
	if k, _ := f.act.Status(); k != Controlling {
		t.Fatalf("activity is %q, want controlling", k)
	}
}

// TestUserInputPauses: HID activity outside every own window pauses the task: the island says
// paused, with the task's intent kept.
func TestUserInputPauses(t *testing.T) {
	ctx := context.Background()
	f := newGuard(t)
	f.g.Guarded().Move(ctx, 300, 200)
	f.clock.Advance(time.Second)
	f.mon.set(f.clock.Now().Add(-100 * time.Millisecond)) // a keypress long after our move
	f.g.poll(ctx)

	if !errors.Is(f.g.Check(), ErrPaused) {
		t.Fatal("the user's input didn't pause the task")
	}
	if k, text := f.act.Status(); k != Paused || text != "Ordering the blue mug" {
		t.Fatalf("activity is %q %q, want paused with the intent", k, text)
	}
}

// TestCursorDriftPauses: the pointer more than 3 px from where we left it is the user's hand,
// even when the monitor saw nothing; 3 px or less is not.
func TestCursorDriftPauses(t *testing.T) {
	ctx := context.Background()
	f := newGuard(t)
	f.g.Guarded().Move(ctx, 300, 200)
	f.clock.Advance(100 * time.Millisecond)

	f.in.user(303, 200)
	f.g.poll(ctx)
	if err := f.g.Check(); err != nil {
		t.Fatalf("3 px of drift paused the task: %v", err)
	}
	f.in.user(305, 200)
	f.g.poll(ctx)
	if !errors.Is(f.g.Check(), ErrPaused) {
		t.Fatal("the pointer moved 5 px and the task didn't pause")
	}
}

// TestPausedRefusesActions: once paused, every action is refused with ErrPaused and posts
// nothing.
func TestPausedRefusesActions(t *testing.T) {
	ctx := context.Background()
	f := newGuard(t)
	in := f.g.Guarded()
	f.clock.Advance(time.Second)
	f.mon.set(f.clock.Now())
	f.g.poll(ctx)

	before := f.in.calls
	for name, act := range map[string]func() error{
		"move":   func() error { return in.Move(ctx, 1, 1) },
		"click":  func() error { return in.Click(ctx, 1, 1, "left", 1) },
		"button": func() error { return in.Button(ctx, "left", true) },
		"drag":   func() error { return in.Drag(ctx, [][2]int{{1, 1}, {2, 2}}) },
		"scroll": func() error { return in.Scroll(ctx, 1, 1, 0, 3) },
		"keys":   func() error { return in.Keys(ctx, []string{"cmd", "a"}) },
		"type":   func() error { return in.Type(ctx, "hi") },
	} {
		if err := act(); !errors.Is(err, ErrPaused) {
			t.Errorf("%s while paused: %v, want ErrPaused", name, err)
		}
	}
	if f.in.calls != before {
		t.Errorf("%d actions were posted while paused", f.in.calls-before)
	}
}

// TestResume: resuming clears the pause, shows controlling again with the intent, and takes
// the user's earlier input and the pointer where it is now as the new baseline.
func TestResume(t *testing.T) {
	ctx := context.Background()
	f := newGuard(t)
	f.clock.Advance(time.Second)
	f.in.user(500, 500)
	f.mon.set(f.clock.Now())
	f.g.poll(ctx)
	if !errors.Is(f.g.Check(), ErrPaused) {
		t.Fatal("not paused")
	}

	f.clock.Advance(time.Second)
	f.g.Resume(ctx)
	if err := f.g.Check(); err != nil {
		t.Fatalf("after Resume: %v", err)
	}
	if k, text := f.act.Status(); k != Controlling || text != "Ordering the blue mug" {
		t.Fatalf("activity is %q %q, want controlling with the intent", k, text)
	}
	f.clock.Advance(100 * time.Millisecond)
	f.g.poll(ctx)
	if err := f.g.Check(); err != nil {
		t.Fatalf("the input from before Resume paused it again: %v", err)
	}
	if err := f.g.Guarded().Move(ctx, 10, 10); err != nil {
		t.Fatalf("an action after Resume: %v", err)
	}

	// New user input after the resume pauses again.
	f.clock.Advance(time.Second)
	f.mon.set(f.clock.Now())
	f.g.poll(ctx)
	if !errors.Is(f.g.Check(), ErrPaused) {
		t.Fatal("user input after Resume didn't pause")
	}
}

// TestStoppedActivityIsNotPaused: a task the user stopped is not shown as paused by a late poll.
func TestStoppedActivityIsNotPaused(t *testing.T) {
	ctx := context.Background()
	f := newGuard(t)
	f.act.Stop()
	f.clock.Advance(time.Second)
	f.mon.set(f.clock.Now())
	f.g.poll(ctx)
	if k, _ := f.act.Status(); k != Idle {
		t.Fatalf("activity is %q after a stop, want idle", k)
	}
}

// TestRunPolls: Run polls on its own until its context ends.
func TestRunPolls(t *testing.T) {
	f := newGuard(t)
	f.g.Every = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.g.Run(ctx); close(done) }()
	f.clock.Advance(time.Second)
	f.mon.set(f.clock.Now())
	deadline := time.Now().Add(5 * time.Second)
	for f.g.Check() == nil && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	if !errors.Is(f.g.Check(), ErrPaused) {
		t.Fatal("Run never paused on the user's input")
	}
}

// hookMonitor runs hook once inside Since: between look's other reads.
type hookMonitor struct {
	*fakeMonitor
	hook func()
}

func (h *hookMonitor) Since(ctx context.Context) (time.Time, error) {
	if f := h.hook; f != nil {
		h.hook = nil
		f()
	}
	return h.fakeMonitor.Since(ctx)
}

// hookInput runs hook once inside Cursor, after the pointer is read.
type hookInput struct {
	*fakeInput
	hook func()
}

func (h *hookInput) Cursor(ctx context.Context) (int, int, error) {
	x, y, err := h.fakeInput.Cursor(ctx)
	if f := h.hook; f != nil {
		h.hook = nil
		f()
	}
	return x, y, err
}

// TestInjectionStartingDuringPollIgnored: one of our actions starts (and posts its first event)
// while a poll reads the monitor; that event is ours, not the user's.
func TestInjectionStartingDuringPollIgnored(t *testing.T) {
	ctx := context.Background()
	f := newGuard(t)
	f.clock.Advance(time.Second)
	f.g.Monitor = &hookMonitor{fakeMonitor: f.mon, hook: func() {
		f.in.mu.Lock()
		f.in.start, f.in.end = f.clock.Now(), time.Time{}
		f.in.mu.Unlock()
		f.clock.Advance(2 * time.Millisecond)
		f.mon.set(f.clock.Now())
	}}
	f.g.poll(ctx)
	if err := f.g.Check(); err != nil {
		t.Fatalf("our own injection paused the task: %v", err)
	}
}

// TestActionDuringCursorReadIgnored: a whole guarded action runs between a poll's cursor read
// and its check; the pointer it read is from before our move, not the user's drift.
func TestActionDuringCursorReadIgnored(t *testing.T) {
	ctx := context.Background()
	f := newGuard(t)
	hi := &hookInput{fakeInput: f.in}
	f.g.Input = hi
	f.g.Begin(ctx)
	f.clock.Advance(time.Second)
	hi.hook = func() { f.g.Guarded().Move(ctx, 300, 200) }
	f.g.poll(ctx)
	if err := f.g.Check(); err != nil {
		t.Fatalf("our own move paused the task: %v", err)
	}
}

// lagInput is a pointer that lags our moves, as on macOS: CGEventPost is asynchronous, so the
// cursor read straight after one of our moves still returns where it was before.
type lagInput struct {
	*fakeInput
	stale  bool
	sx, sy int
}

func (l *lagInput) Move(ctx context.Context, x, y int) error {
	l.sx, l.sy, l.stale = l.fakeInput.x, l.fakeInput.y, true
	return l.fakeInput.Move(ctx, x, y)
}

func (l *lagInput) Cursor(ctx context.Context) (int, int, error) {
	if l.stale {
		l.stale = false
		return l.sx, l.sy, nil
	}
	return l.fakeInput.Cursor(ctx)
}

// TestLaggingCursorIgnored: the pointer read straight after our move is still where it was;
// when the move lands where we sent it, that is not the user's drift. A user's move after it
// still pauses.
func TestLaggingCursorIgnored(t *testing.T) {
	ctx := context.Background()
	f := newGuard(t)
	li := &lagInput{fakeInput: f.in}
	f.g.Input = li
	f.g.Begin(ctx)
	for _, p := range [][2]int{{200, 150}, {321, 243}, {260, 300}} {
		if err := f.g.Guarded().Move(ctx, p[0], p[1]); err != nil {
			t.Fatalf("our own move to %v refused: %v (%s)", p, err, f.g.Reason())
		}
		f.clock.Advance(100 * time.Millisecond)
		f.g.poll(ctx)
	}
	f.clock.Advance(time.Second)
	f.g.poll(ctx)
	if err := f.g.Check(); err != nil {
		t.Fatalf("our own moves paused the task: %v (%s)", err, f.g.Reason())
	}

	f.in.user(400, 400)
	f.g.poll(ctx)
	if !errors.Is(f.g.Check(), ErrPaused) {
		t.Fatal("the user's move after ours didn't pause the task")
	}
	if r := f.g.Reason(); !strings.HasPrefix(r, "pointer") {
		t.Fatalf("reason %q, want the pointer", r)
	}
}

// TestLateHIDStampIgnored: the HID state may stamp our event up to 400 ms after we posted it;
// that is ours, not the user's.
func TestLateHIDStampIgnored(t *testing.T) {
	ctx := context.Background()
	f := newGuard(t)
	if err := f.g.Guarded().Move(ctx, 200, 150); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(400 * time.Millisecond)
	f.mon.set(f.clock.Now())
	f.g.poll(ctx)
	if err := f.g.Guarded().Move(ctx, 321, 243); err != nil {
		t.Fatalf("second own move refused: %v (%s)", err, f.g.Reason())
	}

	f.clock.Advance(time.Second)
	f.mon.set(f.clock.Now())
	f.g.poll(ctx)
	if !errors.Is(f.g.Check(), ErrPaused) {
		t.Fatal("HID activity a second after our move didn't pause the task")
	}
	if r := f.g.Reason(); !strings.HasPrefix(r, "hid") {
		t.Fatalf("reason %q, want hid", r)
	}
}

// TestBeginAfterInjectionIgnoresItsLateStamp: a task that begins (or resumes) straight after
// one of our injections does not take that injection's late HID stamp for the user's — even
// once a newer action has replaced it as the Injecting() window, so only Begin itself could
// have kept it.
func TestBeginAfterInjectionIgnoresItsLateStamp(t *testing.T) {
	ctx := context.Background()
	f := newGuard(t)
	f.in.Move(ctx, 200, 150) // not guarded: before the task began
	f.g.Begin(ctx)
	f.clock.Advance(100 * time.Millisecond)
	stamp := f.clock.Now() // the pre-Begin move's event, stamped late (within its 500 ms)
	f.clock.Advance(50 * time.Millisecond)
	// An action after Begin: Injecting() now returns only its window, so the poll cannot
	// recover the pre-Begin one; Begin must have kept it.
	if err := f.g.Guarded().Move(ctx, 321, 243); err != nil {
		t.Fatalf("our own move after Begin refused: %v (%s)", err, f.g.Reason())
	}
	f.clock.Advance(100 * time.Millisecond)
	f.mon.set(stamp)
	f.g.poll(ctx)
	if err := f.g.Check(); err != nil {
		t.Fatalf("the injection before Begin paused the task: %v (%s)", err, f.g.Reason())
	}
}
