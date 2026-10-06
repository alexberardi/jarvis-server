# 00 — Platform and cross-cutting

Source root: `/home/alex/jarvis/jarvis-command-center`. All `file:line` below are relative to it. The CC `CLAUDE.md` is stale; where it disagrees with code, this doc follows the code and lists the difference in §8.

## 0. Decisions applied (2026-10-06)

Source: `QUESTIONS.md`. §1–§9 still describe today's Python behaviour; changes are flagged inline as "Changed by D#".

- **D4 / D5 / D6 (security).** Close holes for unauthenticated actors and other households; keep wire shapes. `/api/v0/chat` is **dropped** (D5 supersedes D4's "add node auth"): node `chat_text()` moves to `/api/v0/node/llm/chat`, a node-setup change. `/lightweight/chat` is cut. The MQTT result posts get node auth, and the request id must belong to that node. Config `pending`/`ack` get node auth bound to the path node (D6). Household checks look at **all** of the caller's memberships, not just the JWT's active household (D5). `/internal/phone/*` stays open to any registered app for now. `trusted:true` is removed; MQTT trust comes from per-node broker credentials and ACLs.
- **D8.** Known bugs are fixed by default and logged as intended differences, unless a frozen client depends on the buggy behaviour.
- **D9.** Cut: `/tool-router/train` and fastText, `/admin/nodes/{id}/commands`, the never-written attention tables, `ambient_grounding.py`, the legacy `IModelInterface`/`ModelFactory`/`JarvisToolModel`. Errand autonomy (`errands.autonomous_enabled`) is kept.
- **D10.** Factory reset uses the tracked `NodeTask` flow only. The reset token is persisted in SQLite with the task; `core/pending_resets.py` (in-memory, 300 s) goes.
- **D11 (settings hygiene).** Defined-but-unread keys are dropped. The three read-but-undefined keys are hard-coded, not defined: the embedding sweep is always on every 60 s, and trace retention is 7 days. `llm.interface` is renamed **`llm.prompt_provider`**, set at install time by the admin wizard/installer; import maps the old key. **An unknown provider name is a hard error**, not a fallback.
- **D12.** The admin model catalog offers only models with a kept provider (jarvis-admin change, alongside the rename).
- **D17.** The situation matcher is cut: its callback route, debounce task and `proposals.proactive_enabled`. `proposals.enabled` stays.
- **D18.** Attention: journal card and TTL cleanup kept; quiet hours and the journal cron use the **household timezone** (`attention.timezone` goes); TTL cleanup runs at startup, not after a 24 h sleep.
- **D19.** `memory.extraction_enabled` becomes household-scoped, **default ON** (opt-out), alongside `memory.enabled`; both go on the mobile allowlist. CC CLAUDE.md's "opt-in" claim (§8.11) is wrong and is fixed.
- **D20.** Account deletion is one in-process transaction across modules: hard-delete user-owned data, de-identify activity history, leave household-owned data.
- **D3 / D25 / D33 / D38.** Settings that go: `voice.stickiness_*` (and the `b8x9y0z1a2b3` seed), `routines.scheduler_enabled`, the short/long speaker thresholds, `voice.emotion_enabled` / `voice.emotion_min_confidence`.
- **D27 / D26.** One scheduler engine: a `next_fire_at` trigger table on the embedded durable queue. The loops in §2.4 become trigger kinds with a persisted `last_run_at`; missed triggers fire once, late.
- **D29.** Cameras are deferred: no HLS packager thread in v1.
- **D31.** Inbox and push become an in-process `notify` service.
- **D40 (B defaults, via 05).** Bare-key node auth is dropped once the node headers are verified (05.Q10); settings requests are pruned after 24 h (05.Q11).
- **D47 / M4.** `adapter_settings`, `skip_warmup_inference` and `JARVIS_TEST_MODE` are dropped; old nodes still sending them are fine because the decoder ignores unknown fields.
- **M2.** The `llm_trace.log` file is dropped; the metrics JSONL is kept only behind a debug setting with a size cap.
- **M3.** The four settings scopes and the cascade order are kept exactly; env fallback is dropped except for secrets and URLs that become `jarvisd` config.

## 1. Purpose

This doc covers everything in CC that is not one feature:

- the process lifecycle: the app factory, startup/shutdown, and router mounting;
- every **background loop**;
- **auth**: the seven-ish ways a request proves who it is;
- the **settings framework**: the scoped `settings` table, its cache and the `/settings/*` router;
- **service discovery**, **logging and latency traces**, and the error shape;
- the **database and migrations**, plus a table→subsystem map;
- the **LoRA/adapter cut list**: every place non-adapter code touches adapter code.

Who uses it: everyone. Every route depends on one of the auth deps. Every subsystem reads settings. The loops are the only clients with no caller.

## 2. Entry points

### 2.1 Routes owned here

| Method | Path | Auth | Caller (PLAN App. A) | Where |
|---|---|---|---|---|
| GET | `/health` | none | config-service, compose healthcheck, admin | `app/main.py:682-690` |
| GET | `/api/v0/ping` | none | **none, cut** | `app/main.py:862-864` |
| GET | `/settings/`, `/settings/categories`, `/settings/{key:path}` | combined: superuser JWT via `/auth/me`, **or** app creds | admin, mobile (shared contract) | mounted at `app/main.py:229-240`; routes in `jarvis-settings-client/jarvis_settings_client/routes.py:145-233` |
| PUT | `/settings/{key:path}`; POST `/settings/sync-from-env`, `/settings/invalidate-cache` | superuser JWT via `/auth/me` | admin | `routes.py:235-327` |
| POST | `/api/v0/{deep-research,memory-extraction,situation-matcher,characterization-synthesis}/callback` | callback bearer token | llm-proxy queue worker. **They become in-process (App. A)**, so there are no routes in Go. | `app/main.py:1937-2017` |

### 2.2 Routes indexed here but owned elsewhere (`app/main.py`)

| Route | Line | Owner |
|---|---|---|
| POST `/api/v0/conversation/start`, `/conversation/end` | 870, 1100 | 01 |
| POST `/api/v0/voice/command`, `/voice/command/stream`, `/voice/acknowledge` | 1161, 1367, 1317 | 01 |
| POST `/api/v0/voice/command/continue`, `/continue/stream` | 2171, 2070 | 01 |
| POST `/api/v0/tool-router/train` (node auth) | 1602 | 02 |
| POST `/api/v0/adapters/train`, `/adapters/jobs/callback` | 1652, 1881 | **CUT** (§7c) |

### 2.3 Router mount order

Mount order matters only for overlapping paths. It is listed at `app/main.py:697-856`.

- All routers mount under `/api/v0`, except:
  - `traces.admin_router` → `/api/v0/admin` (`:811`);
  - mobile routers → `/api/v0/mobile` (`:812-851`);
  - `phone_sessions` → **no prefix** (`/internal/phone/...`, `:855-856`).
- `/settings` mounts **inside `startup_event`** (`:240`), so it is the last router registered. It must wait for service discovery, because its auth dependency is built with a resolved auth URL.
- Two routers in `admin.py`:
  - `router` at `/api/v0/admin`;
  - `node_public_router` at `/api/v0`, which is unauthenticated: `/nodes/verify-reset` and `/nodes/factory-reset/{id}/status` (`app/admin.py:33,566,765`).

### 2.4 Background loops (deliverable a)

All of these are started by `startup_event` (`app/main.py:130-646`) as fire-and-forget `asyncio.create_task`. Every loop **sleeps first and then works**: the first run happens one interval after boot. Every exception is logged at WARNING and swallowed.

| # | Name | Cadence | Gate | What it does | Owner |
|---|---|---|---|---|---|
| L1 | provisioning-token cleanup | once at boot, then every 3600s | — | `cleanup_expired_tokens` (`:169-198`, `app/provisioning.py:256`) | 05 |
| L2 | errand/workflow sweep | 20s | — | Once at boot: `recover_orphaned_running_workflows`. Each tick: `resume_waiting_errands`, `resume_due_timer_workflows`, `fire_due_schedules` (`:203-226`) | 09 (schedules: 08) |
| L3 | passive memory extraction | `memory.extraction_interval_seconds` (300) | on unless `memory.extraction_enabled` is false-ish | `run_extraction_batch` → enqueues an LLM job → callback (`:243-259`) | 04 |
| L4 | memory embedding sweep | `memory.embedding_interval_seconds` (60). **The key is undefined**, so the interval is always 60s. | `memory.embedding_enabled`, also **undefined**, so the sweep is always on | `MemoryService.embed_missing(limit=100)` in a thread (`:266-292`) | 04 |
| L5 | characterization synthesis | `characterization.synthesis_interval_seconds` (3600) | off unless explicitly true | `run_synthesis_batch` → LLM job → callback (`:298-316`) | 04 |
| L6 | transcript TTL | 86400s | — | deletes transcripts older than `memory.transcript_ttl_days` (7) (`:319-335`) | 04 |
| L7 | trace TTL | 3600s | — | deletes `request_traces` older than `tracing.retention_days`. **The key is undefined**, so retention is always 7 days (`:338-357`). | 05 |
| L8 | expired-memory cleanup | 60s delay, then every 1800s | — | `MemoryService.cleanup_expired` (`:360-376`) | 04 |
| L9 | signal TTL | 90s delay, then every 1800s | — | `SignalService.cleanup_expired` (`:379-395`) | 10 |
| L10 | node-update task timeout | 120s | — | Fails tasks that are pending/dispatched/in_progress and older than 15 min, or in_progress with no update for 10 min (`:409-454`) | 05 |
| L11 | adapter auto-train | `adapter.auto_train_interval_seconds` (3600, min 60) | `adapter.auto_train_enabled` (false) | `run_scheduled_training` (`:459-480`) | **CUT** |
| L12 | phone-session reaper | 30s | — | `reap_phone_sessions`: stale heartbeat or over-limit → failed; expired drafts → expired (`:518-528`) | 11 |
| L13 | routine scheduler | `routines.scheduler_interval_seconds` (30, min 10) | off unless `routines.scheduler_enabled` is true | `run_due_routines` (`:531-553`) | 08 |
| L14 | routine-execution TTL | 86400s | — | deletes executions older than `routines.execution_ttl_days` (7) (`:556-575`) | 08 |
| L15 | attention journal card | 60s tick, then a per-household cron | per household: `attention.enabled` **and** `attention.journal_card_enabled` | Cron is `attention.journal_card_cron` (default `0 21 * * *`) in `attention.timezone`. `last_fired` is **held in memory**, so a restart can post a duplicate card (`:579-611`). | 10 |
| L16 | attention TTL | 86400s | — | `attention_journal.cleanup_expired(ttl=attention.journal_ttl_days=30)` (`:613-629`) | 10 |

Background concurrency outside `main.py`:

| Name | Mechanism | Owner |
|---|---|---|
| L17 service-discovery refresh, plus a nag | `jarvis_config_client` refreshes every 300s (`app/core/service_config.py:96-100`). A daemon thread logs a banner every 30s while `JARVIS_CONFIG_URL` is unset (`:59-63, 87-89`). | 00 |
| L18 MQTT network loop | paho `loop_start()` thread (`app/core/mqtt_client.py:129`) | 05 |
| situation-matcher debounce | a 5s debounced task per household on the main loop (`app/services/situation_matcher_service.py:29,331`), wired by `set_main_loop` (`app/main.py:506-507`) | 10 |
| signal-reaction dispatch | `loop.create_task` from sync ingest (`app/services/signal_reaction_registry.py:98`, `app/main.py:511-516`) | 10 |
| HLS packager | a thread per camera stream (`app/api/hls_packager.py:185`) | 07 |
| trace persistence | a daemon thread **per request** that writes `request_traces` (`app/core/utils/latency_logger.py:272-309`) | 00 |

> **Changed by D27 / D11 / D17 / D18 / D19 / D25:** all of the above become trigger kinds on one scheduler with a persisted `last_run_at`. L4 and L7 have hard-coded intervals (no settings). L3's gate is per household and defaults on. L11, the situation-matcher debounce and the HLS packager (D29) are gone. L13 loses its gate: a routine with an enabled `schedule` is the opt-in. L15 uses the household timezone, and L16 runs at startup.

### 2.5 Other startup side effects (`app/main.py:130-646`, in order)

1. `enforce_secret_security` (`:137`). Startup aborts only when `JARVIS_ENV=production` (`app/deps.py:68-83`).
2. Service discovery (`:140`, `:48-69`), then remote logging (`:142`, `:72-98`).
3. Callback-token posture is logged: an error if the token is unset and insecure mode is off (`:145-156`).
4. MCP client connect (`:159-167`). This is effectively dead: `jarvis_mcp_client` is not in `pyproject.toml`, so the `ImportError` branch always runs.
5. Server-plane callback handlers are registered **before the first request**:
   - phone (`:486-488`);
   - errand and schedule (`:492-496`);
   - proposable-action (`:500-502`);
   - signal leave-by reaction and signal-automation executor (`:513-516`).

   If a handler is missing, callback dispatch returns 400.
6. `settings_service.list_all` is monkeypatched so that `llm.interface` gets `options = PromptProviderFactory.get_available_providers()` (`:632-646`).

Shutdown (`:650-678`) runs these, best-effort: service-config shutdown, MQTT disconnect, MCP disconnect, and log-handler close.

## 3. Behaviour

### 3.1 Auth matrix (deliverable c)

| Mode | Header(s) | Validation path | Caching | Failure codes | Used by |
|---|---|---|---|---|---|
| **Node** `verify_api_key` | `X-API-Key: node_id:node_key` | 1. Look up the **process-local dict cache** keyed by the raw header (`app/deps.py:170-185`). 2. On a miss: POST auth `/internal/validate-node` with CC's app creds and `{node_id,node_key,service_id=JARVIS_APP_ID}`, timeout 5s (`:129-159`). 3. Load the local `nodes` row (`:195`). 4. `touch_node_last_seen`, debounced to 60s (`app/services/node_liveness.py:43,46-70`). 5. Return `NodeContextProvider(node, household_id, household_member_ids)` from the auth response. | 60s TTL (`NODE_AUTH_CACHE_TTL`, `:101`). **Negative results are cached too, including "auth unreachable"** (`:151-157,191`). No size bound. | 401 for bad key, auth outage, auth non-200, or "Node not configured locally" (`:206`). Never 5xx. | ~53 routes, including all voice routes |
| Node, **legacy** | `X-API-Key: <raw key>` (no colon) | Local `nodes.api_key == header` (`:212-217`). **jarvis-auth is skipped entirely, and so is `is_active`**. `nodes.api_key` stores the node_key **in plaintext** (`app/provisioning.py:227`, `app/admin.py:367`). | none | 401 | any node route |
| **Admin key** `verify_admin_key` | `X-API-Key: <ADMIN_API_KEY>` (**the same header name as node auth**) | constant-time compare to env `ADMIN_API_KEY`; unset rejects everything (`app/deps.py:219-225`) | — | 401 | 8 uses: admin node create/patch, adapter admin, traces admin router (`app/api/traces.py:23`), node_commands, node_updates, attention journal |
| **User JWT (local)** `verify_user_jwt` | `Authorization: Bearer` | `jose.jwt.decode(token, JARVIS_AUTH_SECRET_KEY, [JARVIS_AUTH_ALGORITHM default HS256])` (`app/deps.py:305-340`). Claims read: `sub` (cast to int), `email`, `is_superuser`. **HS256 only, single key.** | — | 401 missing/expired/invalid; **500** if the secret is unset (`:318-322`) | ~73 routes: mobile, admin node list/get |
| └ household role `verify_household_role(user, hh, required_role)` | — | POST auth `/internal/validate-household-access` with CC app creds (`:343-387`) | none, so one auth round trip **per call** (and per node in `list_nodes`, `app/admin.py:262-268`) | 503 if `JARVIS_APP_KEY` unset (fail-closed); 502 for unreachable or non-200; 403 if `valid=false` | most mobile routes |
| └ `resolve_household_role` | — | same endpoint with `required_role=member`; returns `role` (`:390-438`) | none | as above, plus 502 if no `role` | memories and other role-branching routes |
| **Provisioning auth** `verify_provisioning_auth` | `X-API-Key` (admin) **or** `Authorization: Bearer` | Admin key: compared with plain `==`, **not constant-time**. Any X-API-Key that doesn't match gives 401 with **no fallthrough to JWT**. JWT: round trip to auth `GET /auth/me`, using user `id` (`app/provisioning.py:68-106`). `require_household_access` lets the admin key bypass; JWT users must be household members (`:109-120`). | none | 401 | ~60 routes. The name undersells it: smart home, routines and others use it. |
| **App-to-app** `require_app_auth` | `X-Jarvis-App-Id`, `X-Jarvis-App-Key` | GET auth `/internal/app-ping` with the caller's creds (`app/deps.py:250-289`) | none | 401 missing/invalid; 502 unreachable; otherwise auth's status code | `/internal/phone/*` (phone-gateway), `/test/command` (cut) |
| **Node-or-app** `_verify_inject_auth`, `_verify_signals_auth` | either of the two above | Try node auth, swallowing its failure, then app-ping (`app/api/memories.py:212-258`, `app/api/signals.py:44-87`). **The signals copy resolves auth from env `JARVIS_AUTH_URL` only** (`signals.py:40-41`), never discovery and never `JARVIS_AUTH_BASE_URL`. | node cache only | 401 / 502 | `/memories/inject`, `/signals` (public plugin API) |
| **Settings combined** | Bearer **or** app creds | Bearer: auth `/auth/me` and require `is_superuser` (403 otherwise). App creds: `/internal/app-ping` (`jarvis-settings-client/.../auth.py:27-97,99-157`). Writes need superuser. | none | 401/403/502 | `/settings/*` |
| **Async-job callback** | `Authorization: Bearer <JARVIS_ADAPTER_CALLBACK_TOKEN>` | `hmac.compare_digest` (`app/main.py:1850-1878`). Unset token → 503, unless `JARVIS_ALLOW_INSECURE_CALLBACKS` (`:1839-1847`). | — | 401 / 503 | 5 callbacks, all of which disappear in Go |
| **Reset token** | `X-Reset-Token` header, or `request_id` in the body | `pending_resets.verify_token`, or `verify_and_consume` (single use, 5 min, in memory) (`app/admin.py:566-600,765-786`) | in memory | 401 / 404 | node factory reset |
| **Provisioning token** | body `provisioning_token` | SHA-256 of the token compared to `provisioning_tokens`, TTL 600s (`app/provisioning.py:30,127-153,203-210`) | DB | 401; 400 if already registered | POST `/nodes/register` |
| **None** | — | — | — | — | See the list below. |

Routes with **no auth at all**, found by an AST scan:

- `/health` and `/api/v0/ping`.
- **POST `/api/v0/chat` and `/lightweight/chat`.** These are an open passthrough to llm-proxy (`app/chat.py:17-39`). `/api/v0/chat` is live (node `chat_text()`).
- GET `/admin/cache/stats`, POST `/admin/cache/clear`, DELETE `/admin/cache/{id}` (`app/admin.py:648-665`). All three are cut.
- MQTT rendezvous result posts, which write `{request_id}.json` into `/tmp` dirs:
  - POST `/mobile/node-tool-reports/{id}` (`app/api/node_tools.py:106`);
  - `/mobile/voice-profile-results/{id}` (`app/api/mobile_voice_profiles.py:296`);
  - `/device-control-results/{id}` and `/device-state-results/{id}` (`app/api/smart_home.py:1201,1421`).
- GET `/nodes/{id}/config/pending` returns K2 ciphertext, and POST `/nodes/{id}/config/{push}/ack` deletes it (`app/api/smart_home.py:2020-2090`).
- GET `/releases/latest` (`app/api/node_updates.py:101`).
- GET `/oauth/callback`, where the state parameter is the auth (07).

> **Changed by D4 / D5 / D6:** `/api/v0/chat` is dropped and `/lightweight/chat` is cut. The four result posts and config `pending`/`ack` require node auth, with the node bound to the request id or the path. `/admin/cache/*` is cut.

### 3.2 Settings framework

- **Definitions.** `SETTINGS_DEFINITIONS` defines 89 keys (`app/services/settings_definitions.py:13-861`). Each `SettingDefinition` has `key, category, value_type ∈ {string,int,float,bool,json}, default, description, env_fallback, requires_reload, is_secret, options` (`jarvis-settings-client/.../types.py:10-37`).
- **The service** is the `jarvis_settings_client.SettingsService` singleton (`app/services/settings_service.py:15-28`).
- **`get(key, default, household_id, node_id, user_id)`** (`service.py:267-351`) works as follows:
  - An unknown key returns the caller's `default` (`:296-298`).
  - For a known key the caller's `default` is **ignored**. The order is: cache, then DB cascade, then env fallback, then the definition default.
  - The cache holds 60s per `(key, hh, node, user)` and is **process-local** (`:120,150-165`).
  - **Cascade** (`:167-265`):
    1. user+node+household;
    2. user-only (hh and node NULL);
    3. node (hh+node, user NULL);
    4. household;
    5. system (all NULL).
  - The DB row's **own `value_type`** wins over the definition's when coercing (`:315-318`).
  - Coercion (`:28-58`):
    - `""` or NULL → definition default;
    - bool is true for `true/1/yes/on`;
    - a parse failure → the default, logged at WARNING.
  - DB errors are swallowed at debug level, falling through to env/default (`:323-324`).
- **`set`** (`:379-467`): exact-scope upsert, then invalidates the cache for that key **at every scope**. An unknown key returns False. It never raises.
- **Uniqueness.** `uq_setting_scope(key, household_id, node_id, user_id)` (`app/models.py:322-324`). In Postgres, NULLs never conflict, so duplicate system rows are possible. `set` reads `.first()`.
- **Seeding.** Definitions carry metadata only, so a key with no DB row doesn't appear in admin unless `list_all` synthesises it. Three migrations seed rows:
  - `f6a7b8c9d0e1_seed_settings.py` seeds `llm.interface` from env `LLM_INTERFACE_SEED`, **default `JarvisAdapterModel`**, plus the tool classifier and conversation keys.
  - `b8x9y0z1a2b3` seeds `voice.stickiness_*`.
  - `k3g4ctxthink007` replaces `model.advanced_thinking` with `model.advanced_context` and `model.include_thinking`.
- **Who scopes how.** Most callers read at system scope only: every loop in §2.4 except L15, `llm.interface`, and the memory, routine and characterization gates. Per-household reads are `attention.*`, `web_search.enabled`, `signals.automations`, `household.location` and similar (13, 10).

**Setting key prefixes → subsystem** (89 keys):

| Prefix (count) | Owner | Notes |
|---|---|---|
| `llm.*` (2) | 03 / 02 | `llm.interface`: default **`Qwen25MediumUntrained`** (a dropped provider), requires_reload, no env fallback. `llm.proxy.url`: env `JARVIS_LLM_PROXY_API_URL`. |
| `tool_classifier.*` (2), `prompt.*` (2), `transcription.cleanup_enabled`, `conversation.cache_ttl_seconds` | — | **No reader in CC** (grep). ConversationCache TTL is hardcoded to 10 min (`app/core/conversation_cache.py:89`). |
| `model.*` (3), `conversation.max_turns`, `persona.household_prompt` | 03 / 02 | |
| `memory.*` (12), `characterization.*` (4), `ambient_context.enabled` | 04 | `memory.extraction_enabled` **defaults True** (`settings_definitions.py:196`) |
| `web_search.enabled`, `web_scraping.allow_external` | 04 / 13 | fail-closed household toggles |
| `voice.*` (6) | 01 / 06 | |
| `smart_home.*` (4), `oauth.*` (2), `network.public_url` | 07 | `smart_home.use_home_assistant`: no CC reader |
| `routines.*` (3) | 08 | |
| `errands.autonomous_enabled` | 09 | |
| `signals.*` (2), `attention.*` (11), `proposals.*` (2) | 10 | `signals.automations` is a JSON blob of rules (`app/services/signal_automation_store.py:35-60`) |
| `phone_calls.*` (9), `household.location` | 11 | `phone_calls.call_context` is_secret |
| `updates.allow_check` | 05 | |
| `admin.api_key` | — | is_secret, env `ADMIN_API_KEY`. **Not read by anything**: deps reads the env var directly. |
| `adapter.*` (13) | **CUT** | |
| *(used, undefined)* `memory.embedding_enabled`, `memory.embedding_interval_seconds`, `tracing.retention_days` | 04 / 05 | `get` on an unknown key returns None, so the hardcoded `or` default always wins |

> **Changed by D11:** the defined-but-unread keys are dropped, the three used-but-undefined keys are hard-coded (sweep always on, 60 s; traces 7 days), and `llm.interface` becomes `llm.prompt_provider` with no default fallback (unknown name = hard error). D3, D17, D18, D25 and D38 remove further keys (see §0).

### 3.3 Service discovery (`app/core/service_config.py`)

- `init(db_engine)` calls `jarvis_config_client.init(config_url, refresh_interval_seconds=300, db_engine)` (`:66-102`). The client persists its cache in a **`service_configs` table in CC's own DB**, created by the client and not by alembic (`jarvis-config-client/jarvis_config_client/client.py:104`).
- `_get_url(name)` tries discovery, then the legacy env var with a WARNING, then raises `ValueError` (`:119-148`). The env names are mapped at `:24-33`:
  - auth: `JARVIS_AUTH_BASE_URL`
  - llm-proxy: `JARVIS_LLM_PROXY_API_URL`
  - logs, whisper, tts, `jarvis-mqtt-broker`, pantry, phone-gateway
- `deps._get_auth_base_url` and `admin._get_auth_base_url` swallow that error and fall back to `JARVIS_AUTH_BASE_URL` or `http://localhost:7701` (`app/deps.py:86-97`, `app/admin.py:45-53`). `signals.py` does its own thing (§3.1).

### 3.4 Logging, usage logs, latency traces

- **Console logging.** Root `basicConfig` at `JARVIS_LOG_CONSOLE_LEVEL` (default WARNING). Every module logs through the `"uvicorn"` logger (`app/main.py:37-42`).
- **Remote logging.** `JarvisLogHandler(service="command-center")` at `JARVIS_LOG_REMOTE_LEVEL` (default DEBUG) is attached to the uvicorn loggers. It is used only if `JARVIS_APP_KEY` is set and the lib is importable (`:72-98`).
- **Usage files.** Three append-only files are written per tool-loop run (`app/core/usage_logging.py`, called from `app/core/tool_execution_engine.py:268-290`):
  - `JARVIS_LLM_USAGE_LOG_PATH` (`/app/temp/llm_usage.log`), key=value tokens;
  - `JARVIS_LLM_TRACE_LOG_PATH` (`/app/temp/llm_trace.log`), JSONL holding **full prompt messages and raw responses**;
  - `JARVIS_LLM_METRICS_LOG_PATH`, JSONL of per-iteration tokens and duration, plus `answered_from_context`.

  None of them is rotated.

  > **Changed by M2:** the trace file is dropped; the metrics JSONL survives only behind a debug setting with a size cap.
- **`latency_logger`** is a global keyed by **`conversation_id`** (`app/core/utils/latency_logger.py:197-218`).
  - `RequestTiming` holds `checkpoint`, `measure` (a context manager), `record_span`, `to_spans` and `to_trace_summary`.
  - `end_request` (`:220-270`) does three things:
    - appends a text block to `temp/latency.log`;
    - logs the top 5 spans over 10ms;
    - if `source != "unknown"`, writes a `RequestTrace` row on a new daemon thread with a **new engine** (`:272-309`).
  - Requests that are started but never ended stay in `_requests` forever.
  - `request_traces` holds the user command and assistant message: this is PII, kept for 7 days (L7).

### 3.5 Error shape

- **Validation errors return 400, not 422.** The body is `{"error":"validation_error","message":"Request validation failed. Please correct the highlighted fields.","details":["body -> field: msg", …]}` (`app/main.py:111-127`).
- `HTTPException` keeps FastAPI's `{"detail": …}`, and `detail` may be a dict (the settings 404 is `{"detail":{"error":{type,message,code}}}`, `routes.py:192-200`).
- `ConversationPreconditionError(ValueError)` maps to **422** on the blocking voice endpoints (`app/core/errors.py:12-16`; the mapping is doc 01).
- An exception escaping a route gives Starlette's plain 500.

### 3.6 Database

- `app/db.py:6-54` is Postgres-only: `DATABASE_URL` or `DB_URL`, and `postgres://` is normalised.
- **`get_session_local()` builds a new engine on every call** (`:38-42`), and `deps.get_db` calls it on every request (`app/deps.py:104-111`). So does every loop, the trace writer and the settings service. A module-level `SessionLocal` exists (`:47-54`) but only some loops use it.
- Alembic: **56 revisions** in `alembic/versions/`, with a linear chain. Notable migrations:
  - `e9f0a1b2c3d4` runs `CREATE EXTENSION vector`, adds `user_memories.embedding vector(384)` and an **HNSW cosine index**.
  - `x4t5u6v7w8x9` is a data fix that sets `nodes.needs_k2=false` for pre-existing nodes.
  - Three settings seeds (§3.2). `f6a7…` has both a Postgres branch and a SQLite `INSERT OR IGNORE` branch.
  - `82feef6dbb91_update.py` is empty.
  - `s9n0o1p2q3r4` and `t0o1p2q3r4s5` create the adapter tables (cut).
- Run path: dev compose runs `alembic upgrade head && uvicorn … --port 7703` (`docker-compose.dev.yaml:47-52`). `apply_migrations.sh` and `make_migration.sh` are helpers.

## 4. Data

**Table → subsystem map.** There are 42 SQLAlchemy tables, plus the config-client's `service_configs`. All are in `app/models.py`.

| Table | Line | Owner | Notes |
|---|---|---|---|
| `nodes` | 19 | 05 | PK `node_id`. Also: `api_key` (**plaintext node key**), `room`, `user`, `voice_mode`, `last_seen`, `adapter_hash` (**cut**), `room_id` FK, `household_id`, version and busy fields, `is_active` (soft delete), `protocols` (JSON text), `needs_k2` |
| `node_tasks` | 72 | 05 | update and factory-reset tasks (L10) |
| `settings_requests`, `settings_snapshots` | 228, 254 | 05 | node settings round trip |
| `provisioning_tokens` | 569 | 05 | sha256 hash, 600s TTL (L1) |
| `request_traces` | 723 | 05 (traces) / 00 (writer) | L7 |
| `rooms`, `devices`, `device_scan_requests`, `device_list_requests`, `config_pushes` | 93, 115, 145, 171, 199 | 07 | |
| `auth_sessions` | 327 | 07 | provider OAuth sessions |
| `bluetooth_scan_requests`, `bluetooth_pair_requests` | 667, 696 | 07 | |
| `settings` | 289 | 00 (framework) / 13 | scoped, `uq_setting_scope` |
| `user_memories` | 369 | 04 | `embedding Vector(384)` (`:386`) → brute-force cosine in Go |
| `person_characterizations` | 392 | 04 | |
| `conversation_transcripts` | 461 | 04 | L6 |
| `signals` | 423 | 10 | L9 |
| `attention_events`, `attention_deliveries`, `attention_source_tiers`, `attention_consents`, `attention_feedback` | 861–956 | 10 | L16 |
| `proposal_suppressions` | 1185 | 10 | |
| `package_install_requests`, `test_install_requests` | 490, 518 | 12 | |
| `prompt_provider_install_requests` | 545 | 03 | **cut with community install** |
| `callback_jobs` | 747 | 13 | `node_id` NULL means the server plane |
| `routines`, `routine_executions` | 806, 843 | 08 | L14 |
| `schedules` | 1154 | 08 (fired by the L2 sweep) | |
| `errand_plans`, `workflows` | 1048, 1102 | 09 | |
| `phone_call_sessions`, `phone_contacts` | 965, 1018 | 11 | |
| `active_adapter`, `adapter_training_state`, `adapter_history`, `adapter_proposals` | 592, 603, 620, 638 | **CUT** | |
| `service_configs` | config-client | 00 | gone in Go (discovery is in-process) |

In-memory state owned here:

- the node-auth cache (`app/deps.py:100`);
- the settings cache (60s);
- `latency_logger._requests`;
- pending factory-reset tokens (`app/core/pending_resets.py`);
- the L15 `last_fired` dict.

Files on disk:

- `temp/latency.log` and `/app/temp/llm_{usage,trace,metrics}.log`;
- `/tmp/jarvis-*` rendezvous dirs (owned by 05/07/12).

## 5. Settings

Keys read by the platform itself:

- the loop gates and intervals in §2.4;
- `llm.interface`, read by `get_model_service` → `ModelService()` (`app/deps.py:228-247`). An invalid value gives a **500** "Invalid LLM Interface" on every route that depends on it.

Environment variables:

| Var | Effect |
|---|---|
| `DATABASE_URL` / `DB_URL` | Postgres, required |
| `JARVIS_CONFIG_URL` | discovery; when unset, a banner is logged every 30s |
| `JARVIS_APP_ID` (default `command-center`) / `JARVIS_APP_KEY` | outbound app creds. Without the key, node auth always fails, role checks return 503 and remote logging is off. |
| `JARVIS_AUTH_SECRET_KEY` / `JARVIS_AUTH_ALGORITHM` (HS256) | local JWT verification. Note the `JARVIS_` prefix, unlike auth's `AUTH_SECRET_KEY`. |
| `ADMIN_API_KEY` | admin and provisioning auth |
| `JARVIS_ENV=production` | an insecure secret becomes fatal (≥16 chars, not a known placeholder; `app/deps.py:34-83`) |
| `NODE_AUTH_CACHE_TTL` | 60 |
| `JARVIS_ADAPTER_CALLBACK_TOKEN`, `JARVIS_ALLOW_INSECURE_CALLBACKS` | callback auth. The token is also sent **outbound** on enqueue by four services (deep_research `:276`, memory_extraction `:180`, characterization `:272`, situation_matcher `:162`). |
| `JARVIS_AUTH_BASE_URL`, `JARVIS_AUTH_URL` (signals only), and the other `*_URL` | discovery fallbacks |
| `JARVIS_LOG_CONSOLE_LEVEL`, `JARVIS_LOG_REMOTE_LEVEL`, `JARVIS_LLM_{USAGE,TRACE,METRICS}_LOG_PATH` | logging |
| `JARVIS_TEST_MODE=1` | enables the adapter override on `/conversation/start` (`app/main.py:960`); cut |
| `LLM_INTERFACE_SEED` | read only by the seed migration |
| `DEBUG`, `DEBUG_PORT` (5678) | debugpy listens on 0.0.0.0 (`app/debug_setup.py:13-44`) |
| `JARVIS_MCP_URL` | dead (§2.5) |

## 6. Dependencies

- **jarvis-auth:**
  - `/internal/validate-node`
  - `/internal/validate-household-access`
  - `/internal/app-ping`
  - `/auth/me`, used by provisioning auth and the settings router
  - `/internal/users/batch`, via speaker_resolver (06)

  In Go all of these become in-process calls into the auth module.
- **jarvis-config-service:** `/services`, through the config-client.
- **jarvis-logs:** through the log-client.
- **llm-proxy:** only indirectly here. The callbacks are its queue worker calling back. `/v1/adapters/date-keys` is fetched at warmup (§7c).
- **Python libraries** that go away: settings-client (framework plus router), config-client, log-client, auth-client (unused by app code), `jose`, `pgvector`, `fasttext` (02).

## 7. Invariants, non-obvious behaviour and the LoRA cut

### 7a. Invariants a port must keep

1. **Node identity comes from the server.** `household_id` and `household_member_ids` come from auth's validate-node response. `room`, `voice_mode` and `user` come from the local `nodes` row (`app/deps.py:195-202`, `app/main.py:886-897`).
2. A node that is valid in auth but **missing locally gets 401** "Node not configured locally" (`app/deps.py:204-206`).
3. **Every authenticated node request refreshes `last_seen`**, debounced to 60s, and never fails the request (`app/services/node_liveness.py:46-70`).
4. **The node-auth cache key is the whole header.** A revoked key keeps working for up to 60s, and a bad key keeps failing for 60s, even after auth recovers.
5. **Admin and node share `X-API-Key`.** On provisioning-auth routes, any X-API-Key other than the admin key gives 401, even when a valid Bearer is also present (`app/provisioning.py:91-94`). Nodes can't use these routes, which may be intentional.
6. **The request validation error shape** is 400 with `{error,message,details[]}` (`app/main.py:111-127`).
7. **Callback auth is fail-closed:** a missing token gives 503 (`app/main.py:1850-1878`). In Go this disappears together with the routes.
8. **Settings cascade order**, user-only precedence, row `value_type`, bool truthy strings, and "unknown key → caller default": `service.py:28-58,167-351`.
9. **`/settings/{key}` masks secrets** as `********` and reports `from_db` (`routes.py:213-233`).
10. **Production refuses to boot** on a weak `ADMIN_API_KEY` or `JARVIS_AUTH_SECRET_KEY` (`app/deps.py:68-83`).
11. **Loops keep running** through exceptions.
12. **Loop gate polarity differs per loop.** L3, L4 and L11 are on unless explicitly falsy. L5 and L13 are off unless explicitly truthy.

### 7b. Non-obvious behaviour

- `int(settings.get(k) or default)`: a setting value of `0` reads as the default (`app/main.py:246,269,302,323,342,534,560,617`).
- The daily loops (L6, L14, L16) **sleep 24h first**. A process restarted at least once a day never runs them.
- `asyncio.create_task` handles are not retained (`app/main.py:198,226,…`).
- `household_member_ids` comes back from validate-node and is cached for 60s, so membership changes lag.

### 7c. LoRA/adapter entanglement list (deliverable b)

The modules to delete outright:

- `app/api/adapters.py`
- `app/services/adapter_{registry,scheduler,proposal_service,example_expansion}.py`
- `app/services/{command_example_expander,training_data_extractor,training_orchestrator,prompt_variant_builder}.py`
- `app/core/models/jarvis_adapter_model.py`, `jarvis_trained_adapter_model.py`
- `app/request_models/adapter_training_request.py`
- `training-data/`
- tests `test_adapter_*`, `test_command_example_expander`, `test_training_*`

`command_example_expander` has **no importers** (it is already dead). The others form a closed cluster rooted at `main.py` and `admin.py`.

Places where **kept** code touches adapters:

| # | Where | What | Cut action |
|---|---|---|---|
| 1 | `app/main.py:459-480` | L11 loop | drop |
| 2 | `app/main.py:1652-1836, 1881-1934` | `/adapters/train` (uses `provider.build_training_*`, `adapter_example_expansion`, `adapter.expansion_*` settings) and `/adapters/jobs/callback`, which writes `nodes.adapter_hash` | drop |
| 3 | `app/main.py:891-975` | `node_context["adapter_hash"]=None`, the dead `if False` AdapterRegistry block, and the `JARVIS_TEST_MODE` `adapter_settings` override | drop. **Changed by D47 (M4):** `adapter_settings` is dropped from the schema too; senders don't break because the decoder ignores unknown fields on this route. |
| 4 | `app/admin.py:61-67,121,670-760` | `NodeResponse.adapter_hash`; `/admin/adapter/{hh}`, `/history`, `/rollback` | drop the routes. Keep `adapter_hash: null` in NodeResponse only if mobile reads it (Q7). |
| 5 | `app/models.py:28,592-664` | `nodes.adapter_hash` and the four adapter tables | do not create them in SQLite; skip them on legacy import |
| 6 | `app/core/conversation_handler.py:263-279,547-559,1356-1400,1719-1722`; `app/core/tool_execution_engine.py:244-250,734-751`; `app/core/models/jarvis_tool_model.py:208-226`; `app/core/model_service.py:258-281`; `app/core/llm_proxy_client.py:64-217` | `adapter_settings` threaded from `node_context.adapter_hash` into the llm-proxy chat payload | drop the parameter end to end; llm-proxy's adapter path is cut too |
| 7 | **`app/core/conversation_handler.py:483-489` → `app/core/llm_proxy_client.py:315-325`** | Warmup fetches the **date-key vocabulary** from llm-proxy **`/v1/adapters/date-keys`**, which lives in llm-proxy's `api/adapter_routes.py` and `services/date_key_adapter.py`. Kept providers inject it as `DT_KEYS:` (inherited from `qwen25_7b_compressed.py:116-131`). **Cutting llm-proxy adapter routes silently empties `DT_KEYS`.** | Move the static key list into CC/Go (doc 03). See Q4. |
| 8 | `app/core/interfaces/ijarvis_prompt_provider.py:258-298` and the providers | `build_training_prompt/completion/system_prompt` are abstract in the interface. The kept Qwen3 providers **inherit** them from dropped classes (`Qwen3_8B_Compressed` ← `Qwen25_7B_Compressed` ← `Qwen25MediumUntrained`; `Qwen3_14B_Compressed` ← `Qwen3LargeUntrained`). | Drop these from the Go provider interface, and flatten the inheritance (doc 03) |
| 9 | `app/core/models/__init__.py`, `app/core/model_factory.py:95-118`, `app/core/prompt_provider_factory.py:38,201` | The legacy `core/models` lookup and the **hardcoded fallback `JarvisToolModel`**, which also carries adapter code | Go fallback should be a kept provider (Q3) |
| 10 | `alembic/versions/f6a7b8c9d0e1_seed_settings.py` | `LLM_INTERFACE_SEED` defaults to `JarvisAdapterModel` | Go seed default (Q3) |
| 11 | `app/services/settings_definitions.py:408-505` | 13 `adapter.*` keys | drop; skip on import |
| 12 | `app/services/inbox_notification_service.py:394-500` | `push_adapter_proposal_to_inbox` and deploy/rollback inbox items (category `adapter_proposal`) | drop. Existing inbox items in notifications become inert (Q7). |
| 13 | `JARVIS_ADAPTER_CALLBACK_TOKEN` | named for adapters but guards all 5 callbacks | gone in Go |
| 14 | `app/core/tool_call_parser.py:342,414` | `primary_examples_only` ("for adapter models"). It is also used by `Qwen25MediumUntrained:111`, which the kept Qwen3-8B inherits. | **Keep the behaviour**; it only looks adapter-related. Doc 03. |
| 15 | `app/deps.py:228-247` docstring; `app/core/interfaces/imodel_interface.py:164` | comments only | — |

**Not LoRA, so keep:**

- `app/core/tool_router/training.py` and `/tool-router/train`: fastText, live caller (App. A).
- `app/api/smart_home.py:307,1344,1522`: "adapter" there means device-protocol adapters.

## 8. Oddities

1. **CC verifies user JWTs locally with HS256 only, using `JARVIS_AUTH_SECRET_KEY`** (`app/deps.py:31-32,326`). Root `CLAUDE.md` says only jarvis-auth and auth-client verify locally. Once auth mints RS256, about 73 mobile routes on CC will 401.
2. **The legacy raw-key node path** (`app/deps.py:211-217`) bypasses jarvis-auth and `is_active`. `nodes.api_key` holds every node key in plaintext. A factory-reset node, which is soft-deleted, still matches.
3. **An auth outage is cached as invalid credentials for 60s** and returned as 401, not 502/503 (`app/deps.py:151-157,191`). Nodes may treat that as "re-provision".
4. **POST `/api/v0/chat` and `/lightweight/chat` are unauthenticated** LLM passthroughs (`app/chat.py:17-39`).
5. **The `/admin/cache/*` routes are unauthenticated** (`app/admin.py:648-665`). They are cut anyway.
6. The MQTT result POSTs and config ack are unauthenticated, and they rendezvous through `/tmp/*.json` files (§3.1).
7. Signals auth uses `JARVIS_AUTH_URL`, defaulting to localhost (`app/api/signals.py:40-41`), unlike every other path.
8. Provisioning admin-key compare is not constant-time (`app/provisioning.py:92`), while `verify_admin_key` is.
9. `get_session_local()` creates a new engine per call: a pool per request (`app/db.py:38-42`, `app/deps.py:104-111`).
10. **Settings drift:**
    - three keys are used but undefined (`memory.embedding_*`, `tracing.retention_days`);
    - about 8 keys are defined but have no CC reader (§3.2);
    - `llm.interface` defaults to a dropped provider (`Qwen25MediumUntrained`), while the migration seed defaults to `JarvisAdapterModel` and the factory fallback is `JarvisToolModel`;
    - the model-factory docstring claims an env fallback `JARVIS_MODEL_INTERFACE` that the definition does not have.
11. **CC `CLAUDE.md` says memory extraction is opt-in.** The code default is `memory.extraction_enabled=True` (`settings_definitions.py:196`) and the loop gate is "on unless false".
12. **CLAUDE.md errors:**
    - it lists 7 workers; there are 16 in `main.py`;
    - it says the node-task timeout is "30 min"; it is 15/10 min;
    - it calls `tracing.retention_days` a setting.
13. `/tool-router/train` runs fastText training synchronously inside an async handler (`app/main.py:1602-1650`), which blocks the event loop. Owner: doc 02.
14. The MCP client and `jarvis_mcp_client.resolve_date_keys` (`app/core/tool_execution_engine.py:72-76,448-457`) are dead, because the library isn't a dependency.
15. The Dockerfile `CMD` uses port **8002** (`Dockerfile`), but compose and the docs use 7703. Caddy fronts `jarvis-voice-api:7703` on 443/9443 (`Caddyfile`). The container name is stale.
16. `llm_trace.log` holds full prompts (memories, PII) with no rotation or retention.
17. `user_id=int(payload["sub"])` raises a 500 on a non-numeric `sub` (`app/deps.py:337`).
18. The warmup comment says date keys are "trimmed to a small subset", but the code sends all of them (`app/core/conversation_handler.py:477-487`).

## 9. Tests

Relevant files in `tests/` (168 entries in total):

- `test_secret_guard.py`: placeholder and length rules, prod abort.
- `test_async_job_callback_auth.py`: token, 503 and insecure-mode matrix.
- `test_household_authz.py`, `test_memories_authz.py`, `test_camera_authz.py`, `test_admin_traces_auth.py`: role checks and fail-closed 503.
- `test_provisioning_tokens.py`, `test_register_node_with_auth.py`, `test_node_liveness.py`.
- `test_settings.py`, `test_settings_service.py`: cascade, coercion and cache.
- `test_service_config.py`, `test_latency_spans.py`.

There are **no tests for the background loops** or for `verify_api_key` caching.

Golden and contract candidates:

- **The auth matrix as a black-box suite.** For each mode, cover the header permutations: missing, malformed, wrong, auth down, and the colon/no-colon forms. Assert status code and `detail` text, including the 400 validation shape.
- **The settings router:** list, get (unknown key → 404 dict shape, secret masking, `from_db`), and put (non-superuser → 403).
- **A settings-cascade table test:** 5 scopes × presence, compared against the Python `SettingsService` with the same rows.
- **Loop behaviour as unit tests on the Go scheduler**, using the same thresholds as the Python code (node-task 15/10 min, gate polarity).

## 10. Questions for the user

1. **[behaviour] Should the legacy raw-key node auth path go, and should node keys stay in CC in plaintext?**
   - *Why it matters:* `nodes.api_key` holds every node key in plaintext, and the no-colon path skips auth and `is_active` (§8.2). In Go, auth and CC share a process and a DB, so node validation becomes a function call against auth's hashed key table.
   - *Options:* (a) port it as is; (b) drop the legacy path, and stop importing `nodes.api_key`; (c) keep it but check `is_active`.
   - **My recommendation:** (b). Every current node sends `node_id:node_key`. I can confirm that with a log grep before the cut.
   - **Decided (D40 default, via 05.Q10):** (b). Drop the bare-key path and don't import `nodes.api_key`; verify the jarvis-dev and prod node headers first.
2. **[behaviour] How should CC verify user JWTs during the HS256 → RS256 window?**
   - *Why it matters:* CC is a third local verifier the RS256 plan doesn't list. It verifies HS256 only with `JARVIS_AUTH_SECRET_KEY` (§8.1).
   - *Options:* (a) CC calls the Go auth module's verifier in-process: both algorithms, keys chosen by algorithm family, with the forged-HS256-with-public-key test; (b) keep a CC-local HS256 verifier.
   - **My recommendation:** (a). There is one verifier in the binary, and `JARVIS_AUTH_SECRET_KEY` disappears.
   - **Open:** not covered by any decision or by the B triage. The recommendation (a) stands as the working assumption.
3. **[behaviour] What should the default and fallback `llm.interface` be, and what happens when an imported DB names a dropped provider?**
   - *Why it matters:* today there are three different defaults, and none of them is kept: `Qwen25MediumUntrained`, `JarvisAdapterModel` and `JarvisToolModel` (§8.10). A legacy import may carry e.g. `Gemma3…`.
   - *Options:* (a) default `Qwen3_8B_Compressed`, and map unknown names to it with a WARNING plus an admin-visible notice; (b) fail warmup with a 500, as today.
   - **My recommendation:** (a). Use `Qwen3_14B_Compressed` instead if the hardware probe says there is enough VRAM.
   - **Decided (D11, D12):** neither option. The key is renamed `llm.prompt_provider` and set at install time by the jarvis-admin wizard / installer (prod = `Qwen3_14B_Compressed`). An unknown or dropped provider name is a **hard error**, not a fallback; import maps the old key name. The admin catalog only offers models with a kept provider.
4. **[scope] Where should the date-key vocabulary live, given that it is served from llm-proxy's adapter routes?**
   - *Why it matters:* cutting LoRA in llm-proxy silently removes `DT_KEYS` from the kept Qwen3 prompts (§7c #7), which changes model behaviour.
   - *Options:* (a) a static list embedded in the CC prompt module; (b) keep a non-adapter `/v1/date-keys` in the LLM module.
   - **My recommendation:** (a). It is static vocabulary, shared with `date_resolution.py`.
   - **Decided (D40 default, via 03.Q9):** (a). One shared Go constant used by the prompt, the matcher and the resolver; the LLM fallback is dropped.
5. **[behaviour] Should passive memory extraction be on or off by default?**
   - *Why it matters:* the code default is ON (`memory.extraction_enabled=True`, gate "on unless false"). CC `CLAUDE.md` calls it opt-in and privacy-sensitive. A fresh `jarvisd` install inherits whichever default we pick.
   - *Options:* (a) on, matching the code; (b) off and opt-in, matching the doc.
   - **My recommendation:** ask, because this is a product call. I lean towards (b) for a new install, and preserving whatever row an imported DB has.
   - **Decided (D19):** (a), on by default (opt-out), but **per household**: honour `memory.enabled` plus a household-scoped `memory.extraction_enabled`. Both keys go on the mobile household-settings allowlist.
6. **[behaviour] Should the unauthenticated routes stay open?** These are `/api/v0/chat`, the MQTT result POSTs, config ack, and `/releases/latest`.
   - *Why it matters:* `/api/v0/chat` is a live caller (node `chat_text()`), but it is an open LLM on the LAN. The result POSTs let anyone spoof a device-state or tool report if they guess a request_id.
   - *Options:* (a) port them open, for contract fidelity; (b) require node `X-API-Key`, since node-setup already sends it on most calls; (c) open, but rate-limited.
   - **My recommendation:** (b) for `/api/v0/chat` and the result POSTs, but only after checking that node-setup sends `X-API-Key` on those calls. Otherwise it's a node change, which is out of scope. Keep `/releases/latest` open.
   - **Decided (D4, D5, D6):** `/api/v0/chat` is **dropped**; node `chat_text()` moves to the node-authed `/api/v0/node/llm/chat` (node-setup change). The result POSTs and config `pending`/`ack` get node auth bound to the request/path node. `/releases/latest` was not decided and stays open per the recommendation.
7. **[scope] Which adapter-shaped fields must survive as inert compatibility fields?**
   - *Why it matters:* the hard constraint is "JSON shapes unchanged".
   - *Options:*
     - (a) keep `adapter_hash: null` in NodeResponse;
     - (b) accept and ignore `adapter_settings` on `/conversation/start`;
     - (c) existing `adapter_proposal` inbox items in notifications: leave them, or delete them on import.
   - **My recommendation:** keep (a) and (b) as ignored or null fields. For (c), delete them on import, because their action buttons hit cut routes.
   - **Decided (D47/M4):** (b) `adapter_settings` is dropped from the schema; senders still work because unknown fields are ignored. **Decided (D9, via 05.Q9):** (a) `adapter_hash` is dropped from `NodeResponse`, after checking mobile ignores it. **Open:** (c) what to do with existing `adapter_proposal` inbox items on import.
8. **[behaviour] Should Go keep the 60s node-auth cache semantics?**
   - *Why it matters:* the cache is a pure cost-saver for an HTTP round trip that no longer exists in-process. Today revocation lags 60s, and an auth outage turns into 401s (§8.3).
   - *Options:* (a) no cache, and validate against SQLite every call (well under 1ms); (b) cache positives only.
   - **My recommendation:** (a).
   - **Open (partly):** negative caching of auth outages goes away with in-process auth (D40 via 05.Q10). Whether to keep a positive cache at all was not decided; (a) stands as the working assumption.
9. **[behaviour] Should the background loops move to the durable job queue with persisted last-run times?**
   - *Why it matters:* today the daily cleanups never run on a box that restarts daily, and the journal card can post twice after a restart.
   - *Options:* (a) a scheduler built on the jobs table, with a `last_run_at` per loop and catch-up on boot; (b) port as in-memory tickers.
   - **My recommendation:** (a). The interval and gate semantics stay identical.
   - **Decided (D27, D26):** (a). One scheduler engine on the durable queue with a persisted `last_run_at`; a missed trigger fires once, late.
10. **[minor] Should the three used-but-undefined keys become real settings?** They are `memory.embedding_enabled`, `memory.embedding_interval_seconds` and `tracing.retention_days`.
    - *Why it matters:* without definitions they are invisible in admin.
    - *Options:* (a) define them with today's effective defaults; (b) hardcode them.
    - **My recommendation:** (a). And delete the eight dead keys (`tool_classifier.*`, `prompt.include_*`, `transcription.cleanup_enabled`, `conversation.cache_ttl_seconds`, `admin.api_key`, `smart_home.use_home_assistant`), unless admin or mobile reads them through `/settings/*`. I'll check that.
    - **Decided (D11):** (b). The three keys stay undefined and are hard-coded to today's behaviour (sweep always on every 60 s; trace retention 7 days). Defined-but-unread keys are dropped.
11. **[minor] Should the `llm_{usage,trace,metrics}.log` and `latency.log` files be kept?**
    - *Why it matters:* the trace log stores full prompts, including memories, forever.
    - *Options:* (a) drop the files and rely on `request_traces` plus the log module; (b) keep the metrics JSONL only, behind a debug setting with a cap.
    - **My recommendation:** (b).
    - **Decided (M2/D47):** (b). Drop the trace file; keep the metrics JSONL only behind a debug setting with a size cap.
12. **[minor] Should the four settings scopes and the env fallback be kept?**
    - *Why it matters:* user-scoped and node-scoped rows appear to be rare. Env fallback is a container-era idea, and in a single binary it would mean `jarvisd` reading old env names.
    - *Options:* (a) port all of it exactly; (b) keep the four scopes and drop env fallback, except for secrets and URLs that become `jarvisd` config.
    - **My recommendation:** (b), with the cascade order preserved exactly.
    - **Decided (M3/D47):** (b), cascade order preserved exactly.

## 11. Go port notes

- **Shape.** The `cc` module gets a `Platform` struct holding the following, injected into every handler package:
  - `DB` (sqlc, one writer and a read pool);
  - `Settings` (the cascade service and a 60s cache, invalidated on write; nothing is shared across processes, so the cache can even be dropped);
  - `Auth` (an interface onto the auth module: `ValidateNode`, `VerifyJWT`, `HouseholdRole`, `ValidateApp`);
  - `Jobs` (the embedded queue);
  - `MQTT` (the embedded broker client);
  - `Clock`.
- **Auth middleware.** One function per mode, returning a typed principal. The error `detail` strings must match the table in §3.1 (clients and tests read them). Use `subtle.ConstantTimeCompare` everywhere.
  - No bare-key node path and no negative caching of auth failures (D40 via 05.Q10).
  - Node-auth the result posts and config `pending`/`ack`, binding the node to the request id or path node (D4, D6). Don't port `/api/v0/chat` or `/lightweight/chat` (D5).
  - Household checks accept any of the caller's memberships, not only the JWT's active household (D5).
- **Validation errors.** A custom binder must produce the 400 `{error,message,details}` shape. `details` uses FastAPI's `loc -> loc: msg` format, so collect the Pydantic `msg` strings in the contract suite.
- **Loops.** Per D27, these are trigger kinds on the one scheduler engine (a `next_fire_at` trigger table on the embedded durable queue, with claims), each `{name, interval func(settings), gate func(settings), run func(ctx)}`. Each run gets a per-run `recover()`, persists `last_run_at`, and respects the global background-concurrency cap (one LLM job at a time). Missed triggers fire once, late (D26).
  - L3's gate is per household, default on (D19). L4 and L7 use hard-coded values (D11). L13 has no gate (D25). L15 uses the household timezone (D18). L11 and the situation-matcher debounce are not ported (D9, D17).
  - L3 and L5 enqueue jobs whose completion handlers replace the HTTP callbacks.
  - L4 calls the embedding engine.
  - L17 is gone: discovery is in-process.
  - L18 becomes the embedded broker.
- **Settings router.** Mount `/settings/*` on the **7703 listener** with combined auth: superuser JWT or app creds. Keep the 404 dict shape, the secret masking and `from_db`. The `llm.prompt_provider` options (renamed from `llm.interface`, D11) come from the Go provider registry.
- **Simplifications enabled by the single binary:**
  - no callback tokens;
  - no `service_configs` table;
  - no per-request engines;
  - no `/tmp` rendezvous files (use in-process channels keyed by request_id; doc 05/07);
  - no remote-logging handler (log straight into the logs module);
  - trace writes go onto a buffered channel instead of a thread per request;
  - no `llm_trace.log`; the metrics JSONL only behind a debug setting with a size cap (M2);
  - factory-reset tokens live in SQLite with the `NodeTask`, not in memory (D10).
- **Risks:**
  - the JWT dual-algorithm verifier;
  - preserving 401-vs-403-vs-502-vs-503 for each auth mode;
  - settings cascade parity on an imported DB with duplicate NULL-scope rows: pick the lowest `id` to match `.first()` with no ORDER BY, and note that Postgres's order is not guaranteed.
- **Legacy import.** Skip the adapter tables, the never-written attention tables, `service_configs`, `prompt_provider_install_requests` and `nodes.api_key` (Q1, D40). Rename `llm.interface` → `llm.prompt_provider`; an unknown provider name is a hard error, not a remap (D11). Drop `adapter.*` settings rows and every key §0 removes (`voice.stickiness_*`, `voice.emotion_*`, `routines.scheduler_enabled`, `attention.timezone`, `proposals.proactive_enabled`, the defined-but-unread keys).
- **Env.** Per M3, read no legacy env-fallback names except secrets and URLs that become `jarvisd` config. `JARVIS_TEST_MODE` is gone (D47).
