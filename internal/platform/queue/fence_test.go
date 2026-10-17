package queue

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Worker A overruns its lease; the reaper requeues; worker B claims attempt 2 and is still
// running when A's late result lands. A's outcome must not overwrite B's run.
func TestLateOutcomeDoesNotOverwriteNewerAttempt(t *testing.T) {
	q, d := newQueue(t)
	ctx := context.Background()
	h := Handler{Run: func(context.Context, Job) ([]byte, error) { return []byte("stale"), nil }, Lease: time.Minute, MaxAttempts: 3, Backoff: noBackoff, Concurrency: 2}
	q.Register("t", h)
	id, err := q.Enqueue(ctx, "t", nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	a, ok, err := q.claim(ctx, "t", q.handlers["t"])
	if !ok || err != nil {
		t.Fatal(ok, err)
	}
	// A's lease expires; reaper requeues.
	d.Write.Exec(`UPDATE platform_jobs SET lease_until = 0 WHERE id = ?`, id)
	if err := q.ReapExpired(ctx); err != nil {
		t.Fatal(err)
	}
	b, ok, err := q.claim(ctx, "t", q.handlers["t"])
	if !ok || err != nil || b.Attempt != 2 {
		t.Fatal(ok, err, b.Attempt)
	}
	// A finally returns.
	q.execute(ctx, q.handlers["t"], a)
	i, _ := q.Get(ctx, id)
	if i.State != Running || i.Attempts != 2 {
		t.Fatalf("late attempt-1 outcome overwrote attempt 2: %+v", i)
	}
	// Same for a late retryable error.
	q.execute(ctx, Handler{Run: func(context.Context, Job) ([]byte, error) { return nil, errors.New("x") }, Lease: time.Minute, Backoff: noBackoff}, a)
	if i, _ := q.Get(ctx, id); i.State != Running {
		t.Fatalf("late attempt-1 error overwrote attempt 2: %+v", i)
	}
}

// The claim carries the job's own max_attempts (Options override the handler's), so a
// retryable error is judged against it without a second query.
func TestClaimCarriesMaxAttempts(t *testing.T) {
	q, _ := newQueue(t)
	ctx := context.Background()
	q.Register("t", Handler{Run: func(context.Context, Job) ([]byte, error) { return nil, errors.New("x") }, MaxAttempts: 1, Backoff: noBackoff})
	id, err := q.Enqueue(ctx, "t", nil, Options{MaxAttempts: 3})
	if err != nil {
		t.Fatal(err)
	}
	j, ok, err := q.claim(ctx, "t", q.handlers["t"])
	if !ok || err != nil || j.maxAttempts != 3 {
		t.Fatal(ok, err, j.maxAttempts)
	}
	q.execute(ctx, q.handlers["t"], j)
	if i, _ := q.Get(ctx, id); i.State != Queued || i.Attempts != 1 {
		t.Fatalf("first failure of a 3-attempt job should retry: %+v", i)
	}
}

// The handler's deadline ends before its lease does, so a handler that honours ctx returns
// before the reaper can hand the job to another worker.
func TestHandlerDeadlineEndsBeforeLease(t *testing.T) {
	q, d := newQueue(t)
	ctx := context.Background()
	var deadline time.Time
	var leaseUntil int64
	q.Register("t", Handler{Lease: time.Minute, Run: func(ctx context.Context, j Job) ([]byte, error) {
		deadline, _ = ctx.Deadline()
		d.Read.QueryRow(`SELECT lease_until FROM platform_jobs WHERE id = ?`, j.ID).Scan(&leaseUntil)
		return nil, nil
	}})
	if _, err := q.Enqueue(ctx, "t", nil, Options{}); err != nil {
		t.Fatal(err)
	}
	j, ok, err := q.claim(ctx, "t", q.handlers["t"])
	if !ok || err != nil {
		t.Fatal(ok, err)
	}
	q.execute(ctx, q.handlers["t"], j)
	if lease := time.UnixMilli(leaseUntil); !deadline.Before(lease.Add(-time.Second)) {
		t.Fatalf("handler deadline %v not safely before lease_until %v", deadline, lease)
	}
}

func TestHandlerTimeout(t *testing.T) {
	for _, c := range []struct{ lease, want time.Duration }{
		{time.Second, 900 * time.Millisecond},
		{5 * time.Minute, 270 * time.Second},
		{time.Hour, time.Hour - 30*time.Second},
	} {
		if got := handlerTimeout(c.lease); got != c.want {
			t.Errorf("handlerTimeout(%v) = %v, want %v", c.lease, got, c.want)
		}
	}
}
