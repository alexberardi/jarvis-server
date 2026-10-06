// Package queue is jarvisd's durable background job queue, replacing Redis + RQ (PLAN §3.2).
//
// Jobs live in SQLite, so they survive restarts. Each job type has its own concurrency cap,
// so e.g. one background LLM job at a time keeps the live voice path responsive on slow
// hardware. Features:
//   - dedup keys: at most one live (queued or running) job per key
//   - delayed jobs (RunAt), which also serve as timers
//   - priorities, retries with backoff, Permanent errors that skip retries
//   - leases: a job whose worker died (crash, restart) is picked up again after its lease
//   - transactional enqueue: EnqueueTx writes the job in the caller's transaction, so a job
//     and the state that caused it commit together (docs/cc D31)
package queue

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/db"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migrations returns the queue's goose migrations (module name "platform_queue").
func Migrations() fs.FS {
	sub, _ := fs.Sub(migrationFiles, "migrations")
	return sub
}

// MigrationModule is the version-table name the queue migrates under.
const MigrationModule = "platform_queue"

// State is a job's lifecycle state.
type State string

const (
	Queued    State = "queued"
	Running   State = "running"
	Done      State = "done"
	Failed    State = "failed"
	Cancelled State = "cancelled"
)

// Job is a claimed job handed to a handler.
type Job struct {
	ID       int64
	Type     string
	Payload  []byte
	Attempt  int // 1 on the first run
	DedupKey string
}

// HandlerFunc runs a job. Returning nil marks it done with result; an error retries it
// (unless wrapped with Permanent or attempts are exhausted).
type HandlerFunc func(ctx context.Context, job Job) (result []byte, err error)

// Handler configures one job type.
type Handler struct {
	Run         HandlerFunc
	Concurrency int           // workers for this type; default 1
	MaxAttempts int           // default 3
	Lease       time.Duration // how long a run may go before it's presumed dead; default 5m
	// Backoff gives the delay before retry n (1-based). Default: 2s, 4s, 8s … capped at 5m.
	Backoff func(attempt int) time.Duration
}

type permanent struct{ error }

func (p permanent) Unwrap() error { return p.error }

// Permanent marks an error as not worth retrying.
func Permanent(err error) error { return permanent{err} }

// Options for Enqueue.
type Options struct {
	DedupKey    string
	Priority    int
	RunAt       time.Time // zero = now
	MaxAttempts int       // 0 = the handler's default
}

// ErrDuplicate is returned when a live job already holds the dedup key; the existing job's
// id is returned alongside it.
var ErrDuplicate = errors.New("queue: duplicate job")

// Queue is the job queue. Register handlers before Start.
type Queue struct {
	db  *db.DB
	log *slog.Logger
	now func() time.Time
	// PollInterval bounds how long a newly due job (delayed, retried, or enqueued by another
	// process) waits before a worker notices it. Enqueue in this process wakes workers at once.
	PollInterval time.Duration

	mu       sync.Mutex
	handlers map[string]Handler
	wake     map[string]chan struct{}
	started  bool
}

// New creates a queue over d. Run Migrations before use.
func New(d *db.DB, log *slog.Logger) *Queue {
	return &Queue{
		db: d, log: log, now: time.Now,
		PollInterval: time.Second,
		handlers:     map[string]Handler{},
		wake:         map[string]chan struct{}{},
	}
}

// Register sets the handler for a job type. It panics if called after Start or twice for a type.
func (q *Queue) Register(jobType string, h Handler) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.started {
		panic("queue: Register after Start")
	}
	if _, dup := q.handlers[jobType]; dup {
		panic("queue: duplicate handler for " + jobType)
	}
	if h.Concurrency <= 0 {
		h.Concurrency = 1
	}
	if h.MaxAttempts <= 0 {
		h.MaxAttempts = 3
	}
	if h.Lease <= 0 {
		h.Lease = 5 * time.Minute
	}
	if h.Backoff == nil {
		h.Backoff = defaultBackoff
	}
	q.handlers[jobType] = h
	q.wake[jobType] = make(chan struct{}, 1)
}

func defaultBackoff(attempt int) time.Duration {
	d := 2 * time.Second << min(attempt-1, 10)
	return min(d, 5*time.Minute)
}

func ms(t time.Time) int64 { return t.UnixMilli() }

// execer is satisfied by *sql.DB and *sql.Tx.
type execer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Enqueue adds a job. With a DedupKey already held by a live job it returns that job's id
// and ErrDuplicate.
func (q *Queue) Enqueue(ctx context.Context, jobType string, payload []byte, o Options) (int64, error) {
	id, err := q.insert(ctx, q.db.Write, jobType, payload, o)
	if err == nil {
		q.notify(jobType)
	}
	return id, err
}

// EnqueueTx adds a job inside the caller's write transaction. Workers are woken by the next
// poll (at most PollInterval later), since the job isn't visible until the caller commits;
// call Notify after commit to start it immediately.
func (q *Queue) EnqueueTx(ctx context.Context, tx *sql.Tx, jobType string, payload []byte, o Options) (int64, error) {
	return q.insert(ctx, tx, jobType, payload, o)
}

// Notify wakes jobType's workers, e.g. after committing an EnqueueTx.
func (q *Queue) Notify(jobType string) { q.notify(jobType) }

func (q *Queue) insert(ctx context.Context, ex execer, jobType string, payload []byte, o Options) (int64, error) {
	now := q.now()
	runAt := o.RunAt
	if runAt.IsZero() {
		runAt = now
	}
	maxAttempts := o.MaxAttempts
	if maxAttempts <= 0 {
		q.mu.Lock()
		maxAttempts = q.handlers[jobType].MaxAttempts
		q.mu.Unlock()
		if maxAttempts <= 0 {
			maxAttempts = 3
		}
	}
	var dedup any
	if o.DedupKey != "" {
		dedup = o.DedupKey
	}
	if payload == nil {
		payload = []byte{}
	}
	var id int64
	err := ex.QueryRowContext(ctx, `
		INSERT INTO platform_jobs (type, payload, dedup_key, priority, max_attempts, run_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id`,
		jobType, payload, dedup, o.Priority, maxAttempts, ms(runAt), ms(now), ms(now)).Scan(&id)
	if err != nil && o.DedupKey != "" && isUniqueViolation(err) {
		var existing int64
		if e := ex.QueryRowContext(ctx, `
			SELECT id FROM platform_jobs
			WHERE dedup_key = ? AND state IN ('queued', 'running')`, o.DedupKey).Scan(&existing); e != nil {
			return 0, fmt.Errorf("queue: dedup lookup: %w", e)
		}
		return existing, ErrDuplicate
	}
	if err != nil {
		return 0, fmt.Errorf("queue: enqueue %s: %w", jobType, err)
	}
	return id, nil
}

func isUniqueViolation(err error) bool {
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}

func (q *Queue) notify(jobType string) {
	q.mu.Lock()
	ch := q.wake[jobType]
	q.mu.Unlock()
	if ch != nil {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Cancel cancels a queued job. A running job is left to finish (its handler should watch its
// own cancellation signal); Cancel reports whether the job was cancelled.
func (q *Queue) Cancel(ctx context.Context, id int64) (bool, error) {
	res, err := q.db.Write.ExecContext(ctx, `
		UPDATE platform_jobs SET state = 'cancelled', updated_at = ?
		WHERE id = ? AND state = 'queued'`, ms(q.now()), id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// CancelByDedupPrefix cancels every queued job whose dedup key starts with prefix, e.g. all
// jobs naming a deleted user (docs/cc D20).
func (q *Queue) CancelByDedupPrefix(ctx context.Context, prefix string) (int64, error) {
	res, err := q.db.Write.ExecContext(ctx, `
		UPDATE platform_jobs SET state = 'cancelled', updated_at = ?
		WHERE state = 'queued' AND substr(dedup_key, 1, length(?)) = ?`, ms(q.now()), prefix, prefix)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Info is a job's stored state.
type Info struct {
	ID        int64
	Type      string
	State     State
	Attempts  int
	LastError string
	Result    []byte
}

// Get returns a job's state.
func (q *Queue) Get(ctx context.Context, id int64) (Info, error) {
	var i Info
	var lastErr sql.NullString
	err := q.db.Read.QueryRowContext(ctx, `
		SELECT id, type, state, attempts, last_error, result FROM platform_jobs WHERE id = ?`, id).
		Scan(&i.ID, &i.Type, &i.State, &i.Attempts, &lastErr, &i.Result)
	i.LastError = lastErr.String
	return i, err
}

// Start launches the workers and the lease reaper; they stop when ctx is cancelled. Start
// returns at once.
func (q *Queue) Start(ctx context.Context) {
	q.mu.Lock()
	q.started = true
	handlers := make(map[string]Handler, len(q.handlers))
	for k, v := range q.handlers {
		handlers[k] = v
	}
	q.mu.Unlock()

	for jobType, h := range handlers {
		for range h.Concurrency {
			go q.worker(ctx, jobType, h)
		}
	}
	go q.reaper(ctx)
}

func (q *Queue) worker(ctx context.Context, jobType string, h Handler) {
	q.mu.Lock()
	wake := q.wake[jobType]
	q.mu.Unlock()
	t := time.NewTicker(q.PollInterval)
	defer t.Stop()
	for {
		for {
			job, ok, err := q.claim(ctx, jobType, h)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				q.log.Error("queue: claim", "type", jobType, "err", err)
				break
			}
			if !ok {
				break
			}
			q.execute(ctx, h, job)
		}
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-t.C:
		}
	}
}

func (q *Queue) claim(ctx context.Context, jobType string, h Handler) (Job, bool, error) {
	now := q.now()
	var j Job
	var dedup sql.NullString
	err := q.db.Write.QueryRowContext(ctx, `
		UPDATE platform_jobs
		SET state = 'running', attempts = attempts + 1, lease_until = ?, updated_at = ?
		WHERE id = (
			SELECT id FROM platform_jobs
			WHERE type = ? AND state = 'queued' AND run_at <= ?
			ORDER BY priority DESC, run_at, id
			LIMIT 1)
		RETURNING id, type, payload, attempts, dedup_key`,
		ms(now.Add(h.Lease)), ms(now), jobType, ms(now)).
		Scan(&j.ID, &j.Type, &j.Payload, &j.Attempt, &dedup)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, false, nil
	}
	j.DedupKey = dedup.String
	return j, err == nil, err
}

func (q *Queue) execute(ctx context.Context, h Handler, j Job) {
	jctx, cancel := context.WithTimeout(ctx, h.Lease)
	result, err := safeRun(jctx, h.Run, j)
	cancel()
	if ctx.Err() != nil {
		// Shutting down: leave the job running; its lease expires and it is retried.
		return
	}
	now := q.now()
	var dbErr error
	switch {
	case err == nil:
		_, dbErr = q.db.Write.ExecContext(context.Background(), `
			UPDATE platform_jobs SET state = 'done', result = ?, lease_until = NULL, updated_at = ?
			WHERE id = ? AND state = 'running'`, result, ms(now), j.ID)
	default:
		var p permanent
		var maxAttempts int
		q.db.Write.QueryRowContext(context.Background(),
			`SELECT max_attempts FROM platform_jobs WHERE id = ?`, j.ID).Scan(&maxAttempts)
		if errors.As(err, &p) || j.Attempt >= maxAttempts {
			q.log.Warn("queue: job failed", "type", j.Type, "id", j.ID, "attempt", j.Attempt, "err", err)
			_, dbErr = q.db.Write.ExecContext(context.Background(), `
				UPDATE platform_jobs SET state = 'failed', last_error = ?, lease_until = NULL, updated_at = ?
				WHERE id = ? AND state = 'running'`, err.Error(), ms(now), j.ID)
		} else {
			q.log.Info("queue: job will retry", "type", j.Type, "id", j.ID, "attempt", j.Attempt, "err", err)
			_, dbErr = q.db.Write.ExecContext(context.Background(), `
				UPDATE platform_jobs SET state = 'queued', last_error = ?, lease_until = NULL, run_at = ?, updated_at = ?
				WHERE id = ? AND state = 'running'`, err.Error(), ms(now.Add(h.Backoff(j.Attempt))), ms(now), j.ID)
		}
	}
	if dbErr != nil {
		q.log.Error("queue: record outcome", "id", j.ID, "err", dbErr)
	}
}

func safeRun(ctx context.Context, run HandlerFunc, j Job) (result []byte, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("queue: handler panic: %v", p)
		}
	}()
	return run(ctx, j)
}

// reaper re-queues running jobs whose lease expired: their worker died (crash, restart) or
// overran. A job that has used all its attempts is failed instead.
func (q *Queue) reaper(ctx context.Context) {
	t := time.NewTicker(q.PollInterval)
	defer t.Stop()
	for {
		if err := q.ReapExpired(ctx); err != nil && ctx.Err() == nil {
			q.log.Error("queue: reap", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// ReapExpired handles expired leases once. Exported for tests and for startup.
func (q *Queue) ReapExpired(ctx context.Context) error {
	now := ms(q.now())
	return q.db.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			UPDATE platform_jobs SET state = 'failed', last_error = 'lease expired', lease_until = NULL, updated_at = ?
			WHERE state = 'running' AND lease_until < ? AND attempts >= max_attempts`, now, now); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			UPDATE platform_jobs SET state = 'queued', lease_until = NULL, run_at = ?, updated_at = ?
			WHERE state = 'running' AND lease_until < ?`, now, now, now)
		return err
	})
}

// Purge deletes finished jobs (done, failed, cancelled) last updated before cutoff.
func (q *Queue) Purge(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := q.db.Write.ExecContext(ctx, `
		DELETE FROM platform_jobs
		WHERE state IN ('done', 'failed', 'cancelled') AND updated_at < ?`, ms(cutoff))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
