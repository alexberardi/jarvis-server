# config schema baseline

Migration: `internal/modules/config/migrations/00001_baseline.sql`. Source: jarvis-config-service.

## Source and head

**Live dev DB** (MacBook Pro, `jarvis_config`) at `006`, equal to the repo head (`006`, on branch
`fix/container-startup-and-remote-discovery` locally; `main` has the same chain).

## Tables (1)

| SQLite table | Legacy table | Notes |
|---|---|---|
| `config_services` | `services` | the discovery registry, UNIQUE `name` |

**Judgement call — kept.** PLAN §3.1 makes discovery of jarvisd's own modules trivial (one host,
legacy ports), but the registry is still needed for things outside the process: services jarvisd
doesn't run (`jarvis-web`, `jarvis-osx-api`, `jarvis-node-setup`, `jarvis-mqtt-broker` are all
rows on dev), Python services reached over HTTP during the strangler phase (PLAN §5), and remote
GPU satellites (PLAN Phase 6). Phase 1 decides whether jarvisd synthesises its own modules' rows
and keeps this table for the rest.

(The `service_configs` table mentioned in planning is **not** config-service's: it is
jarvis-config-client's local discovery cache, found in llm-proxy's DB. Dropped there — see llm.md.)

## Type notes

- `created_at`, `updated_at` were naive `timestamp` with `DEFAULT now()`; the Postgres server
  timezone is `Etc/UTC`, so they hold UTC. Stored as ISO-8601 UTC text.
- `updated_at` had an ORM `onupdate`; there is no trigger, so the app sets it on UPDATE.

## Dropped

`alembic_version`; `settings` (platform-owned); redundant `ix_services_id`.

## Legacy-import notes

- Copy rows 1:1. Consider skipping rows for services that no longer exist
  (`jarvis-settings-server`, `jarvis-mcp`) and rows for modules that jarvisd now serves itself.
- `external_host`/`external_port` (migration 005/006) are kept as-is.

## Setting keys

From `jarvis-config-service/app/services/settings_service.py` (input for the module's settings `Definitions`; not implemented yet). `(reload)` = `requires_reload`.

| key | type | default | env fallback | description |
|---|---|---|---|---|
| `health_check.timeout` | float | `5.0` | HEALTH_CHECK_TIMEOUT | Timeout in seconds for health check requests |
| `health_check.enabled` | bool | `True` | HEALTH_CHECK_ENABLED | Whether health checks are enabled |


## Settings import (all modules with a settings table)

The legacy `settings` table is **not** in this baseline: `internal/platform/settings` creates
`<module>_settings` with the same columns (`key, value, value_type, category, description,
requires_reload, is_secret, env_fallback, household_id, node_id, user_id, created_at,
updated_at`). Import copies rows 1:1, with these transforms:

- booleans → 0/1; timestamps → ISO-8601 UTC text.
- The SQLite table has `UNIQUE(key, COALESCE(household_id,''), COALESCE(node_id,''), COALESCE(user_id,0))`.
  Postgres' `uq_setting_scope` let duplicate system-scope rows through (NULLs never collide), so
  dedupe on import, keeping the most recently updated row.
- `value` is already JSON/text-encoded by the settings client; copy it verbatim.
- Rows whose key is no longer defined (cut keys, listed above) are skipped.
- Keys defined but never seeded are fine: the definition default applies.
