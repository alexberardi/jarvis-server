# Legacy import: carrying prod's nodes, households and users over (2026-10-09)

**Status: decided 2026-10-09 (ID6r, scope (c): everything in §1, §4 B and C); built 2026-10-09 as
`jarvisd import-legacy` (§7), CLI only. The admin-wizard screen is not built; its constraints are §8.** Revisits ID6 (clean start). Trigger: with a clean start every
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
- ~~Pattern to follow~~: built (§7). It reads the legacy Postgres directly instead of an export bundle
  (user, 2026-10-09: the end goal is a one-time migration screen pointed at the old compose folder).

## 7. `jarvisd import-legacy` (built 2026-10-09)

```sh
jarvisd import-legacy --compose ~/.jarvis/compose [--home DIR]            # dry run
jarvisd import-legacy --compose ~/.jarvis/compose [--home DIR] --apply    # import
jarvisd import-legacy --from postgres://USER@HOST:PORT ...                # password from PGPASSWORD
```

Other flags: `--pg-addr HOST:PORT` (reach the Postgres that `--compose` found at another address: a
container IP, an ssh tunnel), `--db KEY=NAME` (a renamed database), `--accept-head DB=HEAD`, `--log FILE`,
`--databases-env FILE` (a source checkout's `DB_NAME_*` file, default `~/.jarvis/databases.env`).

Code: `internal/legacyimport` (the core, behind a `Source` interface) and `cmd/jarvisd/import_legacy.go`
(the CLI). The export-bundle script the first brief asked for was not built: the command reads Postgres
directly.

**Reading.** `PGSource` (pgx, pure Go) connects to each legacy database with
`default_transaction_read_only=on` and checks it with `SHOW` after connecting. Every query runs in a
READ ONLY transaction that is rolled back, and it only SELECTs the columns the import needs (`json_agg`
of a sub-select). It never reads app clients, refresh tokens, `auth_sessions`, push tokens or invites.

A database that doesn't exist counts as "absent". The settings-only databases are optional; auth and cc
are required. Alembic heads are pinned for the three databases whose tables it maps: auth
`c5d6e7f8a9b0`, cc `sb01signals`, notifications `002`. Any other head is refused unless you pass
`--accept-head`. The settings-only databases (config, llm-proxy, tts, whisper, ocr, recipes, logs) are
read at any head, because the `settings` table never changed shape. `DirSource` reads JSON fixtures, for
tests.

**Compose discovery** (`DiscoverCompose`; it never prints the password):

| Layout | Files | Postgres from the host |
|---|---|---|
| installer (jarvis-admin, prod: `~/.jarvis/compose`) | `.env`: `POSTGRES_PASSWORD`, `DB_USER` (the role every service's `DATABASE_URL` uses; else `POSTGRES_USER`, else `jarvis`), `POSTGRES_PORT` | published on `${JARVIS_INFRA_BIND_HOST:-127.0.0.1}:${POSTGRES_PORT:-5432}`; `0.0.0.0` means loopback |
| source checkout (`./jarvis`) | `jarvis-data-services/.env` (`POSTGRES_USER/PASSWORD/PORT`); database names in `~/.jarvis/databases.env` (`DB_NAME_*`) | `127.0.0.1:${POSTGRES_PORT}` (published on every interface) |
| compose export (one `docker-compose.yml`, values inline) | not parsed | refused with "pass `--from`" |

If Postgres can't be reached, the error names the address and asks whether the legacy postgres container
is running. A container that publishes no port can only be reached on its bridge IP, which needs
`--pg-addr`; jarvisd does not call Docker.

**Target.** It refuses in three cases:

- the jarvisd service is running on that home;
- anything answers on the home's config port;
- the database is not fresh. Every table it writes must be empty: auth users, households, memberships,
  node registrations and grants, the cc tables, and the inbox.

A jarvisd that has already started once is fine: it only made its signing key, app client and setup
token.

A dry run copies the home's database (`VACUUM INTO`, read-only), or starts from an empty one in a temp
dir. It migrates the copy and imports into it, so **nothing in the home is written, not even
migrations**. `--apply` migrates the home's database (every module, plus the settings tables) and
imports into it.

**One transaction.** Every row is inserted under a savepoint. When jarvisd's CHECK, NOT NULL, UNIQUE or
FK constraints reject a row, it is reported (table, legacy id, SQLite's constraint message) and the rest
are still checked. A rejected row, a refusal or a dry run rolls the whole transaction back.

The report gives, per table, counts of rows read, imported, skipped (by reason) and failed. It also
lists fix-ups, the imported superusers (email masked), inactive nodes and notes. With `--apply`, a
per-row log goes to `<home>/import-legacy-<time>.log`. That log names users by email, so it is 0600.

**What it does, per table** (beyond §4). It follows the "Legacy import" sections of the schema docs.

- **Types.** Timestamps become ISO-8601 UTC with `Z`, at each module's own precision: microseconds for
  auth and notifications, milliseconds for cc and settings. Naive legacy timestamps are UTC. Booleans
  become 0/1, and NULL becomes the column's default. Ids are kept. The AUTOINCREMENT sequences
  (`auth_users`, `cc_user_memories`, `cc_signals`) advance by themselves.
- **Missing parents.** A row whose household, user or node wasn't imported is skipped ("household not
  imported"). Where the row still makes sense alone, the reference is set to NULL instead (a call's
  `user_id`, a device's room).
- **Auth and nodes.**
  - Roles are lower-cased.
  - Missing `jarvis-command-center` and `jarvis-logs` node grants are backfilled and reported.
  - Inactive registrations stay inactive and are listed.
  - A cc node without an auth registration is skipped, because it could never log in.
  - A registration without a cc row is noted. Command-center answers it 401 "Node not configured
    locally", as legacy did.
  - `cc_nodes.household_id` always comes from the registration, and `is_busy` is cleared.
- **cc data.**
  - Rooms: parents go in first, and a cycle is broken.
  - Memories: active and unexpired only. The embedding is left NULL for the sweep to fill.
  - Schedules: `active` only; `title` is dropped.
  - Contacts: `web` becomes `manual`; `overlay_json` is dropped.
  - Calls: only `done`, `failed`, `declined` and `expired`. `constraints` is dropped and `in_call_at`
    is NULL.
  - Signals: unexpired only. Suppressions are copied byte-for-byte.
  - Inbox: `adapter_proposal` items are dropped.
- **Settings** go into `<module>_settings` for config, auth, cc, tts, whisper (as stt), ocr, recipes and
  logs:
  - Only keys jarvisd defines are imported (cut keys are skipped), checked against the definition's type
    and validator. `llm.interface` becomes `llm.prompt_provider`, which must name a known provider.
  - **A system-scope row is imported only if someone changed it.** Legacy services seed every definition
    at start (`updated_at` NULL or equal to `created_at`), so importing those rows would pin legacy
    defaults over jarvisd's own. Household, node and user rows always carry over (e.g.
    `voice.recognition_enabled`).
  - Rows are deduped per (key, scope) and the newest wins. A key jarvisd has already set wins over the
    legacy value.
  - `auth.algorithm` is never imported. Legacy seeded HS256, but jarvisd mints RS256, and HS256 needs
    the legacy shared secret.
  - The llm-proxy's settings are not imported at all. Legacy model and engine settings don't apply to
    jarvisd's engines, and the wizard chooses the models.
- **Setup.** After an `--apply` that imported a superuser, `<home>/setup-token` is deleted if it exists.
  jarvisd would remove it at its next start anyway, but `jarvisd setup-link` reads the file before that
  and would print a dead link. `setuptoken.go` already behaves correctly for this case: with a superuser
  it writes no token, and `POST /auth/setup` returns 409.

**Different from `docs/schema/cc.md`'s import table, on purpose (ID6r scope):** `config_pushes`,
`person_characterizations` and attention events/deliveries are not imported, and neither are transcripts.
Memories are not re-embedded at import time; the embedding sweep fills them in. No mismatches were found
between the schema docs and the migrations for the imported columns.

**The wizard after an import** (checked in `web/admin`):

1. `needs_superuser` is false, so the boot gate doesn't send anyone to `/setup`. An anonymous visit to
   `/setup` forwards to `/login`.
2. The user signs in with a legacy admin account and its old password. The login page then forwards to
   `/setup`.
3. `/setup` asks the server for `setup_step`. `setup.completed` is false, so the wizard resumes at
   **Hardware** and runs through Models and Privacy to Done. The dashboard's "Setup isn't finished"
   banner links there too.

Nothing in the SPA needed fixing. A legacy superuser with `must_change_password` goes through
`/change-password` first. `jarvisd setup-link` used to print "jarvisd is set up" in this state. It now
tells you to sign in and finish setup whenever a superuser exists but `setup.completed` isn't true.

**Tests.**

- `internal/legacyimport/import_test.go` uses a fixture with every table and every fix-up. It checks that
  a dry run writes nothing, the refusal of a non-fresh database, the head refusal and `--accept-head`,
  and that one bad row or timestamp rolls everything back.
- `compose_test.go` covers both layouts, the compose-export refusal, URL parsing, and an unreachable
  Postgres without leaking the password. `TestPostgresSource` runs against a real legacy Postgres when
  `JARVIS_TEST_LEGACY_PG` is set.
- `cmd/jarvisd/import_legacy_test.go` runs the command, then serves the imported database with the real
  auth and cc modules:
  - the fixture node's legacy `node_id:node_key` (passlib `$2b$12$`) passes cc's `authNode` (heartbeat
    200) and the MQTT broker (CONNACK 0);
  - a wrong key and an inactive node are refused;
  - a legacy password logs in;
  - a member's `GET /api/v0/admin/nodes?household_id=` lists the node.

**Dev data (this box's `jarvis-postgres`, 2026-10-09).** `--compose /home/alex/jarvis` found the source
layout. Every row of the identity graph and the user data imported with no rejections. The only skips
were settings: seeded defaults, cut keys, `auth.algorithm` and the llm-proxy table. A jarvisd started on
the imported scratch home came up with no errors, with setup closed and the backfilled grants in place.

**Prod discovery (read-only, 2026-10-09).** `~/.jarvis/compose/.env` uses the installer layout:
`POSTGRES_PASSWORD`, `DB_USER` and `POSTGRES_PORT`, with no `DB_NAME_*` overrides. `jarvis-postgres`
publishes `127.0.0.1:5432`. Nothing has connected to it yet (heads, dry run). That needs an ssh tunnel
to prod's loopback, then `--compose <copy> --pg-addr 127.0.0.1:<tunnel port>`.

## 8. The wizard screen (not built): constraints and a recommendation

The goal (user, 2026-10-09) is a one-time screen in the setup wizard: the user points at the old
`~/.jarvis/compose` folder and jarvisd copies the data over. On Linux, four things stand in the way:

- jarvisd as a system service runs as user `jarvisd` with `ProtectSystem=strict`, `ProtectHome=yes` and
  `ReadWritePaths=<home>`. **It cannot see `/home/<user>/.jarvis/compose` at all**, whatever the file
  modes.
- It is not in the `docker` group, so it can't `docker exec`, `docker inspect` (for a container IP) or
  `docker start`. It can reach a Postgres published on `127.0.0.1:5432` over TCP, because the unit has
  no network sandboxing.
- `install.sh --stop-legacy` stops `jarvis-postgres` along with everything else, so at wizard time there
  is nothing to connect to.
- The import must land before the wizard's Account step, because an account makes the database not
  fresh. It would also run while the modules are running. Importing inside the live process is safe on a
  fresh database, since nothing is scheduled without a household, but auth's in-memory setup token must
  be dropped afterwards.

Options:

1. **The installer hands over a connection file (recommended).**
   - When `install.sh --stop-legacy` finds a legacy compose directory, it runs discovery as the invoking
     user (a `--print-connection` mode, not built yet).
   - It writes the result to `<home>/legacy-import.json` (0600, owner jarvisd): host, port, user,
     password and database names.
   - It stops every legacy container **except `jarvis-postgres`**.
   - The wizard's first screen offers "Bring over your old Jarvis" when that file exists and the database
     is fresh, and shows the dry-run report.
   - On confirm it imports in process, deletes the file, and gives the one command that stops the old
     Postgres (`docker stop jarvis-postgres`).

   This is small and needs no new privilege. It works the same on macOS (the LaunchDaemon user) and
   Linux, and the secret only ever moves from one 0600 file to another.
2. **Keep the CLI only.** The runbook (§4.3a) stops jarvisd, starts `jarvis-postgres`, copies `.env`
   into the home, runs the import as `jarvisd`, then starts jarvisd. This works today, but it isn't
   something to put in front of non-technical users.
3. **A privileged helper**, like the update helper, that reads the compose dir and starts and stops the
   legacy Postgres for the wizard. This is the most capable option (it could also start a stopped
   Postgres), but it adds a root code path for a one-time job.

Recommendation: (1), with (2) as the documented fallback. If Postgres is stopped, the screen tells the
user to run `docker start jarvis-postgres`; a helper doesn't do it.
