# 13 — Mobile chat, inbox, callbacks, household settings

Source root: `jarvis-command-center/app/` (paths below are relative to it unless prefixed by another repo name).
Files: `api/mobile_chat.py`, `services/inbox_notification_service.py`, `api/node_commands.py` (node notification routes + `/nodes/{id}/actions`), `api/callbacks.py`, `services/server_callback_registry.py`, `api/mobile_household_settings.py`, `services/persona_presets.py`, `services/settings_definitions.py` (household-exposed subset), `api/traces.py` (mobile route only).

---

## 1. Purpose

Four related surfaces that turn CC into something a phone (and the browser) can talk to, and that let node commands reach a phone:

| Surface | User terms | Who uses it |
|---|---|---|
| **Mobile chat** | Type to Jarvis in the app or in jarvis-web. You get the same brain as the voice node, plus the selected node's commands run on that node. | jarvis-node-mobile (`src/api/chatApi.ts`, `src/hooks/useChat.ts`), jarvis-web (`lib/api.ts:186`, via the Next rewrite `/api/cc/:path*` → `CC/api/v0/:path*`, `next.config.ts:52`) |
| **Inbox and push** | Cards in the app's Inbox tab, optionally with a push notification that deep-links to the card. | CC internally (errands, schedules, routines, phone, proposals, signal automations, attention journal, callbacks), and nodes and community packages through `/node/inbox-item`, `/node/push-notification` and `/node/send-link`. These three routes are **public plugin API** (PLAN Appendix A). |
| **Interactive callbacks** | Tapping a row or button inside an inbox card runs code: either a node command's `@callback`, or a server-side handler in CC. The result is shown inline or as a new card. | Mobile (`commandCenterApi.ts:126-146`), the node (`jarvis-node-setup/scripts/mqtt_tts_listener.py:301-447`), and CC's own server tools |
| **Household settings** | A household admin flips allowlisted feature toggles and edits the persona from the mobile Household Settings screen. | Mobile (`src/api/householdSettingsApi.ts`, `screens/Settings/HouseholdEditScreen.tsx:495-532`) |

Mobile chat also has a second action path. When a command returns `actions` (Send/Cancel buttons), the app posts the tap to `POST /nodes/{id}/actions`, which is an MQTT round trip to the node's `handle_action`.

---

## 2. Entry points

All routes are under `/api/v0`. Mount points are at `main.py:722,807,812,816,837`.

| Method + path | Auth | Live caller | Notes |
|---|---|---|---|
| `POST /mobile/chat` | user JWT + household `member` | mobile `chatApi.ts:138`, web `lib/api.ts:186` | SSE stream (§3.1) |
| `POST /mobile/chat/warmup` | user JWT + `member` | mobile `chatApi.ts:289`, web `lib/api.ts:223` | returns `{conversation_id, tools_loaded}` |
| `POST /nodes/{node_id}/actions` | user JWT + `member` of the node's household | mobile `commandCenterApi.ts:39` (chat bubble buttons and inbox confirmation buttons), web `lib/api.ts:628` | blocks for up to 10 s (`node_commands.py:83-135`) |
| `POST /node/inbox-item` | node `X-API-Key` | node `services/inbox_backend.py:86` (SDK `JarvisInbox`), `export_shopping_list_command.py:624`, `export_todo_list_command.py:363`, community packages | `node_commands.py:629-702` |
| `POST /node/push-notification` | node `X-API-Key` | node `agents/reminder_agent.py:151`, community packages | `node_commands.py:333-446` |
| `POST /node/send-link` | node `X-API-Key` | node `commands/send_link_command.py:223` | `node_commands.py:507-582` |
| `POST /callbacks` | user JWT | mobile `commandCenterApi.ts:130` | 201; node plane or server plane |
| `GET /callbacks/{job_id}` | node `X-API-Key` (owner node only) | node `mqtt_tts_listener.py:328` | payload fetch |
| `POST /callbacks/{job_id}/result` | node `X-API-Key` (owner node only) | node `mqtt_tts_listener.py:443` | |
| `GET /callbacks/{job_id}/status` | user JWT + `member` | mobile `commandCenterApi.ts:142` | poll |
| `GET /mobile/household/{hh}/settings` | user JWT + `member` | mobile `householdSettingsApi.ts:43` | |
| `PUT /mobile/household/{hh}/settings/{key:path}` | user JWT + `admin` | mobile `householdSettingsApi.ts:55` | body `{"value": ...}` |
| `GET /mobile/household/{hh}/persona/presets` | user JWT + `member` | mobile `householdSettingsApi.ts:89` | static |
| `GET /mobile/traces/{conversation_id}` | user JWT (**no household check**) | none | **CUT** (Appendix A) |

**MQTT verbs published** (all go to `jarvis/nodes/{node_id}/commands` as `[{"command": X, "details": {..., "request_id": id}}]`, `services/node_command_service.py:64-68`):

- `tool_call`: mobile chat tool execution (`node_command_service.py:170-189`)
- `report_tools`: fetch the node's tools during warmup (`api/node_tools.py:82`, doc 05/12)
- `action`: button tap (`node_commands.py:101`)
- `callback`: interactive callback; `details` carries only `request_id` = job id (`callbacks.py:211-216`)

**Node → CC result channel for `tool_call` and `action`:** `POST /device-control-results/{request_id}` (`api/smart_home.py:1200-1209`). It has no auth and writes `<tmp>/jarvis-device-control/{id}.json`. Owned by doc 05.

**Startup registration** (server-plane callbacks, `main.py:488-502`):

| `command_name` | callbacks | registered at |
|---|---|---|
| `make_phone_call` | `confirm_call`, `cancel_call`, `escalation_answer` | `services/phone_call_service.py:1034-1038` |
| `errand` | `approve_errand_plan`, `refine_errand_plan`, `replan_errand_plan`, `discard_errand_plan`, `approve_replan`, `stop_errand` | `services/errand_service.py:1370-1375` |
| `schedule` | `cancel_schedule` | `services/schedule_service.py:364` |
| `jarvis.proposable_action` | `execute`, `dismiss`, `suppress` | `services/proposable_action_service.py:463-465` |
| `jarvis.signal_automation` | `execute`, `dismiss` | `services/signal_automation_executor.py:371-372` |

No background loop belongs to this subsystem. Notably, nothing sweeps `callback_jobs` (§8).

---

## 3. Behaviour

### 3.1 `POST /mobile/chat` — the SSE stream

Request (`mobile_chat.py:34-44`):

```
message: str (1..5000)        node_id: str            household_id: str
conversation_id: str|null     timezone: str = "America/New_York"
client_tools: [obj]|null      available_commands: [obj]|null
include_reasoning: bool = false
```

Pre-stream checks are ordinary HTTP errors, not SSE events:

- `verify_household_role(member)` gives 403. It is a round trip to auth (doc 00).
- `_validate_node_in_household` gives 404 with `"Node {id} not found in household {hh}"` (`:105-118`, `:575-576`).

Response headers: `text/event-stream`, `Cache-Control: no-cache`, `Connection: keep-alive`, `X-Accel-Buffering: no` (`:578-586`).

Generator flow (`_chat_stream`, `:276-507`):

1. `conversation_id = request.conversation_id or "mobile-" + uuid4().hex[:12]` (`:285`). A latency trace starts with `source="mobile"` (`:286-290`).
2. **Warm check.** If `conversation_cache.get_tools(conversation_id)` is None (cold, or expired after the 10-minute TTL, `core/conversation_cache.py:89`):
   - emit `status` "Starting conversation...";
   - run `_do_warmup`, which **reuses the client's `conversation_id`** (`:294-305`).
3. **Acknowledgment.** Emit `acknowledgment` with `generate_acknowledgment(message)`, a keyword-pool random pick with no LLM (`services/acknowledgment_service.py:75-91`; `:309-310`).
4. Call `model_service.process_voice_command_with_tools(voice_command=message, conversation_id, speaker_user_id=user.user_id, turn_context={"source": "chat"})` (`:313-321`). This is **the exact voice-pipeline entry point** (doc 01/02). `source: "chat"` tells the not_for_me machinery that the speaker is known and that overheard speech is impossible.
5. **Tool loop**, at most `MAX_TOOL_ITERATIONS = 5` (`:28`, `:333`), branching on `result.stop_reason`:
   - **`complete` / `server_tool_complete`:**
     - Strip a leading `[Tool data: …]\n\n` prefix (`:340-344`).
     - Emit **fake streaming**: one `delta` per space-separated word, with a 20 ms sleep between words when there are more than 3 words (`:345-351`).
     - Emit `done` with `stop_reason:"complete"` (always "complete", even for server_tool_complete), plus `actions` / `action_context` / `action_preview` if collected, plus `reasoning` if requested (`:353-367`).
     - End the trace, and fire-and-forget the transcript log for memory extraction (`:373-380`, `_log_mobile_transcript` `:60-87`, doc 04). This is the **only branch that logs a transcript.**
   - **`validation_required`:** emit `done` with `stop_reason:"validation_required"`, `validation` (the `validation_request` dict) and `full_text` (= assistant_message or `validation.question`) (`:383-396`). There is no delta, and the next user message continues the same conversation.
   - **`tool_calls`:**
     1. Emit the LLM's intermediate `assistant_message`, if any, as one `delta` with a trailing space (`:404-406`).
     2. Emit `status` "Running command on node..." (`:408-411`).
     3. Route each tool call **sequentially** to the selected node via `_route_tool_call_to_node` → `dispatch_node_command` (`:414-421`; §3.2).
     4. **Action harvesting** (`:424-444`): if a tool output has `actions` (or `context.actions`), then `pending_actions` = that list (the last one wins), `action_context = {command_name: <fn name>, context: output.context ?? output}`, and `action_preview = context.preview || context.message`.
     5. Call `continue_conversation_with_tool_results(conversation_id, tool_results)` (`:453-456`). This deliberately does *not* use `stream_final_response`, so the llama.cpp prefix cache survives (comment `:446-452`).
     6. If `tool_calls` is empty, break and fall through to the error below.
   - **`error`:** emit `error` with `message = result.error` (`:458-469`).
   - **any other** (in practice `not_for_me`): emit `done` with that `stop_reason` and `full_text` = assistant_message, which may be `""` (`:471-485`).
6. Loop exhausted: emit `error` "Too many tool iterations" (`:487-495`).
7. Any exception: emit `error` with `str(e)` (`:497-507`). The HTTP status stays 200.

#### Frozen SSE contract

The framing is `data: <json>\n\n` with no `event:` or `id:` lines (`_sse_event` `:90-92`).

| `type` | Fields | Emitted at | Consumer behaviour |
|---|---|---|---|
| `status` | `message: str` | cold warmup (`"Starting conversation..."`), before node tools (`"Running command on node..."`) | mobile shows it in the assistant bubble only while that bubble is empty (`useChat.ts:322-333`); web shows it too |
| `acknowledgment` | `text: str` | always, once, before the LLM call | mobile replaces the bubble with it (`useChat.ts:335-348`); web ignores it (no case) |
| `delta` | `text: str` | words of the final answer (each word plus a trailing space except the last); intermediate message plus a trailing space | appended; the first delta clears a status/ack bubble (`useChat.ts:350-365`) |
| `done` | `conversation_id`, `full_text`, `stop_reason` (`complete`, `validation_required` or other), `trace_summary`; optional `actions` [SDK action dicts `{button_text, button_action, button_type, button_icon?, completion_message?}`], `action_context {command_name, context}`, `action_preview`, `validation`, `reasoning` | terminal | **`full_text` replaces the bubble** (`useChat.ts:381`), so the intermediate delta text disappears; the client stores `conversation_id` for the next turn |
| `error` | `message`, `conversation_id`, `trace_summary` | terminal | bubble shows the message; `conversation_id` is kept |

`trace_summary` = `{total_duration_ms, span_count, status, service_hops:[{service, duration_ms, status, steps:[str]}]}` (`core/utils/latency_logger.py:146-194`). Mobile renders it (`chatApi.ts:26-38`).

Every stream ends with exactly one `done` or `error`.

**Client parsing fragility (must be designed around):** both clients parse each network chunk on its own. Mobile uses `parseSSEChunk(newText)` per XHR `onprogress` (`chatApi.ts:102-119,159-169`) and web per `reader.read()` (`lib/api.ts:205-210`). A `data:` line split across two chunks is **silently dropped** ("Incomplete JSON — skip"). The Go server must write each event as **one write followed by one flush**, and keep events small.

### 3.2 Warmup and headless node execution

`POST /mobile/chat/warmup` (`:513-554`):

1. Membership and node validation.
2. Always mint a **new** `mobile-xxxxxxxxxxxx` id.
3. `_do_warmup`.
4. Return `tools_loaded`, which is the count of cached tools minus CC server tools (`:541-544`).

Mobile calls it on screen open and node change, and again after a fresh tool fetch if no messages have been sent yet (`useChat.ts:166-186,235-250`).

`_do_warmup` (`:217-237`):

- **`_build_node_context`** (`:143-214`) builds:
  - `{node_id, room, user: email or "user-<id>", household_id, speaker_user_id, voice_mode: node.voice_mode or "brief", rich_response: True}`;
  - `speaker_name`, from auth `resolve_speaker_name` (cached; failure is non-fatal);
  - `room_hierarchy`, only if any room has a parent;
  - `agents.home_assistant.device_controls`, synthesised from the household's active `Device` rows grouped by `domain` (default `"switch"`) with `state:"unknown"`. This stands in for the node's DeviceDiscoveryAgent context (comment `:186-188`).
- **`_resolve_tools`** (`:121-140`): an **MQTT `report_tools` round trip to the node first (10 s timeout)**, then fall back to the `client_tools` / `available_commands` in the request body. An offline node therefore makes warmup (and cold chat) block for 10 s.
- `model_service.warmup_conversation_with_tools(...)` (doc 02) builds the system prompt and caches tools under the conversation id.

**Headless tool execution**: `dispatch_node_command` (`services/node_command_service.py:145-196`):

1. Publish `tool_call` with `details = {command_name, arguments, tool_call_id, reply_request_id: rid, trusted: True, user_id?, voice_command?, request_id: rid}`. `voice_command` is the typed message, which the node puts into `RequestInformation` (the spotify playlist detection uses it).
2. Register `rid` in the in-memory `_pending_commands` with a 5-minute TTL (`:53-59`). `trusted` makes the node skip `/commands/{id}/verify`.
3. Poll for `<tmp>/jarvis-device-control/{rid}.json` every 100 ms, for up to 10 s (`:118-142`).
4. The node (`mqtt_tts_listener.py:830-927`):
   - looks the command up, refreshing discovery once on a miss;
   - JSON-decodes string args;
   - sets the SDK current user id and runs `cmd.execute(ri, secrets=..., **arguments)`;
   - builds `output = context_data + {success, error?, actions?}`;
   - POSTs `{"output": {...}}` to `/device-control-results/{rid}`.
5. Return `output`. A timeout gives `{"success": False, "error": "the node didn't respond in time", "timeout": True}`; a publish failure gives `{"success": False, "error": "could not dispatch to node: ..."}`. It never raises.

`_route_tool_call_to_node` wraps this as `{"tool_call_id", "output"}` (`mobile_chat.py:243-270`). The errand executor uses the same primitive (doc 09).

### 3.3 Chat action buttons → `POST /nodes/{node_id}/actions`

`node_commands.py:79-135`:

- JWT membership of the node's household (household resolved from the node row, `:32-42`).
- Publish `action` with `{command_name, action_name, context, trusted: True, user_id}`.
- **Synchronously** poll the same result dir for 10 s, sleeping the threadpool thread (`time.sleep(0.1)`).
- Returns `{status:"completed"|"timeout", request_id, success, error}`.

The node runs `cmd.handle_action(action_name, context)` and posts `{success, error, input_required?}` (`mqtt_tts_listener.py:192-298`). The response model drops `input_required` (`ActionResponse` `:69-73`).

Context differs by entry point:

- from chat the app sends `context = action_context.context`, the whole tool output context (`HomeScreen.tsx:411-418`);
- from an inbox confirmation card it sends `{draft: metadata.draft}` with `metadata.node_id` / `metadata.command_name` (`InboxDetailScreen.tsx:170-188`).

### 3.4 Inbox: item schema and producers

The inbox lives in **jarvis-notifications**. CC never stores inbox rows; it calls notifications over app-to-app HTTP (`_get_notifications_url` → discovery or `JARVIS_NOTIFICATIONS_URL`, default `http://localhost:7712`, `inbox_notification_service.py:169-183`).

**Inbox item (notifications `app/models.py:56-69`, `app/api/inbox.py:19-50`):**

```
id: uuid str           user_id: int|null (null = whole household)   household_id: str
title: str(500)        summary: text        body: text (markdown by default)
category: str(50)      source_service: str  metadata: json|null      is_read: bool    created_at: iso
```

- **Create:** `POST /api/v0/inbox` (app auth) `{household_id,title,summary,body,category,source_service,user_id?,metadata?}`.
- **Update in place:** `PATCH /api/v0/inbox/{id}` with `household_id` required and the other fields optional (errand plan refresh, `update_inbox_item_sync` `:267-304`).
- **Reads:** mobile reads straight from notifications. It lists household items whose `user_id` is either the caller or null (`inbox_service.py:97-101`).
- `is_read` is per **row**, so a household-wide item read by one member is read for all.
- There is no inbox retention.
- Mobile's type has a `content_format` field (`inboxApi.ts:14`) that notifications never returns, so it is always treated as markdown.

**Push (`POST /api/v0/notify`, notifications `api/notify.py:14-66`):** `{target_type: "user"|"household", target_id, title, body, data, priority: "default"|"high", category}`. CC always sends `priority:"high"`.

**Deep-link contract (`data`), as read by mobile `App.tsx:49-90`:**

| `data.type` | Other keys | Mobile action |
|---|---|---|
| `open_url` | `url`, `household_id`, `inbox_item_id` | `Linking.openURL(url)` |
| `interactive_list` | `inbox_item_id` | opens InteractiveList screen |
| `adapter_proposal` / `adapter_deployed` | `inbox_item_id` | adapter screens (**cut with LoRA**) |
| `bluetooth_scan` | `node_id` | NodeDetail hardware tab (producer: doc 07) |
| anything else with `inbox_item_id` | — | generic InboxDetail (**the default for every new category**) |

**CC helper functions:**

- **`push_confirmation_to_inbox`** (async, `:46-126`):
  - Inbox `category` is hard-coded to `"confirmation"`.
  - `metadata = {command_name, node_id, actions, draft}`.
  - On success it pushes with `data:{type:"confirmation", inbox_item_id}` and `category:"confirmation"`.
  - Returns the id, or None.
  - Callers: the voice `_maybe_push_actions_to_inbox` (`main.py:2021-2064`: `context.actions` on a voice tool result becomes a card; title is `inbox_title` or `"Confirm: {command_name}"`), and `/node/push-notification`.
- **`post_inbox_item_sync`** (`:192-264`): the generic sync helper.
  - Push `data:{type: push_type or category, inbox_item_id}` and `category: push_category or category`.
  - Push is skipped when `push=False`.
  - Returns the id, or None (failure is non-fatal).
- **`send_link_push_sync`** (`:307-386`):
  - Inbox `{category:"link", summary: body, body: url, metadata:{url, type:"open_url"}}`, then a **user-targeted** push `data:{type:"open_url", url, household_id, inbox_item_id}`.
  - True if the inbox write succeeded.
- **Adapter helpers** (`:394-553`): **CUT**.
- **`_resolve_push_target`** (`:25-43`): `"user"` without a user_id falls back to household with a warning.

**Categories seen in the wild:**

- `confirmation`, `link`, `general`, `callback_result`, `interactive_list`, `reminder`, `alert`;
- errand / schedule / proposal / signal-automation / phone / attention categories (docs 08-11);
- `adapter_*` (cut).

**Well-known metadata keys read by mobile:**

- `actions`, `node_id`, `command_name`, `draft`, `interactive_elements`, `url`, `sources`, `expires_at`, `audio`, `editable_fields` / `editor_schema` / `editable_text`, `revision`, `household_id`;
- the `InteractiveList` v1 payload (SDK `interactive.py`; caps of 6 sections, 100 rows, 6 actions, 2 row actions, labels of 120 chars).

### 3.5 Node notification routes (public plugin API)

All three resolve `household_id` from the authenticated node, never from the body. With `attention.enabled` (default False, `settings_definitions.py:699`) on for the household, they first call the attention broker (`_attention_gate`, `node_commands.py:245-286`; doc 10). The broker returns deliver/withhold and a rung (`push` or `inbox`). Broker errors **fail open** to legacy delivery. The outcome is stamped back via `mark_outcome`.

**`POST /node/inbox-item`** (`:593-702`). Request: `{title, summary="", body="", category="general", metadata?, user_id?, create_push_notification=false, target_type="household", dedupe_key?, force=false}`.

1. An empty title gives `{sent:false}` with HTTP 200. A node without a household gives `{sent:false}`.
2. `metadata.node_id` is **set by the server via setdefault** (`:670`). That way the item's interactive elements route back to the posting node.
3. Gate with rung `push` if `create_push_notification`, else `inbox`. If the broker decides, `push = (rung == "push")`.
4. `post_inbox_item_sync(...)`.
5. Response `{id, sent, withheld_by}`.

The SDK backend maps this to tags (`ok` / `no_cc_url` / `http_error` / …).

**`POST /node/push-notification`** (`:301-446`). Request: `{title, body, priority="default", category="alert", user_id?, target_type="household", dedupe_key?, force=false}`.

- **Withheld:** `{sent:false, withheld_by}`.
- **Demoted to inbox:** `post_inbox_item_sync` with `category=request.category`, `metadata={node_id}` and no push.
- **Legacy or approved push:** `push_confirmation_to_inbox(command_name="reminder", actions=[])`. This creates an inbox item with **category `confirmation`** (not the request's category) and pushes with `priority:"high"`. **`request.priority` is ignored.**
- Response `{sent, inbox_item_id, withheld_by}`.

**`POST /node/send-link`** (`:485-582`). Request: `{user_id: int (required), url, title?, body?}`.

- A non-http(s) URL gives `{sent:false}`.
- Defaults: title "Link from Jarvis", body "Tap to open".
- Gate with `dedupe_key=url`.
- Demoted: an inbox item with `category "link"`, body `"{body}\n\n[{title}]({url})"` and **no `metadata.url`**.
- Otherwise `send_link_push_sync`.
- Response `{sent, withheld_by}`.

`user_id` is trusted as the speaker id that CC handed the node (docstring `:516-519`).

### 3.6 Interactive callbacks — the round trip

The element shape is defined by the SDK (`jarvis_command_sdk/inbox.py:36-50`) and mobile (`commandCenterApi.ts:70-81`): `{id, label, sublabel?, kind?, command, callback, data, navigation_type?: "stack"|"new_notification"|"popover", target?: "node"|"server"}`. It lives in `metadata.interactive_elements`.

**Node plane** (element `target` absent or `"node"`; mobile sends `target_node_id = metadata.node_id`):

```
mobile tap ─POST /callbacks {command_name, callback_name, data, target_node_id, navigation_type}──▶ CC
  CC: node exists (404) → node has household (400) → user is member (403)
      INSERT callback_jobs(status=pending, expires_at=now+5m)            callbacks.py:144-169,189-229
      MQTT [{"command":"callback","details":{"request_id": job.id}}]     (opaque id only)
  ◀── 201 {id, status:"pending", navigation_type, created_at}
node ─GET /callbacks/{id} (X-API-Key)──▶ CC: owner check (404 if not owner), 410 if expired/unknown status;
                                           marks expired-on-read; returns {job_id, command_name, callback_name,
                                           data, user_id, voice_command:"cb:<callback>", conversation_id:"callback:<id>"}
node: cmd.get_callbacks()[callback](data, RequestInformation)            mqtt_tts_listener.py:301-421
node ─POST /callbacks/{id}/result {success, error?, context_data?}──▶ CC: owner check; _record_callback_result
mobile (stack/popover) ─GET /callbacks/{id}/status (poll)──▶ {id,status,navigation_type,completed_at,error_message,context_data}
```

**Server plane** (`target:"server"`; mobile sends `household_id` and omits `target_node_id`), `callbacks.py:232-355`:

1. The handler must be registered, or the tap gets a **400 at tap time**.
2. `household_id` is required (400).
3. Membership check, then a row with `node_id = NULL`.
4. FastAPI `BackgroundTasks` runs `_run_server_callback_job` after the 201. It loads the row, calls `handler(ServerCallbackContext{job_id, household_id, user_id, data, navigation_type})` (sync or async), and requires a `ServerCallbackResult{success, error, context_data}`.
5. Any exception, a missing handler, or a wrong return type records `failed`, so the job **never stays pending** (the "anti-vanishing rule", `:289-293`).
6. Nodes can never read server-plane rows, because `NULL != node_id` (`:374`).

**`_record_callback_result`** (`:442-515`), shared by both planes:

- `status = completed|failed`, `error_message`, `completed_at`.
- `context_data` is JSON-encoded, or dropped if it is not serialisable.
- When `navigation_type == "new_notification"`, success is true, and `context_data.inbox.title` is a non-empty string, it **fans out a new inbox item** via `post_inbox_item_sync`:
  - `category = inbox.category or "callback_result"`;
  - `metadata = inbox.metadata + node_id (setdefault)`;
  - `push = inbox.create_push_notification` (default false);
  - `target_type = inbox.target_type` if valid, else `"user"` (because the tapping user is known).
- For `stack` / `popover` there is no fan-out; mobile renders `context_data.inbox` inline (`commandCenterApi.ts:104-116`).

**Internal reuse.** `proposable_action_service._execute_target_callback_on_node` (`:246-306`) creates a node-plane job itself, with `navigation_type="stack"` (so no fan-out) and an `idempotency_key`, publishes it, and **polls the DB row** until it reaches a terminal state. `_already_completed` (`:217-241`) dedups on `(household_id, idempotency_key, completed)`. Doc 10 owns that flow.

### 3.7 Household settings

`HOUSEHOLD_CONTROLLABLE_SETTINGS` (`mobile_household_settings.py:37-62`) is **the security boundary**. Only these keys are readable or writable here:

| Key | Type | Default (`settings_definitions.py`) | Mobile UI? |
|---|---|---|---|
| `web_search.enabled` | bool | False (`:328`) | yes (`HouseholdEditScreen.tsx:495`) |
| `web_scraping.allow_external` | bool | False (`:382`) | no |
| `proposals.enabled` | bool | False (`:340`) | no |
| `phone_calls.enabled` | bool | False (`:778`) | no |
| `phone_calls.plan_ttl_minutes` | int | 20 (`:800`) | no |
| `phone_calls.audio_retention_days` | int | 30 (`:807`) | no |
| `phone_calls.max_call_seconds` | int | 600 (`:814`) | no |
| `phone_calls.calls_per_day` | int | 10 (`:828`) | no |
| `phone_calls.monthly_minutes_cap` | int | 60 (`:835`) | no |
| `phone_calls.max_concurrent_calls` | int | 1 (`:855`) | no |
| `household.location` | string | "" (`:842`) | yes (`:513`) |
| `persona.household_prompt` | string | `DEFAULT_PERSONA` (`:223`) | yes (`:532`) |

**GET** (`:99-124`):

- Any member may read.
- Each key goes through `settings.get(key, household_id)`. That uses the settings-client cascade: household row, then system row, then env fallback, then definition default, with a 60 s per-process cache (`jarvis-settings-client/.../service.py:8-10,127-155`; framework in doc 00).
- Values are coerced; an uncoercible value becomes `null` rather than failing the whole screen.
- Response `{household_id, settings: {key: value}}`. **All 12 keys are returned**, although mobile's TS type declares only 3.

**PUT** (`:127-176`):

- A key not on the allowlist gives **404**. That check happens **before** the role check, so a non-admin probing gets 404, not 403.
- Then `admin` role (403).
- `_coerce`:
  - bool: string `true` / `1` / `yes`, case-insensitive; anything else is false, and it never fails;
  - int: rejects bools and non-integral floats, and parses strings; failure gives 400.
- A persona longer than `PERSONA_MAX_CHARS = 2000` gives 400. The empty string is allowed and clears the persona layer.
- `settings.set(key, coerced, household_id)`; False gives 500.
- Response `{success:true, key, value}`.

**Persona presets** (`:179-197`, `services/persona_presets.py`): static `{presets:[{id,label,text}] (warm_folksy, terse, dry_witty, classic_jarvis, …), default_preset_id:"warm_folksy", default_text: DEFAULT_PERSONA, max_chars: 2000}`. The persona is rendered into a `<personality>` block under `PERSONA_FRAME` by `core_rules.build_personality_block` (doc 03). **These texts are prompt bytes: copy them verbatim.**

---

## 4. Data

| Store | What | Lifecycle |
|---|---|---|
| `callback_jobs` (`models.py:747-803`) | `id` uuid PK = MQTT request_id; `node_id` FK→nodes ON DELETE CASCADE, **nullable** (server plane, migration `b2c3cbsrv001`); `household_id` (idx); `user_id`; `command_name`, `callback_name` (≤128); `data_json`; `idempotency_key` (idx, migration `pa01`); `navigation_type` (default `new_notification`); `status` pending/completed/failed/expired; `error_message`; `result_context_data_json`; `created_at`, `expires_at` (+5 m), `completed_at` | `expired` is set **only lazily on read** (`callbacks.py:381-385,541-543`). There is no deletion, so rows accumulate forever. |
| `NodeCommandService._pending_commands` | in-memory `{request_id: {node_id, command, created_at, expires_at(+5m)}}` | Pruned on each publish. Every `tool_call`, `action` and `callback` adds an entry even though `trusted` / callback flows never consume it. |
| `<tmp>/jarvis-device-control/{rid}.json` | the node's result for `tool_call` / `action` | Written by the unauthenticated `/device-control-results`, deleted on read or timeout. It exists only because uvicorn may run several worker processes. |
| `conversation_cache` (in-memory) | messages and tools per `conversation_id` | 10-minute TTL (doc 02). Chat warmness is defined by `get_tools()`. |
| `request_traces` | latency trace per chat turn (`source="mobile"`) | `latency_logger.end_request` (docs 00/05) |
| `transcripts` | written on `complete` only | doc 04 |
| `settings` (household rows) | household-controllable values | doc 00 |
| notifications `inbox_items` | everything in §3.4 | no TTL; user deletes |

---

## 5. Settings

| Key | Read by | Effect |
|---|---|---|
| `attention.enabled` (bool, False) | `_attention_gate` via `attention_enabled()` (`attention_broker.py:66-68`) | routes the three `/node/*` notification routes through the broker |
| the 12 household-controllable keys (§3.7) | this router (read/write); consumers in docs 03, 04, 10, 11 | — |
| `persona.household_prompt` | the prompt builder (doc 03) | voice layer of the system prompt; changes the cached prefix |

Env: `JARVIS_NOTIFICATIONS_URL` (fallback only). The timezone default `"America/New_York"` is hard-coded in the request models (`mobile_chat.py:41,52`). Web sends the browser timezone; mobile sends its own.

---

## 6. Dependencies

- **CC subsystems:**
  - voice pipeline / model_service (`warmup_conversation_with_tools`, `process_voice_command_with_tools`, `continue_conversation_with_tool_results`; docs 01/02);
  - acknowledgment service (doc 01);
  - node registry, MQTT and `report_tools` (docs 05/12);
  - smart-home `Device` / `Room` tables for chat context (doc 07);
  - transcripts (doc 04);
  - attention broker (doc 10);
  - every server-callback owner: phone (11), errands (09), schedules (08), proposals and signal automations (10).
- **Other services:**
  - jarvis-auth: `verify_household_role`, and `resolve_speaker_name` (`/auth/me`-style user lookup);
  - jarvis-notifications: `POST/PATCH /api/v0/inbox`, `POST /api/v0/notify` (app-to-app);
  - llm-proxy, indirectly through model_service, using the **live** slot.
- **LLM calls originated here:** none directly. The chat turn uses the conversation's live-model prompt (doc 03). Acknowledgments are not LLM-generated.
- **Node:**
  - `mqtt_tts_listener.handle_tool_call`, `handle_action`, `handle_callback`, `handle_report_tools`;
  - `services/inbox_backend.py` (SDK `InboxBackend`).

---

## 7. Invariants and non-obvious behaviour

1. **The SSE schema in §3.1 is frozen.** Every stream ends with exactly one `done` or `error`. `acknowledgment` is always emitted before the LLM call, even on warm turns (`mobile_chat.py:309-310`). The docstring's list of four types is stale.
2. **`done.full_text` overwrites the bubble.** Never put text only in deltas that `full_text` would lose, unless that loss is intended (§10 Q5).
3. **Resent `conversation_id` with an expired cache** warms up under the *same* id (`:294-305`). Clients rely on that id stability.
4. **`done.stop_reason` is `"complete"` for both `complete` and `server_tool_complete`** (`:357`).
5. **Tool calls run sequentially**, each with its own 10 s timeout. The **last** tool output carrying `actions` wins (`:432`). A tool timeout is not an error: it becomes a `{success:false, timeout:true}` tool result that the LLM narrates.
6. **The `voice_command` passed to node tools is the typed message.** In the callback payload it is `"cb:<callback_name>"` with `conversation_id "callback:<job_id>"` (`callbacks.py:399-400`). Commands may branch on these strings.
7. **The node trusts `trusted: True`** and skips verify for `tool_call` and `action`. The callback plane instead uses the authenticated GET as its verification step (MQTT carries only the opaque id).
8. **`/node/inbox-item` sets `metadata.node_id` with setdefault.** A `node_id` the caller supplies is *kept*, so a command can point taps at a different node. `/callbacks` only checks that the tapping user belongs to the *target* node's household, so the target must be in a household the user belongs to. Port this as-is: it is a setdefault, not an overwrite.
9. **Server-plane handler lookup happens at tap time** and gives a 400. A handler that disappears later records `failed`.
10. **Callback ownership errors are 404 (not 403)**, to avoid leaking job existence (`:369-375`). Expired or unknown status gives 410.
11. **The callback inbox fan-out defaults to a user-scoped push**, while the `/node/*` routes default to `household` (back-compat comments at `inbox_notification_service.py:16-21`).
12. **Household PUT checks the allowlist before the role** (a 404 leaks nothing). Bool coercion never fails. Int coercion rejects booleans.
13. **Inbox/push failures never fail the caller.** Every helper returns None/False and logs. `send_link` reports `sent=true` once the inbox write succeeds, even if the push failed.
14. **Broker errors fail open** to legacy delivery (`node_commands.py:240-243`). `force=true` bypasses every gate.

---

## 8. Oddities

- **SSE chunk-split drop** in both clients (§3.1). This is a latent client bug. The server can only reduce exposure.
- **Fake streaming.** Words are split after the full answer exists, with a 20 ms sleep per word. A 200-word answer adds about 4 s of artificial latency (`:345-351`).
- **Transcripts are logged only on `complete`**, so validation turns and errors are missing from memory extraction.
- **`/node/push-notification` legacy path:**
  - stores inbox `category:"confirmation"` and `metadata.command_name:"reminder"` for *every* caller;
  - push `data.type:"confirmation"`;
  - **ignores `request.priority`** and sends `high`;
  - the demoted path uses `request.category`.
  So the same request lands as two different categories depending on the broker (`node_commands.py:363-417`).
- **The demoted `send-link`** omits `metadata.url`, so InboxDetail's link button only works if the body starts with `http` (`InboxDetailScreen.tsx:323-327`). Here it doesn't; only the markdown link renders.
- **`callback_jobs` is never cleaned up.** `expired` is only set when someone reads the row. A node-plane job whose MQTT publish silently failed (`client is None`, `node_command_service.py:70-73`) stays `pending` until read. With `new_notification`, mobile never learns.
- **`_pending_commands` grows** with entries for flows that never verify (`trusted` / callback). It is harmless but wasted.
- **`/device-control-results/{id}` is unauthenticated.** Anyone who learns a request_id can forge a tool result for the chat LLM (doc 05).
- **`/nodes/{id}/actions` blocks a threadpool thread** for up to 10 s with `time.sleep`. Its response model drops the node's `input_required`.
- **Mobile's `HouseholdSettings` TS type has 3 keys; the server allowlists 12.** Nine keys (phone_calls.\*, proposals, web_scraping) have no UI, so they are only settable by raw API.
- **`GET /mobile/traces/{conversation_id}`** has no household check, which is an IDOR. It is unused, so cut it.
- **The module and model docstrings say server-plane callbacks serve "deep-research follow-ups"** (`callbacks.py:14`, `server_callback_registry.py:6`, `models.py:764`). No deep-research handler is registered.
- **`settings_definitions.py:17` defaults `llm.interface` to `"Qwen25MediumUntrained"`**, a provider being **dropped** (Appendix B). The Go default must be a kept provider (doc 03). The 13 `adapter.*` definitions go with the LoRA cut.
- **Mobile's `content_format`** is never produced by notifications.
- **`popover` is accepted server-side** but mobile says "not implemented yet" (`commandCenterApi.ts:67-68`).
- **Brief vs code:** jarvis-web chats through **`/mobile/chat`** (via `/api/cc`), not `/voice/command/stream`.

---

## 9. Tests

| File | Covers |
|---|---|
| `tests/test_callbacks.py` (560) | node plane: create/membership, payload ownership (404/410), result, status poll, expired-on-read, `new_notification` fan-out vs stack |
| `tests/test_server_callbacks.py` (402) | server plane: 400 unregistered, household required, background execution, handler exceptions → failed, async handlers |
| `tests/test_node_inbox_item.py` (224) | `/node/inbox-item`: node_id injection, push flag, empty title, no household |
| `tests/test_mobile_household_settings.py` (255) | allowlist 404, role gating, coercion, persona cap |
| `tests/test_persona.py` (252) | presets and personality block |
| `tests/test_node_command_dispatch.py` (54), `tests/test_mobile_chat_voice_command_plumbing.py` (68) | `dispatch_node_command` publish/timeout/failure; voice_command plumbing |

**Gaps:**

- There is **no test of the SSE stream itself**: event order, `done` shape, tool loop, action harvesting.
- None for `/node/push-notification`, `/node/send-link` or `/nodes/{id}/actions`.

**Golden / contract candidates for Go:**

1. **SSE transcripts.** A fake model_service plus a fake node gives the exact byte stream for each case: cold, warm, tool_calls with actions, validation_required, error, max iterations, and exception.
2. **Exact outbound notifications payloads** for each helper and each `/node/*` route, in each broker state. Capture them against a recording stub of `/api/v0/inbox` and `/notify`. In Go these become in-process calls, but the resulting inbox rows and push `data` must match.
3. **The callback lifecycle state machine** (pending→completed/failed/expired) with the status codes listed in §3.6.
4. **Household settings GET**, full key set and defaults, as a JSON golden.
5. **The node contract tests** (MQTT `tool_call` / `action` / `callback` detail shapes) shared with doc 05.

---

## 10. Questions for the user

1. **[behaviour] Real token streaming for mobile chat?**
   - *Why it matters:* today's `delta` events replay the finished answer word by word with 20 ms sleeps (`mobile_chat.py:345-351`). That costs seconds of fake latency, and `stream_final_response` was abandoned because of prefix-cache eviction. In Go, `continue_conversation` itself can stream from llama-server on the *same* conversation, so the cache problem goes away.
   - *Options:*
     - (a) keep fake word-streaming, byte-compatible;
     - (b) stream real tokens as `delta`, same schema;
     - (c) (b), plus send `delta` for the first LLM call's text too.
   - **My recommendation: (b).** The schema is unchanged and clients can't tell, except that it gets faster.

2. **[scope] Collapse inbox + push into CC's own module now that notifications is in the same binary?**
   - *Why it matters:* every CC producer does two HTTP calls with app-to-app auth, and failures are swallowed. In-process, an inbox write can share a SQLite transaction with the producer's own row (callback result, errand plan, attention outcome).
   - *Options:*
     - (a) keep an HTTP-shaped client interface;
     - (b) a Go `notify.Inbox` service called directly, with inbox insert and job/outcome update in one transaction, and push enqueued on the durable queue (retries for free; PLAN §2 notes notifications' retry worker never starts).
   - **My recommendation: (b).** Keep notifications' HTTP routes for mobile and other services unchanged.

3. **[behaviour] `/node/push-notification` category and priority.**
   - *Why it matters:* the legacy path stores every node push as category `confirmation` / `command_name:"reminder"` and forces `priority:high`, while the broker-demoted path uses the caller's category. That is two categories for the same request. Community packages (sports, news) send their own `category`.
   - *Options:*
     - (a) preserve byte-for-byte;
     - (b) honour `request.category` and `request.priority` everywhere (the inbox list colours change for reminders; mobile already falls back generically).
   - **My recommendation: (b)**, with `category` defaulting to `alert` as the model already says. Confirm that nothing on mobile filters on `confirmation` for these.

4. **[behaviour] Callback job retention and expiry.**
   - *Why it matters:* `callback_jobs` rows are never deleted, and `expired` is only set lazily. A failed MQTT publish leaves a job pending forever.
   - *Options:*
     - (a) port as-is;
     - (b) a sweeper on the job queue that marks expired at `expires_at` and deletes after N days;
     - (c) (b), plus fail the job immediately when the node is offline or the publish fails.
   - **My recommendation: (c)**, with 7-day retention. Do you want an inbox "couldn't reach <node>" card on failure for `new_notification` taps, or silence?

5. **[behaviour] Should the intermediate message ("Let me check the weather…") survive in the final bubble?**
   - *Why it matters:* it is streamed as a `delta`, then wiped by `done.full_text` (`useChat.ts:381`). Users see it flash and vanish.
   - *Options:*
     - (a) keep as-is (a transient "working" message);
     - (b) prepend it to `full_text`.
   - **My recommendation: (a).** It is presumably intentional, but confirm.

6. **[scope] The nine allowlisted household keys with no mobile UI** (`phone_calls.*`, `proposals.enabled`, `web_scraping.allow_external`).
   - *Why it matters:* they are writable by any household admin via raw API, but no screen exposes them. Are they deliberately API-only, planned UI, or meant to come off the allowlist? For example, should `phone_calls.max_concurrent_calls` really be household-tunable?
   - *Options:*
     - (a) keep all 12;
     - (b) shrink the allowlist to what the UI writes plus `proposals.enabled` / `phone_calls.enabled`;
     - (c) keep them all and add UI later.
   - **My recommendation: (a)** for the port, which keeps the contract. Flag `phone_calls.*` caps for a later review.

7. **[behaviour] The warmup MQTT tool fetch blocks for 10 s on an offline node.**
   - *Why it matters:* the mobile chat to an offline node hangs before falling back to the client-sent tools (`mobile_chat.py:121-140`).
   - *Options:*
     - (a) keep;
     - (b) check node liveness (doc 05) first and skip MQTT when offline;
     - (c) a shorter timeout (2-3 s).
   - **My recommendation: (b).** Liveness is in-process in Go.

8. **[behaviour] What should mobile chat do when the selected node is offline mid-conversation?**
   - *Why it matters:* each node tool call silently waits 10 s, then the LLM narrates "the node didn't respond in time".
   - *Options:*
     - (a) as today;
     - (b) emit a `status` "Node is offline" and fail fast;
     - (c) run node-less: drop client tools and keep server tools only.
   - **My recommendation: (b).** It uses the same events and needs no new client code.

9. **[scope] Server-plane callbacks: should community packages be able to register server-side handlers?**
   - *Why it matters:* the registry is CC-internal today, keyed by `command_name` strings such as `jarvis.proposable_action`. In Go it becomes a compile-time map. If plugins later run server-side, this is the extension point.
   - *Options:*
     - (a) internal only, static map;
     - (b) design for plugin registration.
   - **My recommendation: (a)** for the port.

10. **[minor] `/nodes/{id}/actions` drops `input_required`** from the node's result.
    - *Why it matters:* the node sends it (`mqtt_tts_listener.py:268-272`), but `ActionResponse` strips it. Was there a planned "needs more input" flow on mobile?
    - **My recommendation:** pass it through as an additive optional field. It is harmless to old clients.

11. **[minor] Demoted `send-link` loses the tap-to-open URL.**
    - **My recommendation:** add `metadata:{url, type:"open_url"}` on the demoted path, the same as the push path.

12. **[minor] SSE robustness.**
    - *Why it matters:* clients drop events split across chunks.
    - **My recommendation:** keep the server writing one-event-per-flush in Go. Separately, fix the line-buffering bug in mobile `parseSSEChunk` and web `lib/api.ts` when the clients are next touched. Which client release do you expect to coincide with the cutover?

---

## 11. Go port notes

**Shape.**

- `cc/chat`:
  - `Handler` writes SSE through a small `sseWriter{w http.ResponseWriter; f http.Flusher}` whose `Send(v)` does `json.Marshal` then **a single `Write("data: "+json+"\n\n")` followed by `Flush()`**;
  - it sets `Content-Type: text/event-stream`, `Cache-Control: no-cache`, `Connection: keep-alive` and `X-Accel-Buffering: no`;
  - it writes the 200 header before the first event;
  - pre-stream auth and 404 errors use the normal FastAPI-shaped JSON error.
- Encode the event structs with `omitempty` **only** where Python omits keys. Python always emits `conversation_id`, `full_text`, `stop_reason` and `trace_summary` on `done`, while `actions` / `action_context` / `action_preview` / `reasoning` / `validation` appear only conditionally.
- Use `context.Context` from the request for client-abort cancellation. Python keeps running the LLM after the client disconnects; Go should cancel the node waits but **let the conversation history commit**.

**Node dispatch.** Replace the tmp-file polling with an in-process `pending map[requestID]chan Result` that is fed by the `/device-control-results` handler. That handler stays as an HTTP route because nodes POST to it. With one process, the multi-worker reason for files disappears. The same applies to `/nodes/{id}/actions`: a select on the channel with a 10 s timeout, not a sleeping thread.

**Callbacks.**

- `callback_jobs` becomes `cc_callback_jobs` (TEXT JSON columns kept).
- Server-plane execution becomes a job on the embedded queue (type `cc.callback.server`, concurrency-capped), not a goroutine, so it survives restarts. That gives the anti-vanishing rule for free via the queue's terminal-failure hook.
- `proposable_action`'s DB polling becomes waiting on a per-job notification channel that `recordCallbackResult` signals.
- Add the sweeper (Q4).
- Keep 404-vs-410 semantics and `voice_command "cb:<name>"` / `conversation_id "callback:<id>"`.

**Inbox in the same binary.** This is the main simplification.

- `notify.CreateInboxItem(ctx, tx, item)` and `notify.Push(ctx, target, msg)` are called directly. This removes app-to-app auth, discovery and `JARVIS_NOTIFICATIONS_URL`.
- Producers can write their own row and the inbox row in one transaction (callback result + fan-out; errand plan + card; attention `mark_outcome` + inbox id).
- Push goes on the durable queue with retries, and `/node/*` responses still report `sent` from the inbox write.
- `source_service` stays `"jarvis-command-center"` for every CC-origin row, because mobile may display it.
- Mobile keeps reading `/api/v0/inbox*` on port 7712 unchanged.

**Household settings.** A `map[string]settingType` allowlist in Go plus the shared settings module (doc 00). In-process `verify_household_role` is a direct call into the auth module. Keep the order: allowlist (404), then role (403), then coerce (400), then persona length (400). Port `PERSONA_PRESETS` and `DEFAULT_PERSONA` texts **byte-exact** (they are prompt bytes and also returned to the UI).

**Cuts here:**

- `GET /mobile/traces/{conversation_id}`;
- the adapter inbox helpers and categories, plus mobile's `adapter_*` deep links (client change);
- `adapter.*` settings definitions.

**Risks:**

1. SSE flush/proxy behaviour behind Next's rewrite (jarvis-web). Verify that Go `Flush` reaches the browser through `next start`.
2. Pydantic 422 shapes for `MobileChatRequest` (`min_length`/`max_length` on `message`) and `CallbackCreateBody` (`min_length=1` on `target_node_id`/`household_id`, so an empty string is a 422, not the server plane).
3. The `/node/*` routes are a public plugin API. Lock their request/response JSON with contract tests before touching the Q3/Q11 behaviour changes.
