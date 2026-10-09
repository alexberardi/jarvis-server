package cc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// Household timezone (STATUS decision 2026-10-09; docs/cc/13 §3.7).
//
// A household's zone, most authoritative first:
//
//  1. Module.HouseholdClock, when set (tests).
//  2. The household.timezone setting (household-scoped, set from the mobile Household screen).
//     Empty or unset means automatic. Writes are validated (an IANA name time.LoadLocation
//     accepts, never "Local"); a stored value that no longer loads is ignored.
//  3. The zone the household's most recently seen active node reported (cc_nodes.timezone,
//     recorded at warmup from node_context.timezone).
//  4. "" — callers use UTC.
//
// That is householdTimezone, used by attention (quiet hours, journal), errands, signal
// reactions' date context and the recipes planner's "today" (HouseholdTimezone).
//
// A turn's own zone (conversation.timezone: the prompt's date context, the dates tools,
// schedule_errand, the ambient bundle) and the node's /generate/date-context start from the
// zone the client reported, because a household's nodes may sit in different zones. An
// explicitly set household.timezone wins over it (turnTimezone): a Pi left on UTC, or the
// mobile chat's America/New_York default, must not give wrong local times once the user has
// picked a zone. With the setting empty, a turn uses exactly what the client reported, as
// before. The reported zone is still recorded for the node (step 3) either way.
//
// Routines are not affected: each routine's schedule carries its own validated zone, which the
// mobile routine editor fills from the phone.

const settingHouseholdTimezone = "household.timezone"

// Where an effective household zone came from (GET .../timezone "source").
const (
	tzSourceSetting = "setting"
	tzSourceNode    = "node"
	tzSourceDefault = "default"
)

// HouseholdTimezone is a provider of a household's IANA zone ("" = unknown).
type HouseholdTimezone interface {
	HouseholdTimezone(ctx context.Context, householdID string) string
}

func timezoneDefinition() settings.Definition {
	return settings.Definition{Key: settingHouseholdTimezone, Category: "household", Type: settings.String, Default: "",
		Description: "The household's IANA time zone (e.g. America/New_York) for local times: quiet hours, the " +
			"journal, errands, recipes' today and every turn's date context. Empty means automatic: the zone the " +
			"household's most recently seen node reports.",
		Validate: validateTimezoneSetting}
}

// validateTimezoneSetting accepts "" (automatic) or a zone time.LoadLocation knows, never
// "Local" (the server's own zone, which means nothing to a household).
func validateTimezoneSetting(v any) error {
	s, ok := v.(string)
	if !ok {
		return errors.New("expected a string")
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if !validZone(s) {
		return fmt.Errorf("'%s' is not a known IANA time zone", s)
	}
	return nil
}

// householdTimezone is the household's zone, or "" (callers use UTC).
func (m *Module) householdTimezone(ctx context.Context, hh string) string {
	if m.HouseholdClock != nil {
		return m.HouseholdClock.HouseholdTimezone(ctx, hh)
	}
	if tz := m.settingTimezone(ctx, hh); tz != "" {
		return tz
	}
	return m.nodeTimezone(ctx, hh)
}

// resolveHouseholdTimezone is steps 2-4 for the read endpoint: the effective zone, where it
// came from, and the node-derived zone ("" when no node reported one).
func (m *Module) resolveHouseholdTimezone(ctx context.Context, hh string) (tz, source, node string) {
	node = m.nodeTimezone(ctx, hh)
	if set := m.settingTimezone(ctx, hh); set != "" {
		return set, tzSourceSetting, node
	}
	if node != "" {
		return node, tzSourceNode, node
	}
	return "", tzSourceDefault, ""
}

// settingTimezone is the household's explicitly chosen zone ("" when automatic or invalid).
func (m *Module) settingTimezone(ctx context.Context, hh string) string {
	if hh == "" || m.settings == nil {
		return ""
	}
	tz := strings.TrimSpace(m.settings.String(ctx, settingHouseholdTimezone, settings.Scope{HouseholdID: hh}))
	if !validZone(tz) {
		return ""
	}
	return tz
}

// turnTimezone is a turn's zone: the household's explicit setting, else what the client reported.
func (m *Module) turnTimezone(ctx context.Context, hh, reported string) string {
	if tz := m.settingTimezone(ctx, hh); tz != "" {
		return tz
	}
	return reported
}

// nodeTimezone is the zone the household's most recently seen active node reported, or "".
func (m *Module) nodeTimezone(ctx context.Context, hh string) string {
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
	if tz == "" || tz == "Local" {
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

// handleGetHouseholdTimezone is GET /api/v0/mobile/household/{household_id}/timezone (any
// member): the effective zone, its source, and the node-derived zone the mobile app shows as
// "Automatic". jarvisd-only (the legacy stack has no such route; the app hides the row on 404).
//
//	{"household_id": "...", "timezone": "<effective or empty>", "source": "setting"|"node"|"default",
//	 "node_timezone": "<node-derived or empty>"}
func (m *Module) handleGetHouseholdTimezone(w http.ResponseWriter, r *http.Request, u authn.User) {
	ctx := r.Context()
	hh := r.PathValue("household_id")
	if err := m.requireRole(ctx, u.ID, hh, authn.RoleMember); err != nil {
		m.writeErr(w, err)
		return
	}
	tz, source, node := m.resolveHouseholdTimezone(ctx, hh)
	out := pyjson.NewObject()
	out.Set("household_id", hh)
	out.Set("timezone", tz)
	out.Set("source", source)
	out.Set("node_timezone", node)
	writePy(w, http.StatusOK, out)
}
