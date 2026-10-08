package cc

import (
	"context"
	"time"
)

// Household timezone. jarvisd stores no zone per household; nodes report theirs in every
// conversation's node_context, and the household's zone is the one its most recently seen
// active node reported. Module.HouseholdClock overrides it (tests, or a future setting).

// HouseholdTimezone is a provider of a household's IANA zone ("" = unknown).
type HouseholdTimezone interface {
	HouseholdTimezone(ctx context.Context, householdID string) string
}

// householdTimezone is the household's zone, or "" (callers use UTC).
func (m *Module) householdTimezone(ctx context.Context, hh string) string {
	if m.HouseholdClock != nil {
		return m.HouseholdClock.HouseholdTimezone(ctx, hh)
	}
	if hh == "" || m.deps.DB == nil {
		return ""
	}
	rows, err := m.deps.DB.Read.QueryContext(ctx, `SELECT timezone FROM cc_nodes
		WHERE household_id = ? AND is_active = 1 AND timezone IS NOT NULL AND timezone != ''
		ORDER BY last_seen DESC`, hh)
	if err != nil {
		return ""
	}
	defer rows.Close()
	for rows.Next() {
		var tz string
		if rows.Scan(&tz) == nil && validZone(tz) {
			return tz
		}
	}
	return ""
}

func validZone(tz string) bool {
	if tz == "" {
		return false
	}
	_, err := time.LoadLocation(tz)
	return err == nil
}

// recordNodeTimezone keeps the zone a node reported (a no-op when unchanged or invalid).
func (m *Module) recordNodeTimezone(ctx context.Context, nodeID, tz string) {
	if !validZone(tz) {
		return
	}
	if _, err := m.deps.DB.Write.ExecContext(ctx, `UPDATE cc_nodes SET timezone = ?
		WHERE node_id = ? AND (timezone IS NULL OR timezone != ?)`, tz, nodeID, tz); err != nil {
		m.deps.Log.Warn("cc: recording node timezone failed", "node", nodeID, "err", err)
	}
}

// HouseholdTimezone is the household's IANA zone ("" = unknown) for other modules, e.g. the
// recipes planner's "today".
func (m *Module) HouseholdTimezone(ctx context.Context, householdID string) string {
	return m.householdTimezone(ctx, householdID)
}
