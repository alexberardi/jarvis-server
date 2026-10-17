package queue

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/db"
)

func newQueue(t *testing.T) (*Queue, *db.DB) {
	t.Helper()
	ctx := context.Background()
	// Not t.TempDir: on Windows CI the file can stay locked briefly after Close (the virus
	// scanner opening the just-written DB), and TempDir's cleanup fails the test for it. No
	// connection is left open (checked: all idle, closed by Close).
	dir, err := os.MkdirTemp("", "queue-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for range 50 {
			if os.RemoveAll(dir) == nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	})
	d, err := db.Open(ctx, filepath.Join(dir, "jarvis.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := db.Migrate(ctx, d, MigrationModule, Migrations()); err != nil {
		t.Fatal(err)
	}
	q := New(d, slog.New(slog.NewTextHandler(io.Discard, nil)))
	q.PollInterval = 10 * time.Millisecond
	return q, d
}

func start(t *testing.T, q *Queue) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		q.Wait() // before the DB closes (Windows can't remove a file still open)
	})
	q.Start(ctx)
}

func waitState(t *testing.T, q *Queue, id int64, want State) Info {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second) // hang guard
	for time.Now().Before(deadline) {
		i, err := q.Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if i.State == want {
			return i
		}
		time.Sleep(5 * time.Millisecond)
	}
	i, _ := q.Get(context.Background(), id)
	t.Fatalf("job %d: state %s, want %s (err %q)", id, i.State, want, i.LastError)
	return i
}

func noBackoff(int) time.Duration { return 0 }

func TestRunsJobAndStoresResult(t *testing.T) {
	q, _ := newQueue(t)
	q.Register("echo", Handler{Run: func(_ context.Context, j Job) ([]byte, error) {
		return append([]byte("got:"), j.Payload...), nil
	}})
	start(t, q)
	id, err := q.Enqueue(context.Background(), "echo", []byte("hi"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	i := waitState(t, q, id, Done)
	if string(i.Result) != "got:hi" || i.Attempts != 1 {
		t.Fatalf("%+v", i)
	}
}

func TestDedupHoldsOnlyWhileLive(t *testing.T) {
	q, _ := newQueue(t)
	ctx := context.Background()
	release := make(chan struct{})
	q.Register("slow", Handler{Run: func(context.Context, Job) ([]byte, error) { <-release; return nil, nil }})

	a, err := q.Enqueue(ctx, "slow", nil, Options{DedupKey: "memx:user:1"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := q.Enqueue(ctx, "slow", nil, Options{DedupKey: "memx:user:1"})
	if !errors.Is(err, ErrDuplicate) || b != a {
		t.Fatalf("second enqueue: id=%d err=%v", b, err)
	}

	start(t, q)
	waitState(t, q, a, Running)
	if _, err := q.Enqueue(ctx, "slow", nil, Options{DedupKey: "memx:user:1"}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("running job should still hold the key: %v", err)
	}
	close(release)
	waitState(t, q, a, Done)
	c, err := q.Enqueue(ctx, "slow", nil, Options{DedupKey: "memx:user:1"})
	if err != nil || c == a {
		t.Fatalf("key should be free after done: id=%d err=%v", c, err)
	}
}

func TestRetriesThenSucceeds(t *testing.T) {
	q, _ := newQueue(t)
	var calls atomic.Int32
	q.Register("flaky", Handler{Backoff: noBackoff, Run: func(_ context.Context, j Job) ([]byte, error) {
		if calls.Add(1) < 3 {
			return nil, errors.New("transient")
		}
		return []byte("ok"), nil
	}})
	start(t, q)
	id, _ := q.Enqueue(context.Background(), "flaky", nil, Options{})
	if i := waitState(t, q, id, Done); i.Attempts != 3 {
		t.Fatalf("attempts=%d", i.Attempts)
	}
}

func TestFailsAfterMaxAttempts(t *testing.T) {
	q, _ := newQueue(t)
	q.Register("bad", Handler{MaxAttempts: 2, Backoff: noBackoff, Run: func(context.Context, Job) ([]byte, error) {
		return nil, errors.New("nope")
	}})
	start(t, q)
	id, _ := q.Enqueue(context.Background(), "bad", nil, Options{})
	if i := waitState(t, q, id, Failed); i.Attempts != 2 || i.LastError != "nope" {
		t.Fatalf("%+v", i)
	}
}

func TestPermanentErrorSkipsRetries(t *testing.T) {
	q, _ := newQueue(t)
	q.Register("perm", Handler{Backoff: noBackoff, Run: func(context.Context, Job) ([]byte, error) {
		return nil, Permanent(errors.New("bad input"))
	}})
	start(t, q)
	id, _ := q.Enqueue(context.Background(), "perm", nil, Options{})
	if i := waitState(t, q, id, Failed); i.Attempts != 1 {
		t.Fatalf("attempts=%d", i.Attempts)
	}
}

func TestPanicIsAnError(t *testing.T) {
	q, _ := newQueue(t)
	q.Register("panic", Handler{MaxAttempts: 1, Run: func(context.Context, Job) ([]byte, error) { panic("boom") }})
	start(t, q)
	id, _ := q.Enqueue(context.Background(), "panic", nil, Options{})
	waitState(t, q, id, Failed)
}

func TestConcurrencyCapPerType(t *testing.T) {
	q, _ := newQueue(t)
	var cur, peak atomic.Int32
	run := func(context.Context, Job) ([]byte, error) {
		n := cur.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		cur.Add(-1)
		return nil, nil
	}
	q.Register("llm_background", Handler{Concurrency: 1, Run: run})
	ctx := context.Background()
	var ids []int64
	for range 5 {
		id, _ := q.Enqueue(ctx, "llm_background", nil, Options{})
		ids = append(ids, id)
	}
	start(t, q)
	for _, id := range ids {
		waitState(t, q, id, Done)
	}
	if peak.Load() != 1 {
		t.Fatalf("peak concurrency %d, want 1", peak.Load())
	}
}

func TestPriorityOrder(t *testing.T) {
	q, _ := newQueue(t)
	var mu sync.Mutex
	var order []string
	q.Register("p", Handler{Run: func(_ context.Context, j Job) ([]byte, error) {
		mu.Lock()
		order = append(order, string(j.Payload))
		mu.Unlock()
		return nil, nil
	}})
	ctx := context.Background()
	q.Enqueue(ctx, "p", []byte("low"), Options{Priority: 0})
	q.Enqueue(ctx, "p", []byte("high"), Options{Priority: 10})
	last, _ := q.Enqueue(ctx, "p", []byte("low2"), Options{Priority: 0})
	start(t, q)
	waitState(t, q, last, Done)
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 3 || order[0] != "high" || order[1] != "low" {
		t.Fatalf("order %v", order)
	}
}

func TestDelayedJobWaits(t *testing.T) {
	q, _ := newQueue(t)
	var ranAt atomic.Int64
	q.Register("timer", Handler{Run: func(context.Context, Job) ([]byte, error) {
		ranAt.Store(time.Now().UnixMilli())
		return nil, nil
	}})
	start(t, q)
	runAt := time.Now().Add(150 * time.Millisecond)
	id, _ := q.Enqueue(context.Background(), "timer", nil, Options{RunAt: runAt})
	waitState(t, q, id, Done)
	if ranAt.Load() < runAt.UnixMilli() {
		t.Fatalf("ran %dms early", runAt.UnixMilli()-ranAt.Load())
	}
}

func TestExpiredLeaseIsRetried(t *testing.T) {
	q, d := newQueue(t)
	ctx := context.Background()
	// Simulate a worker that died mid-job: running, with a lease in the past.
	var id int64
	now := time.Now().UnixMilli()
	if err := d.Write.QueryRow(`
		INSERT INTO platform_jobs (type, state, attempts, max_attempts, run_at, lease_until, created_at, updated_at)
		VALUES ('orphan', 'running', 1, 3, ?, ?, ?, ?) RETURNING id`, now, now-1000, now, now).Scan(&id); err != nil {
		t.Fatal(err)
	}
	q.Register("orphan", Handler{Run: func(context.Context, Job) ([]byte, error) { return []byte("recovered"), nil }})
	start(t, q)
	if i := waitState(t, q, id, Done); i.Attempts != 2 || string(i.Result) != "recovered" {
		t.Fatalf("%+v", i)
	}
	_ = ctx
}

func TestExpiredLeaseOnLastAttemptFails(t *testing.T) {
	q, d := newQueue(t)
	now := time.Now().UnixMilli()
	var id int64
	d.Write.QueryRow(`
		INSERT INTO platform_jobs (type, state, attempts, max_attempts, run_at, lease_until, created_at, updated_at)
		VALUES ('orphan', 'running', 3, 3, ?, ?, ?, ?) RETURNING id`, now, now-1000, now, now).Scan(&id)
	if err := q.ReapExpired(context.Background()); err != nil {
		t.Fatal(err)
	}
	if i, _ := q.Get(context.Background(), id); i.State != Failed || i.LastError != "lease expired" {
		t.Fatalf("%+v", i)
	}
}

func TestCancel(t *testing.T) {
	q, _ := newQueue(t)
	ctx := context.Background()
	q.Register("c", Handler{Run: func(context.Context, Job) ([]byte, error) { return nil, nil }})
	id, _ := q.Enqueue(ctx, "c", nil, Options{RunAt: time.Now().Add(time.Hour)})
	u1, _ := q.Enqueue(ctx, "c", nil, Options{DedupKey: "user:42:memx", RunAt: time.Now().Add(time.Hour)})
	u2, _ := q.Enqueue(ctx, "c", nil, Options{DedupKey: "user:42:char", RunAt: time.Now().Add(time.Hour)})
	other, _ := q.Enqueue(ctx, "c", nil, Options{DedupKey: "user:421:memx", RunAt: time.Now().Add(time.Hour)})

	if ok, err := q.Cancel(ctx, id); !ok || err != nil {
		t.Fatalf("cancel: %v %v", ok, err)
	}
	if n, err := q.CancelByDedupPrefix(ctx, "user:42:"); n != 2 || err != nil {
		t.Fatalf("prefix cancel: %d %v", n, err)
	}
	for _, c := range []struct {
		id   int64
		want State
	}{{id, Cancelled}, {u1, Cancelled}, {u2, Cancelled}, {other, Queued}} {
		if i, _ := q.Get(ctx, c.id); i.State != c.want {
			t.Errorf("job %d: %s, want %s", c.id, i.State, c.want)
		}
	}
}

func TestEnqueueTxCommitsWithCaller(t *testing.T) {
	q, d := newQueue(t)
	ctx := context.Background()
	q.Register("tx", Handler{Run: func(context.Context, Job) ([]byte, error) { return nil, nil }})
	d.Write.Exec(`CREATE TABLE inbox (id INTEGER PRIMARY KEY)`)

	var rolledBack int64
	err := d.Tx(ctx, func(tx *sql.Tx) error {
		tx.Exec(`INSERT INTO inbox VALUES (1)`)
		rolledBack, _ = q.EnqueueTx(ctx, tx, "tx", nil, Options{})
		return errors.New("abort")
	})
	if err == nil {
		t.Fatal("want error")
	}
	if _, err := q.Get(ctx, rolledBack); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("rolled-back job exists: %v", err)
	}

	var committed int64
	if err := d.Tx(ctx, func(tx *sql.Tx) error {
		tx.Exec(`INSERT INTO inbox VALUES (2)`)
		var err error
		committed, err = q.EnqueueTx(ctx, tx, "tx", nil, Options{})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	start(t, q)
	q.Notify("tx")
	waitState(t, q, committed, Done)
}

func TestPurge(t *testing.T) {
	q, _ := newQueue(t)
	ctx := context.Background()
	q.Register("x", Handler{Run: func(context.Context, Job) ([]byte, error) { return nil, nil }})
	start(t, q)
	done, _ := q.Enqueue(ctx, "x", nil, Options{})
	waitState(t, q, done, Done)
	pending, _ := q.Enqueue(ctx, "x", nil, Options{RunAt: time.Now().Add(time.Hour)})
	if n, err := q.Purge(ctx, time.Now().Add(time.Minute)); n != 1 || err != nil {
		t.Fatalf("purged %d %v", n, err)
	}
	if _, err := q.Get(ctx, pending); err != nil {
		t.Fatal("purge removed a live job")
	}
}

// A graceful shutdown mid-job puts the job back to queued and refunds the attempt it claimed,
// so the next boot runs it again instead of failing it for an expired lease (#104).
func TestShutdownMidJobRefundsAttempt(t *testing.T) {
	for _, tc := range []struct {
		name        string
		maxAttempts int
		prior       int // failed runs before the interrupted one
	}{
		{"single attempt", 1, 0},
		{"first of three", 3, 0},
		{"last of three", 3, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q, d := newQueue(t)
			clock := time.Now()
			q.now = func() time.Time { return clock }
			ctx := context.Background()
			id, err := q.Enqueue(ctx, "work", nil, Options{MaxAttempts: tc.maxAttempts})
			if err != nil {
				t.Fatal(err)
			}
			if tc.prior > 0 {
				d.Write.Exec(`UPDATE platform_jobs SET attempts = ? WHERE id = ?`, tc.prior, id)
			}
			started := make(chan struct{})
			q.Register("work", Handler{MaxAttempts: tc.maxAttempts, Run: func(ctx context.Context, _ Job) ([]byte, error) {
				close(started)
				<-ctx.Done()
				return nil, ctx.Err()
			}})
			sctx, cancel := context.WithCancel(context.Background())
			q.Start(sctx)
			<-started
			cancel()
			q.Wait()
			i, _ := q.Get(ctx, id)
			if i.State != Queued || i.Attempts != tc.prior {
				t.Fatalf("after shutdown: %+v, want queued with attempts %d", i, tc.prior)
			}
			// Even long after the lease would have run out, nothing fails it.
			clock = clock.Add(2 * time.Hour)
			if err := q.ReapExpired(ctx); err != nil {
				t.Fatal(err)
			}
			if i, _ := q.Get(ctx, id); i.State != Queued {
				t.Fatalf("after reap: %+v", i)
			}
			// The next boot runs it.
			q2 := New(d, q.log)
			q2.PollInterval = 10 * time.Millisecond
			q2.now = q.now
			q2.Register("work", Handler{MaxAttempts: tc.maxAttempts, Run: func(context.Context, Job) ([]byte, error) { return []byte("ok"), nil }})
			start(t, q2)
			if i := waitState(t, q2, id, Done); i.Attempts != tc.prior+1 {
				t.Fatalf("after reboot: %+v", i)
			}
		})
	}
}

// A handler that succeeds as shutdown begins is recorded as done, not re-run.
func TestShutdownAfterSuccessRecordsDone(t *testing.T) {
	q, _ := newQueue(t)
	ctx := context.Background()
	id, _ := q.Enqueue(ctx, "work", nil, Options{MaxAttempts: 1})
	sctx, cancel := context.WithCancel(context.Background())
	q.Register("work", Handler{Run: func(context.Context, Job) ([]byte, error) {
		cancel()
		return []byte("finished"), nil
	}})
	q.Start(sctx)
	q.Wait()
	if i, _ := q.Get(ctx, id); i.State != Done || string(i.Result) != "finished" || i.Attempts != 1 {
		t.Fatalf("%+v", i)
	}
}

// jarvisd is one process, so every running row at boot was orphaned by a crash. Start
// requeues it before any worker runs, without waiting for its lease; the crashed run still
// counts as an attempt, and a row that was already on its extra run is failed (#104).
func TestCrashThenBootRecoversOrphans(t *testing.T) {
	for _, tc := range []struct {
		name        string
		attempts    int
		maxAttempts int
		want        State
		wantErr     string
	}{
		{"attempts left", 1, 3, Queued, ""},
		{"last attempt gets one more run", 1, 1, Queued, ""},
		{"crashed on its extra run", 2, 1, Failed, "interrupted by restart"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q, d := newQueue(t)
			clock := time.Now()
			q.now = func() time.Time { return clock }
			now := clock.UnixMilli()
			var id int64
			// Lease far in the future, as a 30 min / 1 h lease would be right after a crash.
			if err := d.Write.QueryRow(`
				INSERT INTO platform_jobs (type, state, attempts, max_attempts, run_at, lease_until, created_at, updated_at)
				VALUES ('orphan', 'running', ?, ?, ?, ?, ?, ?) RETURNING id`,
				tc.attempts, tc.maxAttempts, now-60_000, now+30*60_000, now-60_000, now-60_000).Scan(&id); err != nil {
				t.Fatal(err)
			}
			// No handler registered, so Start's recovery is all that can touch the row.
			start(t, q)
			i, _ := q.Get(context.Background(), id)
			if i.State != tc.want || i.Attempts != tc.attempts || i.LastError != tc.wantErr {
				t.Fatalf("after boot: %+v, want %s attempts %d err %q", i, tc.want, tc.attempts, tc.wantErr)
			}
			var runAt int64
			var lease sql.NullInt64
			d.Read.QueryRow(`SELECT run_at, lease_until FROM platform_jobs WHERE id = ?`, id).Scan(&runAt, &lease)
			if lease.Valid {
				t.Fatalf("lease_until still set: %d", lease.Int64)
			}
			if tc.want == Queued && runAt != now {
				t.Fatalf("run_at %d, want now %d", runAt, now)
			}
		})
	}
}

// An orphan with attempts left runs to done after boot.
func TestCrashThenBootRunsOrphan(t *testing.T) {
	q, d := newQueue(t)
	now := time.Now().UnixMilli()
	var id int64
	d.Write.QueryRow(`
		INSERT INTO platform_jobs (type, state, attempts, max_attempts, run_at, lease_until, created_at, updated_at)
		VALUES ('orphan', 'running', 1, 1, ?, ?, ?, ?) RETURNING id`, now, now+3_600_000, now, now).Scan(&id)
	q.Register("orphan", Handler{MaxAttempts: 1, Run: func(context.Context, Job) ([]byte, error) { return []byte("ok"), nil }})
	start(t, q)
	if i := waitState(t, q, id, Done); i.Attempts != 2 {
		t.Fatalf("%+v", i)
	}
}

// An idle queue must not touch the single write connection: claims and reaps queue on it
// ahead of everyone else's writes (#120). Hold that connection while the workers and the
// reaper poll; any of them reaching for it shows up as a wait.
func TestIdleQueueLeavesTheWriterAlone(t *testing.T) {
	q, d := newQueue(t)
	for _, typ := range []string{"a", "b", "c"} {
		q.Register(typ, Handler{Concurrency: 2, Run: func(context.Context, Job) ([]byte, error) { return nil, nil }})
	}
	// Finished and future jobs don't count as work to claim or reap.
	if _, err := q.Enqueue(context.Background(), "a", nil, Options{RunAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	start(t, q)
	conn, err := d.Write.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	before := d.Write.Stats().WaitCount
	time.Sleep(20 * q.PollInterval)
	waits := d.Write.Stats().WaitCount - before
	conn.Close()
	if waits != 0 {
		t.Fatalf("idle queue waited for the writer %d times, want 0", waits)
	}
}
