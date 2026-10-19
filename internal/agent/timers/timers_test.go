package timers

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// fakeClock schedules nothing on its own: Advance runs what has come due, in order.
type fakeClock struct {
	mu    sync.Mutex
	now   time.Time
	queue []*scheduled
}

type scheduled struct {
	at      time.Time
	f       func()
	stopped bool
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) func() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := &scheduled{at: c.now.Add(d), f: f}
	c.queue = append(c.queue, s)
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		was := !s.stopped
		s.stopped = true
		return was
	}
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var due []*scheduled
	rest := c.queue[:0]
	for _, s := range c.queue {
		if !s.stopped && !s.at.After(c.now) {
			due = append(due, s)
		} else if !s.stopped {
			rest = append(rest, s)
		}
	}
	c.queue = rest
	c.mu.Unlock()
	for _, s := range due {
		s.f()
	}
}

type fired struct {
	ch chan Timer
}

func (f *fired) next(t *testing.T) Timer {
	t.Helper()
	select {
	case tm := <-f.ch:
		return tm
	case <-time.After(2 * time.Second):
		t.Fatal("no timer fired")
		return Timer{}
	}
}

func (f *fired) none(t *testing.T) {
	t.Helper()
	select {
	case tm := <-f.ch:
		t.Fatalf("%s fired", tm.ID)
	case <-time.After(50 * time.Millisecond):
	}
}

func newService(t *testing.T, dir string, c *fakeClock) (*Service, *fired) {
	f := &fired{ch: make(chan Timer, 8)}
	n := 0
	s := &Service{Path: filepath.Join(dir, "timers.json"), Fire: func(tm Timer) { f.ch <- tm },
		Now: c.Now, AfterFunc: c.AfterFunc, NewID: func() string { n++; return fmt.Sprintf("t%07d", n) }}
	return s, f
}

func str(s string) *string { return &s }

func TestSetListFindCancel(t *testing.T) {
	c := &fakeClock{now: time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)}
	s, f := newService(t, t.TempDir(), c)
	s.Start()
	pasta, _ := s.Set(600, str("Pasta"))
	eggs, _ := s.Set(300, str("eggs"))
	plain, _ := s.Set(60, nil)
	if _, err := s.Set(0, nil); err == nil {
		t.Fatal("a 0 s timer was set")
	}
	c.Advance(30 * time.Second)
	got := s.Get(pasta)
	b, _ := json.Marshal(got)
	if string(b) != `{"duration_seconds":600,"ends_at":"2026-10-18T12:10:00+00:00","label":"Pasta","remaining_seconds":570,"started_at":"2026-10-18T12:00:00+00:00","timer_id":"t0000001"}` {
		t.Fatalf("get = %s", b)
	}
	if a := s.Active(); len(a) != 3 || a[0]["timer_id"] != pasta || a[2]["label"] != (*string)(nil) {
		t.Fatalf("active = %v", a)
	}
	if s.FindByLabel("PAS") != pasta || s.FindByLabel("egg") != eggs || s.FindByLabel("rice") != "" {
		t.Fatal("find_by_label")
	}
	if !s.Cancel(eggs) || s.Cancel(eggs) || s.Get(eggs) != nil {
		t.Fatal("cancel")
	}
	c.Advance(30 * time.Second) // the plain one ends
	if tm := f.next(t); tm.ID != plain || Message(tm.Label) != "Your timer is done!" {
		t.Fatalf("fired %+v", tm)
	}
	c.Advance(10 * time.Minute) // eggs was cancelled: only pasta fires
	if tm := f.next(t); tm.ID != pasta || Message(tm.Label) != "Your Pasta timer is done!" {
		t.Fatalf("fired %+v", tm)
	}
	f.none(t)
	if len(s.Active()) != 0 {
		t.Fatalf("active after firing = %v", s.Active())
	}
}

func TestClearAll(t *testing.T) {
	c := &fakeClock{now: time.Unix(0, 0)}
	s, f := newService(t, t.TempDir(), c)
	s.Start()
	_, _ = s.Set(10, str("a"))
	_, _ = s.Set(20, str("b"))
	if n := s.ClearAll(); n != 2 || len(s.Active()) != 0 {
		t.Fatalf("clear_all = %d, %v", n, s.Active())
	}
	c.Advance(time.Minute)
	f.none(t)
}

// Timers survive a restart: those still running are re-armed for what is left, those that
// ended while the agent was down fire at once, and a fired or cancelled one stays gone.
func TestRestartRearmsAndFiresMissed(t *testing.T) {
	dir := t.TempDir()
	c := &fakeClock{now: time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)}
	s, _ := newService(t, dir, c)
	s.Start()
	short, _ := s.Set(60, str("tea"))
	long, _ := s.Set(3600, str("roast"))
	gone, _ := s.Set(120, str("gone"))
	s.Cancel(gone)
	s.Close() // the agent stops

	c.Advance(5 * time.Minute) // down for five minutes
	s2, f := newService(t, dir, c)
	if n := s2.Start(); n != 1 {
		t.Fatalf("re-armed %d", n)
	}
	if tm := f.next(t); tm.ID != short || *tm.Label != "tea" {
		t.Fatalf("fired at start %+v", tm)
	}
	f.none(t)
	if a := s2.Active(); len(a) != 1 || a[0]["timer_id"] != long || a[0]["remaining_seconds"] != 3300 {
		t.Fatalf("active = %v", a)
	}
	c.Advance(55 * time.Minute)
	if tm := f.next(t); tm.ID != long {
		t.Fatalf("fired %+v", tm)
	}
	// Nothing is left for a third start.
	s3, f3 := newService(t, dir, c)
	if n := s3.Start(); n != 0 {
		t.Fatalf("third start re-armed %d", n)
	}
	f3.none(t)
}

func TestUnreadableFileStartsEmpty(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "timers.json"), []byte("{nope"), 0o600)
	s, _ := newService(t, dir, &fakeClock{now: time.Unix(0, 0)})
	if n := s.Start(); n != 0 || len(s.Active()) != 0 {
		t.Fatalf("start = %d", n)
	}
	if _, err := s.Set(5, nil); err != nil || len(s.Active()) != 1 {
		t.Fatal("set after a bad file")
	}
}

// The real clock: a one-second timer fires on its own.
func TestRealClockFires(t *testing.T) {
	ch := make(chan Timer, 1)
	s := &Service{Path: filepath.Join(t.TempDir(), "timers.json"), Fire: func(tm Timer) { ch <- tm }}
	s.Start()
	id, _ := s.Set(1, nil)
	select {
	case tm := <-ch:
		if tm.ID != id || len(id) != 8 {
			t.Fatalf("fired %+v", tm)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the timer never fired")
	}
}
