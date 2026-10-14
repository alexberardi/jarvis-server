# Household timezone is user-settable

- **Date:** 2026-10-09
- **Moved from** the STATUS.md decision log on 2026-10-14.

**Household timezone is user-settable** (`household.timezone`, household-scoped, mobile Household screen; `""` = automatic). Precedence: `HouseholdClock` (tests) > setting > most recently seen active node's zone > UTC. A turn's own zone (prompt date context, dates tools, `schedule_errand`, ambient bundle, node `/generate/date-context`) is the client-reported one unless the setting is set, then the setting wins (a Pi on UTC, or mobile chat's America/New_York default, no longer gives wrong local times); unset = exactly as before. Routines keep their per-schedule zone. Writes validated by a new `settings.Definition.Validate` hook (IANA, never `Local`). New read `GET /api/v0/mobile/household/{hh}/timezone` → `{household_id, timezone, source: setting|node|default, node_timezone}` (doc cc/13 §3.7).
