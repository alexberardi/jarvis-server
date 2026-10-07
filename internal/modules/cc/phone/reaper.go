package phone

import (
	"context"
	"time"
)

// The phone reaper (phone_call_service.reap_phone_sessions), a 30 s periodic trigger on the
// scheduler (D27). Per-state windows (D40 11.Q7):
//
//   - confirmed for 5 min → failed ("couldn't start the call"): a lost dial hand-off;
//   - dialing with no heartbeat for 150 s (ringing plus the 60 s stream wait never gets
//     reaped mid-ring, oddity 4);
//   - in_call / wrapup with no heartbeat for 60 s;
//   - any active state past max_call_seconds + 120 s from confirmed_at;
//   - expired drafts → expired (no card; the client renders it from expires_at).
//
// Every reaped call goes through the transition table (legal: X → failed, draft → expired),
// is committed first and only then cancelled in the dialer, so a slow hang-up never holds the
// writer. The errand is resumed directly.

const (
	ReaperInterval      = 30 * time.Second
	confirmedStale      = 5 * time.Minute
	dialingStale        = 150 * time.Second
	heartbeatStale      = 60 * time.Second
	overTimeGrace       = 120 * time.Second
	reasonOverTime      = "call exceeded the time limit"
	reasonLostContact   = "lost contact with the call"
	reasonNeverStarted  = "couldn't start the call"
	reasonCancelledUser = "cancelled by user"
)

// Reap runs one pass and returns how many sessions it reaped. It never fails: errors are
// logged and the next tick tries again.
func (s *Service) Reap(ctx context.Context) int {
	now := s.now()
	reaped := 0
	rows, err := s.DB.Read.QueryContext(ctx, `SELECT `+sessionCols+` FROM cc_phone_call_sessions
		WHERE state IN ('confirmed', 'dialing', 'in_call', 'wrapup')`)
	if err != nil {
		s.log().Error("phone: reaper pass failed", "err", err)
		return 0
	}
	var live []*Session
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			rows.Close()
			s.log().Error("phone: reaper scan failed", "err", err)
			return 0
		}
		live = append(live, sess)
	}
	rows.Close()
	for _, sess := range live {
		if reason := s.reapReason(ctx, sess, now); reason != "" {
			if s.failAndCancel(ctx, sess, reason) {
				reaped++
				s.postCard(ctx, card{HouseholdID: sess.HouseholdID, UserID: sess.UserID,
					Title:    "⚠️ Call ended: " + sess.ContactName,
					Summary:  "The call was ended because Jarvis " + reason + ".",
					Metadata: sessMeta(sess)})
			}
		}
	}

	drafts, err := s.DB.Read.QueryContext(ctx, `SELECT `+sessionCols+` FROM cc_phone_call_sessions
		WHERE state = 'draft' AND expires_at IS NOT NULL AND expires_at < ?`, dbTime(now))
	if err != nil {
		s.log().Error("phone: reaper draft query failed", "err", err)
		return reaped
	}
	var expired []*Session
	for drafts.Next() {
		if sess, err := scanSession(drafts); err == nil {
			expired = append(expired, sess)
		}
	}
	drafts.Close()
	for _, sess := range expired {
		if ok, err := s.transition(ctx, sess, StateExpired, nil); ok && err == nil {
			reaped++
			s.resumeErrand(ctx, sess)
		}
	}
	return reaped
}

func (s *Service) reapReason(ctx context.Context, sess *Session, now time.Time) string {
	started := sess.ConfirmedAt
	if started.IsZero() {
		started = sess.CreatedAt
	}
	if sess.State == StateConfirmed {
		if now.Sub(started) > confirmedStale && s.runtime(sess.ID) == nil {
			return reasonNeverStarted
		}
		return ""
	}
	maxSeconds := s.intSetting(ctx, SettingMaxCallSeconds, sess.HouseholdID, 600)
	if now.Sub(started) > time.Duration(maxSeconds)*time.Second+overTimeGrace {
		return reasonOverTime
	}
	last := sess.HeartbeatAt
	if last.IsZero() {
		last = started
	}
	window := heartbeatStale
	if sess.State == StateDialing {
		window = dialingStale
	}
	if now.Sub(last) > window {
		return reasonLostContact
	}
	return ""
}

// failAndCancel marks an active (or confirmed) session failed, then stops its live call.
func (s *Service) failAndCancel(ctx context.Context, sess *Session, reason string) bool {
	ok, err := s.transition(ctx, sess, StateFailed, map[string]any{"error_message": reason})
	if err != nil {
		s.log().Error("phone: reap write failed", "session", sess.ID, "err", err)
		return false
	}
	if !ok {
		return false
	}
	s.log().Warn("phone: session ended", "session", sess.ID, "reason", reason)
	s.cancelRuntime(sess.ID)
	s.resumeErrand(ctx, sess)
	return true
}

// cancelLive ends a live call at the user's request (D40 11.Q3): failed first, then hang up.
func (s *Service) cancelLive(ctx context.Context, sess *Session, reason string) {
	s.failAndCancel(ctx, sess, reason)
}

// CancelHousehold ends every active call of a household (D40 11.Q3: turning
// phone_calls.enabled off cancels live calls). The live call also re-checks the gate on
// every heartbeat, so this is the fast path when a caller knows the toggle changed.
func (s *Service) CancelHousehold(ctx context.Context, hh string) {
	rows, err := s.DB.Read.QueryContext(ctx, `SELECT `+sessionCols+` FROM cc_phone_call_sessions
		WHERE household_id = ? AND state IN ('confirmed', 'dialing', 'in_call', 'wrapup')`, hh)
	if err != nil {
		return
	}
	var live []*Session
	for rows.Next() {
		if sess, err := scanSession(rows); err == nil {
			live = append(live, sess)
		}
	}
	rows.Close()
	for _, sess := range live {
		if sess.State == StateConfirmed {
			if ok, _ := s.transition(ctx, sess, StateDeclined, map[string]any{"error_message": "phone_calls.enabled turned off"}); ok {
				s.resumeErrand(ctx, sess)
			}
			continue
		}
		s.failAndCancel(ctx, sess, "phone calls were turned off")
	}
}
