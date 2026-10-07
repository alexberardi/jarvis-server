package phone

import "context"

// The 09 workflow engine's view of phone sessions: the deadline's status check, and declining
// an errand's calls that have not dialled yet.

func snapshotOf(s *Session) CallSnapshot {
	return CallSnapshot{SessionID: s.ID, ErrandID: s.ErrandID, ErrandStep: s.ErrandStep, State: s.State,
		ContactName: s.ContactName, ErrorMessage: s.ErrorMessage, HouseholdID: s.HouseholdID,
		UserID: s.UserID, OutcomeJSON: s.OutcomeJSON, ConfirmedAt: s.ConfirmedAt}
}

// Snapshot reads a session as an errand sees it. ok=false: no such session.
func (s *Service) Snapshot(ctx context.Context, id string) (CallSnapshot, bool, error) {
	sess, err := s.session(ctx, s.DB.Read, id)
	if isNoRows(err) {
		return CallSnapshot{}, false, nil
	}
	if err != nil {
		return CallSnapshot{}, false, err
	}
	return snapshotOf(sess), true, nil
}

// DeclineErrand declines the errand's sessions still in draft or confirmed, so a cancelled or
// timed-out errand never dials (D48). true when any was declined.
func (s *Service) DeclineErrand(ctx context.Context, errandID string) (bool, error) {
	rows, err := s.DB.Read.QueryContext(ctx, `SELECT `+sessionCols+` FROM cc_phone_call_sessions
		WHERE errand_id = ? AND state IN (?, ?)`, errandID, StateDraft, StateConfirmed)
	if err != nil {
		return false, err
	}
	var pending []*Session
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			rows.Close()
			return false, err
		}
		pending = append(pending, sess)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}
	declined := false
	for _, sess := range pending {
		ok, err := s.transition(ctx, sess, StateDeclined, map[string]any{"error_message": "The errand was stopped"})
		if err != nil {
			return declined, err
		}
		declined = declined || ok
	}
	return declined, nil
}
