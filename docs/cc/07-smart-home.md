# 07 — Smart home

Scope: rooms, the device registry, device control and state, scans and device lists, encrypted config push, cameras (go2rtc and HLS), Bluetooth, provider OAuth, and the two device server tools.

CC source, all under `app/`:

| File | Lines |
|---|---|
| `api/smart_home.py` | 2116 |
| `api/cameras.py` | 387 |
| `api/hls_packager.py` | 266, **not wired** |
| `api/bluetooth.py` | 540 |
| `api/oauth.py` | 636 |
| `core/tools/control_device_tool.py` | 207, **disabled** |
| `core/tools/get_ha_entities_tool.py` | 205 |

All routers are mounted under `/api/v0` (`main.py:715,790,794,802`).

Line references without a path are to `api/smart_home.py`. Other repos are abbreviated:

- `NS:` = jarvis-node-setup
- `MOB:` = jarvis-node-mobile/src
- `PKG:` = the package checkouts in the scratchpad `pkgs/` directory

---

> **Correction (2026-10-06, D9):** the external device manager feature is live. `smart_home.use_external_devices` and `smart_home.device_manager` are written by `PUT smart-home/config` (`smart_home.py:74-127`) and drive the mobile external-devices UI, so they are **not** dead settings. Only the `control-external` *route* is cut.

## 0. Decisions applied (2026-10-06)

Source: `QUESTIONS.md`. Sections below still describe today's Python behaviour; inline "Changed by" notes and §11 say what Go does instead.

- **D1:** Caddy is not ported. External OAuth (e.g. Nest) goes through the cloud relay bounce; local providers (HA) use CC's own `/oauth/callback` over LAN HTTP.
- **D4 (security, wire shapes kept):** node auth on routes 13 and 16, with the rid bound to the issuing node; node-id == path on result uploads (18, 21, Bluetooth 159, 314); household check on `config/push`, OAuth session create (target node in the caller's household), `exchange`/status, and the Bluetooth polls; the OAuth `exchange_url` SSRF is fixed (URLs come from server-side config only). `trusted:true` is removed; per-node broker credentials and ACLs make commands authentic (D7).
- **D5:** household checks consider **all** of the caller's memberships, not just the JWT's active household. Members are not otherwise restricted.
- **D6:** config push `pending`/`ack` (routes 24, 25) require node auth, with the node bound to `{node}` in the path. Real nodes already send `X-API-Key`.
- **D8:** known bugs fixed as intended differences; this includes adding the missing Bluetooth `release` and `auto-connect` routes (Q4).
- **D9:** `POST /devices/control-external` (route 14) is cut. The external device manager feature (`smart_home.use_external_devices`, `smart_home.device_manager`, device listing, `ExternalDeviceDetailScreen`) is **live and kept**. The disabled `ControlDeviceTool` and `ambient_grounding.py` are cut.
- **D20:** rooms, devices and other household-owned smart-home data survive a user's account deletion.
- **D27:** retention sweeps run as trigger kinds on the one scheduler engine.
- **D28:** the device model is ported **as-is**: same routes, the `devices` table, the node's external device manager dual mode, and `/devices/import`. Source of truth revisited after cutover.
- **D29:** **cameras are deferred** until after the port. No go2rtc or ffmpeg in v1. Camera device rows import and stay listed; the stream/HLS routes return a clear "not available" error.
- **D40 (B defaults):** OAuth at-rest key is a dedicated generated key (Q9); retention sweeps for request tables and consumed pushes (Q10); stream naming deferred with cameras (Q12).
- **M7:** drop `smart_home.use_home_assistant`, the voice Bluetooth deep-link branch and `ControlDeviceTool`; keep `smart_home.device_manager`. **M8:** voice control on a node lacking the protocol stays status quo; forwarding is post-port. **M9:** node selection = protocol match → primary → online/most recent `last_seen`; fail fast 503 instead of a 10 s timeout.

## 1. Purpose

CC is the household's **registry and switchboard** for smart-home things. It is **not** the thing that talks to devices: every LAN or cloud protocol call happens on a node, inside a `jarvis-device-*` protocol plugin (hue, kasa, lifx, nest, govee, homekit, apple, homeconnect, schlage, simplisafe, resideo, zwave). CC's jobs:

1. **Store the household topology.**
   - `rooms` (hierarchical) and `devices` (one row per entity).
   - Mobile edits these.
   - Nodes read the device list to seed their local control cache.
   - Voice prompts read the room hierarchy.
2. **Relay control and state requests from mobile to a node.** The relay is synchronous over HTTP and asynchronous over MQTT, and it picks a node that has the device's protocol installed.
3. **Broker node-side discovery** (device-scan, device-list) and Bluetooth jobs, using a request/poll pattern for mobile.
4. **Relay K2-encrypted config blobs** from mobile to a node. CC never sees the plaintext.
5. **Act as the OAuth redirect authority** for provider integrations (Home Assistant, Google Nest, …):
   - build authorize URLs (PKCE and state)
   - exchange codes
   - hold tokens briefly, encrypted
   - hand them to the node once
6. **Cameras:**
   - get a go2rtc source string from the node's protocol plugin
   - register it in go2rtc
   - proxy HLS to mobile with JWT plus household authorization

Users of the subsystem:

| User | What it does here |
|---|---|
| Mobile | Nearly everything |
| Node (mqtt_tts_listener + services) | Result callbacks, `/node/devices`, config pending/ack, credentials |
| Voice pipeline | Room hierarchy injection; the `get_ha_entities` server tool |
| Mobile chat | DB devices injected as agent context |
| Node setup-mode web UI | Proxies `GET rooms` |
| HA integration package | Tries `GET rooms`; see Q7 |

## 2. Entry points

Auth key:

- **P** = `verify_provisioning_auth` (`provisioning.py:84`). It accepts an admin key in `X-API-Key`, or a user JWT. A JWT caller is then checked with `require_household_access` (`provisioning.py:109`), which round-trips to jarvis-auth via `verify_household_role` (`deps.py:343`) with role ≥ member. Admin-key callers bypass the household check.
- **N** = node `X-API-Key node_id:key` (`verify_api_key`).
- **none** = unauthenticated.

### 2.1 Routes: `smart_home.py`

| # | Method and path | Auth | Live caller | Line |
|---|---|---|---|---|
| 1 | GET `/households/{hh}/smart-home/config` | P+hh | MOB NodeSelector, Devices, Routines, Settings | 60 |
| 2 | PUT `/households/{hh}/smart-home/config` | P+hh | MOB Settings | 93 |
| 3 | GET `/households/{hh}/rooms` | P+hh | MOB; NS `setup_mode.py:437`; HA pkg `command.py:1069` (sends a **node** key, so it 401s; Q7) | 523 |
| 4 | POST `/households/{hh}/rooms` | P+hh | MOB (RoomManagement, DeviceRoomAssignment) | 534 |
| 5 | PATCH `/households/{hh}/rooms/{id}` | P+hh | MOB | 576 |
| 6 | DELETE `/households/{hh}/rooms/{id}` | P+hh | MOB | 624 |
| — | GET `/households/{hh}/rooms/tree` | P+hh | **none → CUT** | 641 |
| 7 | GET `/households/{hh}/devices?room_id&recursive&domain&source` | P+hh | MOB (lists, routine param pickers) | 688 |
| 8 | POST `/households/{hh}/devices/import` | P+hh | MOB discovery/assignment; NS `scripts/scan_devices.py` (admin key) | 797 |
| 9 | PATCH `/households/{hh}/devices/{id}` | P+hh | MOB DeviceEdit | 886 |
| 10 | DELETE `/households/{hh}/devices/{id}` | P+hh | MOB | 956 |
| — | POST `/households/{hh}/devices/assign-rooms` | P+hh | **none → CUT** | 976 |
| 11 | GET `/node/devices` | N | NS `direct_device_service.py:102` | 1015 |
| — | GET `/node/devices/{entity_id:path}` | N | **none → CUT** | 1054 |
| 12 | POST `/households/{hh}/devices/{id}/control` | P+hh | MOB `device-controls/*` | 1098 |
| 13 | POST `/device-control-results/{rid}` | **none** | NS `mqtt_tts_listener.py:290,991` | 1200 |
| 14 | POST `/households/{hh}/devices/control-external` | P+hh | **MOB function defined, no screen calls it** (Q5). **CUT (D9)** | 1229 |
| 15 | GET `/households/{hh}/devices/{id}/state` | P+hh | MOB DeviceControlPanel | 1332 |
| 16 | POST `/device-state-results/{rid}` | **none** | NS `device_state_handler.py:160` | 1420 |
| 17 | POST `/nodes/{node}/device-scan/request` | P+hh(node) | MOB DeviceDiscovery | 1473 |
| 18 | POST `/nodes/{node}/device-scan/{rid}/results` | N | NS `device_scan_handler.py:88` | 1514 |
| 19 | GET `/nodes/{node}/device-scan/{rid}` | P+hh | MOB | 1554 |
| 20 | POST `/nodes/{node}/device-list/request` | P+hh(node) | MOB DevicesScreen (external mode) | 1727 |
| 21 | POST `/nodes/{node}/device-list/{rid}/results` | N | NS `device_list_handler.py:113` | 1771 |
| 22 | GET `/nodes/{node}/device-list/{rid}` | P+hh | MOB | 1816 |
| 23 | POST `/nodes/{node}/config/push` | P, **no hh check** | MOB `services/configPushService.ts` | 1971 |
| 24 | GET `/nodes/{node}/config/pending` | **none** | NS `config_push_service.py:91` | 2019 |
| 25 | POST `/nodes/{node}/config/{push}/ack` | **none** | NS `config_push_service.py:449` | 2062 |

### 2.2 Routes: `cameras.py`

> **Changed by D29:** cameras are deferred. In v1 the list route still lists imported camera rows; the stream start/stop and HLS proxy return a clear "not available" error; `camera-credentials` is not published or awaited.

| Method and path | Auth | Caller | Line |
|---|---|---|---|
| GET `/households/{hh}/cameras` | P+hh | MOB `cameraApi.ts:25` | 185 |
| POST `/households/{hh}/cameras/{device_id}/stream` (empty body) | P+hh | MOB CameraViewScreen | 221 |
| POST `/camera-credentials/{rid}` (UUID-validated) | N | NS `camera_credentials_handler.py:94` | 289 |
| DELETE `/households/{hh}/cameras/{device_id}/stream` | P+hh | MOB, on unmount | 313 |
| GET `/cameras/stream/{stream_name}/{path:path}` | P + hh of the stream | MOB expo-av, Bearer header | 339 |

### 2.3 Routes: `bluetooth.py`

| Method and path | Auth | Line |
|---|---|---|
| POST `/nodes/{node}/bluetooth-scan/request` `{role, source, user_id}` | P+hh(node) | 101 |
| POST `/nodes/{node}/bluetooth-scan/{rid}/results` | N | 159 |
| GET `/nodes/{node}/bluetooth-scan/{rid}` | P, **no hh check** | 199 |
| POST `/nodes/{node}/bluetooth/pair` `{mac_address, role}` | P+hh(node) | 263 |
| POST `/nodes/{node}/bluetooth/pair/{rid}/results` | N | 314 |
| GET `/nodes/{node}/bluetooth/pair/{rid}` | P, **no hh check** | 353 |
| POST `/nodes/{node}/bluetooth/disconnect` → 202 | P+hh(node) | 402 |
| POST `/nodes/{node}/bluetooth/discoverable` → 202 `{timeout_seconds:120}` | P+hh(node) | 426 |
| GET `/nodes/{node}/bluetooth/status` | P+hh(node) | 449 |

Mobile **also** calls two routes that do not exist in CC, so they return 404 today (Q4):

- `POST /nodes/{node}/bluetooth/release` with `{mac_address, forget}` (`MOB api/bluetoothApi.ts:155`)
- `POST /nodes/{node}/bluetooth/auto-connect` with `{mac_address, enabled}` (`:174`)

The node already handles the `bluetooth-release` and `bluetooth-auto-connect` topics (`NS mqtt_tts_listener.py:2439,2443`).

### 2.4 Routes: `oauth.py`

| Method and path | Auth | Caller | Line |
|---|---|---|---|
| POST `/oauth/sessions` | P, **no hh check on node_id** | MOB `authSessionApi.ts:34` | 240 |
| GET `/oauth/callback?code&state` | none (state is the capability) | browser redirect, direct mode | 368 |
| POST `/oauth/sessions/{id}/exchange` `{code}` | P, **no ownership check** | MOB, after relay bounce or native redirect | 478 |
| GET `/oauth/sessions/{id}` | P, no ownership check | MOB poll | 572 |
| GET `/oauth/provider/{provider}/credentials` | N (scoped to the calling node) | NS `mqtt_tts_listener.py:1535` | 595 |

### 2.5 MQTT topics published by CC

The node subscribes to its node topic plus `jarvis/auth/+/ready` (`NS mqtt_tts_listener.py:1486,1490`).

| Topic | Payload | Published from |
|---|---|---|
| `jarvis/nodes/{n}/commands` | `[{command:"action", details:{command_name:"control_device", action_name, context:{…device…, **data}, trusted:true, reply_request_id, request_id}}]` | 1160, 1284 via `node_command_service.py:64` |
| `jarvis/nodes/{n}/commands` | `toggle_command {command_name:"control_device", enabled}`, sent to **every** active household node | 157 |
| `jarvis/nodes/{n}/commands` | `invalidate_device_cache {}`, sent to every active household node | 760 |
| `jarvis/nodes/{n}/commands` | `device_removed {entity_id, protocol, domain, cloud_id, local_ip, mac_address, name}`, sent to the protocol node | 780 |
| `jarvis/nodes/{n}/device-state` | `{request_id, entity_id, domain, protocol, source, cloud_id, local_ip, mac_address}` | 1374 |
| `jarvis/nodes/{n}/device-scan` | `{request_id}` | 1667 |
| `jarvis/nodes/{n}/device-list` | `{request_id, manager_name:"all"}` | 1956 |
| `jarvis/nodes/{n}/config/push` | `{push_id, config_type, node_id}` | 2105 |
| `jarvis/nodes/{n}/camera-credentials` | `{request_id, protocol (default "nest"), cloud_id, entity_id, domain}` | `cameras.py:134` |
| `jarvis/nodes/{n}/bluetooth-{scan,pair,disconnect,discoverable}` | see 2.3 | `bluetooth.py:517` |
| `jarvis/auth/{provider}/ready` | `{provider, node_id, session_id, user_id}`; broadcast, and the node filters on `node_id` (`NS :1517`) | `oauth.py:219` |

### 2.6 Server tools and internal callers

- **`get_ha_entities`** is auto-registered: `tool_registry.py:55` walks `core/tools/`.
- **`control_device`** (server) has `enabled=False` (`control_device_tool.py:35`), so it is never registered. On a node, `control_device` is a **client** tool (`NS commands/control_device_command.py`).
- **Room hierarchy injection:**
  - voice `conversation/start`: `main.py:975-993`
  - mobile chat: `mobile_chat.py:172-185`
  - both inject `node_context.room_hierarchy` only if any room has a parent.
- **Mobile chat** injects every active DB device as `agents["home_assistant"].device_controls` with `state:"unknown"` (`mobile_chat.py:190-209`).
- **Node heartbeat** writes `nodes.protocols` (`admin.py:325`; chapter 05). Node routing reads it.
- **Node delete** removes `AuthSession` and `ConfigPush` rows (`admin.py:435`).
- **No background loops.** Nothing sweeps the request tables or `auth_sessions` (§8).

## 3. Behaviour

### 3.1 Who is the source of truth for devices

| Data | Truth | Notes |
|---|---|---|
| Room list and hierarchy | **CC `rooms`** | `ha_area_id` is stored on create only; it is never synced. |
| "Registered" devices: name, room, identity (`entity_id`, protocol, IP, MAC, `cloud_id`) | **CC `devices`** | Written only by `/devices/import` (from a mobile scan, or a node CLI) and by PATCH and DELETE. |
| Which node can drive a protocol | node, reported via heartbeat → `nodes.protocols` | Used by `_pick_node_for_protocol` (417). |
| Protocol code, credentials, OAuth tokens, K2 secrets | **node only**: plugin plus local secrets DB | CC holds OAuth tokens only between exchange and the node's pull. |
| Live device state | the device, via the node; **never stored** | `/state` is a live relay. Mobile chat sends `"unknown"`. |
| HA entities (HA package installed) | **Home Assistant** | The node's HA agent ships them in `node_context.agents.home_assistant` at `conversation/start`. CC does not persist them. |
| "External devices" view (`use_external_devices=true`) | node device managers, live | `/device-list` aggregates every manager on the node (`NS device_list_handler.py:38`). The DB is used only to annotate `already_registered`, room. |

What the node caches:

- `DirectDeviceService` keeps **only `source == "direct"`** rows from `/node/devices` (`NS direct_device_service.py:112`).
- Imported HA rows (`source="home_assistant"`) are visible to mobile and the cameras list but are never driven by the built-in path. The HA package's own `control_device` handles them.

### 3.2 Device control, end to end

**Voice.** CC is *not* on the device path.

1. At `conversation/start` the node lists `control_device` among its client tools. The built-in one comes from `NS commands/control_device_command.py`; the HA package's replaces it when `use_external_devices` toggles the built-in off (143-165).
2. The LLM emits `control_device(device_name, action, entity_id?, value?, color?)`. CC returns the tool call (chapter 01/02).
3. The node runs it **locally**:
   - `ControlDeviceCommand.run` (`NS :228`) resolves the device via the `DirectDeviceService` cache.
   - The cache is seeded from `GET /node/devices` (11), refreshed every 5 min by `DeviceDiscoveryAgent`, and invalidated by the `invalidate_device_cache` command.
   - It then calls `adapter.control()` on the device's protocol (`NS direct_device_service.py:146-200`).
4. The node continues the conversation with the result.

Note: the node that heard the voice executes it. **Protocol-based node routing does not apply**, so if that node lacks the plugin the result is `"No adapter for protocol"` (`NS :174`). See Q6.

**Mobile.** A synchronous relay through CC.

1. Route 12 loads the device. It returns 404 if missing and 400 if `!is_controllable` (1122).
2. It picks a node with `_pick_node_for_protocol(db, hh, protocol)` (1126). Note that it does **not** pass `primary_node_id`, while control-external does (1253).
3. If `get_mqtt_client()` is None, it returns **503** (1134).
4. It publishes `action` on `jarvis/nodes/{n}/commands` with `trusted:true` and `reply_request_id=request_id` (1141-1160).
5. It **blocks the worker thread**, polling `/tmp/jarvis-device-control/{rid}.json` every 100 ms (1166-1178). The timeout is **20 s if the action starts with `"pair"`, else 10 s**.
6. The node:
   - skips `_verify_command` because `trusted` is set (`NS mqtt_tts_listener.py:203`)
   - with no `control_device` command loaded, it dispatches to the protocol directly (`:221`); otherwise it calls `cmd.handle_action(action, context)`
   - POSTs `{success, error, input_required?}` to route 13 (`:277-297`)
7. Route 13 writes the JSON file, with no auth (1206).
8. On timeout the response is **200** `{success:false, error:"Timed out waiting for node response"}`, not a 5xx (1186).
9. `input_required` is passed through, e.g. the Apple TV PIN for `pair_start` followed by `pair_finish`.

Mobile's axios timeout is 15 s (`MOB smartHomeApi.ts:141`), shorter than CC's 20 s pair window.

**Node choice** (417-459):

1. Active household nodes whose `protocols` JSON contains the protocol. Within those, prefer `primary_node_id`, else the most recent `last_seen`.
2. Otherwise `primary_node_id`.
3. Otherwise `nodes[-1]`. This is SQL order with no ORDER BY, so it is effectively arbitrary.

Online status is **not** considered: an offline node is a 10 s timeout.

> **Changed by D4/M9:** Go publishes control and state without `trusted:true` (per-node broker ACLs make the command authentic). Node choice is one policy for control and state: protocol match → `primary_node_id` → online, most recent `last_seen`; with no online node the route fails fast with 503 instead of a 10 s timeout.

**State** (route 15, 1332-1417):

- Same shape on the `device-state` topic. The file is `state-{rid}.json`, with a fixed 10 s timeout.
- `domain` = the column, or the `entity_id` prefix.
- The response is `{entity_id, domain, state, ui_hints, error}`.

### 3.3 Scans, device lists and Bluetooth (request/poll)

All of these share one pattern (1473-1963, `bluetooth.py`):

1. **Mobile POSTs a request.** The node is looked up (404 if missing) and household-checked. A row is created with `status=pending` and `expires_at = now + 2 min`. CC publishes MQTT; if MQTT is down it only logs, and the row stays pending until it expires.
2. **The node POSTs results.**
   - Auth: node key. There is **no check that the authenticated node is the one in the path**.
   - The row is matched by `(id, node_id)`: 404 if missing; 410 and `status=expired` if past `expires_at`.
   - If `error` is set the status becomes `failed`; otherwise `completed`, with `results_json` and `device_count`.
3. **Mobile polls.**
   - A pending row past its deadline becomes `expired` → 410.
   - Pending → `{status:"pending"}`.
   - Failed → `error_message`.
   - Completed → enriched results.

Enrichment of completed results:

- **Device scan** (1597-1648): each raw device is matched against active DB devices by `entity_id`, then `cloud_id`, then lowercased MAC. That sets `already_registered` and `existing_device_id`. `supported_actions` passes through from the node.
- **Device list** (1861-1935): the same matching, plus the matched device's room id and name. `supported_actions` is recomputed by CC from `_PROTOCOL_ACTIONS` / `_DOMAIN_ACTIONS` (306-409). The node's `manager_name` and `can_edit_devices` are echoed.
- **Bluetooth scan** (238-255): a plain mapping.

Bluetooth specifics:

- **`status`** (469-500) is not live. It reinterprets the *most recent completed scan* into connected and paired lists.
- **disconnect and discoverable** are fire-and-forget 202s.
- **Voice-triggered scans** (`source:"voice"` plus `user_id`) send a deep-link push through `send_deep_link_push_sync` (527-540). **No caller sends `source:"voice"`** (§8).

### 3.4 Device registry writes

**Import** (797-883) upserts by `(household_id, entity_id)`:

- `is_controllable = domain ∉ {sensor, binary_sensor, weather, sun, zone, person, device_tracker}` (304).
- **Name uniqueness:** names of devices *outside* the batch are reserved. Every batch item, new or re-imported, gets `_unique_name`, which suffixes collisions with ` (2)`, ` (3)`, … case-insensitively. A re-import can therefore rename a device that is already registered.
- On update it overwrites every identity field, sets `is_active=True`, and changes `room_id` only if one is supplied.
- If anything changed, it broadcasts `invalidate_device_cache`.

**PATCH** (886-953):

- A rename to an empty name → **422**.
- A rename that collides case-insensitively with another device → **409**.
- Otherwise it sets `name`, `room_id` and `is_active` from `exclude_unset`, then broadcasts invalidate.
- The response **omits `supported_actions`** (§8).

**DELETE** (956-973):

1. `device_removed` goes to the protocol node, only for `source=="direct"` with a protocol. This lets HomeKit unpair.
2. The row is hard-deleted.
3. Invalidate is broadcast.

**Rooms** (523-638):

- `normalized_name = strip().lower()`, unique per household → 409.
- The parent must exist in the same household → 400.
- PATCH rejects cycles with `_would_create_cycle` (483), walking up the tree. An explicit `parent_room_id: null` clears the parent; the code checks `exclude_unset` (604).
- DELETE is a hard delete. The FKs are `ON DELETE SET NULL`, so child rooms, devices and nodes are orphaned to null (`models.py:31,101,119`).
- Responses carry `device_count` (active devices only) and `node_count`, each with **N+1 queries**.
- `GET devices?room_id&recursive=true` collects descendants by BFS (467).

**Smart-home config PUT** (93-140):

- It validates that `primary_node_id` belongs to the household (400).
- Changing `use_external_devices` publishes `toggle_command` to every active node, with `enabled = not external` (129).
- `device_manager` is stored but **read by nothing** that matters: device-list always uses `"all"` (1744).

### 3.5 Encrypted config push (K2 relay)

1. Mobile encrypts with the node's K2 key, using AES-GCM with AAD `"{node_id}:{config_type}"` (`NS config_push_service.py:139`), and POSTs `{config_type, ciphertext, nonce, tag}`.
2. CC stores the row. `config_type` starting with `auth:` gets `expires_at = now + 5 min` (1989). CC then publishes on `.../config/push`.
3. **CC never decrypts.** It is a blind store-and-forward.
4. The node fetches `GET config/pending`. That call deletes expired rows and returns pending, unexpired rows; it is **unauthenticated** and keyed only by node_id in the path.
5. The node decrypts and dispatches by type: `auth:*`, `command_registry`, `agent_registry`, `fast_path_registry`, `node_config` (`NS :149-166`).
6. The node acks:
   - `auth:*` rows are **deleted**
   - other rows → `status=consumed` and `consumed_at`, and are **kept forever**
   - an ack of an already-consumed row → `already_consumed`

> **Changed by D4/D6:** `config/pending` and `ack` require node auth with the node bound to the path; `config/push` checks the caller's membership of the node's household (D5: any of their households). Consumed non-auth pushes are swept after 24 h (D40, Q10).

### 3.6 Provider OAuth

**Session create** (240-365):

1. Mobile sends a provider `AuthenticationConfig`, which comes from the node's command or device-family metadata. It may include `client_secret` (e.g. Nest Web OAuth, `PKG jarvis-device-nest/.../protocol.py:225-253`).
2. CC requires the node to exist (404). There is **no household check**.
3. CC generates the state with `token_urlsafe(32)`, plus PKCE S256 when `supports_pkce`.
4. Authorize URL = `authorize_url`, or `provider_base_url + authorize_path`. Exchange URL likewise. If either is missing → 400.

Redirect mode (286-310):

| Condition | `redirect_uri` | `state` | `client_id` | `requires_code_exchange` |
|---|---|---|---|---|
| `native_redirect_uri` set (Nest iOS/PKCE) | that URI | plain | `cfg.client_id` | true |
| `oauth.relay_url` set **and** absolute `authorize_url` (Google-hosted providers) | `{relay}/oauth/bounce` | `b64url({"t":csrf,"r":"jarvis://auth-complete"})`, unpadded | `cfg.client_id` | true |
| else (local, e.g. Home Assistant) | `{external_url}/api/v0/oauth/callback` | plain | **`external_base`** if `authorize_path` (HA requires `client_id` to match the redirect origin), else `cfg.client_id` | false |

`external_url` = the `oauth.external_url` setting, else `X-Forwarded-Proto`/`Host` (185-200).

The authorize URL gets `response_type=code`, `client_id`, `redirect_uri`, `state`, `scope` (space-joined), the PKCE challenge, and then `extra_authorize_params`, which **override** the earlier keys.

Storage: `client_secret` is encrypted into `client_secret_enc`. The session row has `user_id` = the JWT user (None for an admin key) and a TTL of 10 min.

**Direct callback** (368-470):

1. Find the session by `state` → 400 if missing.
2. The session must be pending, else 400. If expired → `status=expired`, 410.
3. POST a form to `exchange_url`: `grant_type`, `code`, `client_id`, optional `client_secret`, `redirect_uri`, optional `code_verifier`. Timeout 15 s. A non-200 → 502.
4. Encrypt `access_token`, `refresh_token` and the whole token JSON. Set `status=active`.
5. Publish `jarvis/auth/{provider}/ready`.
6. 302 to `jarvis://auth-complete?session_id=…`.

**Exchange** (relay or native; 478-569): the same steps, keyed by `session_id`, with the code taken from the body. **`state` is not re-checked server-side.** The relay is stateless and just 302s `code` and `state` to `jarvis://auth-complete` (`PKG jarvis-relay/CLAUDE.md`), so any CSRF check is the app's.

**Credential pull** (595-636):

1. The node pulls the newest `active` session for `(provider, calling node)` and decrypts it.
2. The session is marked `consumed`. The encrypted tokens **stay in the row**.
3. The response is `{access_token, refresh_token, token_data, base_url, user_id}`. The node stores it through the owning command's or family's `store_auth_values`, under the user's scope (`NS mqtt_tts_listener.py:1524-1590`).

Refresh tokens and background refresh live entirely on the node afterwards.

> **Changed by D1/D4/D5/D40:** no Caddy; external providers use the relay bounce and HA uses CC's own callback over LAN HTTP. Session create checks the target node is in one of the caller's households; `exchange` and status check ownership. `exchange_url`/`authorize_url` come from server-side config only, not the client body (SSRF fix). The at-rest key is a dedicated generated key, not derived from `SECRET_KEY` (Q9).

**Encryption** (106-137): this is **AES-256-GCM** (not Fernet), stored as `b64url(nonce‖ct)`.

- Key: `JARVIS_TOKEN_ENCRYPTION_KEY` (64 hex characters).
- Else `sha256(SECRET_KEY or JARVIS_AUTH_SECRET_KEY)`.
- If neither is set, it raises a 500 at use time.

### 3.7 Cameras

> **Changed by D29:** deferred until after the port; nothing below is built in v1. Kept as the reference for the later HLS packager vs WebRTC/MSE decision.

**Start stream** (`cameras.py:221-286`):

1. Look up the device with `domain=="camera"` in the household (404).
2. `_fetch_credentials_from_node` picks the node: `primary_node_id`, else `nodes[-1]`. It **ignores `protocols`**.
3. Publish on `camera-credentials`, then poll `creds-{rid}.json` with an async sleep. Timeout 10 s → **504**.
4. The node's plugin builds the source string (`NS camera_credentials_handler.py`). Nest, for example, returns `nest:?client_id&client_secret&refresh_token&project_id&device_id[&protocols=RTSP]` (`PKG nest/protocol.py:125-162`). A node error → 400. An empty source → 400.
5. CC registers it **verbatim**: `PUT {GO2RTC_URL}/api/streams?name=cam_{entity_id}&src=…`.
   - A non-2xx is tolerated if `GET /api/streams` text contains the name. go2rtc returns 400 when config persistence fails even though the in-memory registration succeeded.
   - Otherwise 502. A connection error → 503.
6. In memory: `_active_streams[device_id] = name` and `_stream_households[name] = hh`.
7. Return `{stream_name, hls_url:"/api/v0/cameras/stream/{name}/stream.m3u8"}`.

**Proxy** (339-387):

1. The stream must be in `_active_streams.values()` (404).
2. A household check against `_stream_households`.
3. The path must end in `.m3u8|.ts|.mp4|.m4s|.aac|.vtt|.key` and contain no `..`. This blocks pivoting to go2rtc's `/api/config`, which holds every household's secrets.
4. Forward `GET {GO2RTC}/api/{path}?src=name&…query`, streamed in 8 KB chunks. A non-200 is passed through.

So today mobile plays **go2rtc's own HLS**.

**Stop** (313-336): pop the maps, then `DELETE /api/streams?src=name`. It is best-effort, and returns `not_streaming` if the stream is unknown.

**`hls_packager.py`** is a finished but **unwired** alternative. Nothing imports it except `tests/test_hls_packager.py`, and it landed inside an unrelated phone commit (`992d053`). Its docstring explains why it exists:

- go2rtc's session-based LL-HLS 404s forever on AVPlayer/ExoPlayer.

What it does:

1. One ffmpeg per stream reads `rtsp://{go2rtc}:8554/{name}`.
2. It re-encodes with libx264 veryfast/zerolatency, `yuv420p`, GOP = 60 and AAC 44.1k stereo.
3. It writes 2 s × 6 MPEG-TS segments to a temp dir.
4. **Pre-warm:** it polls `/api/frame.jpeg` until it gets more than 2000 bytes, for up to 45 s.
5. **Respawn only on death,** with a 3 s backoff. An earlier "no video" watchdog destabilised Nest.
6. `wait_for_video` waits for a segment of at least 120 KB that is no more than 12 s old.

See Q2.

### 3.8 `get_ha_entities` server tool

Implemented at `get_ha_entities_tool.py:96-205`:

1. Reads `conversation_cache.get_node_context(conversation_id).agents` through `device_agent_data`, which prefers `home_assistant` over `device_agent` (`context_builders.py:12-24`).
2. Filters by domain (enum: light, switch, lock, cover, climate, fan, media_player, scene). It drops `unavailable` entries.
3. For lights it merges `light_controls` room groups and de-dupes individual lights against them.
4. Optional `floor` resolves to areas through `floors`; `area` is intersected with that. Matching is case-insensitive.
5. Returns `{domain, count, devices:[lines]}`, with lines formatted as `- {area} — {name}: {entity_id} (currently {state})`.
6. Errors: `missing_conversation_id`, `no_node_context`, `no_ha_data`, `floor_not_found`.

The prompt side lives in `build_agent_context_summary` (`context_builders.py:137-215`):

- ≤ 20 devices → a full room-grouped list.
- Otherwise a compact count summary. Its text says "Call control_device…/get_device_status" and **doesn't mention `get_ha_entities`**, even though the docstring says it should.

## 4. Data

Tables are in `models.py`. All of them move to SQLite as `cc_*`.

| Table | Key columns | Lifecycle |
|---|---|---|
| `rooms` (93) | `id`, `household_id`, `name`, `normalized_name` (unique per hh), `icon`, `ha_area_id`, `parent_room_id` FK self `SET NULL` | Permanent |
| `devices` (115) | `id`, `household_id`, `room_id` FK `SET NULL`, `entity_id` (unique per hh), `name`, `domain`, `device_class`, `manufacturer`, `model`, `source` (default `home_assistant`), `protocol`, `local_ip`, `mac_address`, `cloud_id`, `ha_device_id`, `is_controllable`, `is_active` | Permanent; hard-deleted |
| `nodes.protocols`, `nodes.room_id` | JSON text list; FK to rooms | Chapter 05 |
| `device_scan_requests` (145), `device_list_requests` (171) | `status` pending/completed/failed/expired, `results_json`, `device_count`, `error_message`, `expires_at` (+2 min), plus `manager_name`, `can_edit_devices` on the list table | **Never swept** |
| `bluetooth_scan_requests` (667), `bluetooth_pair_requests` (696) | the same, plus `role`, `source`, `user_id` / `mac_address`, `device_name` | **Never swept** |
| `config_pushes` (199) | `config_type`, `ciphertext`, `nonce`, `tag`, `status` pending/consumed, `expires_at` (`auth:*` only) | `auth:*` deleted on ack or lazily on the next `pending` fetch; others kept forever |
| `auth_sessions` (327) | `state` (unique), `code_verifier`, `exchange_url`, `client_id`, `client_secret_enc`, `redirect_uri`, `user_id`, `*_token_enc`, `token_data_enc`, `status` pending→active→consumed/expired | **Never swept**; deleted only with the node |

**In-memory state:**

- `_active_streams` and `_stream_households` (`cameras.py:44-47`). Lost on restart, at which point the proxy 404s for streams that are still live.
- `hls_packager._packagers` (unused).
- `NodeCommandService._pending_commands` (chapter 05).

**On disk:** `/tmp/jarvis-device-control/{rid}.json`, `state-{rid}.json` and `creds-{rid}.json`. This is cross-process result passing ("avoids in-memory dict issues with reload workers", 1093).

## 5. Settings

From `services/settings_definitions.py:521-566`.

| Key | Scope | Default | Effect |
|---|---|---|---|
| `smart_home.device_manager` | household | `jarvis_direct` (options `jarvis_direct`, `home_assistant`) | Echoed by the config routes only; effectively dead (1744) |
| `smart_home.primary_node_id` | household | `""` | Node preference for control-external, cameras and mobile's NodeSelector. **Not** passed by route 12/15. |
| `smart_home.use_external_devices` | household | false | Mobile shows the live node device list. Toggles the built-in `control_device` on all nodes. |
| `smart_home.use_home_assistant` | global | false | **Read by nothing** |
| `oauth.relay_url` | global | `""` (env `JARVIS_RELAY_URL`) | Enables the relay-bounce mode |
| `oauth.external_url` | global | `""` (env `JARVIS_EXTERNAL_URL`) | Base for the direct callback and HA's `client_id` |

Environment variables:

- `GO2RTC_URL` (default `http://jarvis-go2rtc:1984`)
- `GO2RTC_RTSP_PORT` (8554)
- `JARVIS_TOKEN_ENCRYPTION_KEY`, or `SECRET_KEY` / `JARVIS_AUTH_SECRET_KEY`
- `ADMIN_API_KEY` (the P auth path)

## 6. Dependencies

**Other CC subsystems:**

| Chapter | What this subsystem uses |
|---|---|
| 05 | MQTT client and `NodeCommandService`, the node registry (`is_active`, `last_seen`, `protocols`), node auth, K2 (the mobile-side key the config push relies on) |
| 01/02 | `conversation_cache` for `get_ha_entities`; `main.py` room injection; client-tool routing of `control_device` |
| 03 | `context_builders` (device summary, room hierarchy section) |
| 13 | mobile chat device injection, settings service, `send_deep_link_push_sync` (inbox/notifications) |

**Other services:**

- jarvis-auth: the household role check on **every** P+hh call, synchronous and with a 5 s timeout. JWT validation.
- notifications: the Bluetooth deep-link push.

**Third parties:**

- go2rtc: REST on 1984 and RTSP on 8554.
- ffmpeg: only if the packager is wired.
- The arbitrary OAuth token endpoints, which the client supplies.
- The jarvis-relay `/oauth/bounce` (a cloud service on Fly).

**No LLM calls.**

## 7. Invariants and non-obvious behaviour

1. **Route 13's shape is a node contract.** The node POSTs `{success, error, input_required?}` to `/api/v0/device-control-results/{rid}`, where `rid` = `details.reply_request_id || request_id`. The node sends `X-API-Key` on every REST call (`NS clients/rest_client.py:53-68`), so Go *may* require node auth there without breaking nodes. The same applies to routes 16, 24 and 25.
2. **Pass `trusted:true` on control actions.** Without it the node calls `_verify_command`, and the action is dropped unless the in-memory pending-command check passes (`NS :203`). In jarvisd the broker **must** forbid node clients from publishing to `jarvis/nodes/+/commands`. Today any MQTT client that can publish there can drive devices with `trusted:true`.

   > **Changed by D4/D7:** Go drops `trusted:true`. Consequence: today's nodes will call `POST /commands/{rid}/verify` for every `action`, so CC must record the pending command before publishing and keep the verify route (doc 05) working, or every control is silently dropped.
3. **Control timeouts return 200 with `success:false`,** not 504. Cameras return **504** on a node timeout. Keep both.
4. **The 20 s / 10 s split by `action.startswith("pair")`** (1166).
5. **The stream name is `cam_{entity_id}`, and the HLS URL is relative:** `/api/v0/cameras/stream/{name}/stream.m3u8`. Mobile builds the absolute URL itself (`MOB cameraApi.ts:53`) and sends a Bearer header to the player.
6. **The HLS proxy path allow-list and `..` rejection are security-critical.** go2rtc's `/api/config` exposes every registered `nest:` source, including client secrets and refresh tokens.
7. **The go2rtc PUT tolerates a non-2xx if the stream shows up in `GET /api/streams`.**
8. **Precedence in import enrichment and scan matching:** `entity_id`, then `cloud_id`, then lowercased MAC. In the scan and list enrichment, only active devices count.
9. **Duplicate-name handling differs by route:**
   - import auto-suffixes (` (n)`)
   - PATCH returns 409
   - rooms return 409 on `normalized_name`
10. **Mobile chat injects DB devices under the key `home_assistant`,** even for direct devices. Because `device_agent_data` prefers that key, the injected list wins over any agent data.
11. **Room hierarchy is injected only when at least one room has a parent.**
12. **`invalidate_device_cache` is broadcast after every import, PATCH and DELETE,** and only after the commit. `device_removed` is sent *before* the delete.
13. **OAuth:**
    - `extra_authorize_params` override the base params.
    - HA's `client_id` becomes `external_base` when `authorize_path` is used.
    - Relay state is unpadded base64url JSON `{"t","r"}`. jarvis-relay parses it, so it is a cross-repo contract.
14. **`requires_code_exchange` is true for both native and relay modes.**
15. **Credential pull returns the newest `active` session for `(provider, node)` and flips it to `consumed`.** A second pull → 404.
16. **`auth:*` config pushes have a 5-minute TTL and are deleted on ack.**

## 8. Oddities

**Security gaps.** All of these are live today. Go should fix them; see Q3.

- Routes 13 and 16 have no auth. Anyone can answer a pending control or state request, or plant result files. `rid` goes into the filename unvalidated. Traversal is limited because a single path segment can't contain `/`, but cameras added UUID validation for this reason (`cameras.py:302`) and these routes did not.
- Routes 24 and 25 (`config/pending`, ack) have no auth. Anyone who knows a node_id can read its queued ciphertexts, which are useless without K2, and can **ack them away**, which is denial of service.
- `config/push` (23) has no household check. Any JWT user can queue config to any node. K2 makes forged plaintext infeasible, but it is still noise and DoS.
- Bluetooth polls (199, 353) and OAuth `exchange` and status (478, 572) don't check household or ownership. The IDs are UUIDs, so this is low risk.
- `POST /oauth/sessions` doesn't check that `node_id` is in the caller's household. A user could direct their own provider tokens at another household's node.
- **The OAuth `exchange_url` is client-supplied,** and CC POSTs the code, `client_secret` and verifier to it. That is SSRF and secret exfiltration to any URL. The secret is the caller's own, but the request comes from inside the LAN.
- Result uploads (18, 21, 159, 314) don't check that the authenticated node equals `{node_id}`.

**Broken or dead:**

- **The HA integration's room fetch always 401s.** It sends a node `X-API-Key`, and `verify_provisioning_auth` treats any `X-API-Key` as the admin key (`provisioning.py:90-93`). Appendix A lists this caller as live; in practice it gets `[]`.
- **Mobile Bluetooth `release` and `auto-connect` call routes that never existed in CC.** Searching git history with `-S` finds nothing.
- `control-external` (14) has no screen caller. `ExternalDeviceDetailScreen` is read-only.
- The voice Bluetooth deep-link (`source:"voice"`) has no caller.
- `smart_home.use_home_assistant` and `smart_home.device_manager` are effectively unused.
- `ControlDeviceTool` is disabled dead code (207 lines).
- `hls_packager.py` is unwired (see §3.7). The docstring claims the current go2rtc-HLS path shows black or 404 on standard players, which suggests cameras may not work on mobile today.

**Inconsistencies:**

- The PATCH device response omits `supported_actions`.
- Control (12) and state (15) ignore `primary_node_id`; cameras ignore `protocols`. These are three different node-choice policies.
- `nodes[-1]` fallback ordering is undefined.
- An offline node can be chosen.
- `_stream_households` is keyed by `cam_{entity_id}`, but `entity_id` is unique only *per household*. Two households with the same entity_id overwrite each other's stream owner. Only one wins the go2rtc name, so the other's viewers see the wrong camera or get a 403.
- `CLAUDE.md` says cameras use MinIO/S3. They don't.
- The task brief said "Fernet". The code uses AES-GCM, and the model docstring says "node pulls via app-to-app auth", but it is node auth.
- OAuth tokens remain encrypted in `auth_sessions` after consumption, indefinitely.
- The token key falls back to `sha256(SECRET_KEY)`, which ties token-at-rest security to the HS256 secret being retired.
- Mobile's axios timeout (15 s) is shorter than CC's pair window (20 s).
- The `get_ha_entities` prompt summary never tells the model to call `get_ha_entities`.

## 9. Tests

Existing:

| Test file | Lines | Covers |
|---|---|---|
| `tests/test_device_list_endpoints.py` | 610 | request, upload, poll, enrichment by entity, cloud and MAC; expiry; wrong node; MQTT publish and failure |
| `tests/test_device_name_uniqueness.py` | 99 | import suffixing, PATCH 409 and 422 |
| `tests/test_camera_authz.py` | 69 | proxy household check, path allow-list |
| `tests/test_camera_credentials_callback.py` | 66 | node auth, UUID validation |
| `tests/test_camera_stream_registration.py` | 137 | source registered verbatim, MQTT payload |
| `tests/test_hls_packager.py` | 112 | ffmpeg command, start/stop, `wait_for_video` |
| `tests/test_household_authz.py` | — | includes the Bluetooth scan foreign-household case |

**No tests** for:

- rooms CRUD and cycles
- device control and state
- device scan
- config push, pending and ack
- any OAuth route
- Bluetooth pair or status
- `_pick_node_for_protocol`
- `get_ha_entities`

Contract-test candidates (black-box, fake MQTT node):

1. **Control round trip.** Assert the published `action` payload shape, `trusted` and `reply_request_id`. The fake node POSTs a result; assert the response, including `input_required`. Cover the timeout → 200/false case and the pair → 20 s case.
2. **State round trip.**
3. **Scan, list and Bluetooth lifecycles,** including 410 expiry and enrichment precedence.
4. **Config push → MQTT → pending → ack,** covering `auth:` deletion and TTL.
5. **OAuth, all three modes, against a fake token endpoint.** Assert the authorize-URL query for each mode, including HA's `client_id`, the relay state encoding and PKCE. Then callback/exchange → MQTT `auth/{p}/ready` → credential pull → consumed → 404.
6. **Cameras with a fake go2rtc:** registration, including the 400-but-listed path, proxy authz and the allow-list.
7. **Rooms:** 409, parent validation, cycles, null-clears-parent, recursive device listing.

Golden fixtures:

- `_get_actions_for_raw` over every protocol and domain, and the generic fallback.
- `_unique_name` sequences.
- `get_ha_entities` over recorded `agents` payloads, together with `build_agent_context_summary` (shared with chapter 03).

## 10. Questions for the user

1. **[scope] Is a node, CC, or Home Assistant the long-term owner of "what devices exist"?**
   - Today there are three overlapping truths: the CC `devices` table (direct devices, plus imported HA rows that the built-in path never drives), live node managers (`use_external_devices`), and HA agent context at conversation start.
   - *Why it matters:* it decides whether Go keeps `/devices/import` plus the `/device-list` dual mode, and whether imported `source=home_assistant` rows mean anything.
   - Options:
     - (a) Port as-is.
     - (b) CC DB is the single registry. HA devices get imported too, and the HA package reads the registry.
     - (c) Nodes are the truth, and CC only stores room assignment and names as overlays.
   - **Recommendation: (a) for the port.** Byte-compatible routes, then decide (b) vs (c) after the cutover. Confirm that HA rows in `devices` are wanted at all.

   **Decided (D28):** (a). Same routes, `devices` table, external device manager dual mode and `/devices/import` kept; revisit the source of truth after cutover.
2. **[behaviour] Do cameras actually work on mobile today, and should Go ship the ffmpeg HLS packager?**
   - `hls_packager.py` is finished, tested and unwired. Its docstring says the go2rtc HLS that is actually served goes black or 404s on AVPlayer and ExoPlayer.
   - *Why it matters:* it changes the external-binary story (go2rtc **plus** ffmpeg plus x264) and the proxy (static files instead of go2rtc passthrough).
   - Options:
     - (a) Port the current passthrough.
     - (b) Port with the packager wired: same URL contract, served from a temp dir.
     - (c) Have Go speak WebRTC or MSE instead.
   - **Recommendation: (b)** if you confirm passthrough is broken on your phones. Keep cameras an optional module that is disabled when go2rtc or ffmpeg is absent.

   **Decided (D29):** none for v1. Cameras are deferred ("I don't think it works currently"): no go2rtc or ffmpeg; rows stay listed; stream/HLS routes return "not available". Revisit (b) vs (c) after the port.
3. **[change] May Go close the auth holes in §8 while keeping wire shapes?**
   - The holes:
     - node auth on `device-control-results`, `device-state-results`, `config/pending` and `ack`
     - node-id == path check on uploads
     - household checks on `config/push`, OAuth session create, `exchange`/status and the Bluetooth polls
     - UUID validation of `rid`
   - Every node already sends `X-API-Key`. A new 401 or 403 would hit only an attacker or a misconfigured client.
   - **Recommendation: yes, all of them.** Each one is a behaviour change worth a line in the contract suite: the Python oracle passes it as "200", and Go deliberately diverges.

   **Decided (D4/D5/D6):** yes, all of them, plus the `exchange_url` SSRF fix (server-side config only). Household checks match any of the caller's memberships.
4. **[scope] Mobile's Bluetooth "Release/Forget" and "Auto-connect" buttons call `/bluetooth/release` and `/bluetooth/auto-connect`, which CC never implemented.** The node already handles both topics. Should Go add them?
   - They are thin: a 202 plus a publish of `{mac_address, forget}` / `{mac_address, enabled}`.
   - Python is frozen, so this would be the first "new" route.
   - **Recommendation: add both in Go.** They are tiny, and the UI is already shipped and broken.

   **Decided (D8, P2 "Bluetooth missing routes = add"):** add both.
5. **[scope] Cut `POST /devices/control-external`?** No mobile screen calls it; the API function is orphaned.
   - **Recommendation: cut it**, unless you plan to make `ExternalDeviceDetailScreen` controllable. Even then, it would be better to route through the node's own manager than a CC relay.

   **Decided (D9):** cut the route only. The external device manager feature and its settings are live and kept.
6. **[behaviour] Voice control runs on whichever node heard you, while mobile control is routed to a node that has the protocol plugin.** On a multi-node household where only one node has, e.g., HomeKit paired, voice fails with "No adapter for protocol". Should CC route voice `control_device` calls too?
   - Options:
     - (a) Status quo.
     - (b) When the local node lacks the protocol, the node or CC forwards to the protocol node via the mobile relay path.
     - (c) Require every node to install every device plugin.
   - **Recommendation: (a) for the port, and track (b)** as post-port work.

   **Decided (M8/D47):** (a) status quo for the port; forwarding to a protocol node is post-port work.
7. **[behaviour] The HA integration fetches `GET /households/{hh}/rooms` with a node key, which always 401s.** Should Go accept node auth (scoped to the node's own household) on `GET rooms`?
   - Accepting it makes the HA package's "upstairs" room-hierarchy feature start working for the first time, which could change voice behaviour.
   - **Recommendation: yes, accept node auth on GET rooms only,** and soak it on dev.

   **Decided (D4/D5, P1 policy default):** yes. Accept node auth on `GET rooms` only, scoped to the node's own household; soak on dev. (Covered by P1's list; not asked individually.)
8. **[behaviour] Unify the node-selection policy?**
   - Today control and state use protocol match, then primary, then arbitrary. Control-external uses protocol match plus primary. Cameras use primary, then arbitrary, and ignore protocols. None of them consider online status.
   - **Recommendation:**
     1. Protocol match.
     2. Prefer primary.
     3. Prefer online, then most recent `last_seen`.
     4. Fail fast with 503 "no online node with protocol X" instead of a 10 s timeout.

   This changes the error shape only in the failure case.

   **Decided (M9/D47):** yes, as recommended (cameras are deferred, so this covers control and state).
9. **[change] OAuth token-at-rest key.** The key currently derives from `SECRET_KEY` / `JARVIS_AUTH_SECRET_KEY`, which the RS256 migration retires.
   - **Recommendation:** jarvisd generates a dedicated 32-byte key on first run (in `~/.jarvis/` or its secrets table). Do **not** import `auth_sessions` (they live 10 minutes and are single-use). Purge consumed and expired sessions after 1 h.

   Also: may Go validate `exchange_url` / `authorize_url` (https, or the session's `provider_base_url` host) to stop SSRF?

   **Decided (D40 default, D4):** a dedicated generated key; `auth_sessions` not imported; consumed and expired sessions purged. SSRF is fixed by taking the exchange/authorize URLs from server-side config only (D4).
10. **[behaviour] Retention for the request tables and consumed config pushes.**
    - The four request tables plus `config_pushes` grow forever. Non-auth consumed pushes keep HA config ciphertext indefinitely.
    - **Recommendation:**
      - an embedded-queue sweep that deletes request rows 1 h after `expires_at`
      - delete consumed non-auth pushes after 24 h
      - keep the Bluetooth `status` route's "latest completed scan" semantics by keeping the newest completed scan per node

    **Decided (D40 default):** retention sweeps as recommended.
11. **[minor] Drop the dead settings and the code paths behind them?**
    - The settings: `smart_home.use_home_assistant` and `smart_home.device_manager`. The latter must still be echoed in the config response, because mobile types include it.
    - The code paths: the voice Bluetooth deep-link branch and the disabled `ControlDeviceTool`.
    - **Recommendation:** keep `device_manager` in the JSON as a stored passthrough. Drop the rest.

    **Decided (M7/D47, D9):** drop `smart_home.use_home_assistant`, the voice Bluetooth deep-link branch and `ControlDeviceTool`. Keep `smart_home.device_manager` as a **live** setting (external device manager), not a passthrough.
12. **[minor] Stream-name collisions across households** (`cam_{entity_id}`).
    - **Recommendation:** name streams `cam_{device_id}` (a UUID) in Go. The URL is opaque to mobile (it uses the returned `stream_name`), so this is safe.

    **Decided (D40 default):** deferred with cameras (D29); apply `cam_{device_id}` when cameras are built.

## 11. Go port notes

**Package shape:** `internal/modules/cc/smarthome/`.

| File | Contents |
|---|---|
| `rooms.go`, `devices.go` | sqlc CRUD; `ListRoomsWithCounts` as one GROUP BY query instead of N+1 |
| `actions.go` | `_PROTOCOL_ACTIONS` / `_DOMAIN_ACTIONS` tables, golden-tested |
| `control.go` | control, state, node pick |
| `jobs.go` | scan, list and Bluetooth request/poll |
| `configpush.go` | |
| `oauth.go` | |
| `cameras.go` | v1: list only, stream/HLS routes return "not available" (D29) |
| `tools.go` | `get_ha_entities` |

**Request/response correlation replaces temp files.**

- `platform/mqtt` provides `Await(ctx, requestID) <-chan json.RawMessage` (the PLAN §6 5a mechanism).
- The HTTP result routes (13, 16) stay as **node-facing contracts**: they require node auth, check the rid was issued to that node (D4), then `Fulfill(rid, body)`. No camera-creds route in v1 (D29).
- Timeouts come from `context.WithTimeout`: 10 s, or 20 s for pair. On timeout the code returns the same 200/false body.
- Node pick (M9): protocol match → primary → online/most recent `last_seen`; no online node → 503 fail fast.
- Control and state publish **without** `trusted:true` (D4). Record the pending command before publishing so the node's `_verify_command` call succeeds (§7.2).
- No blocked threads, no `/tmp` files, and no path-traversal surface.
- A single process removes the "multi-worker" reason the files existed.

**Request/poll jobs** (scan, list, Bluetooth):

- Keep the SQLite tables, because mobile polls by ID and the 410 semantics need `expires_at`.
- Add the TTL sweep as a trigger kind on the one scheduler engine (D27, D40): delete request rows 1 h after `expires_at`, consumed non-auth config pushes after 24 h, consumed/expired `auth_sessions`.
- Result uploads check node-id == path; mobile polls check household (D4/D5).
- Add Bluetooth `release` and `auto-connect` (D8): 202 plus a publish of `{mac_address, forget}` / `{mac_address, enabled}`.

**Config push (D4/D6):** `pending`/`ack` require node auth bound to `{node}`; `push` checks household membership (any of the caller's households, D5).
- Alternative: in-memory with TTL. That is acceptable too, but it loses Bluetooth `status`'s "last completed scan" fallback across restarts.

**Embedded broker ACL** (cross-cutting with chapter 05):

- Nodes may subscribe to `jarvis/nodes/{self}/#` and `jarvis/auth/+/ready`.
- Nodes must **not** publish to `jarvis/nodes/+/commands`, `.../device-*`, `.../camera-credentials`, `.../config/push`, `jarvis/auth/#`.
- jarvisd publishes in-process, with no client credentials.
- This replaces `trusted:true`, which Go no longer sends (D4/D7). Per-node credentials, as in doc 05.

**go2rtc** (deferred, D29; notes kept for when cameras are built):

- An optional supervised child process (PLAN §3.2), with config written by jarvisd.
- Bind the API (1984) and RTSP (8554) to **127.0.0.1** only. The proxy is the only way in.
- Disable go2rtc config persistence, so the 400-on-persist quirk disappears. Keep the tolerant check anyway.
- Because go2rtc keeps the `nest:` secrets in memory, never expose `/api/config`. Keep the allow-list as defence in depth.
- On jarvisd restart, re-registration happens lazily: mobile's retry calls start again. Optionally persist active streams.

**HLS packager** (deferred, D29):

- `exec.Cmd` per stream.
- A goroutine pre-warms against `/api/frame.jpeg`, then respawns only on exit with a 3 s backoff.
- The proxy serves `stream.m3u8` and `seg_*.ts` from the per-stream temp dir with `http.ServeFile`, after the same authz.
- `start` should block on `wait_for_video` (40 s), so the first playlist fetch has a picture. The mobile `startCameraStream` call must tolerate that latency; check its axios timeout.
- Stop on DELETE, and also on an idle timer (no segment fetch for N minutes), because a mobile crash skips the DELETE.

**OAuth:**

- Use `crypto/aes` + `cipher.NewGCM` with a dedicated key generated on first run (D40, Q9). `auth_sessions` are not imported.
- Session create, `exchange` and status check the target node / session against the caller's households (D4/D5).
- `exchange_url` and `authorize_url` are resolved from server-side config, never taken from the client body (D4 SSRF fix).
- No Caddy (D1): relay bounce for external providers, CC's own `/oauth/callback` over LAN HTTP for local ones (HA).
- `GET rooms` accepts node auth scoped to the node's household (Q7).
- `net/http` client with a 15 s timeout. Form-encode the exchange.
- The redirect builder is a pure function: golden-test all three modes.
- Relay `state` encoding must stay byte-compatible (unpadded `RawURLEncoding` of `{"t":…,"r":…}` with Python `json.dumps` spacing, `", "` and `": "`).
  - jarvis-relay decodes it with a JSON parser, so spacing likely doesn't matter. Still, match Python's output exactly, to be safe for the contract suite.
- `RedirectResponse` 302 to the `jarvis://` scheme. Go's `http.Redirect` would rewrite a relative URL, but this one is absolute, so it is fine.

**`get_ha_entities`:**

- A pure function over the conversation's `node_context`, registered as a server tool in the chapter 02 registry.
- Port with golden fixtures, because its output lines feed the LLM.

**FastAPI parity traps:**

- `DeviceUpdate` uses `exclude_unset`: a missing field means untouched, while `room_id: null` clears it. The same applies to `RoomUpdate.parent_room_id`.
- Responses serialize `datetime` without a timezone (naive UTC, `datetime.utcnow`).
- 201 status codes on: rooms POST, import, scan, list and Bluetooth requests, config push, OAuth sessions.
- 204 on DELETE rooms and devices. 202 on Bluetooth disconnect and discoverable.

**Simplifications from the single binary:**

- `verify_household_role` becomes an in-process call to the auth module. Today it is a synchronous HTTP round trip on **every** smart-home request.
- The mobile-chat DB device injection and the voice room-hierarchy injection become direct repository calls shared with chapter 01.

**Cut:** route 14 `control-external` (D9), `ControlDeviceTool`, `smart_home.use_home_assistant`, the voice Bluetooth deep-link branch (M7). Keep `smart_home.device_manager` and `use_external_devices` (live).
