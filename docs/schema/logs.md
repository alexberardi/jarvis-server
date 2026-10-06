# logs schema baseline

Migration: `internal/modules/logs/migrations/00001_baseline.sql`. Source: jarvis-logs.

## Source and head

**Live dev DB** (MacBook Pro, `jarvis_logs`) at `002`, equal to the repo head.

## Tables (0)

The only legacy table was `settings`, which the platform settings package owns. Log entries lived
in Loki, not Postgres. The baseline is an intentional no-op (`SELECT 1`) so the module's goose
version table exists and the first real migration is `00002`.

The jarvisd log table with retention and the SSE tail (PLAN §3.2) is new design and lands with the
logs module in Phase 1. It needs no legacy import: Loki history is not migrated.

## Dropped

`alembic_version`; `settings` (platform-owned).

## Setting keys

From `jarvis-logs/app/services/settings_definitions.py` (input for the module's settings `Definitions`; not implemented yet). `(reload)` = `requires_reload`.

| key | type | default | env fallback | description |
|---|---|---|---|---|
| `logs.retention_days` | int | `30` | LOG_RETENTION_DAYS | Number of days to retain logs |
| `logs.max_batch_size` | int | `1000` | LOG_MAX_BATCH_SIZE | Maximum batch size for log ingestion |
| `logs.query_limit` | int | `1000` | LOG_QUERY_LIMIT | Maximum number of logs returned per query |
| `server.port` | int | `7702` | LOG_SERVER_PORT | API server port (reload) |
| `auth.cache_ttl_seconds` | int | `60` | CACHE_TTL_SECONDS | Node auth validation cache TTL in seconds |

- `server.port` is moot in jarvisd (legacy ports are fixed, PLAN §3.1).

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
