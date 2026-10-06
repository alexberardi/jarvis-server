package scheduler

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/db"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

type fixture struct {
	s     *Scheduler
	q     *queue.Queue
	d     *db.DB
	clock *clock
	mu    sync.Mutex
	fires []Fire
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "jarvis.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := db.Migrate(ctx, d, queue.MigrationModule, queue.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, d, MigrationModule, Migrations()); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	f := &fixture{d: d, clock: &clock{t: time.Date(2026, 10, 6, 20, 58, 0, 0, time.UTC)}}
	f.q = queue.New(d, log)
	f.s = New(d, f.q, log)
	f.s.now = f.clock.now
	return f
}

// jobs returns the scheduled-fire payloads currently in the queue.
func (f *fixture) jobs(t *testing.T) []Fire {
	t.Helper()
	rows, err := f.d.Read.Query(`SELECT payload FROM platform_jobs ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []Fire
	for rows.Next() {
		var b []byte
		rows.Scan(&b)
		fr, err := DecodeFire(b)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, fr)
	}
	return out
}

func (f *fixture) run(t *testing.T) int {
	t.Helper()
	n, err := f.s.RunDue(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestIntervalTrigger(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if err := f.s.Put(ctx, Trigger{Name: "memory.extract", Kind: KindInterval, Spec: Spec{Every: time.Hour}, JobType: "memx"}); err != nil {
		t.Fatal(err)
	}
	if f.run(t) != 0 {
		t.Fatal("fired before its first period")
	}
	f.clock.add(time.Hour)
	if f.run(t) != 1 || f.run(t) != 0 {
		t.Fatal("should fire exactly once when due")
	}
	st, _ := f.s.Status(ctx, "memory.extract")
	if !st.NextFireAt.Equal(f.clock.now().Add(time.Hour)) || !st.LastFiredAt.Equal(f.clock.now()) {
		t.Fatalf("status %+v", st)
	}
}

func TestStartNowInterval(t *testing.T) {
	f := newFixture(t)
	f.s.Put(context.Background(), Trigger{Name: "cleanup", Kind: KindInterval, Spec: Spec{Every: 24 * time.Hour, StartNow: true}, JobType: "cleanup"})
	if f.run(t) != 1 {
		t.Fatal("StartNow should fire at once") // the legacy cleanup slept 24 h first and never ran on restart-heavy hosts
	}
}

// D26: missed occurrences fire once, late, then the schedule resumes from now.
func TestMissedFiresOnceLate(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.s.Put(ctx, Trigger{Name: "tick", Kind: KindInterval, Spec: Spec{Every: time.Hour}, JobType: "t"})
	due := f.clock.now().Add(time.Hour)
	f.clock.add(10 * time.Hour) // down for ten periods
	if n := f.run(t); n != 1 {
		t.Fatalf("fired %d times, want 1", n)
	}
	jobs := f.jobs(t)
	if !jobs[0].ScheduledAt.Equal(due) {
		t.Fatalf("scheduled_at %v, want the missed due time %v", jobs[0].ScheduledAt, due)
	}
	st, _ := f.s.Status(ctx, "tick")
	if !st.NextFireAt.Equal(f.clock.now().Add(time.Hour)) {
		t.Fatalf("next %v", st.NextFireAt)
	}
}

func TestCronTriggerInTimeZone(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	// The attention journal card: 21:00 household time. Clock starts 20:58 UTC = 16:58 New York.
	if err := f.s.Put(ctx, Trigger{Name: "journal:hh1", Kind: KindCron, Spec: Spec{Cron: "0 21 * * *", TZ: "America/New_York"}, JobType: "journal"}); err != nil {
		t.Fatal(err)
	}
	st, _ := f.s.Status(ctx, "journal:hh1")
	want := time.Date(2026, 10, 7, 1, 0, 0, 0, time.UTC) // 21:00 EDT
	if !st.NextFireAt.Equal(want) {
		t.Fatalf("next %v, want %v", st.NextFireAt.UTC(), want)
	}
}

func TestOnceTrigger(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	at := f.clock.now().Add(30 * time.Minute)
	f.s.Put(ctx, Trigger{Name: "errand:42:wake", Kind: KindOnce, Spec: Spec{At: at}, JobType: "errand.resume", Payload: []byte(`{"id":42}`)})
	f.clock.add(31 * time.Minute)
	if f.run(t) != 1 {
		t.Fatal("once trigger did not fire")
	}
	f.clock.add(24 * time.Hour)
	if f.run(t) != 0 {
		t.Fatal("once trigger fired twice")
	}
	if j := f.jobs(t); string(j[0].Payload) != `{"id":42}` || j[0].Trigger != "errand:42:wake" {
		t.Fatalf("payload %+v", j[0])
	}
	if st, _ := f.s.Status(ctx, "errand:42:wake"); !st.NextFireAt.IsZero() {
		t.Fatal("finished one-shot should have no next fire")
	}
}

func TestEnsureKeepsExistingSchedule(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	tr := Trigger{Name: "loop", Kind: KindInterval, Spec: Spec{Every: time.Hour}, JobType: "l"}
	f.s.Ensure(ctx, tr)
	first, _ := f.s.Status(ctx, "loop")
	f.clock.add(30 * time.Minute) // "restart" half way through
	f.s.Ensure(ctx, tr)
	again, _ := f.s.Status(ctx, "loop")
	if !again.NextFireAt.Equal(first.NextFireAt) {
		t.Fatal("Ensure reset an existing schedule")
	}
}

func TestDeleteAndBadSpecs(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.s.Put(ctx, Trigger{Name: "x", Kind: KindInterval, Spec: Spec{Every: time.Minute}, JobType: "x"})
	f.s.Delete(ctx, "x")
	f.clock.add(time.Hour)
	if f.run(t) != 0 {
		t.Fatal("deleted trigger fired")
	}
	for _, bad := range []Trigger{
		{Name: "a", Kind: KindInterval, JobType: "x"},
		{Name: "b", Kind: KindCron, Spec: Spec{Cron: "61 * * * *"}, JobType: "x"},
		{Name: "c", Kind: KindCron, Spec: Spec{Cron: "0 9 * * *", TZ: "Mars/Olympus"}, JobType: "x"},
		{Name: "d", Kind: "weekly", JobType: "x"},
	} {
		if err := f.s.Put(ctx, bad); err == nil {
			t.Errorf("%s: accepted", bad.Name)
		}
	}
}

// End to end: the scheduler enqueues, the queue runs the handler.
func TestFiresThroughQueue(t *testing.T) {
	f := newFixture(t)
	f.s.now = time.Now
	f.q.PollInterval = 10 * time.Millisecond
	got := make(chan Fire, 1)
	f.q.Register("hello", queue.Handler{Run: func(_ context.Context, j queue.Job) ([]byte, error) {
		fr, err := DecodeFire(j.Payload)
		got <- fr
		return nil, err
	}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.q.Start(ctx)
	f.s.Put(ctx, Trigger{Name: "hello", Kind: KindOnce, Spec: Spec{At: time.Now()}, JobType: "hello"})
	f.run(t)
	select {
	case fr := <-got:
		if fr.Trigger != "hello" {
			t.Fatalf("%+v", fr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("job never ran")
	}
}
