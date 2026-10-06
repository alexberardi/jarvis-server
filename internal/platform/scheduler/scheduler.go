// Package scheduler is jarvisd's single scheduling engine (docs/cc D27). It replaces the
// separate routine scheduler, errand schedule sweep, background asyncio loops and in-memory
// "last fired" state with one table of triggers on top of the durable job queue.
//
// A trigger has a kind (interval, cron or once), a spec, and a job type. When it comes due the
// scheduler enqueues a job and advances next_fire_at in the same transaction, so a fire is
// recorded exactly once even across restarts. A trigger that was missed while jarvisd was
// down fires once, late, and then resumes its normal schedule (D26); it never replays every
// missed occurrence.
package scheduler

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"strconv"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/db"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migrations returns the scheduler's goose migrations (module "platform_scheduler").
func Migrations() fs.FS {
	sub, _ := fs.Sub(migrationFiles, "migrations")
	return sub
}

const MigrationModule = "platform_scheduler"

// Kinds of trigger.
const (
	KindInterval = "interval"
	KindCron     = "cron"
	KindOnce     = "once"
)

// Spec describes when a trigger fires. Set the field matching its kind.
type Spec struct {
	Every    time.Duration `json:"every,omitempty"`     // interval
	Cron     string        `json:"cron,omitempty"`      // cron expression
	TZ       string        `json:"tz,omitempty"`        // cron time zone (IANA); default UTC
	At       time.Time     `json:"at,omitzero"`         // once
	StartNow bool          `json:"start_now,omitempty"` // interval: first fire now, not after one period
}

// Trigger is a scheduled job.
type Trigger struct {
	Name    string // unique; also the job's dedup key prefix
	Kind    string
	Spec    Spec
	JobType string
	Payload []byte
}

// Fire is the job payload envelope the scheduler enqueues; handlers decode it with DecodeFire.
type Fire struct {
	Trigger     string    `json:"trigger"`
	ScheduledAt time.Time `json:"scheduled_at"` // when it was due; earlier than now if it fired late
	Payload     []byte    `json:"payload,omitempty"`
}

// DecodeFire decodes a queue job payload produced by the scheduler.
func DecodeFire(b []byte) (Fire, error) {
	var f Fire
	err := json.Unmarshal(b, &f)
	return f, err
}

type Scheduler struct {
	db  *db.DB
	q   *queue.Queue
	log *slog.Logger
	now func() time.Time
	// Tick is how often due triggers are checked. Cron and routine granularity is one minute,
	// so the default of 5 s fires within a few seconds of the due time.
	Tick time.Duration
}

func New(d *db.DB, q *queue.Queue, log *slog.Logger) *Scheduler {
	return &Scheduler{db: d, q: q, log: log, now: time.Now, Tick: 5 * time.Second}
}

func ms(t time.Time) int64 { return t.UnixMilli() }

// next computes the first fire time strictly after `after` (or at `after` for a fresh
// StartNow interval). ok=false means the trigger will never fire again.
func next(t Trigger, after time.Time, first bool) (time.Time, bool, error) {
	switch t.Kind {
	case KindInterval:
		if t.Spec.Every <= 0 {
			return time.Time{}, false, errors.New("scheduler: interval must be positive")
		}
		if first && t.Spec.StartNow {
			return after, true, nil
		}
		return after.Add(t.Spec.Every), true, nil
	case KindCron:
		c, err := ParseCron(t.Spec.Cron)
		if err != nil {
			return time.Time{}, false, err
		}
		loc := time.UTC
		if t.Spec.TZ != "" {
			if loc, err = time.LoadLocation(t.Spec.TZ); err != nil {
				return time.Time{}, false, fmt.Errorf("scheduler: time zone %q: %w", t.Spec.TZ, err)
			}
		}
		n, ok := c.Next(after.In(loc))
		return n, ok, nil
	case KindOnce:
		if !first {
			return time.Time{}, false, nil
		}
		return t.Spec.At, !t.Spec.At.IsZero(), nil
	default:
		return time.Time{}, false, fmt.Errorf("scheduler: unknown kind %q", t.Kind)
	}
}

// Put creates or replaces a trigger and schedules its first fire. Replacing a trigger
// recomputes next_fire_at from now but keeps last_fired_at.
func (s *Scheduler) Put(ctx context.Context, t Trigger) error {
	now := s.now()
	n, ok, err := next(t, now, true)
	if err != nil {
		return err
	}
	spec, _ := json.Marshal(t.Spec)
	var nextAt any
	if ok {
		nextAt = ms(n)
	}
	if t.Payload == nil {
		t.Payload = []byte{}
	}
	_, err = s.db.Write.ExecContext(ctx, `
		INSERT INTO platform_triggers (name, kind, spec, job_type, payload, next_fire_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (name) DO UPDATE SET kind = excluded.kind, spec = excluded.spec, job_type = excluded.job_type,
			payload = excluded.payload, next_fire_at = excluded.next_fire_at, updated_at = excluded.updated_at`,
		t.Name, t.Kind, string(spec), t.JobType, t.Payload, nextAt, ms(now), ms(now))
	return err
}

// Ensure creates the trigger only if it doesn't exist, so a restart keeps a loop's schedule
// (and last fire) instead of resetting it. Use it for built-in loops registered at startup.
func (s *Scheduler) Ensure(ctx context.Context, t Trigger) error {
	var exists int
	err := s.db.Read.QueryRowContext(ctx, `SELECT 1 FROM platform_triggers WHERE name = ?`, t.Name).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return s.Put(ctx, t)
	}
	return err
}

// Delete removes a trigger. Deleting a missing trigger is not an error.
func (s *Scheduler) Delete(ctx context.Context, name string) error {
	_, err := s.db.Write.ExecContext(ctx, `DELETE FROM platform_triggers WHERE name = ?`, name)
	return err
}

// Status is a trigger's schedule state.
type Status struct {
	NextFireAt  time.Time // zero if finished
	LastFiredAt time.Time // zero if never fired
}

func (s *Scheduler) Status(ctx context.Context, name string) (Status, error) {
	var nextAt, last sql.NullInt64
	err := s.db.Read.QueryRowContext(ctx,
		`SELECT next_fire_at, last_fired_at FROM platform_triggers WHERE name = ?`, name).Scan(&nextAt, &last)
	if err != nil {
		return Status{}, err
	}
	var st Status
	if nextAt.Valid {
		st.NextFireAt = time.UnixMilli(nextAt.Int64)
	}
	if last.Valid {
		st.LastFiredAt = time.UnixMilli(last.Int64)
	}
	return st, nil
}

// Start runs the tick loop until ctx is cancelled. It returns at once.
func (s *Scheduler) Start(ctx context.Context) {
	go func() {
		t := time.NewTicker(s.Tick)
		defer t.Stop()
		for {
			if _, err := s.RunDue(ctx); err != nil && ctx.Err() == nil {
				s.log.Error("scheduler: run due triggers", "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

type due struct {
	t       Trigger
	nextAt  int64
	specRaw string
}

// RunDue fires every due trigger once and returns how many fired.
func (s *Scheduler) RunDue(ctx context.Context) (int, error) {
	now := s.now()
	rows, err := s.db.Read.QueryContext(ctx, `
		SELECT name, kind, spec, job_type, payload, next_fire_at FROM platform_triggers
		WHERE next_fire_at IS NOT NULL AND next_fire_at <= ? ORDER BY next_fire_at`, ms(now))
	if err != nil {
		return 0, err
	}
	var list []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.t.Name, &d.t.Kind, &d.specRaw, &d.t.JobType, &d.t.Payload, &d.nextAt); err != nil {
			rows.Close()
			return 0, err
		}
		list = append(list, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	fired := 0
	notify := map[string]bool{}
	for _, d := range list {
		ok, err := s.fire(ctx, d, now)
		if err != nil {
			s.log.Error("scheduler: fire", "trigger", d.t.Name, "err", err)
			continue
		}
		if ok {
			fired++
			notify[d.t.JobType] = true
		}
	}
	for jt := range notify {
		s.q.Notify(jt)
	}
	return fired, nil
}

func (s *Scheduler) fire(ctx context.Context, d due, now time.Time) (bool, error) {
	if err := json.Unmarshal([]byte(d.specRaw), &d.t.Spec); err != nil {
		return false, fmt.Errorf("bad spec: %w", err)
	}
	// Advance from now, not from the missed due time: a late trigger fires once, then resumes.
	n, more, err := next(d.t, now, false)
	if err != nil {
		return false, err
	}
	var nextAt any
	if more {
		nextAt = ms(n)
	}
	scheduledAt := time.UnixMilli(d.nextAt)
	payload, _ := json.Marshal(Fire{Trigger: d.t.Name, ScheduledAt: scheduledAt, Payload: d.t.Payload})

	fired := false
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		// Claim: only advance if nobody else already did (compare-and-set on next_fire_at).
		res, err := tx.ExecContext(ctx, `
			UPDATE platform_triggers SET next_fire_at = ?, last_fired_at = ?, updated_at = ?
			WHERE name = ? AND next_fire_at = ?`, nextAt, ms(now), ms(now), d.t.Name, d.nextAt)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		_, err = s.q.EnqueueTx(ctx, tx, d.t.JobType, payload, queue.Options{
			DedupKey: "trigger:" + d.t.Name + ":" + strconv.FormatInt(d.nextAt, 10),
		})
		if errors.Is(err, queue.ErrDuplicate) {
			err = nil
		}
		fired = err == nil
		return err
	})
	return fired, err
}
