package phone

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
)

// Session lifecycle operations the live call drives in process (the gateway's
// /internal/phone/sessions/{id}/events contract, api/phone_sessions.py:206-348). Each keeps
// the legacy precondition; the legacy 409s become ok=false.

// claimDial is the dial authorisation: an atomic confirmed → dialing CAS with exactly one
// winner. Nothing else may start a call.
func (s *Service) claimDial(ctx context.Context, id string) (bool, error) {
	res, err := s.DB.Write.ExecContext(ctx, `UPDATE cc_phone_call_sessions
		SET state = 'dialing', worker_url = 'in-process', heartbeat_at = ?
		WHERE id = ? AND state = 'confirmed'`, dbTime(s.now()), id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// heartbeat refreshes liveness for a non-terminal session.
func (s *Service) heartbeat(ctx context.Context, id string) {
	if _, err := s.DB.Write.ExecContext(ctx, `UPDATE cc_phone_call_sessions SET heartbeat_at = ?
		WHERE id = ? AND state NOT IN ('done', 'failed', 'declined', 'expired')`, dbTime(s.now()), id); err != nil {
		s.log().Warn("phone: heartbeat failed", "session", id, "err", err)
	}
}

// setState is the "state" event: a legal transition plus heartbeat; a failure stores its
// reason (fixes oddity 3: the gateway's reason was dropped) and posts the failure card; a
// terminal state resumes a waiting errand.
func (s *Service) setState(ctx context.Context, id, to, reason, callSID string) (bool, error) {
	sess, err := s.session(ctx, s.DB.Read, id)
	if err != nil {
		return false, err
	}
	extra := map[string]any{"heartbeat_at": dbTime(s.now())}
	if reason != "" {
		extra["error_message"] = reason
	}
	if callSID != "" {
		extra["twilio_call_sid"] = callSID
	}
	ok, err := s.transition(ctx, sess, to, extra)
	if err != nil || !ok {
		return ok, err
	}
	if to == StateFailed {
		summary := reason
		if summary == "" {
			summary = "The call could not be completed."
		}
		s.postCard(ctx, card{HouseholdID: sess.HouseholdID, UserID: sess.UserID,
			Title: "⚠️ Call failed: " + sess.ContactName, Summary: summary, Metadata: sessMeta(sess)})
	}
	if isTerminal(to) {
		s.resumeErrand(ctx, sess)
	}
	return true, nil
}

// appendTurn is the "turn" event: append to transcript_json (read-modify-write inside the
// single writer) and heartbeat. Only for an active session.
func (s *Service) appendTurn(ctx context.Context, id string, turn map[string]any) (bool, error) {
	tx, err := s.DB.Write.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var state string
	var tj sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT state, transcript_json FROM cc_phone_call_sessions WHERE id = ?`, id).
		Scan(&state, &tj); err != nil {
		return false, err
	}
	if !isActive(state) {
		return false, nil
	}
	var turns []any
	if tj.Valid && tj.String != "" {
		if json.Unmarshal([]byte(tj.String), &turns) != nil {
			turns = nil
		}
	}
	turns = append(turns, turn)
	b, err := json.Marshal(turns)
	if err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE cc_phone_call_sessions SET transcript_json = ?, heartbeat_at = ? WHERE id = ?`,
		string(b), dbTime(s.now()), id); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// escalate is the "escalation" event: only while in_call; heartbeat, then the answer card.
func (s *Service) escalate(ctx context.Context, id, question string) (bool, error) {
	sess, err := s.session(ctx, s.DB.Read, id)
	if err != nil {
		return false, err
	}
	if sess.State != StateInCall || question == "" {
		return false, nil
	}
	s.heartbeat(ctx, id)
	s.postEscalationCard(ctx, sess, question)
	return true, nil
}

// recordOutcome is the "outcome" event: store the outcome and audio key, land a live session
// terminal exactly once (→ wrapup → done), auto-save the business, post the outcome card and
// resume the errand. D40 11.Q11: an outcome that arrives after the session already went
// terminal (the reaper failed it) is stored for the record, with no second card and no
// re-resume, except a corrective card when the goal was achieved.
func (s *Service) recordOutcome(ctx context.Context, id string, outcome map[string]any, audioKey string) error {
	sess, err := s.session(ctx, s.DB.Read, id)
	if err != nil {
		return err
	}
	b, err := json.Marshal(outcome)
	if err != nil {
		return err
	}
	cols := map[string]any{"outcome_json": string(b), "heartbeat_at": dbTime(s.now())}
	if audioKey != "" {
		cols["audio_object_key"] = audioKey
	}
	late := isTerminal(sess.State)
	if late {
		if _, err := s.DB.Write.ExecContext(ctx, `UPDATE cc_phone_call_sessions SET outcome_json = ?,
			audio_object_key = COALESCE(?, audio_object_key) WHERE id = ?`, string(b), nullStr(audioKey), id); err != nil {
			return err
		}
		sess.OutcomeJSON = string(b)
		if audioKey != "" {
			sess.AudioKey = audioKey
		}
		if achieved, _ := outcome["goal_achieved"].(bool); achieved {
			s.postOutcomeCard(ctx, sess, true)
		}
		return nil
	}
	if sess.State == StateDialing || sess.State == StateInCall {
		if _, err := s.transition(ctx, sess, StateWrapup, nil); err != nil {
			return err
		}
	}
	if _, err := s.transition(ctx, sess, StateDone, cols); err != nil {
		return err
	}
	cur, err := s.session(ctx, s.DB.Read, id)
	if err != nil {
		return err
	}
	if err := s.upsertContactFromCall(ctx, cur); err != nil {
		s.log().Warn("phone: phonebook auto-save failed", "session", id, "err", err)
	}
	s.postOutcomeCard(ctx, cur, false)
	s.resumeErrand(ctx, cur)
	return nil
}

// upsertContactFromCall remembers a business after a completed call: the dialed number wins,
// an exact normalized-name match is updated (do_not_call is never cleared), else a new
// source='call' contact.
func (s *Service) upsertContactFromCall(ctx context.Context, sess *Session) error {
	if sess.State != StateDone {
		return nil
	}
	number := sess.DialedNumber
	if number == "" {
		number = sess.ResolvedNumber
	}
	name := strings.TrimSpace(sess.ContactName)
	if number == "" || name == "" {
		return nil
	}
	number, err := NormalizeUS(number)
	if err != nil {
		return nil
	}
	norm := NormalizeName(name)
	now := dbTime(s.now())
	var lineType any
	if sess.LineType != "" && sess.LineType != "unknown" {
		lineType = sess.LineType
	}
	var existingID string
	err = s.DB.Read.QueryRowContext(ctx, `SELECT id FROM cc_phone_contacts WHERE household_id = ? AND normalized_name = ?`,
		sess.HouseholdID, norm).Scan(&existingID)
	switch {
	case err == nil:
		_, err = s.DB.Write.ExecContext(ctx, `UPDATE cc_phone_contacts SET number = ?, verified_at = ?,
			line_type = COALESCE(?, line_type),
			address = CASE WHEN address IS NULL OR address = '' THEN ? ELSE address END,
			updated_at = ? WHERE id = ?`, number, now, lineType, nullStr(sess.ContactAddress), now, existingID)
		return err
	case isNoRows(err):
		_, err = s.DB.Write.ExecContext(ctx, `INSERT INTO cc_phone_contacts
			(id, household_id, name, normalized_name, number, address, source, line_type, do_not_call, verified_at, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, 'call', ?, 0, ?, ?, ?)`,
			uuid4(), sess.HouseholdID, name, norm, number, nullStr(sess.ContactAddress), lineType, now, now, now)
		return err
	default:
		return err
	}
}
