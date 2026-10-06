# auth schema baseline

Migration: `internal/modules/auth/migrations/00001_baseline.sql`. Source: jarvis-auth.

## Source and head

- **Live dev DB** (MacBook Pro, `jarvis_auth`) at `a1b2c3d4e5f6` (`add_signing_keys`).
- Git `main` head is `c5d6e7f8a9b0`, and so is `feat/rs256-minting`. `a1b2c3d4e5f6` exists only as
  **uncommitted WIP on the MBP checkout** (`~/jarvis/jarvis-auth`). It revises `c5d6e7f8a9b0` and
  adds one table, `signing_keys`, which persists the RS256 keypair in the DB rather than in
  `AUTH_PRIVATE_KEY`. Everything else matches `c5d6e7f8a9b0` exactly.
- **Judgement call:** `auth_signing_keys` is kept. A DB-held keypair suits jarvisd (one data dir,
  no env file to keep in sync). If Go auth settles on `AUTH_PRIVATE_KEY`-only instead (umbrella
  CLAUDE.md, D5), drop the table in a later migration. Prod has no such table, so there is
  nothing to import for it either way.

## Tables (9)

| SQLite table | Legacy table | Notes |
|---|---|---|
| `auth_users` | `users` | `INTEGER PRIMARY KEY AUTOINCREMENT` (see below) |
| `auth_refresh_tokens` | `refresh_tokens` | self-FK `parent_id` ON DELETE SET NULL; `family_id` UUID text |
| `auth_app_clients` | `app_clients` | |
| `auth_households` | `households` | `id` UUID text, app-generated |
| `auth_household_memberships` | `household_memberships` | `role` CHECK, UNIQUE(household_id, user_id) |
| `auth_household_invites` | `household_invites` | `default_role` CHECK |
| `auth_node_registrations` | `node_registrations` | UNIQUE `node_id` (FK target) |
| `auth_node_service_access` | `node_service_access` | FK `node_id` → `auth_node_registrations(node_id)` CASCADE |
| `auth_signing_keys` | `signing_keys` | WIP table, see above |

All foreign keys and their ON DELETE actions are kept (CASCADE from users/households,
SET NULL for `created_by_user_id`, `registered_by_user_id`, `granted_by`, `parent_id`).

## Type notes

- Every timestamp column was **`timestamptz`**. Stored as ISO-8601 UTC text
  (`strftime('%Y-%m-%dT%H:%M:%fZ','now')` default, replacing `now()`).
- `uuid` (`households.id`, `*.household_id`, `refresh_tokens.family_id`) → TEXT. Legacy had no
  DB default (the ORM called `uuid4()`); the Go app generates them.
- booleans → INTEGER 0/1 with the same defaults.
- `role` / `default_role` were SQLAlchemy non-native enums (`varchar(20)`, no CHECK). Now
  `TEXT CHECK (… IN ('member','power_user','admin'))`, default `'member'`.
- `auth_users` keeps **AUTOINCREMENT**: user ids are referenced without FKs by cc,
  notifications (`user_id`) and recipes (`user_id` as text), so a deleted user's id must never be
  handed to a new user. Other ids are plain `INTEGER PRIMARY KEY`.

## Dropped

- `alembic_version`; `settings` (platform-owned, see below).
- Redundant `ix_<table>_id` indexes on primary keys, and single-column indexes made redundant by
  a composite UNIQUE whose first column is the same (`ix_household_memberships_household_id`,
  `ix_node_service_access_node_id`).
- Added (not in legacy): indexes on the SET NULL / self-referencing FK columns
  (`parent_id`, `created_by_user_id`, `granted_by`) so SQLite's FK actions don't scan.
- The `pgcrypto` extension (unused by any column default).

## Legacy-import notes

- **Enum values:** Postgres stores the enum *names*: `MEMBER`, `POWER_USER`, `ADMIN` (verified on
  the dev DB). The legacy server default `'member'` was never used because the ORM always set the
  column. Import must `lower()` `role` and `default_role`; the CHECK rejects the upper-case names.
- Preserve ids on import (`users.id` especially: other modules reference it). Inserting explicit
  ids into an AUTOINCREMENT table advances `sqlite_sequence` automatically.
- Insert order: users → households → memberships/invites → node_registrations →
  node_service_access → refresh_tokens (parents before children: `ORDER BY id` works for the
  `parent_id` chain because parents are always older).
- Timestamps: convert timestamptz to UTC and format ISO-8601 with `Z`.
- Password hashes (`password_hash`, `key_hash`, `node_key_hash`, `token_hash`) are copied verbatim;
  the Go side must verify them as-is: `password_hash`, `key_hash` and `node_key_hash` are passlib
  bcrypt (`$2b$…`), `token_hash` is hex SHA-256.
- Settings: the dev DB has `auth.algorithm`, `auth.token.access_expire_minutes`,
  `auth.token.refresh_expire_days`; `auth.token.refresh_grace_seconds` was never seeded.

## Setting keys

From `jarvis-auth/jarvis_auth/app/services/settings_service.py` (input for the module's settings `Definitions`; not implemented yet). `(reload)` = `requires_reload`.

| key | type | default | env fallback | description |
|---|---|---|---|---|
| `auth.token.access_expire_minutes` | int | `30` | ACCESS_TOKEN_EXPIRE_MINUTES | Access token expiration time in minutes |
| `auth.token.refresh_expire_days` | int | `14` | REFRESH_TOKEN_EXPIRE_DAYS | Refresh token expiration time in days |
| `auth.token.refresh_grace_seconds` | int | `10` | REFRESH_TOKEN_GRACE_SECONDS | Grace window (s) in which a benign double-submit of a just-rotated refresh token re-gets the cached successor instead of being rejected. |
| `auth.algorithm` | string | `'HS256'` | AUTH_ALGORITHM | JWT signing algorithm |

- No cuts. `auth.token.refresh_grace_seconds` is unseeded on dev (definition default applies).

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
