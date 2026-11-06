package phone

import (
	"context"
	"database/sql"
	"time"
)

// Call retention (docs/decisions/2026-11-06-phone-retention.md): an hourly sweep deletes the
// recording and transcript of every call that ended more than phone_calls.audio_retention_days
// ago (per household; 0 or less keeps nothing once the call has ended). The row stays, with
// its duration, outcome and summary, because the daily and monthly caps count past calls and
// the inbox card is the user's record of the call. A blob that fails to delete keeps its key,
// so the next sweep retries it.

const (
	RetentionSweepCron    = "41 * * * *" // hourly, UTC
	defaultRetentionDays  = 30
	retentionSessionQuery = `SELECT id, household_id, audio_object_key, ended_at FROM cc_phone_call_sessions
		WHERE ended_at IS NOT NULL AND (audio_object_key IS NOT NULL OR transcript_json IS NOT NULL)`
	retentionInboundQuery = `SELECT id, household_id, audio_object_key, ended_at FROM cc_phone_inbound_calls
		WHERE ended_at IS NOT NULL AND audio_object_key IS NOT NULL`
)

type retainedCall struct {
	id, hh string
	audio  sql.NullString
	ended  time.Time
}

// SweepRetention runs one pass. Blob failures are logged and retried next pass; a database
// error ends the pass and is returned.
func (s *Service) SweepRetention(ctx context.Context) error {
	cutoffs := map[string]time.Time{}
	cutoff := func(hh string) time.Time {
		c, ok := cutoffs[hh]
		if !ok {
			days := max(s.intSetting(ctx, SettingRetentionDays, hh, defaultRetentionDays), 0)
			c = s.now().Add(-time.Duration(days) * 24 * time.Hour)
			cutoffs[hh] = c
		}
		return c
	}
	reaped := 0
	for _, t := range []struct{ query, update string }{
		// transcript_json is cleared unconditionally; the key only when its blob is gone (the
		// CASE leaves a key a late outcome wrote meanwhile).
		{retentionSessionQuery, `UPDATE cc_phone_call_sessions SET transcript_json = NULL,
			audio_object_key = CASE WHEN audio_object_key = ? THEN NULL ELSE audio_object_key END WHERE id = ?`},
		{retentionInboundQuery, `UPDATE cc_phone_inbound_calls SET
			audio_object_key = CASE WHEN audio_object_key = ? THEN NULL ELSE audio_object_key END WHERE id = ?`},
	} {
		calls, err := s.retainedCalls(ctx, t.query)
		if err != nil {
			return err
		}
		for _, c := range calls {
			if c.ended.After(cutoff(c.hh)) {
				continue
			}
			var gone any // the key whose blob was deleted, or NULL to keep the key
			if c.audio.Valid {
				if s.Blobs == nil {
					s.log().Warn("phone: retention can't delete a recording without a blob store", "call", c.id)
				} else if err := s.Blobs.Delete(ctx, c.audio.String); err != nil {
					s.log().Warn("phone: retention blob delete failed, retrying next pass", "call", c.id, "err", err)
				} else {
					gone = c.audio.String
				}
			}
			if _, err := s.DB.Write.ExecContext(ctx, t.update, gone, c.id); err != nil {
				return err
			}
			reaped++
		}
	}
	if reaped > 0 {
		s.log().Info("phone: expired call recordings and transcripts deleted", "calls", reaped)
	}
	return nil
}

func (s *Service) retainedCalls(ctx context.Context, query string) ([]retainedCall, error) {
	rows, err := s.DB.Read.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []retainedCall
	for rows.Next() {
		var c retainedCall
		var ended string
		if err := rows.Scan(&c.id, &c.hh, &c.audio, &ended); err != nil {
			return nil, err
		}
		c.ended = parseTS(ended)
		out = append(out, c)
	}
	return out, rows.Err()
}
