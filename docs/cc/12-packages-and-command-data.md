# 12 — Packages and command data

Source: `jarvis-command-center/app` (paths below are relative to it unless prefixed). Node-side references are to `jarvis-node-setup`, mobile to `jarvis-node-mobile/src`, SDK to `jarvis-command-sdk/jarvis_command_sdk`.

Out of scope here: `/prompt-providers/*` (doc 03, being cut); the generic node `commands` MQTT topic and liveness (doc 05); how mobile chat consumes the node tool list (doc 02 / doc 13).

---

## 0. Decisions applied (2026-10-06)

- **D39:** (Q1) slow installs get a 5-minute **pickup** deadline until the node verifies, then `expires_at = verify + 15 min`, keeping the +120 s restart extension; server-only, node unchanged. (Q8) a setting `pantry.base_url`, defaulting to the public Pantry URL, so a household can point at a private Pantry; it must be reachable from the node.
- **D5:** ~~Forge test install is dropped from Go~~. **Reversed 2026-10-08 (user): Forge test install is ported** (§11 "Test install"): the four `test-install` routes, `cc_test_install_requests` (migration 00160) and the `test-install` MQTT nudge, with the same deliberate fixes as package requests (D39 deadlines, D8 sticky, D4 node binding, household check on the poll). Install, uninstall and revert are allowed for **any household member, from any URL** (no allowlist; private Pantry instances are coming). Household checks look at the caller's memberships, not just the JWT's active household.
- **D4:** package verify/results are node callbacks: node auth, with the authenticated node bound to `{node_id}` in the path. `node-tool-reports` gets node auth and the request id must belong to that node. `trusted:true` is removed; per-node broker credentials and ACLs make broker-level spoofing (including the §8 op-confusion replay) impossible.
- **D8:** known bugs fixed: sticky terminal status (Q6), idempotent verify, unguarded node responses.
- **D27:** the 30-day request-row sweeper is a trigger kind on the one scheduler engine.
- **D40 defaults:** Q5 nodes with no household fail closed; Q7 request tables not imported, 30-day sweeper; Q9 keep 200-empty on node-tools timeout and log it; Q10 invalidate the schema cache in-process; Q11 reproduce the substring error mapping exactly; Q12 keep sending `github_repo_url`/`git_tag`.

---

## 1. Purpose

Three related mobile features. In each one, **CC is a broker and record-keeper, never the executor**. The node does the work, and CC keeps the request state or relays the round-trip.

| Feature | User story | Who calls CC |
|---|---|---|
| **Package install / uninstall / revert** | From the Store screen in the mobile app, install a Pantry package (command, agent, device protocol, bundle) on one or more nodes. Also remove it, or roll it back to the previous version. | Mobile (JWT) creates and polls; the node (X-API-Key) verifies and posts results. |
| **Test install (Forge)** | A developer in Pantry's AI Forge gets a 6-character share code. They type it into mobile, and the draft is installed on a node as a temporary test command (20-minute node-side TTL). **Ported** (D5 reversed 2026-10-08). | Same split; CC also calls Pantry once to validate the code. |
| **Command-data browser** | Browse, create, edit and delete the records a command stored on a node via `JarvisStorage` (reminders, shopping/todo lists, medications, …) without SSH. | Mobile (JWT); CC does a synchronous MQTT round-trip to the node. |
| **Node tools view** | Mobile asks which tools and commands a node exposes, which packages are installed (with versions, `previous_version` and health), so the Store can show Install/Update/Revert. | Mobile (JWT); the node posts back via an HTTP callback. |

CC **stores**: install/test-install request rows (status, result JSON), plus two small in-memory caches (schema, user display names).

CC **never stores**: package code, manifests, installed-package lists, or any command-data records. All of that lives on the node:

- `~/.jarvis/packages/<name>/` with `.previous/` snapshots
- `commands/test_commands/<pkg>/`
- the node's SQLite/SQLCipher `command_data` table

---

## 2. Entry points

All routes are in Appendix A's live set. The prefixes are mounted at `main.py:743-748` (`/api/v0`), `main.py:819` and `main.py:833` (`/api/v0/mobile`).

### 2.1 Package install / uninstall / revert (`api/package_install.py`)

| Method + path | Auth | Caller | Line |
|---|---|---|---|
| `POST /api/v0/nodes/{node_id}/package-install` → 201 | `verify_provisioning_auth` (admin `X-API-Key` == `ADMIN_API_KEY`, or JWT via `/auth/me`) + household member | mobile `packageInstallApi.ts:18` | 97 |
| `GET  …/package-install/{request_id}/verify` | node `verify_api_key` | node `package_install_handler.py:64` (**also used for uninstall and revert**) | 148 |
| `POST …/package-install/{request_id}/results` | node | node `package_install_handler.py:651`, `:254` | 189 |
| `GET  …/package-install/{request_id}` | provisioning + household | mobile `InstallProgressScreen.tsx:74` | 242 |
| `POST /api/v0/nodes/{node_id}/package-uninstall` → 201 | provisioning + household | mobile `NodeSettingsScreen.tsx:587/639/689` | 319 |
| `POST …/package-uninstall/{request_id}/results` | node | node (action=`uninstall`) | 368 |
| `GET  …/package-uninstall/{request_id}` | provisioning + household | mobile | 417 |
| `POST /api/v0/nodes/{node_id}/package-revert` → 201 | provisioning + household | mobile `StoreDetailScreen.tsx:226` | 496 |
| `POST …/package-revert/{request_id}/results` | node | node (action=`revert`) | 549 |
| `GET  …/package-revert/{request_id}` | provisioning + household | mobile | 598 |

There is no `/package-uninstall/{id}/verify` or `/package-revert/{id}/verify`. All three operations verify through the `package-install` verify route, because all three share the `package_install_requests` table (`package_install_handler.py:46-70`).

### 2.2 Test install (`api/test_install.py`)

> **Ported** (D5's drop reversed 2026-10-08): `internal/modules/cc/testinstall.go`. Go adds the household check to the poll (§8) and binds verify/results to the authenticated node (D4).

| Method + path | Auth | Line |
|---|---|---|
| `POST /api/v0/nodes/{node_id}/test-install` → 201, body `{share_code}` | provisioning + household | 73 |
| `GET  …/test-install/{request_id}/verify` | node | 143 |
| `POST …/test-install/{request_id}/results` | node | 186 |
| `GET  …/test-install/{request_id}` | provisioning (**no household check**; see §8) | 226 |

The callers are mobile `TestInstallScreen.tsx:67` and `InstallProgressScreen.tsx:73`, and node `test_install_handler.py:127,206`.

### 2.3 Command-data browser (`api/mobile_command_data.py`, all JWT via `verify_user_jwt`)

| Method + path (`/api/v0/mobile` prefix) | MQTT op | Line |
|---|---|---|
| `GET /command-data/nodes` | none (DB only) | 330 |
| `GET /command-data/nodes/{node_id}/commands` | `commands` | 360 |
| `GET /command-data/nodes/{node_id}/commands/{command_name}/schema` | `schema` (cached) | 372 |
| `GET …/{command_name}/records` | `list` (+ `schema` if not cached) | 401 |
| `GET …/{command_name}/records/{key}` | `get` | 456 |
| `POST …/{command_name}/records` body `{data:{}}` | `create` | 494 |
| `PATCH …/{command_name}/records/{key}` body `{patch:{}}` | `update` | 539 |
| `DELETE …/{command_name}/records/{key}` | `delete` | 570 |

The caller is mobile `api/commandDataApi.ts`, from the screens in `screens/CommandData/` (DataBrowserHome, RecordsList, RecordDetail, RecordEdit). All 8 routes are used.

### 2.4 Node tools (`api/node_tools.py`)

| Method + path | Auth | Caller | Line |
|---|---|---|---|
| `GET /api/v0/mobile/nodes/{node_id}/tools` | JWT + household member (only if the node has a household) | mobile `chatApi.ts:317` (`fetchNodeTools`, 15 s client timeout) | 28 |
| `POST /api/v0/mobile/node-tool-reports/{request_id}` | **none** (**D4:** node auth, request id bound to that node) | node `mqtt_tts_listener.py:536` | 105 |

### 2.5 MQTT topics (CC → node; the node subscribes to `jarvis/nodes/{node_id}/#`)

| Topic | Payload | Publisher |
|---|---|---|
| `jarvis/nodes/{id}/package-install` | `{request_id, command_name, github_repo_url, git_tag}` | `package_install.py:840-846` |
| `jarvis/nodes/{id}/package-uninstall` | `{request_id, command_name, component_type?}` | `:468-475` |
| `jarvis/nodes/{id}/package-revert` | `{request_id, command_name, package_name}` (the same value twice) | `:648-653` |
| `jarvis/nodes/{id}/test-install` | `{request_id}` only | `test_install.py:305-306` |
| `jarvis/nodes/{id}/commands` | `[{"command":"report_tools","details":{"reply_request_id":R,"trusted":true,"request_id":R}}]` | `node_tools.py:78-82` via `node_command_service.py:64-68` |
| `jarvis/nodes/{id}/command-data/{op}` | see §3.4 | `mobile_command_data.py:126` |
| node → CC: `jarvis/nodes/{id}/command-data/{op}/response/{correlation_id}` | see §3.4 | node `command_data_handler.py:58` |

All publishes use QoS 1 (`core/mqtt_client.py:154`, `:251-254`).

There are no background loops in this subsystem. There is no row cleanup and no expiry sweeper: expiry is evaluated lazily on read and write.

---

## 3. Behaviour

### 3.1 Install / uninstall / revert sequence (the verify handshake)

```
mobile ──POST /nodes/N/package-install {command_name, github_repo_url, git_tag}──▶ CC
   CC: node row must exist (404); require_household_access(node.household_id)  (:109-115)
   CC: INSERT package_install_requests(status=pending, expires_at=now+5min)        (:117-131)
   CC ──MQTT jarvis/nodes/N/package-install {request_id, …repo info}──▶ node      (:139)
   CC ◀── 201 {id, status:"pending", created_at}
node: on_message → _handle_package_install_notification uses ONLY request_id      (listener :1935-1959)
node ──GET /nodes/N/package-install/R/verify  (X-API-Key)──▶ CC
   CC: row (id=R AND node_id=N) or 404; expired → set expired, 410; status≠pending → 409   (:165-179)
   CC ◀── {confirmed:true, command_name, github_repo_url, git_tag}
node: install_from_github(url_from_CC, tag_from_CC)   (handler :100-102)
   failure → POST …/package-install/R/results {success:false, error}  → CC: failed      (:127-133)
   success → write ~/.jarvis/.pending_install_result.json (fsync+rename)               (:221-232)
           → POST …/results {success:true, restarting:true, details}  (best effort, 3s) (:235-261)
               CC: status=restarting, expires_at += 120s, results_json=details           (:73-89, :211-220)
           → os._exit(1); systemd respawns                                              (:218)
node boot, after discovery: flush_post_restart_install_result()                         (:274-338)
   install: checks that the components loaded; if failed and a .previous exists → auto-rollback,
            rewrite the marker (rollback_attempted=true), POST restarting again, exit again  (:341-412)
   POST …/results {success, error?, details}  → CC: completed | failed, completed_at=now  (:222-233)
mobile: polls GET …/package-install/R every 750 ms (InstallProgressScreen.tsx:32) or every 2 s × 60
        (revert/uninstall screens). It stops on completed|failed|expired (utils/packageStatus.ts:13).
```

Uninstall and revert work the same way. Differences:

- The node calls `run_uninstall_and_upload` (`:518`) or `run_revert_and_upload` (`:579`).
- Both verify through the **install** verify route and use only `command_name` from the response.
- Results go to `…/package-{action}/R/results` (`:651`).
- On the node, uninstall calls `command_store_service.remove(name, component_type)` (`:1940`). `component_type` comes from the untrusted nudge, and the node treats it as a "narrowing hint" (`handler :527`).
- Revert calls `revert_package(name)` (`:2080`), which restores `.previous` and deletes it (only N-1 is kept).
- Both restart on success, the same as install.

**Why the node calls back to verify.** The node treats an MQTT message as an *untrusted nudge*: anyone who can publish to the broker could forge one. Package code is executed as the node's user, so a forged `package-install` that carried a repo URL would be remote code execution.

The verify GET is authenticated with the node's own credential. It returns the authoritative package identity only for a row that CC created for **this** `node_id`, on behalf of an authenticated household member (`package_install.py:158-164`, listener `:1938-1942`). The node ignores the `github_repo_url` and `git_tag` in the MQTT payload, and CC still sends them (`:841-846`). A spoofed or stale `request_id` gets 404, 410 or 409, and the node drops it silently (`handler :66-68`).

The test-install payload carries *only* `request_id` (`test_install.py:292-306`), which is the cleaner form of the same idea.

Git refs: Pantry pins installs to the validated **commit SHA** (it passes it as `git_tag`), which defeats validate-then-repoint TOCTOU (`command_store_service.py:224-262`). CC passes `git_tag` through opaquely (`String(100)`).

### 3.2 Request status state machine (package_install_requests)

```
            POST (create)
                │
                ▼
   ┌────── pending ──────┐ results{restarting:true}      results{success}
   │           │         └──────────────▶ restarting ───────────────┐
   │ now>expires_at       │                 │  ▲  (repeatable: +120s  │
   │ (on verify/results/  │ results{success:true|false}   each time)   │
   │  poll)               ▼                 │                           ▼
   └──▶ expired      completed | failed ◀──┘ results{success:true|false}
```

- `expires_at = created_at + 5 min` (`:127`, `:348`, `:529`). There is no setting for this.

  > **Changed by D39:** 5 min is a pickup deadline; on verify, `expires_at = verify + 15 min`. The +120 s restart extension stays. **Changed by D8 (Q6):** terminal status is sticky.
- A `restarting` post extends `expires_at` by **120 s from the old expiry**, not from now (`:85-87`). It is accepted any number of times; the auto-rollback path relies on that (`handler :391-399`).
- Expiry is **lazy**:
  - verify and results: if `expires_at < now`, the row is set to `expired`, committed, and the call returns 410, *regardless of the current status*. A late result for a `restarting` row also expires (`:173-176`, `:206-209`).
  - poll: only `pending` or `restarting` past expiry flips to `expired`. The **GET mutates the row** (`:265-271`).
- verify requires `status == "pending"`, else 409 (`:178-179`). Verify does **not** change the status: the row stays `pending` until a result arrives, so verify can be repeated.
- `results` has **no status precondition**. A late post can overwrite a terminal state (completed→failed, or completed→restarting).
- `completed_at` is set on terminal results only (`:232`).
- Test-install requests use the same machine **without** `restarting` (`test_install.py:208-216`, `:247`).

### 3.3 Test install

> **Ported** (D5's drop reversed 2026-10-08). This is legacy behaviour; Go's differences are listed in §11 "Test install".

1. Mobile POSTs `{share_code}`. CC checks the node and household (`:85-89`), then normalises the code with `strip().upper()`. Length ≠ 6 → 400 `"Invalid share code"` (`:91-93`).
2. CC `GET {pantry}/v1/forge/drafts/{code}` with a 10 s timeout (`:96-99`). Errors map as follows:
   - exception, *including Pantry URL unresolved* → 502 `"Could not reach Pantry service"`
   - 404 → 404 `"Share code not found or expired"`
   - other non-200 → 502
3. The row stores `share_code` and `package_name` (`draft.package_name`, default `"unknown"`, `:110`), with expiry now+5 min. CC publishes the nudge `{request_id}` and returns 201 `{id, status, package_name, created_at}`.
4. The node verifies (`:143-183`). Same checks as package install, plus 502 if the Pantry URL can't be resolved. It gets back `{confirmed, package_name, pantry_download_url: "{pantry}/v1/forge/drafts/{code}"}`.

   Pantry's draft GET is **unauthenticated**: the share code is the only secret (`jarvis-pantry/app/api/forge_drafts.py:151-182`, 15-minute draft TTL `:26`).
5. The node downloads the draft JSON `{package_name, files:[{filename,content,language}]}` directly from Pantry. It writes the files to `commands/test_commands/<package_name>/` and runs `pip install -r requirements.txt`, diffing `pip freeze` before and after. It writes `.test_meta.json` with a 20-minute expiry, then calls `refresh_now()` on discovery **in-process, without a restart** (`test_install_handler.py:28-112`). It then posts the result.

   If verify fails, the node *posts a failure result* (`:131-133`), unlike package install, which stays silent.
6. Node-side cleanup runs every 20 minutes and removes expired test commands and the pip packages they added (`services/test_install_cleanup.py`).

### 3.4 Command-data browser round-trip (frozen wire contract)

Every route:

1. `_resolve_node_in_household`: node row must exist (404 `"Node {id} not found"`). **If** `node.household_id` is set, the user must be at least a `member` (`:168-182`).
2. `_mqtt_request(node, op, payload)` (`:108-162`):
   - `correlation_id = uuid4` (`setdefault`).
   - **Subscribe to** `jarvis/nodes/{id}/command-data/{op}/response/{cid}` **before publishing** to `jarvis/nodes/{id}/command-data/{op}`, then wait up to 10 s (`core/mqtt_client.py:210-258`).
   - No MQTT client → 503. Timeout → 504 `"Node {id} did not respond within 10.0s"`. Non-JSON response → 502.
   - On any answer, `record_node_seen(node_id)` is called (proof of life, best effort, `:146-151`).

**Request payloads** (CC → node). `requesting_user_id` is the JWT user id, never client-supplied:

| op | payload |
|---|---|
| commands | `{correlation_id}` |
| schema | `{correlation_id, command_name}` |
| list | `{correlation_id, command_name, requesting_user_id}` |
| get / delete | `{correlation_id, command_name, key, requesting_user_id}` |
| create | `{correlation_id, command_name, data:{…}, requesting_user_id}` |
| update | `{correlation_id, command_name, key, patch:{…}, requesting_user_id}` |

**Node responses** (`command_data_handler.py`):

| op | success | failure messages (`error.message`) |
|---|---|---|
| commands | `{commands:[{command_name, mode, storage_name}]}`. **There is no `ok` key.** Commands with mode `disabled` are omitted (`:112-134`). | none |
| schema | `{ok:true, mode, supports_create, fields:[FieldSpec]}` (`:161-166`) | `command_name required`, `command not found` |
| list | `{ok:true, records:[{key, summary:{title,subtitle,icon}, data:{…}}], truncated, count}`. The node caps the list at 200 *visible* rows; `count` = number returned (`:45`, `:197-229`). | `command not found` |
| get | `{ok:true, record:{…}, schema:{mode, supports_create, fields}}` (`:266-274`) | `command or key not found`, `record not found` |
| create | `{ok:true, record, key}` (`:413-417`) | `command or values missing`, `command is read-only`, `command does not support adding records`, or any `ValueError` text from `data_browser_create` |
| update | `{ok:true, record}` | `command, key, or patch missing`, `command is read-only`, `patch has no editable fields`, `record not found`, reminder-service errors |
| delete | `{ok:true}` | `command or key not found`, `command is read-only`, `record not found`, `delete failed` |
| any crash | `{ok:false, error:{message:"handler error: …"}}` (`:586-594`) | |

**CC → mobile mapping.** It is by **substring** on the node's message, and must be preserved exactly:

| route | 200 body | error mapping |
|---|---|---|
| commands | `{commands: response.commands ?? []}` | none (CC doesn't read `ok`) |
| schema | `{mode, supports_create (default false), fields}`, then cached | `!ok` → 404 with the message (default `"command not found"`) |
| records list | `{records (user_ref-enriched), truncated: bool, count: response.count ?? len}` | `!ok` → 404 |
| record get | `{record (enriched), schema}`. The schema defaults to `{mode:"enabled", fields:[]}` and is written to the cache. | `!ok` → 404 (default `"record not found"`) |
| create | `{record (enriched only if the schema is cached), key}` | first `"read-only"` → 403; then `"not found"`/`"missing"`/`"does not support"` → 404; else 400 (`:517-530`) |
| update | `{record}` (**not** enriched) | `"not found"`/`"no editable"` → 404; then `"read-only"` → 403; else 400 (`:558-565`) |
| delete | `{ok:true}` | `"read-only"` → 403; else 404 |

**user_ref enrichment** (`:193-312`). The cached schema's FieldSpecs are walked recursively for `type=="user_ref"`. For each integer value, CC batch-resolves display names via auth `GET /internal/users/batch?user_ids=1,2` (app-to-app headers, 5 s, 300 s per-process name cache). It writes `"{field}_display": "Alex"` **into the record's `data` dict next to the raw id**. Mobile reads `record[`${name}_display`]` (`RecordDetailScreen.tsx:132`, `RecordEditScreen.tsx:177`). Every failure degrades to "no `_display` key".

**Visibility and ownership are enforced node-side**, and CC is the only source of `requesting_user_id`:

- Rows with `user_id` are visible only to that user; rows without one are "legacy, visible to all" (`command_data_handler.py:87-91`).
- Updates are filtered to `editable` fields, and `data_key` is always dropped (`:314-318`).
- Create accepts `editable ∪ create_only` fields, and `data_browser_create` stamps the owner (SDK `command.py:572-607`). It fails closed when `requesting_user_id` is None.

### 3.5 FieldSpec and storage schema (SDK, frozen)

`FieldSpec.to_dict()` (`field_spec.py:68-94`) emits `name` and `type` always. The other keys are emitted **only when not default**: `label`, `description`, `editable:false`, `create_only:true`, `required:true`, `enum_values`, `item_type`, `fields` (nested, recursive) and `placeholder`.

- `type` is a free string. The vocabulary is `string text int float bool enum datetime date time duration array object id user_ref`, and unknown types render as text on mobile.
- Mobile's TS mirror is `commandDataApi.ts:20-34`.
- CC treats the schema opaquely, except for the `user_ref` walk. **The Go port must pass FieldSpec dicts through untouched** (no struct round-trip that would add zero-value keys).
- `mode` is `enabled | disabled | readonly` plus unknown values (forward-compatible). `storage_name` defaults to `command_name`; for example, the reminder command is `reminder` with storage `set_reminder`, which the node special-cases (`command_data_handler.py:491-528`).
- `RecordSummary` is `{title, subtitle|null, icon}`, with `icon` defaulting to `information-outline` (`record_summary.py:36-56`).

### 3.6 Node tools

1. `GET /mobile/nodes/{id}/tools`: 404 if the node is unknown. A household role check applies only if the node has a household (`node_tools.py:43-47`).
2. CC publishes `report_tools` on the generic `commands` topic, with `trusted:true` and `reply_request_id`. **Changed by D4:** no `trusted` flag; the broker ACL makes the command authentic.
3. CC polls for `/tmp/jarvis-node-tools/{request_id}.json` every 100 ms for up to 10 s (`:64-102`).
4. The node refreshes discovery and builds `client_tools` (OpenAI schemas), `available_commands` and `installed_packages:[{name, version, previous_version?, health:"ok"|"failed"}]`. It POSTs them to `/mobile/node-tool-reports/{request_id}`, which writes the file (`:105-116`; node `mqtt_tts_listener.py:454-552`).
5. On timeout, the response is **200 with three empty lists**, so mobile cannot distinguish "offline" from "nothing installed" (`:57-61`).

The file handoff exists because of multiple uvicorn workers. It is not needed in Go.

---

## 4. Data

**`package_install_requests`** (`models.py:490-515`, migration `l2g3h4i5j6k7`):

| Column | Type | Notes |
|---|---|---|
| `id` | varchar(36) | uuid4 |
| `node_id` | | FK `nodes.node_id`, `ON DELETE CASCADE` |
| `household_id` | varchar(255), indexed | `""` if the node has no household |
| `command_name` | varchar(255) | for revert, holds `command_name` or `package_name` |
| `github_repo_url` | text | `""` for uninstall and revert |
| `git_tag` | varchar(100), nullable | |
| `status` | varchar(20) | `pending` / `restarting` / `completed` / `failed` / `expired` |
| `results_json` | text | the node's `details` |
| `error_message` | text | |
| `created_at`, `expires_at`, `completed_at` | naive UTC | |

Install, uninstall and revert rows are **indistinguishable**: there is no `action` column.

**`test_install_requests`** (`models.py:518-542`, migration `m3h4i5j6k7l8`). Same shape, but `share_code` varchar(6) and `package_name` replace the repo, URL and tag columns, and there is no `restarting` status.

(`prompt_provider_install_requests`, `models.py:545-566`, belongs to doc 03 and is being cut.)

**Lifecycle and TTL:** rows are never deleted except by node cascade. There is no sweeper.

**In-memory state:**

| What | Where | TTL / invalidation |
|---|---|---|
| `_schema_cache[(node_id, command_name)]` | `mobile_command_data.py:58-102` | 600 s. Never invalidated by install, uninstall or reconnect. |
| `_USER_NAME_CACHE[user_id]` | `:188-190` | 300 s |
| `node_command_service._pending_commands` | for `report_tools` | 5 min |

Both of the first two caches are per-process.

**On disk:** `/tmp/jarvis-node-tools/{request_id}.json`, an ephemeral handoff file that is deleted on read.

**Node-side state (not CC's):**

- `~/.jarvis/.pending_install_result.json`, the deferred-result marker
- `~/.jarvis/packages/<name>.json` and `<name>/.previous/`
- `commands/test_commands/<pkg>/.test_meta.json`
- the `command_data` table

---

## 5. Settings

None. No settings-DB keys are read. The hard-coded constants a port must reproduce:

| Constant | Value | Where |
|---|---|---|
| Install / uninstall / revert / test-install expiry | 5 min | `package_install.py:127`, `test_install.py:121` |
| Restart extension | +120 s on the previous `expires_at` | `package_install.py:70` |
| Pantry draft fetch timeout | 10 s | `test_install.py:98` |
| Command-data MQTT timeout | 10 s | `mobile_command_data.py:51` |
| Schema cache | 600 s | |
| User-name cache | 300 s | |
| Auth batch timeout | 5 s | |
| Node tools wait | 10 s, 100 ms poll | `node_tools.py:65` |

Environment variables:

- `JARVIS_PANTRY_URL`: fallback when config-service has no `jarvis-pantry` entry (`core/service_config.py:35,185`).
- `ADMIN_API_KEY`: the admin path of provisioning auth.
- `JARVIS_APP_ID` / `JARVIS_APP_KEY`: needed for the auth batch call. If the key is empty, enrichment is skipped (`:216`).

---

## 6. Dependencies

- **CC subsystems:**
  - doc 05: node registry (`Node` rows, `household_id`, `is_active`, `room`), the MQTT client (`node_settings.get_mqtt_client`, `request_response`), `node_command_service`, liveness (`record_node_seen`, `touch_node_last_seen` from `verify_api_key`).
  - doc 00: auth deps (`verify_api_key`, `verify_user_jwt`, `verify_household_role`, `provisioning.verify_provisioning_auth` / `require_household_access`).
  - The phone planner's `services/context_provider_client.py` reuses the same request/response transport on `jarvis/nodes/{id}/context/query` (doc 11).
- **jarvis-auth:**
  - `GET /auth/me` (provisioning JWT validation, `provisioning.py:68-81`)
  - `/internal/validate-node` (node auth)
  - `verify_household_role`
  - `GET /internal/users/batch`

  All of these become in-process in Go.
- **Pantry (cloud, out of scope for porting):** CC → Pantry is exactly **one** call, `GET /v1/forge/drafts/{code}`, at test-install request time. CC also *hands the node* a Pantry URL in verify.

  Everything else involving Pantry bypasses CC:
  - mobile → Pantry: catalog, and `GET /v1/commands/{name}/download`, which yields the `github_repo_url` and pinned ref that mobile then POSTs to CC (`pantryApi.ts:70-78`, `StoreDetailScreen.tsx:338`)
  - node → GitHub: archive or git (`command_store_service.py:193-262`)
  - node → Pantry: the draft download, and pip-dependency verification
- **No LLM calls.**

---

## 7. Invariants and non-obvious behaviour

1. **The node never trusts the MQTT payload for package identity.** Verify is the only source of the repo, ref and name (`package_install.py:158-164`). Keep the verify response shape `{confirmed, command_name, github_repo_url, git_tag}` exactly. Keep `confirmed:true` as well: the node checks it (`handler :66`).
2. **One verify route serves three operations.** A Go port that adds `/package-uninstall/{id}/verify` gains nothing, because the frozen node won't call it.
3. **`restarting` is non-terminal everywhere.** It is accepted repeatedly, it extends expiry, and it can be followed by `completed` or `failed` (`:211-220`). Mobile depends on the terminal set being `{completed, failed, expired}` (`packageStatus.ts:13-17`).
4. **Expiry is checked against the row on results even when the node succeeded.** A real result arriving late becomes 410 and status `expired` (`:206-209`). See Q1.
5. **Poll is a writing GET.** It flips expired rows. In Go, compute the status lazily without writing, or write: either way, the observable state must be identical.
6. **The two poll response shapes differ:**
   - install and test-install polls branch by status. They put `"Install request expired — node may be offline"` in `error_message` on expiry, and return `details` **only when completed**, even though a `restarting` row has `results_json` (`:273-306`).
   - uninstall and revert polls return the raw status, with `error_message` only for failed/expired (so a bare expiry gives null), and always return `details` if present (`:446-452`).

   All Pydantic fields are always present (null, not omitted). `created_at` serialises as a naive ISO string with no `Z`.
7. **Ownership in command-data comes only from the JWT.** CC must never forward a client-supplied `requesting_user_id`, `user_id` or `correlation_id` (`:324-327`, `:512-516`).
8. **Subscribe before publish** for command-data. Response topics are unique per correlation id, so concurrent requests don't collide (`mqtt_client.py:210-258`).
9. **The error mapping is substring-based**, as described in §3.4. The frozen node's message strings are the contract. Note that a create validation message containing "missing" maps to **404**, not 400.
10. **FieldSpec and record dicts are opaque pass-through.** The only mutation is adding `{name}_display` into the record dict. In list results the mutation targets `record["data"]`; in `get`/`create` it targets the record itself (`:288`, `:307-312`).
11. **`list_nodes` hides nodes with no household** (`:344`; `admin.py:369-376` explains why the admin create path now sets it). But `_resolve_node_in_household` *allows* any JWT user on such a node (`:180`). See Q5.
12. **Node tools never errors on timeout:** it returns 200 with empty lists (`node_tools.py:57-61`).
13. **The uninstall body requires `component_type`** (a required Pydantic `str`, `:316`). Revert accepts `command_name` or `package_name`, and gives 422 `"command_name or package_name is required"` if both are missing (`:508-510`). That 422 has a string `detail`, unlike FastAPI's validation 422.

---

## 8. Oddities

- **Verify is not bound to the authenticated node.** `verify_api_key` authenticates *a* node, but the routes never compare it with `{node_id}` in the path. Node A could verify or post results for node B's request if it knew the UUID (`package_install.py:155`, `deps.py:162-210`). The same applies to test install. The risk is low (unguessable UUIDs), but it's a free fix in Go.
- **The verify response doesn't say which operation it authorises.** An attacker who can publish to the broker and read a live install nudge's `request_id` (it is in the install payload) can republish it on `/package-uninstall`. The node verifies successfully and **removes** the package (`handler :518-544`). The reverse direction (uninstall id on the install topic) fails harmlessly, because the URL is empty. See Q3.
- **Verify is not single-use.** A duplicate QoS-1 delivery runs the install twice concurrently, because the row stays `pending` until the result arrives.
- **Results can overwrite terminal states** (there is no precondition). The node deliberately avoids posting `restarting` after a terminal result for this reason (`handler :168-171`).
- **`POST /mobile/node-tool-reports/{id}` is unauthenticated** and writes an arbitrary JSON body to `/tmp` (`node_tools.py:105-116`). The node *does* send `X-API-Key` (`clients/rest_client.py:53-57`), so requiring it in Go is free.
- **The test-install poll lacks `require_household_access`** (`test_install.py:230-242`). Any authenticated user who knows the node id and request id can read `package_name` and the error.
- **Lazy-expiry write races:** concurrent poll and results calls can each commit a different status.
- **There is no row retention.** Request tables grow forever.
- **Who may install:** any household **member** can install arbitrary GitHub code on a shared node. CC does not validate `github_repo_url` (any host) or check it against Pantry (`:30-33`). See Q2. **Kept by D5:** this is intended for a self-hosted install.
- **Stale caches:** the schema cache is not invalidated after install/uninstall/revert, which changes a node's commands and FieldSpecs. Mobile can see a stale form for up to 10 minutes. The docstring admits this (`:66-69`).
- **Unguarded node responses:** `get_schema` and `list_records` index `response["mode"]` and `["fields"]` directly, and `get_record` indexes `response["record"]`. A node answering `ok:true` without them gives a 500 (`:393-395`, `:489`).
- **`update` responses are not user_ref-enriched; `create` is enriched only if the schema happens to be cached.**
- **Inconsistent node behaviour on install:** test installs reload in-process (`refresh_now`), while package installs always restart because "in-process reload corrupted long-lived state" (`package_install_handler.py:8-14`). This is node-side, but it means a test install can hit exactly the bug the restart was designed to avoid.
- **Pantry URL resolution:** a `ValueError` from an unresolved Pantry URL in `request_test_install` is swallowed into a 502 "Could not reach Pantry" (`:100-102`).
- **Docs vs code:**
  - CC `CLAUDE.md:171` names the route `/api/v0/mobile/node-tools`; the real route is `/api/v0/mobile/nodes/{id}/tools`.
  - `CLAUDE.md:220` says `package_install (Pantry → install on node)`, but CC never talks to Pantry for package installs.
  - Mobile's `fetchNodeTools` docstring claims a CC in-memory tool cache, but there is none (`chatApi.ts:309-311` vs `node_tools.py:1-4`).
  - The module docstring at `package_install.py:1-4` still advertises prompt-provider installs.
- **`trusted:true` on `report_tools`** makes the node skip `_verify_command` (node listener `:203`). That is harmless for a read-only report, but it is a blanket bypass flag that sits in doc 05's domain.

---

## 9. Tests

| File | Coverage | Count |
|---|---|---|
| `tests/test_package_install.py` | install request/verify (404/410/409, other node)/results (restarting, extend expiry, overwrite)/poll; uninstall and revert results/poll; revert `package_name` alias; MQTT topic and payload | 43 tests |
| `tests/test_household_authz.py:79-140` | cross-household install/uninstall/revert blocked; member allowed (the "cross-household RCE class") | |
| `tests/test_mobile_command_data.py` | schema cache, user_ref walk, enrichment, `_mqtt_request` 504/503/502, create forwarding and the 400/403/404 mapping | 23 tests |

There are **no CC tests** for `test_install.py` or `node_tools.py`.

Node-side: `test_package_install_handler.py`, `test_command_data_handler.py`, `test_command_data_repository.py`, `test_report_tools_installed_packages.py`.

**Golden and contract candidates:**

- **State-machine table test** over (status × event × expired?) → (new status, HTTP code), for all three package operations and test install. It is the cheapest way to pin §3.2.
- **Poll response JSON goldens** per status for each of the 4 poll routes, including the null-vs-message differences from invariant 6.
- **Command-data:** a fake-node MQTT responder replaying each node message string from §3.4, asserting the HTTP status. Also enrichment goldens (nested `user_ref`, list vs get placement).
- **End-to-end:** install-e2e Phase 3 already provisions a real node over MQTT. Add one install→restarting→completed round-trip against a local fixture package, and one command-data list/patch on the reminder command.

---

## 10. Questions for the user

1. **[behaviour] A slow install currently gets reported as "expired" even when it succeeds.** The 5-minute expiry is absolute from creation, and results posted after it get 410 (`:206-209`). But a node's pip install alone may take up to 600 s (`command_store_service.py:927-931`) on a Pi Zero.
   - *Why it matters:* the user sees "node may be offline", while the node actually installs and restarts. Mobile's revert/uninstall loops also give up at 120 s.
   - *Options:* (a) keep 5 minutes absolute; (b) a 5-minute *pickup* deadline (until verify), then on verify reset `expires_at = now + 15 min`, keeping the +120 s restart extension; (c) never expire once verified.
   - **Recommendation: (b).** It is server-only, so the frozen node is unaffected, and the observable states are unchanged.
   - **Decided (D39):** (b): 5-minute pickup deadline, then `verify + 15 min`, keeping the +120 s restart extension.
2. **[behaviour] Who may install code on a node?** Today any household *member* can make a node run arbitrary code from any URL. CC neither validates the host nor checks the package against Pantry.
   - *Options:* (a) keep member; (b) require household admin/owner for install, uninstall, revert and test install, and keep member for command-data and the tools view; (c) (b) plus restrict `github_repo_url` to `https://github.com/`.
   - **Recommendation: (b) + (c),** if mobile users who install are already household admins in practice. Is that true for your household setup?
   - **Decided (D5):** (a): any household member, any URL. No allowlist, because private self-hosted Pantry instances are planned. A power-user gate is possible later.
3. **[behaviour] The op-confusion gap in verify.** A broker-level attacker can replay a live install `request_id` on `/package-uninstall` and remove the package (§8).
   - *Options:* (a) accept it, and rely on broker ACLs (the embedded broker can make only `jarvisd` publish to `jarvis/nodes/+/…` except response topics); (b) also add an additive `action: "install"|"uninstall"|"revert"` field to the verify response, and an `action` column, so a post-freeze node can check it; (c) both.
   - **Recommendation: (c).** The ACL fixes it now; the field future-proofs it.
   - **Decided (D4), partly:** (a): per-node broker credentials and ACLs mean only `jarvisd` publishes to `jarvis/nodes/+/…`. **Open:** whether to also add the additive `action` field and column (b).
4. **[behaviour] Bind node credentials to the path in Go?**
   - (a) Verify and results reject when the authenticated node ≠ `{node_id}` (403).
   - (b) `node-tool-reports` requires node auth and must match the node the report was requested from.

   Nodes always send their own id and key, so neither breaks a live client. **Recommendation: do both.**
   **Decided (D4, D5):** both. Verify and results require node auth bound to `{node_id}`; `node-tool-reports` requires node auth and must match the requested node.
5. **[behaviour] What should happen to nodes with no household?**
   - Today, command-data and the node tools view allow *any* JWT user on such a node, package routes deny every JWT user (403), and `list_nodes` hides it.
   - *Options:* (a) keep this mix; (b) fail closed everywhere (JWT → 403, admin key only).
   - **Recommendation: (b).** `admin.py` now always sets `household_id`, so this should only affect legacy rows.
   - **Decided (D40 default):** (b): fail closed everywhere.
6. **[behaviour] Should a terminal status be sticky?** Today a late or duplicate results post can turn `completed` into `failed`, or back into `restarting`.
   - *Options:* (a) keep last-write-wins; (b) terminal is sticky: ignore later posts with 200 `{"status":"ok"}`, so the node doesn't retry or log errors; (c) sticky with 409.
   - **Recommendation: (b).** Also make verify idempotent rather than single-use, so a duplicate QoS-1 nudge doesn't produce a spurious failure. Is the double-install risk from duplicate nudges something you've seen?
   - **Decided (D8, P2):** (b): terminal is sticky, later posts get 200 `{"status":"ok"}`; verify is idempotent.
7. **[scope] Legacy import and retention for the request tables.**
   - *Options:* (a) import them; (b) don't import (rows are minutes-lived and mobile only polls fresh ids), and add a sweeper that deletes rows older than N days.
   - **Recommendation: (b), with N = 30.**
   - **Decided (D40 default):** (b): don't import; 30-day sweeper.
8. **[scope] Where does `jarvisd` get the Pantry URL, and is Forge test install still wanted?** Today it comes from config-service `jarvis-pantry` or `JARVIS_PANTRY_URL`.
   - *Options:* (a) a setting `pantry.base_url` defaulting to the public Fly URL; (b) env-only; (c) cut test install.
   - The verify response hands the node `{pantry}/v1/forge/drafts/{code}`, so the URL must be reachable *from the node*, not just from `jarvisd`.
   - **Recommendation: (a), and keep test install.**
   - **Decided (D39, D5):** (a) `pantry.base_url`, defaulting to the public Pantry URL, reachable from the node. Test install is **dropped** (D5), future work.
9. **[behaviour] Node tools timeout semantics.** A node that doesn't answer within 10 s yields 200 with empty lists, so the Store shows "not installed" for everything.
   - *Options:* (a) keep (frozen); (b) 504 on timeout (mobile shows an error, which is a client-visible change).
   - **Recommendation: (a)** for Phase 5, plus a log line. Revisit with a mobile change.
   - **Decided (D40 default):** (a): keep 200-empty and log it.
10. **[minor] Invalidate the command-data schema cache in-process when a package install, uninstall or revert completes for that node** (and when the node reconnects to the embedded broker)?
    - **Recommendation: yes.** It is free in a single process, and it doesn't change the wire contract.
    - **Decided (D40 default):** yes, invalidate in-process.
11. **[minor] Error mapping.** Should the Go port reproduce the substring mapping byte-for-byte (including "missing" → 404 on create), or map by an error `code`?
    - The node is frozen and sends no code. **Recommendation:** reproduce exactly, and add `error.code` support later as an additive node change.
    - **Decided (D40 default):** reproduce the substring mapping exactly.
12. **[minor] Keep sending `github_repo_url`/`git_tag` in the install MQTT payload,** even though nodes ignore them?
    - Older node builds may have read them. **Recommendation:** keep them for wire parity, and drop them once all nodes are verify-based.
    - **Decided (D40 default):** keep `github_repo_url`/`git_tag` in the payload.

---

## 11. Go port notes

**Shape.** One `packages` package in the CC module. It contains:

- a `Requests` store over SQLite: `package_install_requests` (and `test_install_requests`, ported 2026-10-08), plus a `verified_at` column (Q1, D39) and, if Q3(b) is taken, an `action` column
- a pure `transition(row, event, now) (row, httpStatus)` function that implements §3.2. This is the unit-test surface.
- thin handlers

The `commanddata` package holds the 8 handlers plus a generic `mqttRPC(ctx, nodeID, op, payload, 10s)`. That helper is shared with `context_provider_client` (doc 11).

**Embedded broker.** `request_response` becomes an in-process waiter: a `map[correlationID]chan []byte`, filled by a broker publish hook on `jarvis/nodes/+/command-data/+/response/+`. No subscribe/unsubscribe churn is needed, and the subscribe-before-publish race disappears by construction: register the channel, then publish.

The same hook mechanism lets `node-tool-reports` complete a channel instead of writing to `/tmp`. The HTTP route must stay, because the frozen node POSTs to it; it now requires node auth bound to the requested node (D4).

**ACLs (coordinate with doc 05; D4).** With an embedded broker and per-node credentials, `jarvisd` decides who may publish to `jarvis/nodes/{id}/package-*` and `…/command-data/{op}`. Only `jarvisd` itself should be allowed. A node credential should be allowed to publish only to `jarvis/nodes/{own id}/…/response/#`. This is what makes `requesting_user_id` trustworthy, since the node believes whatever it receives.

**Auth in-process.** `verify_provisioning_auth` (admin key or JWT) and the household role check become local calls. Any household member may install, uninstall and revert from any URL (D5); the household check covers all of the caller's memberships. Nodes with no household fail closed (D40). Verify/results bind the node credential to `{node_id}` (D4). There are no more `/auth/me` or `/internal/users/batch` round-trips: user display names come straight from the auth module's tables, and the 300 s cache can go.

**Expiry.** 5-minute pickup deadline, then `verify + 15 min`, plus +120 s per `restarting` (D39). Terminal status is sticky (D8). A 30-day sweeper trigger deletes old rows (D40, D27). Compute the effective status on read: `pending|restarting && now > expires_at ⇒ expired`. Persist it only on the same events Python persists on, or always; it isn't observable either way. Use UTC timestamps. Serialise `created_at` without a zone suffix, to match the naive FastAPI output (put this in a contract-test fixture).

**JSON fidelity.**

- Poll responses include every key, with null.
- FieldSpec, records and `details` are `json.RawMessage` or `map[string]any`. Never decode them into structs.
- Keep the `{name}_display` injection placement (invariant 10).
- 422 bodies: the revert "name required" error has a string `detail`, and missing-field errors must match FastAPI's validation shape (shared contract-test helper, doc 00).

**Pantry.** The only outbound Pantry call is test install's share-code check, `GET {pantry.base_url}/v1/forge/drafts/{code}` (10 s). The `pantry.base_url` setting (D39) defaults to the public Pantry URL and must be reachable from the node, which downloads the draft from the same URL.

**Test install (ported 2026-10-08, reversing D5's drop).** `testinstall.go`, table `cc_test_install_requests` (cc migration 00160). Mirrors `test_install.py` route for route: create `POST …/test-install {share_code}` → 201 `{id, status, package_name, created_at}` (provisioning auth + household; `strip().upper()`, length ≠ 6 → 400 `Invalid share code`; Pantry 404 → 404 `Share code not found or expired`, other non-200 → 502 `Pantry returned an error`, unreachable → 502 `Could not reach Pantry service`; `package_name` defaults to `"unknown"`), MQTT nudge `jarvis/nodes/{id}/test-install {request_id}`, node verify → `{confirmed, package_name, pantry_download_url}`, results `{success, error?, details?}` → `{"status":"ok"}`, poll → `{status, request_id, package_name, error_message, details}` (fixed `Test install request expired — node may be offline` on expiry, details only when completed). Statuses `pending|completed|failed|expired`. The row reuses the package state machine (no `restarting`), so the deliberate differences from legacy are the package ones: D39 pickup/verify deadlines (5 min, then verify + 15 min: a Pi Zero pip install would otherwise expire mid-install), D8 sticky terminal status (a late result is acknowledged and ignored; legacy overwrote), D4 verify/results bound to `{node_id}` (403 `Node mismatch`), the poll's household check (§8 bug), verify on a completed/failed row answers 409 even past its expiry (legacy: 410), the 30-day row sweep, and a finished test install invalidates the node's command-data schema cache. A draft body that isn't JSON is a 502 (legacy: 500). Nodes with no household fail closed for members (D40), as for packages.

**Risks.**

- The frozen node's behaviour is half the contract: restart-and-deferred-flush, the double restart on auto-rollback, and the verify-route sharing. Black-box test against a real node (install-e2e Phase 3) before cutover.
- Q1 (D39) and Q6 (D8) change edge-case behaviour, so the tests should pin them down explicitly.
- No contract test for test install: it needs a live Pantry share code (a Forge draft, 15-minute TTL). Unit tests use a fake Pantry.

**Simplifications.**

- No file handoff, and no multi-worker concerns.
- The schema cache becomes a `sync.Map` with in-process invalidation (Q10).
- No Redis or HTTP callbacks are involved anywhere in this subsystem.
