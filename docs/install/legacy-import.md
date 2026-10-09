# Legacy import: carrying prod's nodes, households and users over (draft, 2026-10-09)

**Status: decided 2026-10-09 (ID6r, scope (c): everything in §1, §4 B and C); building.** Revisits ID6 (clean start). Trigger: with a clean start every
legacy node is orphaned. It thinks it is provisioned, jarvisd rejects its key, it never reaches the
broker, so it is invisible in the app and can't be updated or reset remotely; only SSH or a reflash
recovers it (node-setup `provisioning/startup.py`: AP mode only when the `.provisioned` marker is
missing, since v0.1.138). User requirement (2026-10-09): broken features are acceptable; a node that
needs SSH to recover is not.

A node can't come over alone: it needs its household, the household needs its members, so the
import is the identity graph plus whatever hangs off it.

## 1. What a node needs (the minimum)

| Row | Where | Why |
|---|---|---|
| user, household, membership | `auth_users`, `auth_households`, `auth_household_memberships` | The node's household; app access to it |
| node registration + service grants | `auth_node_registrations`, `auth_node_service_access` | HTTP and MQTT login (`jarvis-command-center` grant; also `jarvis-logs`) |
| node row | `cc_nodes` | `authNode` returns 401 "Node not configured locally" without it (`cc/authz.go:38`); the app's node list reads it |

Nothing else is required: settings, routines and devices fall back to defaults when missing.

## 2. Compatibility (verified in source)

- **Schemas are 1:1.** jarvisd's auth tables are the legacy ones with an `auth_` prefix and no new
  NOT NULL columns (`internal/modules/auth/migrations/00001_baseline.sql` vs
  `jarvis-auth/jarvis_auth/app/db/models.py`). cc, notifications and settings map per
  `docs/schema/{cc,notifications,config}.md` (their "Legacy import" sections).
- **ID types match.** Users are integers, households UUID text, nodes and rooms text. If user and
  household ids are kept, no row anywhere needs remapping.
- **Node keys verify unchanged.** Legacy bcrypt `$2b$12$` over a 64-char key; jarvisd verifies with
  Go bcrypt + 72-byte truncation (`auth/util.go:130`). The same key is the node's MQTT password
  (`cc/authz.go:270-292`); there is no MQTT credential table to move.
- **User passwords verify unchanged.** passlib bcrypt, same cost, same 72-byte truncation
  (`auth/util.go:110-119`). Users keep their logins; nobody re-registers.
- **Fix-ups:** membership and invite roles to lower case (CHECK constraint); `cc_nodes.household_id`
  filled from the auth registration (legacy allows NULL; a NULL-household node is hidden from the app).

## 3. Old node versions with their records imported

| Band | Logs in | In the app | Update from app | Reset from app | Extra needed |
|---|---|---|---|---|---|
| 0.1.0–0.1.3 | yes | yes | no | no | Reflash by hand |
| 0.1.4–0.1.130 | yes | yes | yes | no (MQTT login rejected) | Update to 0.3.4 first; that fixes MQTT |
| 0.1.131–0.1.149 | yes | yes | only with `allow_updates` on | 0.1.135+ with empty saved creds only | `allow_updates` via app config push, then update |
| 0.2.0 demo container | yes | yes | no (Docker ignores updates) | yes | Redeploy the container image |
| 0.3.1 (kitchen, living room) | yes | yes | only with `allow_updates` on | yes | none; jokes/briefings broken until 0.3.4 |

Updates come back in the heartbeat response, not over MQTT, so even nodes that can't reach the
broker can be updated. `allow_updates` (since v0.1.131) defaults to off, and the app can turn it on
with a K2 config push. Prod's four 0.1.x nodes (exact versions unknown) were last seen Jun–Aug.

## 4. The rest of the data, by category

- **B: the node works as before.** `cc_rooms` (before nodes and devices), `cc_devices`,
  `cc_settings` (rename `llm.interface` → `llm.prompt_provider`, drop cut keys, dedupe system scope),
  household `voice.recognition_enabled`.
- **C: user data.** `cc_user_memories` (active, unexpired; embeddings refilled by the 60 s sweep),
  `cc_routines`, active `cc_schedules`, `cc_phone_contacts` (`web` → `manual`), unexpired
  `cc_signals` + `cc_proposal_suppressions` (byte-exact keys), `notifications_inbox_items` (drop
  `adapter_proposal`), terminal `cc_phone_call_sessions`. Recipes: `jarvisd import-recipes` already
  exists; with ids kept, its email remap becomes an identity mapping.
- **D: drop.** Refresh tokens and push device tokens (the app re-registers on launch), app clients,
  invites, transcripts/attention journal/traces (TTL'd), request/response tables, `auth_sessions`
  (needs the legacy encryption key), node tasks, cut tables. Voice profiles: re-enroll (or
  re-embed from the WAVs later).

## 5. Ordering

Import into a **fresh database before jarvisd's first start and before the setup wizard**:

- The wizard's `POST /auth/setup` creates a superuser and a "My Home" household; afterwards an
  imported owner collides on id and email. An imported superuser instead closes setup
  (409, and the setup token file is removed).
- Schedules arm and default routines seed at startup (`cc/routines.go:108`, `:819`); the import
  must land first.
- jarvisd creates its own `jarvisd` app client; the import must not bring one with that id.

## 6. Open items

- **Never inspected on prod:** actual values against jarvisd's CHECK constraints. Dry-run against
  the T-1 dump (`pg_dumpall`, runbook §3.3) before T-0.
- Prod's auth head is `c5d6e7f8a9b0` (auth.md); confirm at T-1.
- Inactive nodes (`is_active=0`) import inactive and can't log in; decide whether to activate them.
- The four 0.1.x nodes' exact versions decide which row of §3 they land in.
- Pattern to follow: `cmd/jarvisd/import_recipes.go` + `internal/modules/recipes/legacy_import.go`
  (dry run unless `--apply`, mapping log, masked emails) and `scripts/legacy/recipes-export.sh`.
