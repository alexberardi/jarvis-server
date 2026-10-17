package policy

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/db"
)

// Store is the autonomy activity log (cc_autonomy_events, docs/autonomy/PLAN.md §0.3): what
// ran without a tap, what was asked and what was blocked, by which rule.
type Store struct {
	DB  *db.DB
	Now func() time.Time
}

// Record appends one event. CreatedAt is filled from Now (default time.Now).
func (s *Store) Record(ctx context.Context, e Event) error {
	now := s.Now
	if now == nil {
		now = time.Now
	}
	e.CreatedAt = now()
	var detail any
	if e.Detail != nil {
		b, err := json.Marshal(e.Detail)
		if err != nil {
			return err
		}
		detail = string(b)
	}
	_, err := s.DB.Write.ExecContext(ctx, `INSERT INTO cc_autonomy_events
		(household_id, user_id, source, source_id, tool, effect, outcome, rule_id, reason, detail, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.HouseholdID, e.UserID, e.Source, e.SourceID, e.Tool, string(e.Effect), e.Outcome, e.RuleID, e.Reason, detail,
		e.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z"))
	return err
}

// Events returns the household's newest events first. limit ≤ 0 means 100; above 500 is capped
// at 500.
func (s *Store) Events(ctx context.Context, hh string, limit int) ([]Event, error) {
	switch {
	case limit <= 0:
		limit = 100
	case limit > 500:
		limit = 500
	}
	rows, err := s.DB.Read.QueryContext(ctx, `SELECT id, household_id, user_id, source, source_id, tool, effect,
		outcome, rule_id, reason, detail, created_at
		FROM cc_autonomy_events WHERE household_id = ?
		ORDER BY created_at DESC, id DESC LIMIT ?`, hh, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var (
			e      Event
			uid    sql.NullInt64
			rid    sql.NullInt64
			detail sql.NullString
			ca     string
		)
		if err := rows.Scan(&e.ID, &e.HouseholdID, &uid, &e.Source, &e.SourceID, &e.Tool, &e.Effect,
			&e.Outcome, &rid, &e.Reason, &detail, &ca); err != nil {
			return nil, err
		}
		e.UserID = nullInt64(uid)
		e.RuleID = nullInt64(rid)
		if detail.Valid {
			if err := json.Unmarshal([]byte(detail.String), &e.Detail); err != nil {
				return nil, err
			}
		}
		if e.CreatedAt, err = time.Parse("2006-01-02T15:04:05.000Z", ca); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// PurgeUser de-identifies the user's events inside the caller's transaction (D20): the rows
// stay, they are the household's audit; the user id goes.
func (s *Store) PurgeUser(ctx context.Context, tx *sql.Tx, userID int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE cc_autonomy_events SET user_id = NULL WHERE user_id = ?`, userID)
	return err
}

func nullInt64(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	v := n.Int64
	return &v
}
