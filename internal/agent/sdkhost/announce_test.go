package sdkhost

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func alertAt(now time.Time, id string, prio int, ttl time.Duration) Alert {
	return Alert{ID: id, SourceAgent: "test", Title: "T " + id, Summary: "say " + id, Priority: prio,
		CreatedAt: now, ExpiresAt: now.Add(ttl)}
}

// The LED's level signal (N:on_change): every write reports the announceable count, priority 3
// and up only; Flush always reports 0; Sweep re-reports and drops the expired ones.
func TestAlertsReportTheAnnounceableCount(t *testing.T) {
	now := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	q := &Alerts{Now: func() time.Time { return now }}
	var got []int
	q.Watch(func(n int) { got = append(got, n) })

	q.Add(alertAt(now, "news", 1, time.Hour))
	q.Add(alertAt(now, "rem", 3, 10*time.Minute))
	q.Add(alertAt(now, "rem", 3, 10*time.Minute)) // a duplicate title: still a write
	q.Add(alertAt(now, "old", 3, -time.Minute))   // expired: refused, nothing reported
	_ = q.Pending()                               // reads don't report
	_ = q.Count()
	q.Remove([]string{"news"})
	now = now.Add(11 * time.Minute)
	q.Sweep()
	q.Add(alertAt(now, "urgent", 3, time.Hour))
	q.Flush()
	q.Flush()
	if want := []int{0, 1, 1, 1, 0, 1, 0, 0}; !reflect.DeepEqual(got, want) {
		t.Fatalf("reported %v, want %v", got, want)
	}
}

// fakeSay records what was said; fail maps a text to its error.
type fakeSay struct {
	mu   sync.Mutex
	said []string
	fail map[string]error
	hook func(text string)
}

func (f *fakeSay) Say(_ context.Context, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hook != nil {
		f.hook(text)
	}
	if err := f.fail[text]; err != nil {
		return err
	}
	f.said = append(f.said, text)
	return nil
}

func (f *fakeSay) Said() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.said...)
}

func ids(list []Alert) []string {
	out := []string{}
	for _, a := range list {
		out = append(out, a.ID)
	}
	return out
}

// The drain speaks only the announceable alerts, most urgent and then oldest first, and removes
// exactly those: news stays for whats_up, as does an alert added while speaking.
func TestDrainSpeaksTheAnnounceableAlertsOnly(t *testing.T) {
	now := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	q := &Alerts{Now: func() time.Time { return now }}
	q.Add(alertAt(now, "news", 1, time.Hour))
	q.Add(alertAt(now, "a", 3, time.Hour))
	q.Add(alertAt(now.Add(time.Second), "b", 3, time.Hour))
	s := &fakeSay{}
	s.hook = func(text string) {
		if text == "say a" {
			q.Add(alertAt(now, "late", 3, time.Hour))
		}
	}
	a := &Announcer{Alerts: q, Say: s.Say}
	if n := a.Drain(context.Background()); n != 2 {
		t.Errorf("Drain said %d, want 2", n)
	}
	if want := []string{"say a", "say b"}; !reflect.DeepEqual(s.Said(), want) {
		t.Errorf("said %v, want %v", s.Said(), want)
	}
	if got := ids(q.Pending()); !reflect.DeepEqual(got, []string{"late", "news"}) {
		t.Errorf("left %v, want [late news]", got)
	}
}

// Nothing played (no microphone listening): the alert and the ones after it stay queued; one
// whose speech failed otherwise is dropped and the next one is said (Python's isolation).
func TestDrainKeepsWhatWasNotHeard(t *testing.T) {
	now := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	q := &Alerts{Now: func() time.Time { return now }}
	for i, id := range []string{"a", "b", "c", "d"} {
		q.Add(alertAt(now.Add(time.Duration(i)*time.Second), id, 3, time.Hour))
	}
	s := &fakeSay{fail: map[string]error{"say a": errors.New("tts broke"), "say c": fmt.Errorf("%w: no mic", ErrUnheard)}}
	a := &Announcer{Alerts: q, Say: s.Say}
	if n := a.Drain(context.Background()); n != 1 {
		t.Errorf("Drain said %d, want 1", n)
	}
	if got := ids(q.Pending()); !reflect.DeepEqual(got, []string{"c", "d"}) {
		t.Errorf("left %v, want [c d]", got)
	}
}

// The button reads every pending alert, low priority too, then they are gone; over an empty
// queue it says "No new notifications." and re-syncs the LED to 0.
func TestReadAllIsTheButton(t *testing.T) {
	now := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	q := &Alerts{Now: func() time.Time { return now }}
	var led []int
	q.Watch(func(n int) { led = append(led, n) })
	q.Add(alertAt(now, "news", 1, time.Hour))
	q.Add(alertAt(now, "rem", 3, time.Hour))
	s := &fakeSay{}
	a := &Announcer{Alerts: q, Say: s.Say}
	if n, err := a.ReadAll(context.Background()); n != 2 || err != nil {
		t.Errorf("ReadAll = %d, %v", n, err)
	}
	if n, err := a.ReadAll(context.Background()); n != 0 || err != nil {
		t.Errorf("empty ReadAll = %d, %v", n, err)
	}
	if want := []string{"say rem", "say news", NoAlerts}; !reflect.DeepEqual(s.Said(), want) {
		t.Errorf("said %v, want %v", s.Said(), want)
	}
	if q.Count() != 0 || led[len(led)-1] != 0 {
		t.Errorf("count %d, LED reports %v", q.Count(), led)
	}

	// Nothing heard: kept, and the caller learns it (the LED flashes the error).
	q.Add(alertAt(now, "x", 2, time.Hour))
	s.fail = map[string]error{"say x": fmt.Errorf("%w: no mic", ErrUnheard)}
	if _, err := a.ReadAll(context.Background()); !errors.Is(err, ErrUnheard) || q.Count() != 1 {
		t.Errorf("ReadAll unheard = %v, %d queued", err, q.Count())
	}
}

// Run drains as soon as an announceable alert is added, not for news; a tick's sweep retries one
// not heard before, and the expired ones go without being said.
func TestRunAnnouncesOnAddAndRetriesOnTick(t *testing.T) {
	var mu sync.Mutex
	now := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	q := &Alerts{Now: clock}
	said := make(chan string, 10)
	tried := make(chan string, 10) // not heard
	var deaf atomic.Bool           // nobody is listening
	a := &Announcer{Alerts: q, Say: func(_ context.Context, text string) error {
		if deaf.Load() {
			tried <- text
			return fmt.Errorf("%w: no mic", ErrUnheard)
		}
		said <- text
		return nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tick := make(chan time.Time)
	done := make(chan struct{})
	go func() { a.Run(ctx, tick); close(done) }()
	waitWatched(t, q)

	q.Add(alertAt(now, "news", 1, time.Hour))
	q.Add(alertAt(now, "rem", 3, time.Hour))
	if got := next(t, said); got != "say rem" {
		t.Fatalf("said %q, want the reminder", got)
	}

	deaf.Store(true)
	q.Add(alertAt(now, "later", 3, time.Hour))
	if got := next(t, tried); got != "say later" {
		t.Fatalf("tried %q", got)
	}
	q.Add(alertAt(now, "stale", 3, 5*time.Second))
	if got := next(t, tried); got != "say later" {
		t.Fatalf("tried %q", got)
	}
	deaf.Store(false)
	mu.Lock()
	now = now.Add(6 * time.Second)
	mu.Unlock()
	tick <- now
	if got := next(t, said); got != "say later" {
		t.Fatalf("after the tick said %q, want the one kept", got)
	}
	select {
	case got := <-said:
		t.Errorf("also said %q", got)
	case <-time.After(50 * time.Millisecond):
	}
	if got := ids(q.Pending()); !reflect.DeepEqual(got, []string{"news"}) {
		t.Errorf("left %v, want [news]", got)
	}
	cancel()
	<-done
}

func waitWatched(t *testing.T, q *Alerts) {
	t.Helper()
	waitFor(t, func() bool { q.mu.Lock(); defer q.mu.Unlock(); return len(q.watchers) > 0 })
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); !ok(); {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(time.Millisecond)
	}
}

func next(t *testing.T, c <-chan string) string {
	t.Helper()
	select {
	case s := <-c:
		return s
	case <-time.After(2 * time.Second):
		t.Fatal("nothing said")
		return ""
	}
}
