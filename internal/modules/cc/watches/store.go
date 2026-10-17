package watches

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/db"
)

const timeFormat = "2006-01-02T15:04:05.000Z"

// defaultWebCadence is a web watch's check interval when it is created without one.
const defaultWebCadence = 3600

// Store is the household's watches (cc_watches, docs/autonomy/PLAN.md §2).
type Store struct {
	DB  *db.DB
	Now func() time.Time
}

func (s *Store) now() time.Time {
	if s.Now == nil {
		return time.Now().UTC().Truncate(time.Millisecond)
	}
	return s.Now().UTC().Truncate(time.Millisecond)
}

const columns = `id, household_id, user_id, title, source_kind, source_spec, condition, cadence_seconds,
	state, fire_policy, cooldown_seconds, expires_at, last_value, last_checked_at, last_fired_at,
	last_error, created_at, updated_at`

// Create validates and stores a new, active watch. It fills the id, ExpiresAt (now + DefaultTTL
// when unset) and a web watch's cadence (an hour when unset), and returns the stored watch.
func (s *Store) Create(ctx context.Context, w Watch) (Watch, error) {
	if err := w.Validate(); err != nil {
		return Watch{}, err
	}
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return Watch{}, err
	}
	now := s.now()
	w.ID = "wt_" + hex.EncodeToString(b)
	w.State = StateActive
	w.CreatedAt, w.UpdatedAt = now, now
	if w.ExpiresAt == nil {
		exp := now.Add(DefaultTTL)
		w.ExpiresAt = &exp
	} else {
		exp := w.ExpiresAt.UTC().Truncate(time.Millisecond)
		w.ExpiresAt = &exp
	}
	if w.SourceKind == SourceWeb && w.CadenceSeconds <= 0 {
		w.CadenceSeconds = defaultWebCadence
	}
	src, err := json.Marshal(w.Source)
	if err != nil {
		return Watch{}, err
	}
	cond, err := json.Marshal(w.Condition)
	if err != nil {
		return Watch{}, err
	}
	_, err = s.DB.Write.ExecContext(ctx, `INSERT INTO cc_watches
		(id, household_id, user_id, title, source_kind, source_spec, condition, cadence_seconds,
		 state, fire_policy, cooldown_seconds, expires_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		w.ID, w.HouseholdID, w.UserID, w.Title, w.SourceKind, string(src), string(cond), w.CadenceSeconds,
		w.State, w.FirePolicy, w.CooldownSeconds, fmtTime(*w.ExpiresAt), fmtTime(now), fmtTime(now))
	if err != nil {
		return Watch{}, err
	}
	return w, nil
}

// Get returns the household's watch, or nil, nil when it is missing or another household's.
func (s *Store) Get(ctx context.Context, hh, id string) (*Watch, error) {
	ws, err := s.query(ctx, `SELECT `+columns+` FROM cc_watches WHERE household_id = ? AND id = ?`, hh, id)
	if err != nil || len(ws) == 0 {
		return nil, err
	}
	return &ws[0], nil
}

// List returns the household's watches in creation order: one member's when userID is set, in
// the given states when any are given.
func (s *Store) List(ctx context.Context, hh string, userID *int64, states ...string) ([]Watch, error) {
	q := `SELECT ` + columns + ` FROM cc_watches WHERE household_id = ?`
	args := []any{hh}
	if userID != nil {
		q += ` AND user_id = ?`
		args = append(args, *userID)
	}
	if len(states) > 0 {
		q += ` AND state IN (?` + strings.Repeat(", ?", len(states)-1) + `)`
		for _, st := range states {
			args = append(args, st)
		}
	}
	return s.query(ctx, q+` ORDER BY created_at, id`, args...)
}

// CountLive counts the household's active and paused watches (watches.max_active's measure).
func (s *Store) CountLive(ctx context.Context, hh string) (int, error) {
	var n int
	err := s.DB.Read.QueryRowContext(ctx, `SELECT COUNT(*) FROM cc_watches
		WHERE household_id = ? AND state IN ('active', 'paused')`, hh).Scan(&n)
	return n, err
}

// SetState moves the household's watch to state; false when there is no such watch.
func (s *Store) SetState(ctx context.Context, hh, id, state string) (bool, error) {
	if err := checkState(state); err != nil {
		return false, err
	}
	res, err := s.DB.Write.ExecContext(ctx, `UPDATE cc_watches SET state = ?, updated_at = ?
		WHERE household_id = ? AND id = ?`, state, fmtTime(s.now()), hh, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// checkState refuses a state the table does not know.
func checkState(state string) error {
	switch state {
	case StateActive, StatePaused, StateDone:
		return nil
	}
	return fmt.Errorf("watch: unknown state %q", state)
}

// RecordCheck notes a check: last_checked_at is now, last_value is replaced only by a non-empty
// value (a failed check keeps the last good one), last_error is errText or cleared.
func (s *Store) RecordCheck(ctx context.Context, id, value, errText string) error {
	now := fmtTime(s.now())
	_, err := s.DB.Write.ExecContext(ctx, `UPDATE cc_watches SET last_checked_at = ?,
		last_value = CASE WHEN ? = '' THEN last_value ELSE ? END, last_error = ?, updated_at = ?
		WHERE id = ?`, now, value, value, nullString(errText), now, id)
	return err
}

// RecordFire notes that the watch fired and moves it to state (done for a once watch).
func (s *Store) RecordFire(ctx context.Context, id, state string) error {
	if err := checkState(state); err != nil {
		return err
	}
	now := fmtTime(s.now())
	_, err := s.DB.Write.ExecContext(ctx, `UPDATE cc_watches SET last_fired_at = ?, state = ?, updated_at = ?
		WHERE id = ?`, now, state, now, id)
	return err
}

// ActiveSignalWatches returns the household's active watches on signals of kind.
func (s *Store) ActiveSignalWatches(ctx context.Context, hh, kind string) ([]Watch, error) {
	return s.query(ctx, `SELECT `+columns+` FROM cc_watches
		WHERE household_id = ? AND source_kind = 'signal' AND state = 'active'
		AND json_extract(source_spec, '$.kind') = ? ORDER BY created_at, id`, hh, kind)
}

// ExpireDue moves every live (active or paused) watch whose expiry has come to done and returns
// their ids.
func (s *Store) ExpireDue(ctx context.Context) ([]string, error) {
	now := fmtTime(s.now())
	rows, err := s.DB.Write.QueryContext(ctx, `UPDATE cc_watches SET state = 'done', updated_at = ?
		WHERE state IN ('active', 'paused') AND expires_at IS NOT NULL AND expires_at <= ? RETURNING id`, now, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// PurgeUser deletes the user's watches inside the caller's account-deletion transaction (D20).
// The household's watches (user_id NULL) stay.
func (s *Store) PurgeUser(ctx context.Context, tx *sql.Tx, userID int64) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM cc_watches WHERE user_id = ?`, userID)
	return err
}

func (s *Store) query(ctx context.Context, q string, args ...any) ([]Watch, error) {
	rows, err := s.DB.Read.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Watch
	for rows.Next() {
		var (
			w                                          Watch
			uid                                        sql.NullInt64
			src, cond, ca, ua                          string
			exp, lastValue, checked, fired, lastErrStr sql.NullString
		)
		if err := rows.Scan(&w.ID, &w.HouseholdID, &uid, &w.Title, &w.SourceKind, &src, &cond, &w.CadenceSeconds,
			&w.State, &w.FirePolicy, &w.CooldownSeconds, &exp, &lastValue, &checked, &fired,
			&lastErrStr, &ca, &ua); err != nil {
			return nil, err
		}
		if uid.Valid {
			w.UserID = &uid.Int64
		}
		if err := json.Unmarshal([]byte(src), &w.Source); err != nil {
			return nil, fmt.Errorf("watch %s: source_spec: %w", w.ID, err)
		}
		if err := json.Unmarshal([]byte(cond), &w.Condition); err != nil {
			return nil, fmt.Errorf("watch %s: condition: %w", w.ID, err)
		}
		w.LastValue, w.LastError = lastValue.String, lastErrStr.String
		if w.CreatedAt, err = time.Parse(timeFormat, ca); err != nil {
			return nil, err
		}
		if w.UpdatedAt, err = time.Parse(timeFormat, ua); err != nil {
			return nil, err
		}
		for _, t := range []struct {
			src sql.NullString
			dst **time.Time
		}{{exp, &w.ExpiresAt}, {checked, &w.LastCheckedAt}, {fired, &w.LastFiredAt}} {
			if !t.src.Valid {
				continue
			}
			v, err := time.Parse(timeFormat, t.src.String)
			if err != nil {
				return nil, errors.Join(fmt.Errorf("watch %s: bad time", w.ID), err)
			}
			*t.dst = &v
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func fmtTime(t time.Time) string { return t.UTC().Format(timeFormat) }

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
