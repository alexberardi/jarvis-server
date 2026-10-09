# 05 — Nodes

Node registry, provisioning, node identity and credentials, node settings and K2, the MQTT client and its request/response patterns, node commands with the verify pattern, liveness, node updates, ambient-noise calibration, and request traces.

All paths are relative to `jarvis-command-center/app/` unless prefixed with another repo name. Every route is mounted under `/api/v0`; `admin.router` is under `/api/v0/admin` (`main.py:697`), `admin.node_public_router` under `/api/v0` (`main.py:700`), and the traces routers under `/api/v0/admin` and `/api/v0/mobile` (`main.py:811-812`).

---

## 0. Decisions applied (2026-10-06)

Source: `QUESTIONS.md`. Sections below still describe today's Python behaviour; inline "Changed by" notes and §11 say what Go does instead.

- **D4 (security):** `trusted:true` is removed from every published command. Fresh installs get **per-node broker credentials and ACLs** (a node subscribes only to its own `jarvis/nodes/{self}/#` plus `jarvis/auth/+/ready`, and publishes only its own responses; CC is the only other publisher). The result sinks (`/device-control-results`, `/device-state-results`, `/mobile/node-tool-reports`, `/mobile/voice-profile-results`) require node auth, and the rid must belong to that node. Ambient-noise trigger/poll and the settings `/result` poll get the household check. Wire shapes are unchanged.
- **D4/D5 (provisioning):** token minting adds a household-membership check against the *target* household, across **all** of the caller's memberships (users can belong to several households), not just the JWT's active one.
- **D5:** `/api/v0/chat` is dropped; node `chat_text()` moves to `/node/llm/chat` (§3.11), which becomes a core node route, not only a plugin one. Needs a node-setup change. ~~Forge test install (topic 19, `test-install`) is dropped.~~ **Reversed 2026-10-08:** Forge test install is ported (doc 12 §11); topic 19 is published again.
- **D6 (owned by 07):** config push `pending`/`ack` get node auth bound to the path node. DELETE still hard-deletes `config_pushes` (§3.4).
- **D7:** commands are authentic by construction via the D4 broker ACLs; the node-side verify step is no longer a security boundary.
- **D8:** known bugs are fixed and logged as intended differences: `/k2/ack` checks the path node (§8.5), no negative caching of auth outages (§8.7), the sweeper filters by kind (§8.8), and `create_settings_request` no longer skips authz on a NULL household (§8.6).
- **D9:** `/nodes/{id}/commands` (LoRA `train_adapter`) is cut, with the verb. `adapter_hash` goes with LoRA.
- **D10:** factory reset uses the **tracked flow only**: `POST /admin/nodes/{id}/factory-reset` creates a `NodeTask` (single-in-flight), the node reports via `/nodes/factory-reset/{task_id}/status`, and the sweeper times it out. Mobile's delete switches to it (mobile change). The DELETE + `verify-reset` flow is dropped once mobile switches; `verify-reset` stays only while older node builds need it. The reset token is **persisted in SQLite with the task**, so an offline node or a restart can still complete the reset.
- **D11:** `tracing.retention_days` is not declared; trace retention is hard-coded to 7 days. Defined-but-unread keys are dropped.
- **D18 (owned by 10):** the attention broker interposes on `/node/push-notification`, `/node/inbox-item` and `/node/send-link`; with `attention.enabled` off they stay byte-identical.
- **D24 (owned by 08):** the `routine` verb carries the full routine definition in `details`.
- **D29 (owned by 07):** cameras are deferred, so the camera-creds result waiter is not built in v1.
- **D40 (B defaults):** stuck update dispatch → 400 at request time (Q5); broker sessions in memory (Q7); persist `include_values`/`user_id` on settings requests (Q8); drop bare-key node auth after verifying node headers (Q10); prune settings requests/snapshots after 24 h (Q11).
- **M10:** `/nodes/{id}/actions` passes `input_required` through as an optional field. **M11:** a demoted `send-link` keeps its URL as `metadata:{url, type:"open_url"}` (doc 13).

## 1. Purpose

A **node** is a Pi Zero (or Docker/dev) voice client. This subsystem owns:

- **Identity.** Creating node records, paired with a `node_key` minted by jarvis-auth, via either a short-lived provisioning token (mobile flow) or the admin key (seed scripts and install-e2e).
- **Registry.** Room, household, version, busy flag, installed protocols, `needs_k2`, and online/offline state, for the mobile node list.
- **Server→node push.** CC publishes MQTT messages to `jarvis/nodes/{node_id}/…`. Nodes answer over HTTP, either a verify callback or a result POST, or over MQTT response topics for the two request/response verbs.
- **Encrypted settings relay.** Mobile asks for a node's settings; the node uploads a K2-encrypted snapshot; mobile decrypts it. CC never sees plaintext.
- **K2 relay** for headless/Docker nodes that can't be reached over the provisioning AP.
- **Lifecycle.** Heartbeat, OTA update tasks, and delete (which triggers a factory reset).
- **Small node utilities.** Ambient-noise calibration, LED preview, `config.json` edits, and the public plugin endpoints (`/node/push-notification`, `/node/llm/chat`, `/node/send-link`, `/node/inbox-item`). The inbox semantics of those are in doc 13; only the transport is here.
- **Request traces** for the admin trace visualizer.

Users: nodes (X-API-Key), mobile (JWT), jarvis-admin (admin key, traces only), seed scripts and install-e2e (admin key), and 3 background loops.

---

## 2. Entry points

### 2.1 HTTP routes

Auth: **N** = node `X-API-Key: node_id:node_key` (`deps.py:162-217`); **J** = user JWT; **A** = `X-API-Key` == `ADMIN_API_KEY` (`deps.py:219-225`); **–** = none.

| Method | Path (`/api/v0` + …) | Auth | Live caller | Code |
|---|---|---|---|---|
| GET | `/admin/nodes?household_id&include_inactive` | J (member, superuser sees all) | mobile `listNodes` (`nodeApi.ts:29`) | `admin.py:233` |
| GET | `/admin/nodes/{id}` | J (member) | mobile `getNode` | `admin.py:277` |
| POST | `/admin/nodes/heartbeat` | N | node `_heartbeat_loop`, every 300 s (`mqtt_tts_listener.py:2611`) | `admin.py:296` |
| POST | `/admin/nodes` | A | install-e2e `seed.py:83`, `scripts/seed_dev.py:226`, node-setup `authorize_node.py:118` | `admin.py:345` |
| PATCH | `/admin/nodes/{id}` | A | `authorize_node.py:171` | `admin.py:626` |
| DELETE | `/admin/nodes/{id}` | J (power_user; superuser if no household) | mobile `deleteNode` (`nodeApi.ts:52`), `bootstrap_multi_node.py:196` | `admin.py:393` |
| POST | `/admin/nodes/{id}/factory-reset` | J | **none: CUT** (Appendix A). **Kept by D10:** becomes the only reset flow; mobile's delete switches to it | `admin.py:469` |
| POST | `/nodes/factory-reset/{task_id}/status` | `X-Reset-Token` | node, but only when the MQTT message carries `task_id`, i.e. only via the cut route | `admin.py:564` |
| POST | `/nodes/verify-reset` | – | node legacy path (`mqtt_tts_listener.py:2149`). **This is the live path**, because DELETE publishes without `task_id`. **D10:** kept only while older node builds need it | `admin.py:765` |
| GET/POST/DELETE | `/admin/cache/*` | – | **CUT** | `admin.py:648-665` |
| GET/POST | `/admin/adapter/*` | A | **CUT** (LoRA) | `admin.py:691-751` |
| POST | `/provisioning/token` | A or J (member) | mobile `requestProvisioningToken` (`commandCenterApi.ts:152`) | `provisioning.py:154` |
| POST | `/nodes/register` | provisioning token in body | node `provisioning/registration.py:42`, `setup_mode.py:515`, `authorize_node.py:245` | `provisioning.py:202` |
| POST | `/nodes/{id}/settings/requests?include_values` | J (power_user if node has household) | mobile `nodeSettingsApi.ts:39` | `node_settings.py:181` |
| GET | `/nodes/{id}/settings/requests` | N (self) | node reconnect backstop (`mqtt_tts_listener.py:1420`) | `node_settings.py:230` |
| GET | `/nodes/{id}/settings/requests/{rid}` | N (self) | node snapshot handler | `node_settings.py:266` |
| PUT | `/nodes/{id}/settings/requests/{rid}/snapshot` | N (self) | node | `node_settings.py:301` |
| GET | `/nodes/{id}/settings/requests/{rid}/result` | J (**no household check**) | mobile poll (`nodeSettingsApi.ts:54`) | `node_settings.py:365` |
| POST | `/nodes/{id}/k2` | J (member) | mobile (`nodeSettingsApi.ts:70`) | `node_settings.py:451` |
| GET | `/nodes/{id}/k2/provision/{rid}` | N (self) | node `_fetch_k2_from_cc` (`mqtt_tts_listener.py:1610`) | `node_settings.py:531` |
| POST | `/nodes/{id}/k2/ack/{rid}` | N (**not checked against the path id**) | node `_ack_k2_provision` (`:1751`) | `node_settings.py:569` |
| GET | `/node/mqtt-credentials` | N | node `utils/mqtt_credentials.py:58` | `api/node_mqtt.py:22` |
| POST | `/nodes/{id}/actions` | J (member) | mobile (`commandCenterApi.ts`) | `api/node_commands.py:79` |
| POST | `/nodes/{id}/commands` | A | jarvis-admin `routes/nodes.ts:44`, which only sends `train_adapter`. So it is **effectively LoRA-only.** **CUT (D9)** | `api/node_commands.py:138` |
| POST | `/nodes/{id}/node-config` | J (member) | mobile `nodeApi.ts:73` | `api/node_commands.py:156` |
| POST | `/nodes/{id}/led/preview` | J (member) | mobile `nodeApi.ts:88` | `api/node_commands.py:189` |
| POST | `/commands/{rid}/verify` | N | node `_verify_command` (`mqtt_tts_listener.py:128`) | `api/node_commands.py:222` |
| POST | `/node/push-notification` | N | node agents and packages (public plugin API) | `api/node_commands.py:333` |
| POST | `/node/llm/chat` | N | packages (public plugin API) | `api/node_commands.py:449` |
| POST | `/node/send-link` | N | node `send_link` command | `api/node_commands.py:507` |
| POST | `/node/inbox-item` | N | packages and SDK (public plugin API) | `api/node_commands.py:629` |
| POST | `/device-control-results/{rid}` | **–** | node `_post_action_result` (`:290`), `_post_tool_call_result` (`:991`) | `api/smart_home.py:1200` (the shared result sink; see §3.6) |
| GET | `/releases/latest` | **–** | mobile `nodeUpdateApi.ts:36` | `api/node_updates.py:100` |
| POST | `/nodes/{id}/update` | J (member) | mobile `nodeUpdateApi.ts:51` | `api/node_updates.py:117` |
| POST | `/nodes/tasks/{tid}/status` | N (owner) | node `update_service._report_task_refused` (`services/update_service.py:69`) | `api/node_updates.py:171` |
| GET | `/tasks/{tid}` | J (member) | mobile `nodeUpdateApi.ts:60` | `api/node_updates.py:224` |
| POST | `/nodes/{id}/tasks/{tid}/cancel` | J (member) | mobile `nodeUpdateApi.ts:82` | `api/node_updates.py:241` |
| GET | `/nodes/{id}/tasks?limit` | J (member) | mobile `nodeUpdateApi.ts:71` | `api/node_updates.py:278` |
| POST | `/nodes/{id}/ambient-noise-measurements` | J (**no household check**) | mobile `nodeApi.ts:121` | `api/ambient_noise.py:80` |
| POST | `/nodes/{id}/ambient-noise-measurements/{rid}/result` | N (self) | node (`mqtt_tts_listener.py:1292`) | `api/ambient_noise.py:109` |
| GET | `/nodes/{id}/ambient-noise-measurements/{rid}` | J (**no household check**) | mobile `nodeApi.ts:132` | `api/ambient_noise.py:141` |
| GET | `/admin/traces?limit&offset&status&source&household_id&node_id` | A | jarvis-admin `server/src/routes/traces.ts` | `api/traces.py:199` |
| GET | `/admin/traces/{trace_id}` | A | jarvis-admin | `api/traces.py:253` |
| GET | `/mobile/traces/{conversation_id}` | J | **none: CUT** | `api/traces.py:288` |

### 2.2 Background loops (`main.py` lifespan)

| Loop | Cadence | Code |
|---|---|---|
| Provisioning-token cleanup | Once at startup, then every 3600 s | `main.py:169-198` → `provisioning.py:256` |
| Trace TTL cleanup (`tracing.retention_days`, default 7) | 3600 s | `main.py:336-357` |
| Node-task timeout sweeper | 120 s | `main.py:398-454` |

### 2.3 Internal callers of this subsystem

`get_mqtt_client()` (`node_settings.py:40`) is the process-wide MQTT singleton. It is imported by bluetooth, cameras, package_install, test_install, smart_home, routines, oauth, mobile_command_data, context_provider_client and control_device_tool.

`NodeCommandService` (`services/node_command_service.py`) is used by smart_home, routines, callbacks, mobile_voice_profiles, node_tools, proposable_action_service, ambient_noise and node_commands.

`dispatch_node_command` (`node_command_service.py:145`) is the headless tool_call primitive. Its callers are mobile_chat (`:262`), workflow_engine (`:585`), signal_automation_executor (`:179`, `:343`) and signal_reaction_bridge (`:119`).

### 2.4 MQTT topic catalogue (contract to freeze)

The broker is the URL from discovery or `JARVIS_MQTT_BROKER_URL`, default `mqtt://localhost:1883` (`core/mqtt_client.py:17-36`). Schemes: mqtt, mqtts, ws, wss.

- **CC client:** MQTT 3.1.1, `client_id=jarvis-cc-{pid}-{hex8}`, clean session, keepalive 60, publishes at **QoS 1, retain=false** (`:104-156`).
- **Node client:** `client_id=jarvis-node-{node_id}`, **`clean_session=False`** (it relies on the broker queuing offline QoS-1 messages). It subscribes to `jarvis/nodes/{node_id}/#` and `jarvis/auth/+/ready`, both QoS 1 (`mqtt_tts_listener.py:2733-2753`, `1485-1491`). The node subscription is overridable via `config.mqtt_topic`.
- **Dispatch:** the node matches by **topic suffix** (`endswith`) in a fixed order. Anything that matches no suffix is parsed as a commands array (`:2394-2530`). The node never handles `…/response/…` topics.

| # | Topic | Dir | Payload | Correlation / reply | Owner doc |
|---|---|---|---|---|---|
| 1 | `jarvis/nodes/{nid}/commands` | CC→node | `[{"command":verb,"details":{…,"request_id"}}]` (verbs in §3.5) | per verb, §3.5 | 05 |
| 2 | `jarvis/nodes/{nid}/settings/request` | CC→node | `{request_id,node_id,include_values?:true,user_id?:int}` | HTTP GET + PUT snapshot | 05 |
| 3 | `jarvis/nodes/{nid}/k2/provision` | CC→node | `{request_id}` | HTTP GET pull + POST ack; CC waits 15 s | 05 |
| 4 | `jarvis/nodes/{nid}/factory-reset` | CC→node | `{request_id:<hex token>,node_id,task_id?}` | `verify-reset` or task status | 05 |
| 5 | `jarvis/nodes/{nid}/config/push` | CC→node | `{push_id,config_type,node_id}` | node polls pending configs, acks `/nodes/{id}/config/{push_id}/ack` | 07 |
| 6 | `jarvis/nodes/{nid}/routines/sync` | CC→node (each active household node) | `{event:"routines_changed",household_id}` | node pulls over HTTP | 08 |
| 7 | `jarvis/nodes/{nid}/device-scan` | CC→node | `{request_id}` | POST `/nodes/{id}/device-scan/{rid}/results` | 07 |
| 8 | `jarvis/nodes/{nid}/device-list` | CC→node | `{request_id,manager_name}` | POST `/nodes/{id}/device-list/{rid}/results` | 07 |
| 9 | `jarvis/nodes/{nid}/device-state` | CC→node | `{request_id,entity_id,domain,protocol,source,cloud_id,local_ip,mac_address}` | POST `/device-state-results/{rid}` (–), file `state-{rid}` | 07 |
| 10 | `jarvis/nodes/{nid}/camera-credentials` | CC→node | `{request_id,protocol,cloud_id,entity_id,domain}` | file `creds-{rid}`, 10 s (`cameras.py:62`) | 07 |
| 11 | `jarvis/nodes/{nid}/bluetooth-scan` | CC→node | `{request_id,role}` | POST `/nodes/{id}/bluetooth-scan/{rid}/results` | 07 |
| 12 | `jarvis/nodes/{nid}/bluetooth-pair` | CC→node | `{request_id,mac_address,role}` | POST `/nodes/{id}/bluetooth/pair/{rid}/results` | 07 |
| 13 | `jarvis/nodes/{nid}/bluetooth-disconnect` | CC→node | `{mac_address}` | none | 07 |
| 14 | `jarvis/nodes/{nid}/bluetooth-discoverable` | CC→node | `{timeout:120}` | none | 07 |
| 15 | `jarvis/nodes/{nid}/bluetooth-release`, `…/bluetooth-auto-connect` | node subscribes only | – | **no publisher anywhere** (dead) | 07 |
| 16 | `jarvis/nodes/{nid}/package-install` | CC→node | `{request_id,command_name,github_repo_url,git_tag}` | node GETs `…/package-install/{rid}/verify`, POSTs `…/results` | 12 |
| 17 | `jarvis/nodes/{nid}/package-uninstall` | CC→node | `{request_id,command_name,component_type?}` | POST `…/package-uninstall/{rid}/results` | 12 |
| 18 | `jarvis/nodes/{nid}/package-revert` | CC→node | `{request_id,command_name,package_name}` | POST `…/package-revert/{rid}/results` | 12 |
| 19 | `jarvis/nodes/{nid}/test-install` | CC→node | `{request_id}` | GET `…/test-install/{rid}/verify`, POST `…/results` | 12 |
| 20 | `jarvis/nodes/{nid}/command-data/{op}` op∈`commands,schema,list,get,create,update,delete` | CC→node | JSON incl. `correlation_id` | node publishes **`…/command-data/{op}/response/{correlation_id}`** (QoS 1); CC waits 10 s → 504 | 12 |
| 21 | `jarvis/nodes/{nid}/context/query` (node also accepts `context/operations`; CC never sends it) | CC→node | JSON incl. `correlation_id`, `operation`, `params`, `user_id` | **`…/context/query/response/{correlation_id}`**; 8 s per node, tries nodes in liveness order | 10 |
| 22 | `jarvis/auth/{provider}/ready` | CC→**all nodes** | `{provider,node_id,session_id,user_id}` | node filters on `node_id`, pulls creds over HTTP | 07 |

Nodes publish **only** response topics (rows 20 and 21). Every other node→CC message goes over HTTP.

---

## 3. Behaviour

### 3.1 Node identity and credential lifecycle

```
mobile (JWT)             CC                          jarvis-auth                 node
  │ POST /provisioning/token {household_id, room?, name?, node_id?}
  │──────────────────────▶│ node_id = uuid4 (or reuse if refresh & not registered)
  │                       │ raw = "prov_"+token_urlsafe(32); store sha256(raw), TTL 600s
  │◀── {token, node_id, expires_at, expires_in:600}
  │    (jarvisd adds node_command_center_url / node_config_service_url: the URLs a LAN node
  │     should use, never loopback — see internal/modules/cc/nodeurl.go, 2026-10-09)
  │ (phone joins node AP) POST node:/api/v1/provision {wifi, room, command_center_url,
  │   config_service_url, household_id, node_id, provisioning_token}  (node-local API)
  │                                                                           │
  │                       │◀──── POST /nodes/register {node_id, provisioning_token, room?}
  │                       │ validate (hash, node_id, unconsumed, unexpired)
  │                       │── POST /internal/nodes/register (app-to-app) ──▶│
  │                       │   {node_id, household_id, name, services:["jarvis-logs"]}
  │                       │◀── 201 {node_key}
  │                       │ insert nodes row (api_key=node_key, room, user="default",
  │                       │   voice_mode="brief", household_id); consume token
  │                       │──── 201 {node_id, room, user, voice_mode, node_key} ─────▶│
  │                                                                  node stores key
  │                       │◀──── GET /node/mqtt-credentials (X-API-Key) ─────────────│
  │                       │──── {username|null, password|null} (env MQTT_USERNAME/PASSWORD)
  │ K2: over the AP (node:/api/v1/provision/k2) or, headless, via POST /nodes/{id}/k2 (§3.3)
  │ DELETE /admin/nodes/{id} → factory reset (§3.4)
```

- Token creation: `provisioning.py:154-199`. A refresh with an existing `node_id` returns 400 if that node is already registered, and deletes prior unconsumed tokens (`:163-172`). Auth is the admin key, *or* a JWT validated by a round-trip to `/auth/me` (`:68-106`). That is a third JWT path, separate from `verify_user_jwt`. The household check happens **only** in `require_household_access` (`:109-119`). **`create_provisioning_token` never calls it**, so any authenticated user can mint a token for any `household_id` (see §8).
- Register: `provisioning.py:202-248`. Room precedence is body > token > `"default"` (`:222`). `_register_node_with_auth` maps auth's 400 and 404 to the same codes and other errors to the passthrough status; a network error becomes 502 (`admin.py:129-190`). If auth registration succeeds but the local commit fails, the auth record is orphaned. There is no compensation.
- Retry behaviour on the node: 8 connect retries, 4 s apart; any HTTP response is final (`jarvis-node-setup/provisioning/registration.py:20-86`).
- Admin create (`admin.py:345-390`) is the same, minus the token, plus `user` and `voice_mode` from the body.
- Node auth on every request (`deps.py:162-217`):
  1. Cache lookup, keyed by the raw header, TTL `NODE_AUTH_CACHE_TTL`=60 s (`deps.py:99-100`). **Negative results are cached too, including "auth service unavailable"** (`:190-191`).
  2. If the header has the `node_id:node_key` form, POST jarvis-auth `/internal/validate-node` with `service_id=JARVIS_APP_ID` (`:129-159`). It must also have a local row, or 401 "Node not configured locally".
  3. Otherwise the **legacy** path: `nodes.api_key == header`, i.e. the bare node_key (`:211-217`).

  Every success calls `touch_node_last_seen` (§3.7). `is_active` is **not** checked.

  > **Changed by D40 (05.Q10):** Go drops the legacy bare-key path (after confirming the jarvis-dev and prod nodes send `node_id:node_key`) and never caches "auth unavailable" (auth is in-process).
- MQTT credential: one **shared** credential for all nodes and CC (`core/mqtt_client.py:39-50`). Nulls mean connect anonymously. When the broker rejects with CONNACK 4 or 5, the node self-heals by re-fetching, at most once per 300 s (`mqtt_tts_listener.py:1440-1470`).

  > **Changed by D4:** per-node broker credentials and ACLs; the `/node/mqtt-credentials` response shape is unchanged.
- The QR payloads in node-mobile (`qrPayloadService.ts`) are mobile↔mobile K2 export, `{v, mode, node_id, kid, k2, cc_url}`. CC never sees them.

### 3.2 Settings request / snapshot (mobile ↔ node, end-to-end encrypted)

1. Mobile: `POST /nodes/{id}/settings/requests?include_values=bool`. CC inserts `settings_requests` (status `pending`, `expires_at` = now+5 min; `models.py:11-13,245`). It then publishes `jarvis/nodes/{id}/settings/request` `{request_id, node_id, include_values?, user_id?}` (`node_settings.py:148-174`). A publish failure is swallowed.
2. Node: on that message, or on **every MQTT (re)connect** via `GET …/settings/requests` (the backstop, `mqtt_tts_listener.py:1398-1432`), it confirms with `GET …/{rid}` (410 if expired). It builds a snapshot encrypted with K2 (AES-256-GCM) and `PUT …/{rid}/snapshot` `{ciphertext, nonce, tag, aad_schema_version, aad_commands_schema_version, aad_revision}`. 409 if the request is not pending (`node_settings.py:301-362`).
3. Mobile polls `GET …/{rid}/result`. Responses are 202 `{status:"pending",request_id,message}`; 410 if expired-and-pending; 200 `{status:"fulfilled", request_id, snapshot:{snapshot_id,ciphertext,nonce,tag,aad:{node_id,schema_version,commands_schema_version,revision,request_id},created_at}}`; 500 if the snapshot row is missing (`:365-426`).

`include_values` and `user_id` exist **only in the MQTT payload**. The backstop path re-runs with `include_values=False, user_id=None` (`mqtt_tts_listener.py:1426-1430`).

### 3.3 K2 relay for headless nodes (`node_settings.py:434-580`)

```
mobile ──POST /nodes/{id}/k2 {k2,kid,created_at}──▶ CC (blocks ≤15 s, sync handler)
CC: write /tmp/jarvis-k2-pending/{rid}.json {node_id,k2,kid,created_at}
CC ──MQTT jarvis/nodes/{id}/k2/provision {"request_id"}──▶ node   (nudge only, no key)
node ──GET /nodes/{id}/k2/provision/{rid} (X-API-Key)──▶ CC: one-time read+unlink; node_id must match → {k2,kid,created_at}
node: save_k2(); ──POST /nodes/{id}/k2/ack/{rid} {success, error?}──▶ CC writes /tmp/jarvis-k2-provision/{rid}.json
CC poll loop (0.3 s) sees result → 200 {ok:true,node_id,kid} | 502 {detail:error} | 504 after 15 s
finally: unlink pending file (key never outlives the call)
```

The 503 "MQTT not available" check runs before anything is written (`:473-475`). A spoofed nudge makes the node fetch → 404 → ack `{success:false, error:"verification failed"}` (`mqtt_tts_listener.py:1636-1640`). That ack carries an arbitrary rid; CC writes the file anyway and nobody reads it (a /tmp litter leak).

### 3.4 Delete and factory reset

The **live** path is `DELETE /admin/nodes/{id}` (`admin.py:393-442`):

1. Authz: power_user in the household, or superuser if the node has no household.
2. `reset_token = uuid4().hex`, stored **in memory** with a 300 s TTL (`core/pending_resets.py:15-34`).
3. Publish `jarvis/nodes/{id}/factory-reset` `{request_id: reset_token, node_id}`. No `task_id`. Best-effort.
4. Deactivate in jarvis-auth: `DELETE /internal/nodes/{id}`. 404 counts as success; other failures are logged and the delete continues (`admin.py:193-225`).
5. Hard-delete `auth_sessions`, `config_pushes`, `settings_requests`, `settings_snapshots` and the `nodes` row (`node_tasks` cascades; `request_traces.node_id` is set NULL).

The node, without `task_id`, takes the **legacy** path (`mqtt_tts_listener.py:2141-2165`). It first checks `node_id == my node_id`. Then:

- It sends `POST /nodes/verify-reset {node_id, request_id}`, unauthenticated, which **consumes** the token (`admin.py:765-786`, `pending_resets.py:37-50`).
- On 200 it runs `factory_reset()` and reboots. On anything else it aborts.

The **cut** tracked path (`admin.py:469-561`) creates `node_tasks(kind="factory_reset")` and publishes with `task_id`. 409 if one is already in flight. The node then:

1. POSTs status `in_progress`, which is the auth check, with a non-consuming `verify_token`.
2. Wipes.
3. POSTs `success` with the cached token.

On success CC marks `is_active=False` and deactivates in auth (`admin.py:564-623`). Terminal states are idempotent (`:604-606`).

> **Changed by D10:** the tracked path is the one Go keeps, and mobile's delete moves to it. The reset token is stored in SQLite on the `node_tasks` row instead of `core/pending_resets.py`'s in-memory 300 s dict, so a node that comes back later, or a server restart, can still complete the reset. The DELETE + `verify-reset` path above survives only until mobile switches and older node builds are gone.

### 3.5 Node commands topic and the verify pattern

`NodeCommandService._publish` (`node_command_service.py:49-81`):

1. It records `_pending_commands[rid] = {node_id, command, created_at, expires_at: +5 min}` **in memory**.
2. It publishes **one** topic, `jarvis/nodes/{id}/commands`, with the payload `[{"command": <verb>, "details": {...details, "request_id": rid}}]`. The payload is a JSON **array**; the node iterates it (`mqtt_tts_listener.py:2498-2530`).

If MQTT is unavailable it logs and returns the rid anyway. A publish error is logged, not raised.

**Verify pattern (exact sequence)**, for verbs that verify (`measure_ambient_noise`, `action` without `trusted`, `train_adapter`):

```
CC: rid = uuid4; pending[rid] = {node_id, exp=now+5m}; PUBLISH jarvis/nodes/{nid}/commands [{command, details:{…, request_id: rid}}]  (QoS1)
node: on_message → executor → handler: POST /api/v0/commands/{rid}/verify  (X-API-Key, body {})
CC: verify_command(rid, caller_node_id): missing → false; node mismatch → false (entry KEPT);
    expired → delete, false; else delete (one-time) → true          → 200 {"valid": bool}
node: valid → execute; else log + drop silently (no result posted)
node: POST result to verb-specific sink (§3.6)
```

**Which verbs actually verify** is the node's choice, not CC's (`mqtt_tts_listener.py:128-143`, `183`, `203`, `1276`):

> **Changed by D4/D7:** Go never sends `trusted:true`. Command authenticity comes from per-node broker ACLs (only CC may publish to `jarvis/nodes/{nid}/commands`). `train_adapter` is cut (D9). The `routine` verb carries the full definition (D24, doc 08).

| Verb (`commands` topic) | Published by | Node verifies? | Result sink | CC wait |
|---|---|---|---|---|
| `action` | `/nodes/{id}/actions` (`trusted:true`, `node_commands.py:101`), device control (`smart_home.py:1160,1284`, `control_device_tool.py:181`, `trusted:true`) | **only if `trusted` is falsy** | `/device-control-results/{reply_request_id or rid}` `{success, error, input_required?}` | 10 s file poll |
| `tool_call` | `dispatch_node_command` (`trusted:true`) | no | `/device-control-results/{reply_request_id}` `{output:{…}}` | 10 s default (async, 0.1 s) |
| `routine` | `routines.py:548` (`trusted:true`) | no | `/device-control-results/{reply_request_id}` | 20 s (`routines.py:401`) |
| `callback` | `callbacks.py:211`, `proposable_action_service.py:293` | no; the node's `GET /callbacks/{id}` is the verification | `/callbacks/…` (doc 13) | doc 13 |
| `report_tools` | `node_tools.py:82` (`trusted:true`) | no | `/mobile/node-tool-reports/{rid}`, **unauthenticated** (`node_tools.py:105`), written to `/tmp/jarvis-node-tools` | 10 s |
| `enroll_voice` / `verify_voice` | `mobile_voice_profiles.py:216,272` | no | `/mobile/voice-profile-results/{rid}`, **unauthenticated** (`:295`) | mobile polls (doc 06) |
| `update_node_config` | `/nodes/{id}/node-config` | no; the node strips identity, CC-URL and broker keys (`mqtt_tts_listener.py:1107-1110`) | none | fire-and-forget |
| `preview_led_pattern` | `/nodes/{id}/led/preview` | no | none | fire-and-forget |
| `measure_ambient_noise` | `ambient_noise.py:97` | **yes** | `/nodes/{id}/ambient-noise-measurements/{rid}/result` (N) | mobile polls; 300 s in-memory TTL |
| `toggle_command` | `smart_home.py:157` (broadcast to the household) | no | none | – |
| `invalidate_device_cache` | `smart_home.py:760` (broadcast) | no | none | – |
| `device_removed` | `smart_home.py:780` | no | none | – |
| `train_adapter` | `/nodes/{id}/commands` (admin) | yes | none | **CUT with LoRA** |
| `tts` | **nobody in CC** | no | none | dead verb on the node |

### 3.6 Result correlation today (the temp-file pattern)

Every synchronous wait works the same way:

1. Publish.
2. Poll for `<tmpdir>/<dir>/{rid}.json` every 0.1–0.3 s until the deadline.
3. Read it, unlink it, return.

The HTTP sink only writes the file. Directories:

- `jarvis-device-control/`: action, tool_call, routine, camera creds as `creds-{rid}.json`, device state as `state-{rid}.json`.
- `jarvis-k2-provision/` and `jarvis-k2-pending/`.
- `/tmp/jarvis-node-tools/` and `/tmp/jarvis-voice-profile-results/`.

The reason given is "cross-process access" (`smart_home.py:1205`). CC actually runs **one** uvicorn worker (`Dockerfile:16`, `docker-compose.prod.yaml:58`), and `_pending_commands` is in-memory anyway, so the files buy nothing. On timeout the waiter unlinks the file. A result that arrives late is written and never deleted, which is litter.

The command-data and context verbs use real MQTT request/response instead (`core/mqtt_client.py:242-291`):

1. Subscribe to the response topic, then publish.
2. Wait on a `threading.Event`, then unsubscribe.
3. Return `None` on timeout.

### 3.7 Liveness

`Node.is_online()` is `last_seen >= now − 15 min` (`models.py:16,57-62`). `last_seen` is written by:

- the heartbeat (unconditionally, `admin.py:315`);
- every authenticated node HTTP request, debounced to 60 s (`services/node_liveness.py:39-77`, called from `deps.py:180,197,216`);
- a successful command-data MQTT round-trip (`mobile_command_data.py:147-149`). It is **not** written for context-query round-trips.

Liveness writes never raise.

### 3.8 Heartbeat and updates

The heartbeat (`admin.py:296-342`) sends `{version_info:{version,install_mode,git_sha}, is_busy, protocols[], needs_k2, thread_status?, memory?}`. Unknown fields are ignored. All fields are optional; `protocols` is stored as JSON text. The handler then:

1. `reconcile_open_task` (`node_updates.py:342-394`) looks at the newest open update task:
   - If the reported version equals the target → `success`.
   - Else, if it is `dispatched` and ≥30 s since `updated_at` → `in_progress`.
   - Else nothing; `updated_at` is deliberately not bumped.
2. `dispatch_pending_task` (`:309-339`): if the node is not busy and has a `pending` update task (oldest first), set it to `dispatched` and return `pending_update:{task_id,target_version}` in the response.

The update request (`node_updates.py:117-168`):

- 409 if an update task is already open.
- `resolve_target_version` (`services/github_releases.py:498-513`): `"latest"` or None → GitHub `/repos/alexberardi/jarvis-node-setup/releases/latest`, gated by `updates.allow_check` (default **false**, fail-closed, `:429-452`), cached 300 s. An explicit `vX.Y.Z` is used as-is with the `v` stripped and no egress. If the result is None → 503.

The node (`jarvis-node-setup/services/update_service.py:210-290`):

- If `allow_updates` is false → POST `/nodes/tasks/{tid}/status {state:"failed", error_message}`.
- It **silently ignores** the task when an install is already in flight, when `install_mode != tarball`, when the node is busy, or when the target is not newer (no downgrade).
- Otherwise it spawns the installer. Success is inferred from the next heartbeat's version.

`/nodes/tasks/{tid}/status` accepts only `failed`. It does a conditional UPDATE against non-terminal states and returns 409 if the task is already terminal (`node_updates.py:171-221`). Cancel sets `failed` with "Cancelled by user" (`:241-275`).

Sweeper (`main.py:398-454`), every 120 s. It fails a task in any open state if `created_at` is older than 15 min, or if it is `in_progress` and `updated_at` is older than 10 min. The message is `"Timeout: no heartbeat confirming {target_version}"`, and **the sweep does not filter on kind**, so it also sweeps `factory_reset` tasks ("…confirming None").

### 3.9 Ambient noise

The trigger clamps duration to 1–10 s, default 3 (`ambient_noise.py:93-94`). The node verifies, captures, and POSTs `{success, duration_seconds, chunks, p50/p75/p95/max_rms, suggested_silence_threshold, error}`. The result is stored in an in-memory dict with a 300 s TTL. The poll returns `pending` until the result exists; it returns 404 if the stored `node_id` differs from the path.

### 3.10 Traces

The voice pipeline's `latency_logger.py:285` writes the rows (doc 00/01). The admin list returns rows plus `span_count` and `{traces,total}`; detail returns the parsed `spans`. Unparseable JSON becomes `[]` (`traces.py:199-282`).

### 3.11 Public plugin endpoints (transport only)

All four resolve `household_id` from the authenticated node, never the body.

- `/node/push-notification`, `/node/send-link`, `/node/inbox-item` pass through `_attention_gate` (fail-open to legacy delivery, `node_commands.py:245-286`), then the inbox/notification service (doc 13).
- `send-link` rejects non-http(s) URLs with `{sent:false}` (`:529-531`).
- `inbox-item` force-sets `metadata.node_id` (`:663`).
- `/node/llm/chat` sends `{messages[{role∈system|user|assistant,content}], model="live", temperature=0}` → `LLMProxyClient.lightweight_chat` → `{content: choices[0].message.content or ""}`. Exceptions propagate as 500 (`:449-489`).

---

## 4. Data

| Table | Key columns | Lifecycle |
|---|---|---|
| `nodes` (`models.py:19-69`) | `node_id` PK; `api_key` (node_key, plaintext, NOT NULL); `room` NOT NULL; `user`; `voice_mode`; `last_seen`; `adapter_hash` (LoRA, cut); `room_id` FK rooms; `household_id`; `last_seen_version`; `install_mode` (tarball/docker/dev); `git_sha`; `is_busy`; `is_active`; `protocols` (JSON text); `needs_k2` (default true) | Created by register/create. Hard-deleted by DELETE. Soft-deleted by the cut factory-reset path. |
| `node_tasks` (`:72-89`) | `id` uuid; `node_id` FK CASCADE; `kind` (`update`/`factory_reset`); `target_version`; `state` (pending→dispatched→in_progress→success/failed); `error_message`; `created_at`; `updated_at` (ORM onupdate); `finished_at` | Never pruned. |
| `provisioning_tokens` (`:569-588`) | `token_hash` sha256 unique; `node_id`; `household_id`; `room`; `name`; `created_by_user_id`; `expires_at`; `consumed_at` | TTL 600 s. Deleted when expired >24 h, or consumed >24 h ago. |
| `settings_requests` (`:228-251`) | `request_id` uuid4 PK; `node_id` FK; `status` (`pending`/`fulfilled`; "expired" is never written); `expires_at` +5 min | **Never pruned** except by node delete. |
| `settings_snapshots` (`:254-284`) | `snapshot_id`; `request_id` FK; `ciphertext`/`nonce`/`tag` (base64url); `aad_*` | **Never pruned**; ciphertext accumulates. |
| `request_traces` (`:723-743`) | `id`; `conversation_id`; `request_type`; `source`; `node_id` (SET NULL); `household_id`; `user_command`; `assistant_message`; `status`; `error_message`; `total_duration_ms`; `spans_json`; `created_at` | Pruned after `tracing.retention_days` (7). |

**In-memory state, all lost on restart:**

- `_pending_commands` (verify map, 5 min).
- `pending_resets._pending` (300 s).
- ambient `_results` (300 s).
- the node-auth validation cache (60 s).
- the GitHub release cache (300 s).
- the MQTT singleton.

**Files on disk:** the `/tmp` directories in §3.6. K2 key material sits briefly in `/tmp/jarvis-k2-pending`.

---

## 5. Settings

| Key | Default | Effect |
|---|---|---|
| `updates.allow_check` | `false` (`settings_definitions.py:396`) | Gates the GitHub lookup. Read per household for `/nodes/{id}/update` and **globally** (household None) for `/releases/latest`. |
| `tracing.retention_days` | 7 (fallback `main.py:342`) | Trace TTL. **Not declared in `settings_definitions.py`**, so it is not settable from the UI. |

Env: `MQTT_USERNAME`, `MQTT_PASSWORD`, `JARVIS_MQTT_BROKER_URL`, `NODE_AUTH_CACHE_TTL`, `ADMIN_API_KEY`, `JARVIS_APP_ID`/`JARVIS_APP_KEY`, `JARVIS_AUTH_BASE_URL`. These constants are hard-coded:

| Constant | Value |
|---|---|
| Provisioning token TTL | 600 s |
| Settings-request TTL | 5 min |
| Command verify TTL | 5 min |
| Reset-token TTL | 300 s |
| Online threshold | 15 min |
| Liveness debounce | 60 s |
| K2 wait | 15 s |
| Action wait | 10 s |
| Sweeper | 120 s interval / 15 min ceiling / 10 min no-progress |
| Dispatch→in_progress grace | 30 s |

---

## 6. Dependencies

- **jarvis-auth** (Go: the auth module, in-process):
  - `/internal/nodes/register` and `DELETE /internal/nodes/{id}`;
  - `/internal/validate-node`;
  - `/internal/validate-household-access`, whose default role is `power_user` (`deps.py:343-373`);
  - `/auth/me` (provisioning JWT path).
- **MQTT broker** (Mosquitto today; embedded in Go).
- **llm-proxy** `lightweight_chat` (`/node/llm/chat`, model slot `live` by default).
- **notifications** via `inbox_notification_service` (doc 13) and the **attention broker** (doc 10).
- **GitHub** API (optional and gated).
- **Other CC subsystems:** smart home (07) owns `/device-control-results` and `_pick_node_for_protocol`; routines (08), callbacks (13), voice profiles (06), packages (12) and the context provider (10) all ride on `NodeCommandService` and `get_mqtt_client`. The voice pipeline (01) writes `request_traces`.
- No LLM prompts belong to this subsystem.

## 7. Invariants and non-obvious behaviour (preserve)

1. The **`commands` payload is a JSON array** of `{command, details}`, and `request_id` is **injected into `details`**, overriding any caller value (`node_command_service.py:65-68`). `reply_request_id` is a separate field that some verbs use as the result key.
2. Subscribe **before** publish in request/response (`mqtt_client.py:281-285`). Response topics are `{request_topic}/response/{correlation_id}`, and the `correlation_id` is the one already in the payload if present (`mobile_command_data.py:125`).
3. `verify_command` is **one-time**. A node-mismatch returns false but does **not** consume the entry (`node_command_service.py:88-93`).
4. `verify-reset` **consumes** the token; the task status endpoint does **not** (`pending_resets.py:37-68`). The node relies on this to post `in_progress` and then `success` with one token.
5. The verify-reset and factory-reset status endpoints are deliberately unauthenticated or token-only, because the node's key is revoked or wiped by then (`admin.py:575-586`, `760-777`).
6. **DELETE publishes the reset before revoking auth** (`admin.py:414-432`). The node path must still work after its key is gone.
7. Heartbeat **dispatch** happens only when `is_busy` is false. `reconcile_open_task` must **not** bump `updated_at` for an in_progress task that still reports the old version (`node_updates.py:380-394`); the sweeper depends on it.
8. Node-reported task status may only be `failed` (`node_updates.py:61-66`). Success must come from the heartbeat version.
9. Terminal task states are immutable. The status route uses a conditional UPDATE so it can't clobber "Cancelled by user" (`node_updates.py:198-220`).
10. `GET /admin/nodes` hides `is_active=false` unless `include_inactive=true`. Non-superusers silently skip nodes in foreign households, or nodes with no household (`admin.py:253-274`).
11. Nodes never receive key material over MQTT. K2, callback payloads, test-install and package-install are all "nudge + authenticated pull" (`node_settings.py:479-483`).
12. `/node/*` plugin endpoints take household from the node identity. `/node/inbox-item` sets `metadata.node_id` with `setdefault`, so a caller-provided value wins (`node_commands.py:663`).
13. `/settings/requests/{rid}/result` returns **202** with a body while pending, which is not a usual FastAPI shape. Mobile depends on it.
14. Both register and admin create return `NodeCreateResponse {node_id, room, user, voice_mode, node_key}`. Register returns **201** (`provisioning.py:202`), but admin create declares no `status_code`, so it returns **200** (`admin.py:345`). The node accepts 200 or 201 (`registration.py:57`).

## 8. Oddities

1. **The verify pattern is mostly bypassed.** Most verbs carry `trusted:true` or never verify (§3.5 table). `action`'s check is `if not details.get("trusted")` (`mqtt_tts_listener.py:203`), and `trusted` comes from the **MQTT payload**. Anyone who can publish to the broker can therefore:
   - run any node command via `tool_call`, with any `user_id`;
   - rewrite `config.json` via `update_node_config` (identity and URL keys are stripped);
   - toggle commands.

   The comment calls this a workaround for a "multiprocess request_id mismatch" (`:201-202`), but CC is single-process. With one shared broker credential, every node is such a publisher.
2. **Unauthenticated result sinks.** `/device-control-results/{rid}` (`smart_home.py:1200`), `/device-state-results/{rid}`, `/mobile/node-tool-reports/{rid}` and `/mobile/voice-profile-results/{rid}` accept any body from anyone. A guessed or sniffed rid (rids travel in clear on the anonymous broker) can forge results.
3. `create_provisioning_token` never calls `require_household_access`. Any JWT user can mint tokens for any household (`provisioning.py:154-190`; the helper at `:109` is used only by bluetooth).
4. Ambient-noise trigger and poll have **no household check** (`ambient_noise.py:84-106,145`). The settings `/result` poll has no household check either; its docstring claims power_user (`node_settings.py:376-377`).
5. `/k2/ack/{rid}` does not check that the node_id matches the path, and writes a file for any rid.
6. `create_settings_request` skips authz entirely when `node.household_id` is NULL (`node_settings.py:209-210`).
7. The node-auth cache stores **transient failures** (auth unreachable) for 60 s (`deps.py:190-191`). The legacy bare-key path still accepts a header without `node_id:` (`deps.py:211-217`). `is_active` is never checked.
8. The sweeper does not filter on `kind`, so factory-reset tasks get the "no heartbeat confirming None" message (`main.py:420-443`).
9. **The update dispatch can strand.** A task goes to `dispatched` on heartbeat, but the node silently ignores it if it is busy, non-tarball, a downgrade, or already in flight. Only the sweeper ends it, 15 min later, with a misleading message. Docker nodes can request updates that can never succeed.
10. A factory reset queued to an offline node (persistent session) arrives after the 300 s in-memory token TTL, or after a CC restart. The node aborts, and it stays configured but with its auth revoked: a zombie that needs manual re-flash.
11. `include_values` and `user_id` live only in MQTT, so the backstop downgrades a secret-sync request to a plain view (`mqtt_tts_listener.py:1426-1430`).
12. `get_mqtt_client()` checks `is_connected`, which only becomes true in the async `on_connect`. Two calls in quick succession after a fresh connect can tear down and rebuild the client (`node_settings.py:47-69`).
13. `NodeResponse` and `NodeUpdate` still expose `adapter_hash` (LoRA).
14. node-setup `authorize_node.py` calls `GET` and `DELETE /admin/nodes` with the admin key, but those routes require a JWT, so they always 401. Only POST and PATCH work.
15. **CC CLAUDE.md is stale.** It says node updates are "MQTT-dispatched" (they ride the heartbeat), that `/admin/nodes` CRUD uses the "admin token" (GET and DELETE are JWT), and that "MQTT is the only async server→node channel" (the heartbeat response is a second one).
16. `settings_requests` and `settings_snapshots` are never pruned. Status `"expired"` is never written.
17. Node-side `bluetooth-release` / `bluetooth-auto-connect` have no publisher, and the `tts` command verb has no publisher.

## 9. Tests

- **CC:**
  - `test_provisioning_tokens.py` (388 lines)
  - `test_register_node_with_auth.py`
  - `test_node_settings.py` (673 lines: requests, snapshots, MQTT format, K2 pull one-time/mismatch)
  - `test_factory_reset.py` (the cut task path and inactive filtering)
  - `test_node_task_status.py` (status and heartbeat reconcile)
  - `test_updates_gate.py`
  - `test_node_liveness.py`
  - `test_node_mqtt_credentials.py`
  - `test_node_command_dispatch.py`
  - `test_node_inbox_item.py`
  - `test_admin_traces_auth.py`
  - `test_household_authz.py`
  - `test_mobile_command_data.py` and `test_context_provider_client.py` (request/response)
- **Gaps:** nothing tests `verify_command` itself, the DELETE→verify-reset path, the K2 15 s wait/ack round trip, `/device-control-results`, or the sweeper.

**Golden and contract candidates:**

1. **MQTT catalogue** (§2.4): a fake node subscribed to `jarvis/nodes/{nid}/#` records the exact topic, payload bytes and QoS for each triggering route. This needs a fake-node harness on the Python stack first.
2. Provision → register → `mqtt-credentials` → heartbeat → list (online, `needs_k2`).
3. Settings request round trip, including the 202 body and the 410 cases.
4. K2 relay with a fake node: success, spoof (404, then ack false → 502), and timeout (504 detail text).
5. The update state machine: dispatch on heartbeat; 30 s → in_progress; version match → success; refusal; cancel; 409s.
6. DELETE → reset message → verify-reset (200 once, 404 on replay).
7. Verify: valid once, replay false, node-mismatch false.

## 10. Questions for the user

1. **[behaviour] Should the Go port make MQTT-delivered commands actually trustworthy?** Today `trusted:true` in the payload skips verification, and most verbs never verify (§8.1). With one shared broker credential, any node, or anyone holding that credential, can make any node run arbitrary commands as any user. *Why it matters:* it decides the broker auth design and whether the node needs changes. Options:
   - (a) Keep it byte-identical; this is the frozen contract.
   - (b) Use embedded-broker ACLs: per-node credentials, where a node may subscribe only to `jarvis/nodes/{self}/#` and `jarvis/auth/+/ready`, and publish only to its own `…/response/#`. CC is the only other publisher. This needs no node code change if the credential handout becomes per-node (the `/node/mqtt-credentials` shape is unchanged).
   - (c) (b) plus a later node change to always verify.

   **My recommendation: (b)** for the port, which closes the hole server-side without touching the node, then (c) post-port.

   **Decided (D4/D7):** (b). Per-node broker credentials and ACLs; `trusted:true` removed. Commands are authentic by construction, so a node-side always-verify change is not required.
2. **[behaviour] Should the unauthenticated result sinks get authentication?** These are `/device-control-results`, `/device-state-results`, `/mobile/node-tool-reports` and `/mobile/voice-profile-results`. Nodes already send `X-API-Key` on these POSTs via RestClient. Options:
   - (a) Port them unauthenticated.
   - (b) Require node auth and match the rid to the node it was issued to.

   **My recommendation: (b).** Accept the node key when present and bind rid→node_id in the in-process waiter map. First confirm that RestClient always attaches the key.

   **Decided (D4):** (b). Node auth required, and the rid must belong to that node.
3. **[scope] Which factory-reset flow survives?** The live path is DELETE plus unauthenticated `verify-reset`. The tracked task path (`POST …/factory-reset` and `/nodes/factory-reset/{tid}/status`) has no caller but is fully implemented on the node. Options:
   - (a) Port only DELETE + verify-reset, and cut both task routes.
   - (b) Switch DELETE to publish a `task_id`, so the node uses the status path and the hard-delete happens after `success` or a timeout.
   - (c) Port both.

   **My recommendation: (a)** for the port (zero client change). Consider (b) later for the zombie problem.

   **Decided (D10):** the tracked flow only (`POST …/factory-reset` + task status). Mobile's delete switches to it (mobile change). DELETE + `verify-reset` is dropped once mobile switches; `verify-reset` stays only while older node builds need it.
4. **[behaviour] What should happen to a delete or reset queued for an offline node** after the 300 s in-memory token expires, or after a restart (§8.10)? Options:
   - (a) Same as today: the node becomes a zombie.
   - (b) Persist reset tokens in SQLite with a longer TTL, e.g. 7 days, consumed once.

   **My recommendation: (b).** It costs little and fixes the restart loss as well.

   **Decided (D10):** (b). The reset token is persisted in SQLite with the task, so an offline node or a restart can still complete the reset.
5. **[behaviour] Should the update dispatch stay stuck until the sweeper?** When a node silently ignores a dispatched update (busy, docker, downgrade), the task waits 15 min. Options:
   - (a) Keep it.
   - (b) Reject at request time when `install_mode != "tarball"` or the target ≤ `last_seen_version` (400 with a reason).
   - (c) (b), plus re-dispatch on a later heartbeat if the node was busy.

   **My recommendation: (b).** It needs no node change and gives honest errors.

   **Decided (D40 default):** (b). A dispatch that can't succeed is rejected with 400 at request time.
6. **[behaviour] Should provisioning-token minting check household membership?** It currently doesn't (§8.3), and neither does the ambient-noise trigger/poll or the settings `/result` poll (§8.4). Should the Go port enforce membership as a "fix while porting", even though that changes behaviour for any caller relying on it? **My recommendation:** yes, enforce member (power_user for settings), and pin it with a contract test.

   **Decided (D4/D5):** yes. Provisioning checks membership of the *target* household among all of the caller's memberships. Ambient-noise trigger/poll and the settings `/result` poll get the household check. Members are not otherwise restricted on their own household.
7. **[behaviour] Should the embedded broker persist sessions across `jarvisd` restarts?** Nodes depend on `clean_session=False` offline queuing, plus the HTTP backstop that exists only for settings. mochi-mqtt keeps sessions in memory unless a storage hook is added. Options:
   - (a) In-memory only; a restart drops queued messages, as a Mosquitto restart does today.
   - (b) Add a SQLite persistence hook.

   **My recommendation: (a)** plus generalising "pull on reconnect" later. Every nudge already has an HTTP pull behind it, except commands.

   **Decided (D40 default):** (a). Broker sessions in memory.
8. **[scope] Should `include_values` and `user_id` be persisted on the settings request?** That would let the reconnect backstop honour secret-sync (§8.11). It would also need a node change to read them from `GET …/settings/requests`. **My recommendation:** add the columns now (harmless), and expose them in the list response. The node can adopt them whenever.

   **Decided (D40 default):** add the columns and expose them in the list response.
9. **[scope] Should `/admin/nodes/{id}/commands` be cut?** Its only caller is jarvis-admin's train-adapter, which is LoRA. **My recommendation:** cut it along with the `train_adapter` verb, and also drop `adapter_hash` from `NodeResponse`/`NodeUpdate`. The extra JSON field is harmless if mobile ignores it; I would verify that first.

   **Decided (D9):** cut the route and the `train_adapter` verb (LoRA). `adapter_hash` goes with LoRA; verify mobile ignores it before dropping the field.
10. **[behaviour] Should legacy bare-key node auth and the negative caching of auth outages be kept?** In-process auth makes outages impossible, so the caching question goes away. The real decision is whether the bare-`node_key` header (no `node_id:`) is still needed by any deployed node. **My recommendation:** drop it, and check the jarvis-dev and prod nodes' headers first.

   **Decided (D40 default, verify first):** drop bare-key auth after checking the jarvis-dev and prod nodes' headers; no negative caching.
11. **[minor] Should settings requests and snapshots be pruned?** Today they grow forever. **My recommendation:** delete after 24 h in the token-cleanup loop.

   **Decided (D40 default):** delete after 24 h in the token-cleanup loop.
12. **[minor] Should `tracing.retention_days` be declared?** It is read but not defined in the settings definitions. Declare it (default 7) or hard-code it? **My recommendation:** declare it.

   **Decided (D11):** hard-code it. The key is verified never set, so trace retention is a fixed 7 days and the setting check goes away.

## 11. Go port notes

- **Shape.** `cc/nodes` owns a `Registry` (SQLite `cc_nodes`, `cc_node_tasks`, `cc_provisioning_tokens`, `cc_settings_requests`, `cc_settings_snapshots`, `cc_request_traces`), a `Bus`, an `Updates` service and the HTTP handlers. Other CC modules depend on a `NodeBus` interface:

  ```go
  type NodeBus interface {
      Publish(nodeID, sub string, v any) error                  // QoS1, no retain
      Command(nodeID, verb string, details map[string]any) (rid string)
      CommandAwait(ctx, nodeID, verb string, details map[string]any, replyKey string) (json.RawMessage, error)
      Request(ctx, nodeID, sub string, payload map[string]any) (json.RawMessage, error) // command-data/context
      VerifyCommand(rid, nodeID string) bool
  }
  ```

- **Replace the temp-file correlation** (Phase 5a) with an in-process `waiters map[rid]chan json.RawMessage`, using `sync.Mutex` and TTL cleanup. The HTTP sinks (`/device-control-results`, `/k2/ack`, `node-tool-reports`, `voice-profile-results`, `device-state-results`, camera creds) deliver to the channel; a late result with no waiter is dropped. The K2 pending key also becomes a map entry, with no disk. This removes every `/tmp` directory and the 0.1–0.3 s poll latency.
- **Embedded broker** (mochi-mqtt):
  - Implement an auth hook against CC's credential.
  - Embedded mode lets CC publish directly via `server.Publish` and receive response topics through an inline client or hook. Still keep exact topic strings and QoS 1, because nodes match by suffix.
  - It must support persistent sessions, `clean_session=False` and offline QoS-1 queuing (mochi does, in memory).
  - It must serve WebSocket for `ws://` and `wss://` broker URLs.
  - ACLs implement Q1(b) (D4): per-node credentials issued via `/node/mqtt-credentials` (shape unchanged); a node may subscribe only to `jarvis/nodes/{self}/#` and `jarvis/auth/+/ready`, and publish only its own responses. Nothing publishes `trusted:true`. Consequence for frozen nodes: the node verifies any `action` without `trusted` (`mqtt_tts_listener.py:203`), so `VerifyCommand` and `POST /commands/{rid}/verify` stay, and every `action` publish must record its pending entry first.
  - Sessions stay in memory (D40, Q7).
  - **Built (5a):** the broker credential is username = `node_id`, password = the node's own `node_key`, validated in process against the auth module with CC's service grant. Nothing extra is stored; deactivation and key rotation apply on the next connect, and a node with a stale password self-heals via CONNACK 4/5 → `/node/mqtt-credentials`. The registry advertises the broker as `jarvis-mqtt-broker` (scheme `mqtt`). Env: `JARVIS_MQTT_ADDR` (default `:1884`), `JARVIS_MQTT_WS_ADDR` (default `:9883`), `""` disables; `JARVIS_MQTT_ALLOW_ANONYMOUS=1` for dev only.
- **Pending maps** (verify, ambient results) become typed maps with expiry. **Reset tokens live in SQLite on the `node_tasks` row (D10)**, consumed per §7.4.
- **Factory reset (D10):** port the tracked flow (`POST /admin/nodes/{id}/factory-reset`, single-in-flight 409, `/nodes/factory-reset/{task_id}/status`, sweeper timeout). Keep DELETE + `verify-reset` only as a transition path for mobile and older node builds; plan its removal.
- **Result sinks (D4):** require node auth and check the rid's issuing node in the waiter map before delivering. `/k2/ack` checks the path node (D8). `/nodes/{id}/actions` passes `input_required` through (M10). No camera-creds waiter in v1 (D29).
- **Household checks (D4/D5):** provisioning-token minting, ambient-noise trigger/poll and the settings `/result` poll check membership of the target household among all the caller's memberships. Settings create no longer skips authz on a NULL household (D8).
- **Updates (D40, Q5):** reject at request time (400 with a reason) when `install_mode != "tarball"` or the target ≤ `last_seen_version`.
- **Cut:** `/nodes/{id}/commands` and `train_adapter` (D9), `adapter_hash` (LoRA), `/api/v0/chat` (D5: `/node/llm/chat` replaces it for node `chat_text()`).
- **Settings tables (D40, Q8/Q11):** add `include_values` and `user_id` columns to settings requests and expose them in the list; prune requests and snapshots after 24 h.
- **Long-poll handlers.** The K2 15 s and action 10 s waits become `select` on channel / `ctx.Done()` / timer. Keep the exact status codes (504 and 502 detail strings for K2; for actions, 200 with `status:"timeout"`).
- **Loops** go to the platform scheduler as trigger kinds (D27): token+settings cleanup hourly, trace TTL hourly (fixed 7 days, D11), and the task sweeper every 120 s. The sweeper writes a kind-specific message (D8), since factory-reset tasks are now live (D10).
- **Auth.** Node validation is an in-process call to the auth module. The 60 s cache can stay for parity, but it should not cache "unavailable". Bare-key auth is dropped once node headers are verified (D40, Q10).
- **Liveness.** Keep the 60 s debounce. Consider also touching `last_seen` on context-query round-trips, for consistency with the command-data round-trips.
- **Risks:**
  - Topic suffix ordering on the node means a new topic ending in an existing suffix would be misrouted, so freeze the catalogue (§2.4).
  - The 202-with-body and 201/200 status quirks (§7.13–14).
  - The sync handlers that sleep today (K2, actions) must not hold the SQLite writer connection while waiting.
