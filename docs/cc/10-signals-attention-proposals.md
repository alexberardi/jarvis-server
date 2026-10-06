# 10 — Signals, attention, proposals

Source: `/home/alex/jarvis/jarvis-command-center/app` (paths below are relative to it unless prefixed). About 3.9k LOC across 20 files, plus about 3.2k LOC of tests. This is an **R&D area that is still changing**. It has three PRDs: `prds/signal-bus-situation-matcher.md` ("Draft for build", 2026-08-08), `prds/attention-broker.md` ("Scoped, not yet built", which is stale because phase 1 *is* built), and the precision harness in `evals/signal_precision/`. Several of its planes are off by default, and one module (`autorun_gate.py`) has no caller at all.

## 0. The concepts in plain language, and how they connect

| Term | Meaning | Where |
|---|---|---|
| **Signal** | A short-lived fact about the household, such as "Alex is home", "dentist at 3pm, 40 min away" or "Jets won 24–17". It has a `kind` label, a stable `source_key` (re-observing the same fact updates the same row), a one-line `summary`, `facts` JSON and an optional TTL. It is never control flow. | `signals` table, `services/signal_service.py` |
| **Signal Bus** | The ingest (`POST /signals`, plus mobile presence and voice presence) and the fan-out that follows ingest. | `api/signals.py`, `services/signal_dispatch.py` |
| **Reactive render** | Every live Signal's `summary` is appended to the ambient context block of the voice prompt, so the LLM "knows" who's home and similar facts. | `core/conversation_handler.py:3251-3260` |
| **Reaction** | Deterministic server code registered for one `kind` that runs on ingest. Today there are two: **leave-by** (`appt.upcoming` → drive time → reminder card) and **automation**. | `signal_reaction_registry.py`, `signal_reaction_bridge.py` |
| **Signal automation** | A free-text rule a household admin writes per catalog kind, such as "When I leave home: lock the front door". When the signal fires, **one background-LLM tool call** picks a node tool. It then either runs the tool immediately (`automatic`) or posts a confirm card (`notification`). | `signal_automation_executor.py`, `signal_automation_store.py`, `api/mobile_signal_automations.py` |
| **Situation (matcher)** | The *proactive* plane. After a signal edge (debounced 5 s), it bundles **all** live household Signals together with a node's *proposable actions*, and asks the background LLM "is there one action worth proposing?". **Off by default** and gated by a precision harness that does not yet pass. | `situation_matcher_service.py`, `proposal_matcher.py` |
| **Proposal / proposable action** | A tap-to-confirm inbox card offering to run a node command's `@callback`. Commands **opt in** by declaring `proposable_actions` (SDK). The card's Confirm goes to one generic server dispatcher, `jarvis.proposable_action.execute`, which re-checks the gate, opt-in, params and idempotency, then runs the callback on the node. Proposals come from: directed `/signals` (`command` set), the leave-by reaction, the proactive matcher, and node agents via SDK `JarvisInbox.propose_action` (e.g. the email `appointment_scan` agent). | `proposal_card.py`, `proposable_action_service.py` |
| **Suppression** | "Never suggest this". A row keyed on (household, user, command, source_key/descriptor), written when a card's suppress button is tapped. It is read by the node detector agents (`GET /proposals/suppressions`) and by the leave-by reaction. **Not** read by the proactive matcher or the directed path (§8). | `proposal_suppressions.py`, `api/proposals.py` |
| **Autorun** | A designed-for-later allowlist (`get_drive_time`, `reminder`, ≤3 steps) under which a signal-triggered plan could run without a tap. **No caller anywhere** (only `tests/test_autorun_gate.py`). | `autorun_gate.py` |
| **Attention broker** | A separate, LLM-free governor for *notifications* rather than signals. When `attention.enabled` is set, it interposes `/node/push-notification`, `/node/inbox-item` and `/node/send-link`. It journals each event, dedups it, and walks the ladder journal→inbox→push through these gates: consent, tier, budget, quiet hours. A daily "journal" inbox card shows what was withheld. **Off by default.** | `attention_broker.py`, `attention_journal.py`, `api/attention.py`, `api/node_commands.py:236-700` |
| **Presence** | One upserting Signal per user (`source_key = presence:{user_id}`, kind `presence.seen`/`presence.left`). It is written by the phone (foreground geofence, `POST /mobile/presence`), by voice (a confident speaker ID), or by any SDK producer. | `signal_service.record_presence`, `api/mobile_presence.py` |

**The pipeline as one picture:**

```
producers                     store                     fan-out (dispatch_signal_edges)
──────────                    ─────                     ──────────────────────────────
node agents (SDK) ─┐                                   ┌─▶ reaction registry (by kind)
  POST /signals    ├─▶ SignalService.save_signal ──────┤     ├─ appt.upcoming → leave_by → reminder.set_at CARD
mobile /presence ──┤     (UPSERT on hh+source_key)     │     └─ catalog kinds → automation → LLM → run | CARD
                   │                                   └─▶ situation edge (debounce 5s)
voice speaker ID ──┘ (writes row only, NO fan-out)            └─ [proposals.enabled ∧ proactive_enabled]
                                                                 → bg LLM job → matches → anti-nag → CARD
       │
       └─▶ ambient render into voice prompt (if ambient_context.enabled)

directed /signals (command=X) ──▶ resolve node advertising X ──▶ CARD
CARD ──tap──▶ POST /callbacks (server plane) ──▶ jarvis.proposable_action.{execute|dismiss|suppress}
                                              └▶ jarvis.signal_automation.{execute|dismiss}

/node/push-notification | inbox-item | send-link ──[attention.enabled]──▶ broker gates ──▶ notifications svc
```

**Live vs dormant.** These are defaults. The prod settings values are unknown to me; see Q2.

| Plane | Gate (default) | Status |
|---|---|---|
| `/signals` ingest + store + TTL sweep | `signals.enabled` = **true**, fail-open | Live. Producers: calendar `appt.upcoming`, sports `game.final`, mobile presence. |
| Reactive render | `ambient_context.enabled` = false | Opt-in. |
| Voice presence write | `ambient_context.enabled` = false | Opt-in. |
| Leave-by reaction | `proposals.enabled` = false, fail-closed | Opt-in. |
| Signal automations | **No master gate.** Per-rule `enabled`. | Live once a user authors a rule in mobile. |
| Proposable-action dispatcher | `proposals.enabled` = false | Opt-in. Used by the email appointment agent. |
| Proactive situation matcher | `proposals.enabled` ∧ `proposals.proactive_enabled` (both false) | Dormant. The harness gate fails (PRD §15). |
| Live `match_situation` / `match_proposals` | — | **No prod caller** (only `evals/` and tests). |
| Attention broker | `attention.enabled` = false | Dormant. Its two direct routes are unused (Appendix A). |
| Autorun gate | — | **Dead code.** |

## 1. Purpose

These planes let Jarvis act on what it observes without being asked. The design rules are:

- Producers (node agents, phone, voice, third parties) post Signals.
- Jarvis renders those Signals into the voice context.
- Jarvis reacts deterministically (leave-by), interprets user-authored rules (automations), or eventually proposes things on its own (situation matcher).
- Anything that writes goes through a tap-to-confirm card, except `automatic` automations (see Q1).
- The attention broker is the matching brake on *notifications*.

Users: node agents and community packages (public plugin API), mobile (presence, automations UI, suppression management, card taps), voice (presence), and background loops.

## 2. Entry points

**Routes.** All are under `/api/v0`.

| Method + path | Auth | Live caller | File |
|---|---|---|---|
| `POST /signals` | Node `X-API-Key`, **or** app-to-app (round trip to `JARVIS_AUTH_URL/internal/app-ping`) | SDK `JarvisSignals` via node-setup `services/signals_backend.py:51`; calendar and sports packages. **Public plugin API.** | `api/signals.py:243` |
| `POST /mobile/presence` | User JWT + household `member` | node-mobile `presenceService.ts:556` | `api/mobile_presence.py:59` |
| `GET /mobile/household/{hh}/signal-automations` | JWT + `member` | node-mobile `signalAutomationsApi.ts` | `api/mobile_signal_automations.py:79` |
| `PUT /mobile/household/{hh}/signal-automations/{kind}` | JWT + `admin` | same | `:113` |
| `GET /proposals/suppressions?command&user_id` | Node `X-API-Key` | node-setup `services/proposal_client.py:30` (email `appointment_scan`) | `api/proposals.py:36` |
| `GET /mobile/proposal-suppressions?household_id&command?` | JWT + `member` | node-mobile `proposalSuppressionsApi.ts` | `api/proposals.py:57` |
| `DELETE /mobile/proposal-suppressions/{id}?household_id` | JWT + `member` | same | `api/proposals.py:71` |
| `POST /situation-matcher/callback` | Bearer `JARVIS_ADAPTER_CALLBACK_TOKEN` (`main.py:1867`) | llm-proxy queue worker. **Becomes in-process.** | `main.py:1977` |
| `POST /attention/events`, `GET /attention/journal` | Node key / admin token | **None. Cut** (Appendix A). | `api/attention.py:55,112` |

**Interposed routes**, documented in doc 05/13; the broker hook lives in this subsystem: `/node/push-notification` (`api/node_commands.py:358`), `/node/send-link` (`:537`) and `/node/inbox-item` (`:665`).

**Server callbacks** are registered at startup (`main.py:498-516`) and reached via `POST /callbacks` without `target_node_id` (doc 13):

- `jarvis.proposable_action.{execute,dismiss,suppress}` (`proposable_action_service.py:461-465`)
- `jarvis.signal_automation.{execute,dismiss}` (`signal_automation_executor.py:364-372`)

**Background loops** (`main.py`):

| Loop | Cadence | Lines |
|---|---|---|
| Signal TTL sweep | First run at 90 s + 1800 s, then every 30 min | `:378-395` |
| Attention journal card | Ticks every 60 s. For each household with broker events in 24 h, checks `attention.journal_card_cron` (default `0 21 * * *`, household tz) via `routine_scheduler.is_due`. `last_fired` is in memory. | `:579-611` |
| Attention TTL cleanup | Sleeps 86400 s **before** its first run | `:613-629` |
| Situation debounce | Not a loop: one asyncio task per household, 5 s | `situation_matcher_service.py:309-344` |

**Internal callers:**

- `conversation_handler` renders Signals (`:3251`) and writes voice presence (`:3535-3560`).
- `capability_registry.list_proposable_actions` / `resolve_proposable_action` are shared with doc 12. They do an MQTT `report_tools` round trip on every call, with no cache.

## 3. Behaviour

### 3.1 `POST /signals` (`api/signals.py`)

1. **Auth** (`:44-87`). A valid `X-API-Key` → `auth_type=node` with `household_id` and `node_id`. A bad node key **falls through** to app auth (`:63-64`). App auth reaches auth via a sync `httpx.get` with a 5 s timeout: unreachable → **502**, non-200 → **401**. Neither header → 401 `"Authentication required (X-Api-Key or X-Jarvis-App-Id/Key)"`. App auth carries no household, so the body must name one, and **any** valid app credential may write to **any** household.
2. **Household** = `body.household_id or auth.household_id`. A missing household → 400. With node auth, a body/node mismatch → 403 (`:251-262`).
3. **Kill switch**. If `signals.enabled` is explicitly `false`/`0` → **409**. Errors fail open (`:121-131`).
4. **Rate limit**. An in-memory token bucket of 60/min keyed on `(household, source_agent)` (`:117-147`). The key is client-chosen, and it resets on restart. Exceeding it → **429** with `Retry-After: 1`.
5. `node_id = scope.node_id or auth.node_id` (`:275`). Note that in the open path a client-supplied `scope.node_id` is stored unvalidated.
6. `SignalService.save_signal` (`signal_service.py:40-118`):
   - If `cacheable` is set, it regex-rejects floats, `H:MM:SS` times and relative phrases ("ago", "minutes", "in N min") anywhere in `summary` plus `json(facts)` → **422** (`:24-29,135-151`).
   - It then UPSERTs on `(household_id, source_key, is_active=True)`, overwriting **every** column including `user_id`, `node_id` and `expires_at`. `expires_at = now + ttl` or NULL (never expires).
7. **Directed** (`command` set) → `_emit_directed_proposal` (`:150-239`):
   - Splits `"cmd.callback"`.
   - Drops a `node_id` that is not in the household (`:184-185`).
   - Otherwise resolves a node that advertises the command. Probes run concurrently, 4 s per node with a 6 s overall cap (`proposable_action_service.py:117-183`).
   - All of this runs via `asyncio.run` on the sync threadpool worker.
   - Not advertised → `proposed=false`. The signal is still stored.
   - Otherwise it builds the card params from `data` filtered to the declared params, excluding `idempotency_param`. The idempotency key is `match:`+sha256(`{"d":{"items":[data]},"c","a"}`)[:16]. The card title is `card_title` or `"Run {command}?"`, and `source=source_key`, which adds the suppress button.
8. **Fan-out** (`signal_dispatch.py:19-54`). This runs for both open and directed signals. It calls `signal_situation_edge(hh, node_id)` and `schedule_signal_reactions(ctx)`. Both are fire-and-forget on the captured main loop (`call_soon_threadsafe`) and never raise.
9. Response 200: `{"signal_id": int, "mode": "open"|"directed", "proposed": bool}`.

### 3.2 Presence producers

- **Mobile** (`api/mobile_presence.py:59-109`):
  - `state ∈ {home, away}`, otherwise 422.
  - Requires the `member` role, then the same `signals.enabled` (409) and rate-limit gates (`source_agent="mobile"`, 429 **without** Retry-After).
  - Calls `record_presence(... ttl = body.ttl or 4h)`, then `dispatch_signal_edges(node_id=None, facts={user_id,state,room})`.
  - `user_id` always comes from the JWT.
  - Returns `{"ok":true,"signal_id","kind"}`.
- **`record_presence`** (`signal_service.py:154-199`): `home`→`presence.seen`, anything else→`presence.left`. `subject="user:{id}"`, `facts={user_id,state,room}`, `cacheable=False`, summary "X is home in the {room}" / "X is away".
- **Voice** (`conversation_handler.py:3535-3560`, `signal_service.py:202-228`):
  - Fires on a confident STT speaker ID only (not the sticky fallback).
  - Gated on `ambient_context.enabled`, **not** on `signals.enabled`.
  - Writes `presence.seen`, TTL 900 s, summary "X was recently heard at the {node} node".
  - **Does not call `dispatch_signal_edges`**, so it never triggers automations or the matcher.
- **SDK** `emit_presence` (`jarvis_command_sdk/signals.py:120-150`) writes `facts={"user": id, ...}`, not `user_id`. The catalog docstring acknowledges the drift (`signal_catalog.py:20-23`).

### 3.3 Reaction registry (`signal_reaction_registry.py`)

- A `dict[kind] → [(name, handler)]`. Registration is idempotent per `(kind, name)` (`:47-54`).
- `schedule_signal_reactions` creates one task per handler on the main loop (`:82-101`). Handlers return a status string and must never raise. `_run_reaction` logs it (`:74-79`).
- Registered at startup (`main.py:513-516`):
  - `appt.upcoming → leave_by`
  - `{presence.left, presence.seen, appt.upcoming} → automation`
- So `appt.upcoming` fires **both** reactions.

### 3.4 Leave-by reaction (`signal_reaction_bridge.py:243-351`)

The guards run in this order, and each one returns its status:

1. `proposals.enabled`, fail-closed → `disabled`
2. `node_id` → `no_node`
3. `facts.location` → `no_location`
4. `facts.start_iso|start` → `no_start`
5. `user_id` → `no_user`
6. Suppression lookup: `"leaveby"` in source_keys for command `reminder` → `suppressed`
7. **In-memory claim** on `hh:event_id` → `duplicate` (`:77-83`). The claim happens *before* the work, so a later failure leaves it claimed.

Then:

- Probes the node for `get_drive_time`, 4 s (`:99-110`). Missing → `no_drive_time`.
- Dispatches `get_drive_time {destination, resolution:"strict"}` over MQTT `tool_call`, 20 s (`:113-125`). Failure or a non-int `duration_minutes` → `no_route`.
- `due_at = start − (drive + 5 min)` (`:315`). Already past → `departure_passed`. A bad ISO → `bad_start`.
- Saves the observability signal `leave_by.suggested`: `source_key=leaveby:{event}`, TTL until start + 5 min, **no fan-out** (`:128-174`).
- Emits the card (`:177-239`):
  - Resolves the node advertising `reminder.set_at`, which may differ from the drive-time node.
  - `params={text:"Leave for {title}", due_at_iso, idempotency_key:"leaveby:{event}"}`.
  - Card summary "{title} at {time} — {N min} drive from home".
  - `source="leaveby"` plus the descriptor "leave-by reminders for calendar events".
  - Status `proposed` or `not_advertised`.

### 3.5 Signal automations (`signal_automation_executor.py`, `signal_automation_store.py`)

**Storage** is one household setting, `signals.automations`: a JSON string `{kind: {instruction, enabled, delivery}}` (`store:24-62`). There is no table.

- `PUT` with a blank instruction deletes the rule.
- A kind outside the catalog → 404 (`api/mobile_signal_automations.py:125-128`).
- The instruction is limited to 500 chars.
- `delivery ∈ {automatic, notification}`, default `notification`.
- The `GET` response adds `observed` (a DISTINCT on `kind` over live signals).

**Execution** (`react_to_signal_automation`, `:253-322`):

1. No enabled rule → `no_rule`.
2. **Dedup** (`:67-84`). For presence, both kinds share the key `(hh, uid, "presence")` and the value is `facts.state`, so leave→arrive→leave fires three times and a heartbeat is skipped. Other kinds are keyed on `event_id|id|sorted(facts)`. The store is in memory.
3. Resolve a node: prefer `ctx.node_id`, then household nodes by recency. The first node that returns non-empty `client_tools` wins (4 s per node, sequential, `:95-119`). The menu is the node's **full tool list**, not only proposable actions.
4. **LLM** (`:123-164`): `chat_completion(model="background", temperature=0, tools=client_tools, tool_choice="auto", max_tokens=512, reasoning_budget=0, include_date_context=False)`.
   - System: "You carry out a household's standing automation rule… call ONE of the available tools… If no available tool fits… call no tool."
   - User: `Event: {label}\nEvent details: {facts json}\nThe user's instruction for this event: "{instruction}"\n…`
5. The dedup latches **after** the LLM call and before dispatch (`:297`). No tool → `no_action`.
6. `automatic`: `dispatch_node_command(node, name, args, trusted=True, 20 s)` → `ran:`/`failed:`.
   `notification`: an inbox card titled "Confirm automation", category `proposal`, whose `_action={node_id, command_name, arguments, idempotency_key}` is **embedded in the card data** (`:192-249`).
7. On confirm, `_execute_confirmed_action` (`:332-355`) dispatches **whatever `_action` the tap carries**. It does not check that the node belongs to the household, that the command is opted in, or that the call is idempotent. See §8.

### 3.6 Proactive situation matcher (`situation_matcher_service.py`)

- **Edge**: `signal_situation_edge` cancels any pending task for the household and schedules `_debounced_run` 5 s later. The latest edge wins (`:309-344`).
- `run_match_batch` (`:121-149`):
  - Gate: both `proposals.enabled` and `proposals.proactive_enabled` truthy. Note `_truthy` treats any non-empty string other than "false"/"0" as true (`:48-49`).
  - Node: the given node or the most recent household node (**not** command-aware).
  - Bundle: every live signal except `appt.upcoming` and `leave_by.suggested` (`:37,86-118`), each as `{source_key, kind, data: facts (+summary)}`.
  - Actions: `list_proposable_actions(node)`.
  - Prompt: `_build_situation_prompt` (`proposal_matcher.py:78-121`).
- **Enqueue** (`:152-204`) posts to `llm-proxy /internal/queue/enqueue`:
  - Header `X-Internal-Token: LLM_PROXY_INTERNAL_TOKEN`.
  - Envelope `{job_id:"situation-<hex16>", job_type:"chat", ttl_seconds:600, metadata:{type:"situation_match", household_id, node_id, bundle}, request:{model:"background", messages:[user prompt], sampling:{temperature:0}, reasoning_budget:0}, callback:{url, auth_type:"bearer", token}}`.
  - No `max_tokens`.
- **Callback** `handle_match_callback` (`:219-267`):
  - Requires `status=="succeeded"`.
  - Tolerant JSON parse (first `{` to last `}`).
  - Re-fetches the node's actions at callback time, then calls `finalize_situation_matches` (`proposal_matcher.py:146-205`):
    - drops unadvertised (command, action) pairs, after the dotted-label repair at `:124-143`
    - keeps the cited `sources` (none cited → all)
    - idempotency key over only the contributing observations' `data`
    - injects `idempotency_param`
    - runs `validate_against_params`
  - **Anti-nag** (`:275-298`): the exact idempotency key has never fired, at most 12 cards per household per UTC day, and a 6 h cooldown per (household, first source_key, command). All of it is in memory.
  - Emits the card with `source=first source_key`, **no `user_id`** (so it is household-wide) and default labels/blast tier.
- **Prompt text.** This must be byte-identical if the matcher is kept. The fixed preamble is at `proposal_matcher.py:105-121`. The menu is split into "ACTIONS designed for the current signals" (`listening_signal_types` ∩ bundle kinds) and "Other available actions…", or a single "ACTIONS:" list. Menu rows render as `- {command}.{action}: {card_title} | args: {json}` (`:71-75`). Output schema: `{"matches":[{"command","action","args","sources"}]}`.

### 3.7 Proposable-action dispatcher (`proposable_action_service.py:323-458`)

- **`execute`** runs steps A–F:
  - **A.** `proposals.enabled`, fail-closed. Truthy strings are `true/1/yes/on`.
  - **B.** Node: `_action.node_id`, or a household node that advertises the command. The card's node_id is **not** checked against the household either; it relies on the node advertising the action.
  - **C.** `resolve_proposable_action`; a miss → "Couldn't run that".
  - **D.** `validate_against_params` (`:189-212`): drops undeclared keys, requires the required ones, checks enums, and applies no type coercion.
  - **E.** If a `CallbackJob` with the same (household, idempotency_key) has status `completed` → "Already done".
  - **F.** Inserts a `CallbackJob(navigation_type="stack", 5 min expiry)`, publishes the MQTT `callback`, then polls the row every 0.5 s for up to 25 s.
  - Every failure returns `context_data.inbox` with a visible card. Success uses the node's `message` as the card title.
- **`dismiss`** is a no-op.
- **`suppress`** calls `record_suppression` (dedups on (hh, user, command, source_key) and refreshes `created_at`) and replies "Won't suggest that again".

### 3.8 Attention broker (`attention_broker.py:114-304`)

`record_and_route` always inserts an `AttentionEvent` and one `AttentionDelivery` containing the `gate_trail` JSON. It truncates title to 500, category to 50 and source to 100 (`:145-147`). The rung starts at `min(requested, push)`.

The gates run in order:

| Gate | Behaviour |
|---|---|
| 0. Dedup | Same (household, source, dedupe_key) within `dedupe_window_hours` (24) → journal, `withheld_by=dedupe`. Skipped for `force`. Skipped for safety categories **unless** the producer set an explicit key; the title-hash fallback must never dedup a recurring medication dose (`:173-184`, Keppra incident 2026-07-19). |
| Force | Bypass all gates. |
| Safety class | `attention.safety_categories` → bypass all gates. |
| 1. Consent | An `attention_consents.max_rung` ceiling; `never` → journal. |
| 2. Tier | An `attention_source_tiers` row caps the rung (T0 journal, T1 inbox, T2/T3 push). |
| 3. Budget | `source_daily_cap` (4) of non-journal deliveries per source → journal. Push and inbox budgets (8 and 30 per local day) each demote one rung. |
| 4. Quiet hours | `HH:MM-HH:MM` in household tz, may cross midnight. Demotes push → inbox. |

Callers deliver when `rung != journal` and then stamp `mark_outcome`. In `node_commands`:

- Broker errors or broker off → **legacy delivery, byte-identical** (`_attention_gate` returns None; fail-open, `:245-286`).
- A withheld request returns `sent=false, withheld_by`.
- An inbox-demoted push posts an inbox item without push.
- Source identity is just the request's `category`: `/node/push-notification` uses `source=category`, `/node/send-link` uses `source="send_link"`, `category="link"`, `dedupe_key=url`.

The journal card (`attention_journal.py:40-107`) is a markdown body listing delivered items (≤20) and withheld items with gate counts and the detail of the withholding gate. It is posted to the household inbox with category `attention_journal` and `push=False`.

## 4. Data

| Table | Key columns | Lifecycle |
|---|---|---|
| `signals` (`models.py:423-458`, `alembic/versions/sb01_add_signals_table.py`) | int `id`; `household_id`, `user_id` (NULL = household), `node_id`, `room`, `kind`, `subject`, `source_key` (512), `summary`, `facts` (JSON TEXT), `source_agent`, `cacheable`, `salience`, `observed_at`, `expires_at`, `is_active`, timestamps. **UNIQUE(household_id, source_key)**. Indexes on household, kind, user_id, expires_at. | Upsert. Hard-deleted every 30 min once `expires_at ≤ now`. NULL never expires. `is_active` is never set false anywhere. |
| `proposal_suppressions` (`models.py:1185-1204`, `pa02`) | `id = "sup_"+hex`; household, user_id, `command` (128), `source_key`, `descriptor`, `created_at` | Permanent until the user deletes the row in mobile. |
| `attention_events` (`:861-883`) | uuid; household, `source`, `category`, `title`, `summary`, `dedupe_key`, `target_user_id`, `origin_node_id`, `payload_json` | Deleted after `attention.journal_ttl_days` (30). |
| `attention_deliveries` (`:886-906`) | uuid; `event_id` FK **ON DELETE CASCADE**; `rung`, `gate_trail_json`, `withheld_by`, `inbox_item_id`, `request_id` (reserved), `outcome` | Cascades from events. |
| `attention_source_tiers`, `attention_consents`, `attention_feedback` (`:909-962`) | — | **No writer exists anywhere.** The broker reads tiers and consents (always empty). Feedback is never read or written. These are the PRD's phase 2–3 placeholders. |
| `callback_jobs` (doc 13) | `idempotency_key` used by dispatcher step E | — |
| Setting `signals.automations` | JSON string of rules | Per household. |

**In-memory state** (single process; resets on restart):

- `_rate_state` (`signals.py:118`)
- `_fired` leave-by claims (`signal_reaction_bridge.py:48`, unbounded)
- `_last_signature` automation dedup (`executor:59`)
- matcher `_pending`, `_fired_keys` (unbounded), `_last_fired`, `_daily` (`situation_matcher_service.py:40-44`)
- `_reactions` registry and the captured `_main_loop` (×2)
- journal-card `last_fired`

All timestamps are naive UTC.

## 5. Settings

| Key | Default | Effect |
|---|---|---|
| `signals.enabled` | `true` (`settings_definitions.py:53`) | Off → `/signals` and `/mobile/presence` return 409. **Fail-open.** It does *not* gate voice presence or the render. |
| `signals.automations` | `"{}"` (`:60`) | Automation rules. |
| `ambient_context.enabled` | `false` (`:312`) | Gates the render of signals and memories into the voice prompt, and the voice presence write. |
| `proposals.enabled` | `false` (`:340`) | Gates leave-by, dispatcher execute, and (with the next key) the matcher. Fail-closed. |
| `proposals.proactive_enabled` | `false` (`:353`) | The proactive matcher. |
| `attention.enabled` | `false` (`:699`) | Broker interposition and the journal card. |
| `attention.daily_push_budget` / `daily_inbox_budget` / `source_daily_cap` | 8 / 30 / 4 | Budgets. |
| `attention.dedupe_window_hours` | 24 | — |
| `attention.quiet_hours` | `"22:00-07:00"` | — |
| `attention.timezone` | `"UTC"` | Separate from any household tz used elsewhere. |
| `attention.safety_categories` | `"medication,reminder,security,safety"` | — |
| `attention.journal_ttl_days` | 30 | Read **without** a household (global). |
| `attention.journal_card_enabled` / `journal_card_cron` | `true` / `"0 21 * * *"` | — |

Hard-coded constants a port must carry:

| Constant | Value | Where |
|---|---|---|
| Rate limit | 60/min | `signals.py:117` |
| Mobile presence TTL | 4 h | `mobile_presence.py:44` |
| Voice presence TTL | 900 s | — |
| Leave-by buffer | 5 min | `bridge:37` |
| Matcher debounce | 5 s | — |
| Matcher cooldown | 6 h | — |
| Matcher daily cap | 12 | — |
| Automation `max_tokens` | 512 | — |
| Probe timeouts | 4 s per node, 6 s total | — |
| Dispatch timeout | 20 s | — |
| Dispatcher poll | 25 s | — |
| TTL cap on `/signals` | ≤ 604800 s (7 days) | — |
| `/attention/events` batch cap | 50 | Route being cut. |

## 6. Dependencies

- **CC subsystems:**
  - node MQTT `report_tools` (`api/node_tools._request_tools_from_node`) and `tool_call` / `callback` publish (doc 05)
  - inbox posting `post_inbox_item_sync` (doc 13)
  - `/callbacks` server plane and `CallbackJob` (doc 13)
  - settings service (doc 00)
  - `routine_scheduler.is_due` (doc 08)
  - `errand_planner._run_planner`, only on the uncalled live matcher path (doc 09)
  - `ToolBuilder.strip_jarvis_extensions` (doc 02)
  - ambient render in the prompt builder (docs 01 and 03; `render_signal_block` is at `prompt_providers/shared/core_rules.py:395-416`, which sorts by (kind, subject) and joins summaries)
- **Services:**
  - jarvis-auth `/internal/app-ping`, called per app-authed signal
  - notifications, via the inbox helpers
  - llm-proxy: the queue for the matcher, and sync chat for automations
- **LLM calls** (all on the **background** slot, `reasoning_budget: 0`):

  | # | Call | Prompt | Cadence | Status |
  |---|---|---|---|---|
  | 1 | Automation tool pick | §3.5 | ≤1 per distinct signal occurrence per enabled rule | Live |
  | 2 | Proactive situation match | §3.6 | ≤1 per household per 5 s debounce window, on every `/signals` or mobile-presence edge | Dormant |
  | 3 | Live `match_situation` via `_run_planner` | max_tokens 6000 | Only from `evals/` | Not prod |

- **Cost** (my estimate; nothing is metered):
  - Call 1 sends the node's **entire** `client_tools` schema (every installed command, often several thousand tokens) for each firing. On slow hardware it competes with every other background job.
  - Call 2's prompt grows with the number of live signals (all of them, no salience ranking or cap) and with the proposable menu. A chatty producer such as sports `game.final` (6 h TTL) re-arms it on every emit.
- **Third parties**: none directly. Drive time comes via the node's `get_drive_time`.

## 7. Invariants and non-obvious behaviour

1. **Wire contract for `/signals`** (SDK `signals.py:98-118`, node-setup `signals_backend.py`). This must stay stable.
   - Body: `{"household_id"?: str, "signal": {"kind": str≤255, "source_key": str≤512, "subject"?: ≤255, "summary"?: ≤2000, "scope"?: {"user_id"?, "node_id"?, "room"?}, "ttl_seconds"?: 1..604800, "cacheable": bool=false, "salience"?: float, "source_agent": str≤255="external"}, "data": object|null, "command"?: "cmd" | "cmd.callback"}`
   - Response: `{signal_id, mode, proposed}`.
   - Status codes: 400, 401, 403, 409, 422 (Pydantic shape *and* the cacheable rule, which uses `detail` as a string), 429 with `Retry-After: 1`, 502.
   - The SDK maps any non-2xx to `"http_error"` and producers retry on their next cycle (calendar `agent.py:324-328`, sports `:318-323`). Turning a 4xx into a 5xx is therefore harmless, but **200 must remain the accept code**.
2. UPSERT overwrites all fields. A re-emit without `ttl_seconds` makes the row **non-expiring** (`signal_service.py:69,93`).
3. Mobile presence: identity always comes from the JWT, never the body (`mobile_presence.py:86-95`). The `name` field is display-only.
4. Directed `scope.node_id` must belong to the household before it is contacted (`signals.py:184-185`). The dispatcher's opt-in check (C) is the authoritative gate for proposals.
5. Fan-out and reactions must never block or fail ingest. The response does not wait for them (`signal_dispatch.py`, `registry:82-101`).
6. `leave_by.suggested` must not fan out, and the matcher must exclude `appt.upcoming` and `leave_by.suggested` from its bundle (`situation_matcher_service.py:37`). Otherwise the user gets a double card.
7. Presence dedup in automations shares one key across `presence.left` and `presence.seen` (`executor:67-84`). Keying them separately would latch "away" forever.
8. Leave-by `idempotency_key` travels **inside params** because it is a required declared param of `reminder.set_at` (`bridge:189-216`).
9. The card element shape is mirrored by SDK `propose_action` and consumed by mobile (`proposal_card.py:42-94`): ids `confirm-/dismiss-/suppress-{idem}`, `target:"server"`, `navigation_type:"new_notification"`, control data under `_action`, declared params **stringified** at the top level of `data` so that mobile field edits merge. Category `"proposal"`.
10. The broker never drops: the floor is the journal. Safety categories are never deduped on the title-hash fallback. Broker failures fail open to legacy delivery. With `attention.enabled` off, the interposed endpoints must behave byte-identically to the pre-broker code.
11. `proposals.enabled` fails **closed**; `signals.enabled` fails **open**. This is deliberate (`proposable_action_service.py:50-66`).
12. Attention cleanup relies on the FK cascade. In SQLite that needs `PRAGMA foreign_keys=ON`.

## 8. Oddities

1. **Security: confirm-card injection and cross-household execution.**
   - `jarvis.signal_automation.execute` runs `dispatch_node_command(node_id, command_name, arguments)` with `trusted=True`, taken directly from the tap's `data._action` (`executor:332-355`).
   - `POST /callbacks` checks only that the user is a member of the *body's* household (`api/callbacks.py:252-266`).
   - So any authenticated household member can run any tool on any node, including another household's, with arbitrary arguments.
   - The automatic path likewise skips `proposals.enabled`, the proposable opt-in, and the (dead) autorun allowlist.
   - The proposable dispatcher is bounded by opt-in but also does not check that `_action.node_id` belongs to the household.
2. **"Never suggest this" is a no-op on most cards.**
   - Directed cards (`source=source_key`) and proactive cards (`source=first source_key`) show the button.
   - Neither path ever reads `proposal_suppressions`. Only leave-by (`bridge:64-74`) and node detector agents do.
3. **Leave-by claims before succeeding** (`bridge:283`). A node that is offline, or a missing route, on the first emit latches the event until restart, even though the calendar agent re-emits. `_fired` and `_fired_keys` are unbounded sets.
4. **Double reaction on `appt.upcoming`.** A user automation on "An appointment is coming up" *and* leave-by both fire, which can produce two reminder cards. The catalog's own example ("Remind me 30 minutes before I have to leave") is exactly the leave-by feature.
5. `cacheable` and `salience` are accepted, validated and stored, but **never read**. The render ignores `cacheable`; signals always ride the trailing ambient block (`conversation_handler.py:3251`).
6. Voice presence overwrites the mobile presence row (the same `presence:{uid}` key). It shortens the TTL from 4 h to 15 min and changes the summary, without fan-out and under a different gate (`ambient_context.enabled`).
7. The catalog docstring says presence and `appt.upcoming` are "the only three kinds produced today" (`signal_catalog.py:13`). But sports emits `game.final`, and calendar `add_event` listens for `appt.detected`, which **no producer emits**.
8. `_action_idem` uses Python's `hash()` (`executor:187-189`). It is salted per process, so it is not stable across restarts, and the execute handler never consults it anyway: a double tap runs twice.
9. The matcher picks the "most recent node" without regard to commands (`situation_matcher_service.py:127-131`), even though a command-aware resolver exists. Its cards carry no `user_id` and no descriptor.
10. The comment says `reasoning_budget: 0` is the thinking-off contract (`proposal_matcher.py:34-41`). The PRD (§15) says the sidecar did *not* honor it and that only `enable_thinking:false` worked. The doc and the code disagree; the code is newer.
11. `_emit_directed_proposal` calls `asyncio.run` inside a sync threadpool handler, which blocks a worker for up to about 10 s on a directed signal.
12. The rate-limit key is client-chosen (`source_agent`), so a producer can evade it by varying the value. App auth may target any household.
13. The attention PRD header says "not yet built", but phase 1 is built. The tier, consent and feedback tables have no writer. The cleanup loop sleeps a full day before its first run, so on a frequently restarted host it never runs. `journal_ttl_days` is read globally. The journal loop hands a session to `asyncio.to_thread`.
14. The callback auth token is named `JARVIS_ADAPTER_CALLBACK_TOKEN`, a LoRA-era name (`main.py:1867`). It disappears when the callback goes in-process.
15. `is_active` is part of the upsert filter and the unique key, but it is never false. If it ever were, a re-insert would violate `UNIQUE(household_id, source_key)`. Two concurrent first-time inserts of the same key race to a 500.
16. `autorun_gate.py` has no caller. `match_proposals` and `match_situation` have no prod caller.

## 9. Tests

All are in `tests/`:

| File | Tests | Covers |
|---|---|---|
| `test_signals_ingest.py` | 9 | Ingress contract |
| `test_signal_service.py`, `test_signals_migration.py` | — | Store and migration |
| `test_signal_presence.py`, `test_voice_presence.py`, `test_mobile_presence.py` | — | Presence |
| `test_signal_render.py` | — | `render_signal_block` |
| `test_signal_reaction_registry.py`, `test_signal_reaction_bridge.py` | 15 (bridge) | Reactions |
| `test_signal_automation_executor.py`, `test_signal_automation_store.py`, `test_signal_catalog.py`, `test_mobile_signal_automations.py` | 16 (executor) | Automations |
| `test_situation_matcher_enqueue.py`, `test_situation_matcher_callback.py`, `test_situation_prompt.py`, `test_match_situation.py`, `test_proposal_matcher.py` | — | Matcher |
| `test_proposable_action_service.py`, `test_proposal_card.py`, `test_capability_registry.py` | 16 (dispatcher) | Dispatcher |
| `test_attention_broker.py` | 30 | Broker |
| `test_autorun_gate.py` | — | Autorun |
| `test_signal_precision_harness.py` | — | Precision harness |

**Contract (black-box) candidates:**

- `/signals`, every status code: node vs app auth, 403 mismatch, 409, 429 with Retry-After, cacheable 422, upsert returning the same `signal_id`, directed refused (`proposed:false`) vs proposed against the fake MQTT node.
- `/mobile/presence` and the automations GET/PUT, including the 404 for an unknown kind and the blank-instruction clear.
- Suppressions on the node and mobile planes.
- The three interposed `/node/*` endpoints with `attention.enabled` off; they must stay byte-identical. These belong to doc 05/13, but the oracle lives here.

**Golden fixtures:**

- The cacheable regex verdicts.
- `_stable_idempotency_key` (sha256 of `json.dumps(sort_keys=True, default=str)`). Python's separators `", "`/`": "` matter.
- `_build_situation_prompt` for representative bundles and menus. Byte-exact, if kept.
- `finalize_situation_matches`: dotted-key repair, out-of-range sources, idempotency injection.
- `validate_against_params`.
- Broker gate trails across scenarios: dedup and safety, consent, budgets, quiet hours across midnight, DST.
- The leave-by `due_at` arithmetic.
- `render_signal_block` ordering.
- The proposal card element JSON.

## 10. Questions for the user

1. **[scope][behaviour] Should signal automations be able to act without a tap, and through which gate?**
   - Today `automatic` runs any node tool the LLM picks, with `trusted=True`. The `notification` card's Confirm executes whatever node, command and arguments the tap carries, with no household or opt-in check (§8.1). That is a real cross-household hole a port must not copy.
   - `autorun_gate.py` (an allowlist of `get_drive_time` and `reminder`, at most 3 steps) looks like the intended brake, but nothing calls it.
   - *Why it matters:* this is the only path in the system where an LLM turns a background event into an unattended side effect.
   - *Options:*
     - (a) Port as-is.
     - (b) Keep both delivery modes, but store the chosen action **server-side** with the card carrying only an opaque id; check the node belongs to the household; route `automatic` through the autorun allowlist, and fall back to a card when it fails.
     - (c) Drop `automatic`, so everything is a card.
   - **My recommendation:** (b). Ask whether the allowlist should be user-extensible per rule.
2. **[scope] What is the product intent for the proactive situation matcher, and should it be ported now?**
   - It is off by default, its harness gate fails on Qwen3.5-9B (PRD §15: recall 0.86 with 9% nag, or 0.57 with 0% nag), and its live `match_situation` path has no prod caller.
   - It brings a byte-exact prompt, a queue job type, anti-nag state and the eval harness.
   - Also: are `proposals.enabled`, `proposals.proactive_enabled`, `attention.enabled` or `ambient_context.enabled` on for any prod household today? I could not see prod settings.
   - *Options:*
     - (a) Port it fully but dormant.
     - (b) Port the Signal store, render, reactions and dispatcher now; defer the matcher to post-port R&D in Go and keep `evals/` as an offline Python tool.
     - (c) Cut it.
   - **My recommendation:** (b). The matcher is the most experimental piece and its correctness is model-dependent, not port-dependent.
3. **[scope] Should the attention broker be ported?**
   - It is phase 1 of 3. The tier, consent and feedback tables have no writer, the two direct routes are unused, and it is off by default.
   - However, it is interposed on three *public plugin* endpoints, and its Keppra-dose safety rule shows it was used in anger at least once.
   - *Options:*
     - (a) Port phase 1 as-is, including the dead tables.
     - (b) Port the gates, journal and card, and drop the tier, consent and feedback tables until phase 2/3 is actually built.
     - (c) Cut it, keeping only the legacy delivery path.
   - **My recommendation:** (b) if you still want notification governance, otherwise (c). Either way the legacy (flag-off) path must stay byte-identical.
4. **[behaviour] Should "Never suggest this" be enforced centrally?**
   - Today it only works for leave-by cards and the email detector agent. On directed and proactive cards the button records a row that nothing reads (§8.2).
   - *Options:*
     - (a) Port as-is.
     - (b) Check suppressions inside `emit_proposal_card`, keyed on (household, user or any, command, source), so every proposer honors them.
     - (c) Hide the button on cards whose proposer does not honor it.
   - **My recommendation:** (b).
5. **[behaviour] Should dedup and anti-nag state survive restarts?**
   - Leave-by claims, automation signatures and matcher cooldowns and caps are all in-process and partly unbounded. Leave-by claims *before* success, so one transient failure suppresses that appointment until restart (§8.3).
   - *Options:*
     - (a) Keep them in memory.
     - (b) Persist them in a small SQLite table with TTLs, and claim only on terminal outcomes (proposed, suppressed, departure passed).
   - **My recommendation:** (b). It is cheap with SQLite and fixes the retry bug.
6. **[behaviour] When a user writes an automation for "An appointment is coming up", should the built-in leave-by reaction still fire?**
   - Today both fire, which can produce two cards (§8.4).
   - *Options:*
     - (a) Both fire.
     - (b) A user rule for `appt.upcoming` replaces leave-by.
     - (c) Show leave-by in the automations UI as a built-in default rule the user can switch off.
   - **My recommendation:** (c), or (b) if the UI is frozen.
7. **[behaviour] How should voice presence interact with phone presence?**
   - Voice overwrites the user's single presence row, cutting the TTL from 4 h to 15 min. It is gated by `ambient_context.enabled`, not `signals.enabled`, and it never fires automations.
   - *Options:*
     - (a) Keep as-is.
     - (b) Voice refreshes but never shortens an existing `home` row's TTL, and honors `signals.enabled`.
     - (c) Give voice a separate `source_key` (`presence:voice:{uid}`).
   - **My recommendation:** (b). Keep no fan-out from voice, so automations don't fire every time you speak.
8. **[scope] Should signal kinds be declared by producers or kept as a static catalog?**
   - The catalog is hard-coded to presence plus `appt.upcoming`. `game.final` is emitted but not authorable, and `appt.detected` is listened for but never emitted.
   - *Options:*
     - (a) Keep the static catalog in Go.
     - (b) Let the SDK declare `emitted_signal_types` on agents and build the catalog from installed packages, which is an SDK change after the port.
   - **My recommendation:** (a) for the port, with (b) noted for later. Please confirm whether `appt.detected` is planned (from the email agent?) or stale.
9. **[minor] Should `cacheable` and `salience` keep their meaning?**
   - Both are accepted and `cacheable` is validated (422), but nothing reads either.
   - **My recommendation:** keep both on the wire and keep the 422 rule for compatibility, with no other behaviour. Or drop the 422, since no producer sends `cacheable: true` (calendar and sports send false).
10. **[minor] How should `/signals` handle auth and rate limits?**
    - Any app-to-app credential can write to any household, and the rate limit keys on the client-chosen `source_agent`.
    - Inside `jarvisd` the remaining app callers are external only (node-setup uses node keys).
    - **My recommendation:** keep app auth for compatibility, but key the bucket on (household, auth principal), with `source_agent` used only for logging.

## 11. Go port notes

**Shape.** Use one package, `internal/modules/cc/signals`, containing:

- **`Store`** (sqlc): `INSERT … ON CONFLICT(household_id, source_key) DO UPDATE`, which removes the `is_active` filter and the insert race. Keep `is_active` as a column only for schema parity.
- **`Bus`**: `Ingest(ctx, Signal) (id, error)` followed by `Dispatch(evt)`. `Dispatch` sends to a buffered channel drained by a goroutine pool. Reactions are an `map[kind][]Reaction` registered at module init. This replaces `call_soon_threadsafe` and both captured `_main_loop`s.
- **Reactions**: `LeaveBy` and `Automation`, each a plain Go func with the status-string return kept for logs and tests.
- **`Proposals`**: the card emitter, the dispatcher (registered on the server-callback registry from doc 13) and suppressions.
- **`Matcher`**: only if Q2 says so. It becomes an embedded-queue job type `situation_match` with a per-household dedup key (`situation:{hh}`). That gives you the debounce (a delayed job that replaces any pending one) and the background concurrency cap of 1 for free. The `/situation-matcher/callback` route disappears, and the job completion calls `finalize` directly.
- **`Attention`**: only if Q3 says so. It is a pure `Route(ctx, Event) Decision` plus its tables, called by the `/node/*` handlers through an interface, so with the flag off they take the legacy branch unchanged.

**Automation LLM call.** Make it an embedded-queue job too (type `signal_automation`, background slot, dedup key = the occurrence signature), not an inline goroutine. This bounds load on slow hardware and makes the signature dedup durable for free (Q5). Pass `reasoning_budget: 0` / `enable_thinking:false` through the in-process LLM interface.

**Node probes.** `report_tools` and `tool_call` / `callback` go over the embedded MQTT request/response (doc 05). Add a short-TTL (about 30 s) per-node cache of the tools report: today every directed signal, leave-by, automation and confirm re-probes the node. Keep the timeouts: 4 s per probe, 6 s total for resolution, 20 s for dispatch, 25 s for the callback.

**Dispatcher step F** polls a `CallbackJob` row every 0.5 s. In-process, it can instead await a completion channel that the node-result handler signals; keep the row for idempotency (step E).

**Byte-exact items:**

- the `/signals` JSON (including FastAPI's 422 shape)
- the card element JSON (stringified params)
- `_stable_idempotency_key`: reproduce `json.dumps(sort_keys=True, default=str)` exactly, including `", "` and `": "` separators and `ensure_ascii=True` escaping, or existing idempotency keys and suppressions stop matching
- the situation prompt, if kept

`_action_idem` uses salted `hash()`, so it does not need to match. Replace it with sha256.

**SQLite:**

- Enable `PRAGMA foreign_keys=ON` for the attention cascade.
- Run all three sweeps (signals every 30 min, attention daily, and the journal-card cron tick) as scheduled jobs on the embedded queue, with the first attention cleanup run at startup.
- Timestamps stay naive UTC in TEXT to match `import-legacy`.

**Risks:**

- The security fix in Q1 changes card data. Pending, unconfirmed automation cards in mobile at cutover will fail closed, which is acceptable.
- In Python, signal fan-out is fire-and-forget after the response. In Go, make sure `Dispatch` is non-blocking on a full channel: drop and log rather than block ingest. This preserves invariant 7.5.
- Leave-by and automation both depend on MQTT nodes being online. In the strangler period, with the nodes module still in Python, they reach node dispatch through the HTTP-client implementation of the nodes interface.

**Simplifications from the single binary:**

- No `JARVIS_ADAPTER_CALLBACK_TOKEN` and no `LLM_PROXY_INTERNAL_TOKEN` hop.
- No `asyncio.run`-in-threadpool hack.
- No `/internal/app-ping` round trip for in-process callers.
- Rate-limit and anti-nag state can live in one `sync.Map` or a SQLite table, as Q5 decides.
