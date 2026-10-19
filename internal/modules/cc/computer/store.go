package computer

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
	"github.com/alexberardi/jarvis-server/internal/platform/db"
)

// Run states (cc_computer_runs.state).
const (
	RunRunning = "running"
	RunWaiting = "waiting" // parked on a confirmation or a question
	RunPaused  = "paused"  // the user took over the computer
	RunDone    = "done"
	RunFailed  = "failed"
	RunStopped = "stopped"
)

// Run is one row of cc_computer_runs.
type Run struct {
	ID, HouseholdID      string
	UserID               *int64
	NodeID, WorkflowID   string
	Step                 int
	Goal, Target, State  string
	StopRequested        bool
	Rounds               int
	History              []Entry
	Pending              *Held // the batch waiting for a confirmation, or nil
	Model, Error         string
	CreatedAt, UpdatedAt time.Time
}

// Entry is one action in a run's history: what it meant to do and what came of it, as text. It
// never holds an image (the images live for one model call only).
type Entry struct {
	Round  int    `json:"round"`
	Kind   string `json:"kind"`
	Intent string `json:"intent"`
	Ref    string `json:"ref,omitempty"`
	Result string `json:"result"` // ok | failed: <code> <error> | blocked: … | not run
}

// Held is a batch parked for a confirmation: every action of the batch, in order, and which of
// them need the user's approval (those with a digest).
type Held struct {
	Target  computeruse.Target `json:"target"` // as the digests were computed
	Actions []HeldAction       `json:"actions"`
}

// HeldAction is one action of a held batch. Digest is set for the risky ones (the approval is
// bound to it).
type HeldAction struct {
	Action   computeruse.Action `json:"action"`
	Category string             `json:"category,omitempty"`
	Reason   string             `json:"reason,omitempty"`
	Digest   string             `json:"digest,omitempty"`
}

// Store keeps computer task runs (cc_computer_runs).
type Store struct {
	DB  *db.DB
	Now func() time.Time
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

const timeFormat = "2006-01-02T15:04:05.000Z"

func dbTime(t time.Time) string { return t.UTC().Format(timeFormat) }

// Create inserts a new running run and returns it with its id (cr_<hex>).
func (s *Store) Create(ctx context.Context, r Run) (*Run, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	r.ID = "cr_" + hex.EncodeToString(b)
	r.State = RunRunning
	r.CreatedAt = s.now()
	r.UpdatedAt = r.CreatedAt
	hist, err := historyJSON(r.History)
	if err != nil {
		return nil, err
	}
	_, err = s.DB.Write.ExecContext(ctx, `INSERT INTO cc_computer_runs
		(id, household_id, user_id, node_id, workflow_id, step, goal, target, state, rounds, history, model,
		 created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.HouseholdID, r.UserID, r.NodeID, r.WorkflowID, r.Step, r.Goal, r.Target, r.State, r.Rounds,
		hist, r.Model, dbTime(r.CreatedAt), dbTime(r.UpdatedAt))
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// ErrNoRun is Get's error for an unknown id.
var ErrNoRun = errors.New("computer: no such run")

// Get reads one run.
func (s *Store) Get(ctx context.Context, id string) (*Run, error) {
	var (
		r                Run
		uid              sql.NullInt64
		stop             int
		hist             string
		pending          sql.NullString
		created, updated string
	)
	err := s.DB.Read.QueryRowContext(ctx, `SELECT id, household_id, user_id, node_id, workflow_id, step, goal,
		target, state, stop_requested, rounds, history, pending, model, error, created_at, updated_at
		FROM cc_computer_runs WHERE id = ?`, id).Scan(&r.ID, &r.HouseholdID, &uid, &r.NodeID, &r.WorkflowID,
		&r.Step, &r.Goal, &r.Target, &r.State, &stop, &r.Rounds, &hist, &pending, &r.Model, &r.Error, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoRun
	}
	if err != nil {
		return nil, err
	}
	if uid.Valid {
		r.UserID = &uid.Int64
	}
	r.StopRequested = stop != 0
	if err := json.Unmarshal([]byte(hist), &r.History); err != nil {
		return nil, err
	}
	if pending.Valid {
		r.Pending = &Held{}
		if err := json.Unmarshal([]byte(pending.String), r.Pending); err != nil {
			return nil, err
		}
	}
	r.CreatedAt, _ = time.Parse(timeFormat, created)
	r.UpdatedAt, _ = time.Parse(timeFormat, updated)
	return &r, nil
}

// Save writes a run's progress: state, rounds, history, the held batch, model and error.
func (s *Store) Save(ctx context.Context, r *Run) error {
	hist, err := historyJSON(r.History)
	if err != nil {
		return err
	}
	var pending any
	if r.Pending != nil {
		b, err := json.Marshal(r.Pending)
		if err != nil {
			return err
		}
		pending = string(b)
	}
	r.UpdatedAt = s.now()
	_, err = s.DB.Write.ExecContext(ctx, `UPDATE cc_computer_runs SET state = ?, rounds = ?, history = ?,
		pending = ?, model = ?, error = ?, updated_at = ? WHERE id = ?`,
		r.State, r.Rounds, hist, pending, r.Model, r.Error, dbTime(r.UpdatedAt), r.ID)
	return err
}

// RequestStop sets the run's durable stop flag; the loop sees it before its next round.
func (s *Store) RequestStop(ctx context.Context, id string) error {
	_, err := s.DB.Write.ExecContext(ctx, `UPDATE cc_computer_runs SET stop_requested = 1, updated_at = ? WHERE id = ?`,
		dbTime(s.now()), id)
	return err
}

// StopRequested reports whether a stop was asked for the run.
func (s *Store) StopRequested(ctx context.Context, id string) (bool, error) {
	var stop int
	err := s.DB.Read.QueryRowContext(ctx, `SELECT stop_requested FROM cc_computer_runs WHERE id = ?`, id).Scan(&stop)
	if err != nil {
		return false, err
	}
	return stop != 0, nil
}

func historyJSON(h []Entry) (string, error) {
	if h == nil {
		h = []Entry{}
	}
	b, err := json.Marshal(h)
	return string(b), err
}
