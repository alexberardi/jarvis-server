# 11 — Phone calls

Scope: AI outbound phone calls to businesses, the household phonebook, per-user "call context" (PII the agent may use), the web-search number lookup, the gateway-facing `/internal/phone/*` contract, the `make_phone_call` tool, and the 30 s phone reaper.

CC source files (all under `/home/alex/jarvis/jarvis-command-center/app/`):
`services/phone_call_service.py` (1364 lines), `api/phone_sessions.py` (348), `services/call_context.py` (374), `api/mobile_call_context.py` (112), `services/phone_number_search.py` (275), `api/mobile_phone_contacts.py` (277), `core/tools/make_phone_call_tool.py` (155). Tables: `phone_call_sessions` and `phone_contacts` (`models.py:965-1045`; migrations `c3d4phone001`, `d4e5phone002`).

Gateway (external, Python, frozen): `jarvis-phone-gateway`, port 7713. I read a snapshot of it at `scratchpad/pkgs/jarvis-phone-gateway` (`main.py`, `services/dial_worker.py`, `services/session_client.py`, `queues/dial_queue.py`, `services/turn_pipeline.py`, `services/prompt.py`).

---

## 1. Purpose

**In user terms:** you say "Call Tony's Pizzeria and order a large pie for pickup at 6", or an errand step asks for it. Jarvis then:

1. Works out the number: first the household phonebook, then a web search.
2. Drafts a brief for the call.
3. Pushes a **confirm card** to your phone. The card lets you edit the number and the brief, and it expires.
4. When you tap **Call now**, places a real PSTN call through **Twilio**.

On the call, an **LLM agent** talks to the business in real time: whisper STT, then the llm-proxy model, then TTS, carried over Twilio Media Streams. It opens with a disclosure that cannot be skipped: *"Hi, I'm an automated AI assistant calling on behalf of {user}. This call may be recorded."* (gateway `services/prompt.py:20-23`).

During the call, the agent can:
- push a question to you mid-call (**escalation**, which waits about 25 s);
- ask CC whether a proposed appointment time fits your calendar (**check-time**).

When the call ends you get an **outcome card**: a summary, `goal_achieved`, and "what the business said". A successful call also saves the business to the phonebook automatically.

**Who uses it:**

| Actor | How |
|---|---|
| Node / mobile chat (voice turn) | The `make_phone_call` server tool, offered on every turn |
| Errand / workflow engine (09) | `_place_call` is a deferred step handler that suspends until the call finishes |
| Mobile app | Phonebook CRUD, call-context grid, confirm/cancel/escalation-answer taps (server-plane `/callbacks`) |
| jarvis-phone-gateway | `/internal/phone/sessions/*` (app-to-app auth). It is also called *by* CC: line-type lookup, escalation answer, cancel |
| Background loop | Phone reaper, every 30 s |

**Scope:** US-only (NANP) and outbound to businesses only. There are no inbound calls. Off by default: `phone_calls.enabled=false`, and the check fails closed.

---

## 2. Entry points

### Routes

| Method | Path | Auth | Live caller | Code |
|---|---|---|---|---|
| GET | `/internal/phone/sessions/{id}` | app-to-app (any app) | gateway `session_client.get_session` (before claim) | `api/phone_sessions.py:129-168` |
| POST | `/internal/phone/sessions/{id}/check-time` | app-to-app | gateway `turn_pipeline.py:403-410`, per turn, only when `constraints` is non-empty and the utterance looks like a time; 3 s timeout | `api/phone_sessions.py:175-203` |
| POST | `/internal/phone/sessions/{id}/events` | app-to-app | gateway: claim_dial, state, turn, heartbeat, escalation, outcome | `api/phone_sessions.py:206-348` |
| GET | `/api/v0/mobile/household/{hh}/phone-contacts` | user JWT, role member | mobile `phoneContactsApi.ts` | `api/mobile_phone_contacts.py:138-158` |
| POST | same | JWT member | mobile | `:161-206` |
| PATCH | `…/phone-contacts/{id}` | JWT member | mobile | `:209-256` |
| DELETE | `…/phone-contacts/{id}` | JWT member | mobile | `:259-277` |
| GET | `/api/v0/mobile/call-context` | JWT (user-scoped) | mobile `callContextApi.ts:69` | `api/mobile_call_context.py:71-79` |
| PUT | `/api/v0/mobile/call-context` | JWT | mobile `callContextApi.ts:80` | `api/mobile_call_context.py:82-112` |

Router mounts are at `main.py:846-856`. The `/internal/phone/*` routes have no `/api/v0` prefix.

### Server-plane callbacks

All of these arrive as card taps on `POST /callbacks` with no `target_node_id`. Doc 13 owns that route. The only requirement is household **member** (`api/callbacks.py:231-260`). They are registered at `phone_call_service.py:1033-1039` and called from `main.py:486-488`.

| command.callback | Handler | Card |
|---|---|---|
| `make_phone_call.confirm_call` | `_handle_confirm_call` `:837-943` | Plan card, "Call now" |
| `make_phone_call.cancel_call` | `_handle_cancel_call` `:946-976` | Plan card "Cancel" **and** escalation card "End the call" |
| `make_phone_call.escalation_answer` | `_handle_escalation_answer` `:979-1030` | Escalation card, "Send answer" |

### Outbound HTTP from CC to the gateway

All three use app-to-app headers from env:

| Call | Code | Notes |
|---|---|---|
| `POST {gateway}/internal/lookup/line-type` | `:221-248` | 8 s timeout; any failure gives `"unknown"`. The gateway URL comes from discovery `jarvis-phone-gateway` / `JARVIS_PHONE_GATEWAY_URL` (`core/service_config.py:36,180-182`) |
| `POST {worker_url}/internal/call/{id}/escalation-answer` | `:1009-1030` | 10 s timeout |
| `POST {worker_url}/internal/call/{id}/cancel` | `:1322-1334` | Reaper only; best effort |

`worker_url` is whatever the gateway sent in `claim_dial`. In practice that is the gateway's **public tunnel URL**, `cfg.public_url` (gateway `dial_worker.py:191-193`). So CC reaches those two internal endpoints through the public Cloudflare hostname.

### Redis

`LPUSH phone:dial {"session_id","household_id"}` (`:1047-1065`). It needs `REDIS_URL`; if that is missing it raises, and the session is marked `failed`.

### Tool

`make_phone_call` (`core/tools/make_phone_call_tool.py`). It is a server tool with `is_risky=True` (`:36-39`) and takes the params `business` and `goal` (both required) (`:60-77`).

It is **always offered**, even when the gate is off, and it is force-added on the text-tool path (`core/conversation_handler.py:325-330`). The errand planner offers it only when the gate is on (`services/errand_planner.py:121-138`). The workflow engine treats it as a DEFERRED tool (`services/workflow_engine.py:216`, `_place_call` at `:236-257`).

### Background loops

| Loop | Cadence | Code | Does |
|---|---|---|---|
| Phone reaper | 30 s fixed | `main.py:518-528`, `phone_call_service.py:1286-1364` | Stale/over-time active calls → `failed`, plus a cancel to the worker and a card. Expired drafts → `expired` |
| Errand resume sweep (owned by 09) | 20 s | `main.py:203-224`, `errand_service.py:792-850` | Resumes workflows waiting on a terminal call. Times out after 60 min (`:731`) |

---

## 3. Behaviour

### 3.1 Full lifecycle

```
voice "call X and …" ─▶ make_phone_call.execute (sync)
  gate off → spoken refusal │ no speaker_user_id → refusal │ else create_task(create_call_plan) + spoken ack
                                    ▼
create_call_plan (background) ─ caps? ─ phonebook fuzzy ─ DNC? ─ web search ─ line-type ─ LLM draft brief
   ─ availability envelope (node calendar) ─ call context PII ─ prior errand context ─ INSERT draft row ─ confirm card (push)
                                    ▼
user taps Call now (/callbacks) ─ _handle_confirm_call: gate, single-use, TTL, caps, re-validate number,
   audit fields, draft→confirmed, LPUSH phone:dial
                                    ▼
gateway BLPOP ─ GET /internal/phone/sessions/{id} ─ events{claim_dial} (CAS confirmed→dialing, 409 = drop)
   ─ synth disclosure (fails → state failed, never dials) ─ Twilio calls.create ─ wait ≤60 s for media stream
   ─ events{state:in_call} ─ per turn: events{turn}, maybe check-time, maybe events{escalation}
   ─ heartbeat every 25 s ─ hangup / max_call_seconds ─ events{state:wrapup} ─ LLM assessment ─ upload WAV
   ─ events{outcome} (CC lands done)
                                    ▼
CC outcome: outcome_json, audio key, wrapup→done, phonebook upsert, outcome card, errand resume
```

### 3.2 Tool execute (`make_phone_call_tool.py:79-155`)

This step is synchronous and returns immediately. It takes the household and `speaker_user_id` from `conversation_cache.get_node_context` (`:92-97`), then refuses in this order:

1. Missing params.
2. No conversation context.
3. No household.
4. **Gate off.** Returns `phone_calls_disabled` with "A household admin can enable them in Household Settings" (`:103-114`).
5. **No identified speaker.** Returns `no_identified_speaker` with "Try again from the Jarvis app" (`:116-127`). This is deliberate: the confirm card has to land on a specific user's phone.

If nothing refuses, it fires `create_call_plan` with `loop.create_task` (`:132-140`) and returns `status: accepted` plus the spoken ack (`:149-155`).

### 3.3 Plan creation (`phone_call_service.py:335-577`)

The rule is "never vanish": every failure posts an inbox card.

1. **Caps** (`check_caps`, `:256-308`). Exceeding one posts a "📵 Call not started" card and returns None (`:362-372`).
   - Daily: sessions created since **UTC** midnight, including drafts, declined and expired, must be under `calls_per_day`.
   - Concurrent: sessions in `dialing|in_call|wrapup` must be under `max_concurrent_calls`.
   - Monthly: the sum of `duration_seconds` this UTC month must be under `monthly_minutes_cap*60`.
   - Any exception means "temporarily unavailable" (fail closed).
2. **Phonebook resolve** (`resolve_contact`, `:161-189`). This is a rapidfuzz `process.extractOne(normalized_query, {id: normalized_name}, scorer=WRatio, score_cutoff=80)` over **all** of the household's contacts. Normalisation (`:157-158`): lowercase, strip `[^a-z0-9 ]`, trim. Any exception returns None (best effort). See §9 for golden cases.
3. **DNC.** If the matched contact has `do_not_call`, post a "📵 Call refused" card and return None (`:378-389`). The phonebook number is then normalised; if it is invalid, `resolved_number` becomes None (`:390-394`).
4. **Web search** runs only when *no contact matched* (`:406-432`, `phone_number_search.find_business_number`, run in a thread).
   - Gated on `web_search.enabled` for the household, checked directly (`phone_number_search.py:95-118`).
   - The query is `"{business} {household.location} phone number"` (`:241-245`).
   - It scrapes the top 3 results (`_MAX_PAGES`, `:31`). The first number that is regex-shaped and normalisable wins (`:259-272`). Phone regex: `:36-46`. It needs separators, so bare 10-digit runs are rejected.
   - It extracts an address with a strict regex (`:61-77`). If the address is in a different US state from `household.location`, the card shows ⚠️ (`location_mismatch`, `:173-198`). This only warns; it never blocks.
   - A miss produces a card note naming the specific reason: `web_search_disabled`, `no_results`/`no_number_found` or `search_failed` (`:192-218`).
5. **Line type.** If a number is known and its type is unknown, CC asks the gateway (`:437-438`). A `mobile` result adds "this appears to be a mobile number" to the card.
6. **Brief drafting** (`_draft_details`, `:775-829`). This uses the background model slot, temperature 0.3, `max_tokens=200`, `extra_body={"reasoning_budget": 0}` and `include_date_context=True`. The system prompt is inline at `:788-805`; the user message is `Business/Goal/Caller name`. If drafting fails, the brief falls back to the raw goal.
   - The caller's name comes from `resolve_speaker_name` (jarvis-auth) (`:761-772`).
   - `_ensure_times_section` (`:592-604`) always appends an `Acceptable times: (fill in …)` placeholder when the goal looks like scheduling. The keywords are `appointment|book|schedule|reservation|reserve|visit` (`:581-589`).
7. **Availability envelope** (`:696-745`). This runs only for scheduling goals with no real times yet. It calls `context_provider_client.query_context(hh, "availability", {start: today, end: +7d}, user_id)`; doc 05 covers that node calendar command.
   - `free` and `busy` (at most 6 each) become `Acceptable times: a; b` and `Do not book: c; d` (`:644-662`).
   - If the answer is not ok, the brief gets `Acceptable times: (calendar unavailable — fill in …)`.
8. **Call context.** `apply_call_context(details, user_id, categories=None)` appends the caller's PII block (`:466`, `:665-693`; see §3.8).
9. **Prior errand context** is appended when it is present (`:470-471`).
10. **INSERT the draft row** (`:473-495`): `expires_at = now + plan_ttl_minutes` (default 20). `contact_address` is the contact's address, else the search address. The business's own address is deliberately **not** put in the brief (`:449-454`).
11. **Confirm card** (`:497-560`). Its metadata includes:
    - `editor_schema: 2`;
    - `editable_fields`: `dialed_number` (tel, required, prefilled) and `details` (multiline, required);
    - `expires_at`;
    - `number_source` (`phonebook|web|none`);
    - `interactive_elements`: confirm-call and cancel-call (`command: make_phone_call`, `target: server`).
    
    It is posted to the initiating user with `push=True` and `category=phone_call` (`_post_card`, `:1252-1276`).
12. Returns the `session_id`. Errands suspend on it. **A number miss still drafts a session**, so the errand suspends rather than failing (`:349-357`).

### 3.4 Confirm tap (`:837-943`)

1. Load the session, scoped by `household_id`.
2. **Re-check the gate.** If it is off: draft → `declined` with `error_message`, and refuse (`:858-864`).
3. **Single use.** If the state is not `draft`, return success with an "Already handled" card (`:866-877`).
4. **TTL.** If it has expired → `expired` and refuse (`:878-884`).
5. **Caps again.** If over a cap → `declined` (`:886-891`).
6. **Re-validate the edited number** with `normalize_us_number`. If it is invalid, refuse, and the session stays draft (`:893-898`).
7. Details must not be empty (`:900-904`).
8. **Audit fields:** `dialed_number`, `number_edited = dialed != resolved`, `details` (the user-edited brief replaces the draft), `confirmed_by = ctx.user_id`. Then → `confirmed` (`:906-911`).
9. `enqueue_dial`. On any failure → `failed` with "Couldn't start the call — try again in a minute." (`:913-923`).
10. Returns a "📞 Calling {name}…" inbox context (`:929-941`).

**Cancel** (`:946-976`) moves `draft → declined` **only**. In any other state it still returns "🚫 Call cancelled — Won't call X" and does nothing else. See §8.

### 3.5 Gateway events (`api/phone_sessions.py:206-348`)

| `type` | Precondition | Effect | Errors |
|---|---|---|---|
| `claim_dial` | `worker_url` | **Atomic** `UPDATE … SET state='dialing', worker_url, heartbeat_at WHERE id=? AND state='confirmed'`; rowcount must be 1 (`:219-247`) | 400 if no worker_url; **409** = do not dial |
| `state` | `state` | `transition()`, heartbeat, optional `twilio_call_sid`/`duration_seconds`/`error`. `failed` posts a "⚠️ Call failed" card. A terminal state schedules the errand resume (`:249-280`) | 409 illegal transition |
| `turn` | `turn` dict; state active | Appends to `transcript_json` (read-modify-write of the JSON list) and heartbeats (`:282-297`) | 409 if not active |
| `heartbeat` | not terminal | `heartbeat_at = now` (`:299-304`) | 409 if terminal |
| `escalation` | `question`; state `in_call` | Heartbeat, then `post_escalation_card` (`:306-319`) | 400 / 409 |
| `outcome` | `outcome` dict | Stores `outcome_json`, `audio_object_key`, duration. If not terminal: dialing/in_call → wrapup → done. Then phonebook upsert (best effort), outcome card, errand resume (`:321-346`) | 400 |

Unknown type returns 400 (`:348`). Turn payload shape (gateway `turn_pipeline.py:196-212`): `{n, heard, said, timings:{stt_ms, llm_ttft_ms, tts_ttfb_ms, total_ms}, events:[…]}`. In notice-off mode `heard` and `said` are redacted, but that mode cannot be reached (§8).

Outcome payload shape (gateway `dial_worker.py:358-367`): `{summary, goal_achieved: bool|null, facts: [str], turns, escalation_unanswered, audio_available}`.

### 3.6 Session snapshot (GET, `:129-168`)

Returns:
- `id`, `state`, `household_id`, `contact_name`, `contact_address`, `dialed_number`, `goal`, `details`, `line_type`;
- `initiator_name`, resolved live from auth; the disclosure needs it, otherwise the gateway falls back to "a customer";
- `restricted_details`: `[{key, label, value}]` for IF_ASKED fields, recomputed from the stored call context and not parsed from the brief (`:84-115`). This feeds the gateway's spoken-output guard;
- `constraints = s.constraints or extract_constraint_envelope(details)` (`:163`). The `constraints` column is never written, so this is always the derived value;
- `max_call_seconds` (setting).

**`record_enabled` is not sent.** The gateway defaults it to True (`dial_worker.py:215`).

### 3.7 Check-time (`:175-203`)

`time_window.check_time(envelope, utterance)` returns `{time_detected, available, proposed_label, acceptable_summary}`. It does deterministic interval arithmetic over the `Acceptable times:` and `Do not book:` lines (`services/time_window.py`). Doc 08 owns that parser; this doc owns the route. A bad utterance never raises.

`extract_constraint_envelope` (`phone_call_service.py:612-641`) keeps only lines that start with `acceptable times:` or `do not book:`, and drops any containing `(fill in`.

### 3.8 Call context (PII) (`services/call_context.py`)

**Storage.** A user-scoped setting `phone_calls.call_context` holding a JSON string `{"fields":[{key,label,value,category,tier}]}`. It is marked `is_secret` (`settings_definitions.py:784-798`).

**Controls.**
- Categories: `general, medical, auto, home, financial, dining` (`:47-63`).
- Tiers: `state` (may be said freely) and `if_asked` (`:66-68`).
- Well-known fields with fixed defaults: `:84-94`. `full_name` is GENERAL/STATE. `address` and `callback_number` are GENERAL/IF_ASKED. Medical, auto and so on are mostly IF_ASKED.

**Coercion** (`_coerce_entry`, `:110-137`):
- A row with no key or value is dropped.
- An unknown category falls back to the well-known default, else `general`.
- An unknown tier falls back to the well-known default, else **`if_asked`**.
- Duplicate keys: the first wins (`:159-166`).

**Write path.** PUT derives the key by slugifying the label (`:243-266`) and replaces the whole list. If the settings write fails, it returns 500, not silent success (`mobile_call_context.py:99-107`). GET also returns `catalog()` (`:298-318`), the vocabulary the grid renders.

**Selection today.** `select_for_call` returns **every field regardless of category** (`:204-216`). This is a deliberate decision recorded as "Alex, 2026-07-23". `select_fields` (category filter) exists but is unused (`:185-201`).

**Brief block** (`build_context_block`, `:321-374`):
- Opens with an "ON BEHALF OF … never claim to be them" preamble.
- Then lists the STATE fields.
- Then the IF_ASKED fields with "give it directly if asked… never volunteer".
- **`full_name` is always forced into the if-asked group** (`:345-346`). Live tests showed the model introducing itself as the user.

### 3.9 Escalation (`:1192-1249`, `:979-1030`)

**Card.** The callee's question is quoted and attributed ("They asked: …"). It has a single editable `answer` field. Buttons: send-answer (`escalation_answer`) and end-call (`cancel_call`). The card expires after 10 min, but the real window is the gateway's 25 s (gateway `services/escalation.py:18`).

**Answer handling.** The session must be active and have a `worker_url`. The answer is POSTed to `{worker_url}/internal/call/{id}/escalation-answer` as `{answer, answered_by}`. A non-200 response gives "Couldn't reach the call — it may have just ended."

### 3.10 Outcome and phonebook auto-save

**Outcome card** (`:1073-1121`):
- Icon: ✅ when achieved, ⚠️ when false, 📞 when null.
- `facts` may be a list (what the gateway sends), a dict or a scalar (`:1086-1097`; the list case caused a live crash on 2026-07-19).
- The body ends with an attribution disclaimer.
- Metadata carries `audio_object_key`.

**Auto-save** (`upsert_contact_from_call`, `:1124-1189`):
- Only when the state is `done`.
- The **dialed** number wins over the resolved one.
- The match is exact on `normalized_name` (no fuzzy matching here).
- Updating an existing contact: overwrite the number, refresh `verified_at`, fill `line_type` and an empty address, and promote `source` from `web` to `call`. **`do_not_call` is never cleared.**
- New contacts get `source="call"`.

### 3.11 Reaper (`:1286-1364`)

**Active sessions** (`dialing|in_call|wrapup`):
- `over_time` = `now − (confirmed_at or created_at) > max_call_seconds + 120`.
- `stale` = `(heartbeat_at or started) < now − 60 s` (`HEARTBEAT_STALE_SECONDS`, `:1283`).
- Either one sets `state='failed'` directly. **This bypasses `transition()`.** It also POSTs best-effort to `{worker_url}/internal/call/{id}/cancel` and posts a "⚠️ Call ended … because Jarvis {reason}" card.
- **No errand resume is scheduled here.** The 20 s errand sweep picks it up.

**Drafts:** any draft with `expires_at < now` → `expired`. There is no card; the client renders the expired state from `expires_at`.

Any exception is logged; the loop never dies.

### 3.12 Errand linkage

`errand_id`/`errand_step` on the session point at a **workflow run id** (`errand_service.py:805`).

**When a session goes terminal:**
- The `state`/`outcome` events schedule `resume_errand_after_call` (`phone_sessions.py:34-51`), which calls `deliver_signal(errand_id, step, "phone_call", session)` (`errand_service.py:710-724`).
- `declined` and `expired` are only caught by the 20 s sweep. It also synthesises a `failed` result after 60 min.

**How the workflow reads the outcome** (`workflow_engine.py:370-395`):

| Outcome | Step result |
|---|---|
| `done` and `goal_achieved is True` | Success; the summary is threaded into later briefs |
| `done` and anything else | Fail fast |
| Any other terminal state | Failed, with a reason |

**Workflow cancel** declines `draft`/`confirmed` sessions (`errand_service.py:959-985`). But `confirmed→declined` is **not a legal transition** (`phone_call_service.py:52-58`). So a confirmed, not-yet-claimed call still dials.

---

## 4. Data

### `phone_call_sessions` (`models.py:965-1015`)

| Column | Notes |
|---|---|
| `id` String(36) PK | uuid4 |
| `household_id` | indexed |
| `user_id` | The initiating speaker; the target of the cards |
| `confirmed_by` | The JWT user whose tap authorised the dial. It may differ from `user_id`, because any household member can tap |
| `contact_id` | FK → `phone_contacts.id` `ON DELETE SET NULL` |
| `contact_name` | The contact's name, or the raw `business` string on a miss |
| `contact_address` | From migration `d4e5phone002`. Phonebook address, else search address; used by auto-save |
| `errand_id` (indexed), `errand_step` | Workflow run link |
| `goal` NOT NULL | |
| `details` | The **confirmed brief**: the guardrail boundary; the user's edits win |
| `constraints` | **Never written.** Always derived from `details` |
| `resolved_number` | What CC found |
| `dialed_number` | What the user confirmed (E.164) |
| `number_edited` | bool |
| `line_type` | `mobile|landline|voip|unknown` |
| `state` | indexed; see the state machine |
| `error_message` | |
| `transcript_json` | JSON list of turn dicts. No reader anywhere (§8) |
| `outcome_json` | |
| `audio_object_key` | A MinIO key the gateway uploaded |
| `worker_url` | Session affinity; the public tunnel URL |
| `heartbeat_at` | |
| `twilio_call_sid` | **The gateway never sends it** |
| `duration_seconds` | **The gateway never sends it** (§8) |
| `created_at`, `confirmed_at`, `expires_at`, `ended_at` | Naive UTC |

**State machine** (`phone_call_service.py:52-58`):

```
draft     → confirmed | declined | expired | failed
confirmed → dialing (only via claim CAS) | failed | expired
dialing   → in_call | wrapup | failed
in_call   → wrapup | failed
wrapup    → done | failed
terminal: done, failed, declined, expired
```

`transition()` sets `confirmed_at` on entering confirmed and `ended_at` on entering any terminal state (`:316-327`).

**The reaper writes `failed` directly** (`:1317-1318`, `:1354-1355`). Those paths are not checked against the transition table.

### `phone_contacts` (`models.py:1018-1045`)

| Column | Notes |
|---|---|
| `id` | |
| `household_id` | |
| `name` | |
| `normalized_name` | **UNIQUE(`household_id`, `normalized_name`)** |
| `number` | E.164, validated on every write path |
| `address` | |
| `source` | `manual|call|web`. `web` is never written by current code: a search hit is not saved until the call succeeds, and then it becomes `call` |
| `line_type` | |
| `do_not_call` | |
| `notes` | |
| `overlay_json` | **Unused** |
| `verified_at` | |
| `created_at`, `updated_at` | |

### Lifecycle and retention

- Sessions are **never deleted**.
- `phone_calls.audio_retention_days` (default 30, "audio AND transcript") is declared and exposed in mobile settings, but **nothing reads it** in CC or the gateway.
- Audio is stored in MinIO bucket `PHONE_CALLS_BUCKET` by the gateway (`services/recording.py`).

**In-memory state in CC:** none. The gateway holds all live-call state in its own `call_runtimes` and its token registry.

---

## 5. Settings

Defined at `settings_definitions.py:776-859`. The mobile household-controllable allowlist is at `api/mobile_household_settings.py:44-52`; it covers every key except `attempt_cap` and `call_context`. Write role is admin.

| Key | Scope | Default | Effect |
|---|---|---|---|
| `phone_calls.enabled` | household | `false` | Master gate. Read at tool execute and at confirm. Fails closed on error (`:74-95`) |
| `phone_calls.call_context` | **user** | `""` | PII blob (§3.8). Secret |
| `phone_calls.plan_ttl_minutes` | household | 20 | Draft expiry |
| `phone_calls.max_call_seconds` | household | 600 | Sent to the gateway (its watchdog). Reaper allows +120 s |
| `phone_calls.calls_per_day` | household | 10 | Counts every session created since UTC midnight, drafts included |
| `phone_calls.monthly_minutes_cap` | household | 60 | Sum of `duration_seconds`. **Inert in practice** (§8) |
| `phone_calls.max_concurrent_calls` | household | 1 | Counts `dialing|in_call|wrapup` (not `confirmed`) |
| `phone_calls.audio_retention_days` | household | 30 | **Unread** |
| `phone_calls.attempt_cap` | household | 2 | **Unread** |
| `web_search.enabled` | household | (doc 04) | Gates the number search |
| `household.location` | household | `""` | Search bias and state-mismatch warning. Also used for node `home_context` (`main.py:905`) |

`_int_setting` (`:98-112`) returns the default for None, for a bool, or on any exception.

---

## 6. Dependencies

**Other CC subsystems**
- **13**: inbox (`post_inbox_item_sync`, push) and the server-plane `/callbacks` dispatch.
- **09**: errand/workflow engine (`_place_call`, `deliver_signal`, sweep).
- **04**: quick-search machinery (`_search_web`, `_scrape_results` in `quick_search_tool.py`; SSRF-guarded scrape).
- **05**: `context_provider_client.query_context` (the node calendar "availability" provider).
- **08**: `time_window` parser.
- **02**: llm-proxy client and the tool registry.
- **01**: `conversation_cache` node context (household, `speaker_user_id`).
- Settings service.
- `speaker_resolver.resolve_speaker_name`, which goes to jarvis-auth.

**LLM calls in CC: one.** `_draft_details` on the `background` slot, with reasoning off. All live-call LLM traffic (live model plus wrapup assessment, both `background`) happens in the gateway, which calls llm-proxy directly.

**Other services**
- jarvis-auth: speaker name, and app-auth validation of the gateway.
- jarvis-phone-gateway, which in turn uses whisper, llm-proxy, tts and MinIO.
- Redis.

**Third parties**
- **Twilio**: Voice, Media Streams and Lookup v2. Credentials live only in the gateway's env.
- **Cloudflare named tunnel**: public wss ingress to the gateway.
- Web search provider (doc 04).

---

## 7. Invariants and non-obvious behaviour (preserve these)

1. **The queue is transport, never authorisation.** The *only* dial authorisation is the `confirmed→dialing` single-winner CAS on the row (`phone_sessions.py:219-247`). The job payload carries only `{session_id, household_id}`; the gateway drops any extra fields (`dial_queue.py:40-58`). Any replacement transport must keep the CAS as the gate.
2. **Human confirmation on a JWT device is the authorisation to call.** The tool never dials (`make_phone_call_tool.py:1-9`). The gate is re-checked at confirm time, and turning the toggle off invalidates pending plans.
3. **The confirm tap is single-use and TTL-bound.** A second tap is a friendly no-op that returns success, not an error (`:866-877`).
4. **An edited number is re-validated** with the same `normalize_us_number`. The rules (`:126-149`), with a Go-side test sketch after this list:
   - Strip everything except digits and `+`.
   - Reject `911/112/933/988`, alone or `+`-prefixed.
   - Drop a leading `1` from 11 digits.
   - Fewer than 7 digits → "Short codes…".
   - Not 10 digits → "Only US numbers…".
   - Area code 900/976 → premium.
   - Area code starting 0/1 → invalid.
   - Output `+1XXXXXXXXXX`.
   
   The same validator gates the phonebook writes and the scraped numbers.
5. **Caps fail closed, at both plan time and confirm time.**
6. **Context enters a call only at plan time and is user-visible on the card.** The live call never gets a calendar tool. Callee speech is untrusted:
   - escalation text and outcome facts are rendered *attributed*;
   - escalation actions are only CC's fixed chips.
7. **`restricted_details` and the brief come from the same selection** (`select_for_call`), so the spoken-output guard's denylist covers exactly what the brief contains (`phone_sessions.py:105-112`).
8. **`full_name` is never in the "state freely" group** (`call_context.py:345-346`). Custom fields default to `if_asked`.
9. **The constraints are derived from the *confirmed* `details`**, so a user's edits to times on the card become the negotiation bounds. A placeholder line is never a constraint.
10. **Auto-save never clears DNC.** The dialed number supersedes the old one. Only `done` sessions are saved.
11. **Never vanish.** Every early exit in plan creation and every failure posts a card. A number miss still drafts a session, so the user can type the number.
12. **The tool is always in the prompt**, even when disabled. Without it the model hallucinates other tools for "call X" (`make_phone_call_tool.py:11-15`).
13. **No identified speaker → refuse.** No anonymous call plans from voice.
14. **A wrong-state search result warns but never blocks.** Same-state is never flagged. State detection is case-sensitive and uses the last token (`phone_number_search.py:156-198`).
15. **Phonebook routes return 404, not 403, for a foreign id** (`mobile_phone_contacts.py:103-118`). A duplicate normalised name returns 409.

The Go port should pin item 4 as a table test. The sketch below shows the shape, assuming a `NormalizeUS(raw string) (string, error)` in the phone package (the name is illustrative). The happy-path rows follow the rules above, and the four messages are copied from `phone_call_service.py:129-148`. The `+1 908…` row matches the test number in `tests/test_phone_call_service.py`. Run each input through the Python function once to confirm before freezing the table.

```go
func TestNormalizeUS(t *testing.T) {
	cases := []struct {
		in      string
		want    string // E.164 on success
		wantErr string // user-facing message on refusal
	}{
		{in: "(732) 592-4183", want: "+17325924183"},
		{in: "+1 908 555 1234", want: "+19085551234"},
		{in: "911", wantErr: "Emergency and service numbers can't be called."},
		{in: "+988", wantErr: "Emergency and service numbers can't be called."},
		{in: "12345", wantErr: "Short codes and service numbers can't be called."},
		{in: "900-555-1234", wantErr: "Premium-rate numbers can't be called."},
		{in: "123-456-7890", wantErr: "That doesn't look like a valid US number."},
		{in: "+44 20 7946 0958", wantErr: "Only US numbers are supported right now (10 digits)."},
	}
	for _, c := range cases {
		got, err := NormalizeUS(c.in)
		if c.wantErr != "" {
			if err == nil || err.Error() != c.wantErr {
				t.Errorf("%q: got (%q, %v), want error %q", c.in, got, err, c.wantErr)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%q: got (%q, %v), want %q", c.in, got, err, c.want)
		}
	}
}
```

---

## 8. Oddities, bugs and contradictions

### Bugs and dead paths

1. **"End the call" on the escalation card does not end the call.** It is wired to `cancel_call` (`:1227-1234`), which only acts on `draft` (`:962-964`). For a live call it returns "🚫 Call cancelled — Won't call X" and the call continues. Nothing in CC ever calls the gateway's `/cancel` except the reaper.
2. **The monthly minutes cap is inert.** CC reads `duration_seconds` from `state`/`outcome` events, but the gateway never sends it: there is no `duration` field in `dial_worker.py` or `session_client.py`. `twilio_call_sid` is likewise never sent. So `used_seconds` is always 0.
3. **Gateway failure reasons are lost.** The gateway's `_fail` posts `{"type":"state","state":"failed","reason":…}` (`dial_worker.py:428-437`), but CC's body model reads `error` (`phone_sessions.py:73`). Pydantic drops `reason`, so the card always says "The call could not be completed." and `error_message` stays null.
4. **A ringing call can be reaped.** `claim_dial` sets `heartbeat_at`. The gateway does not heartbeat until the media stream starts, and it waits up to 60 s for that (`dial_worker.py:52,257-264`) after disclosure synthesis and `calls.create`. The staleness window is 60 s and the tick is 30 s, so a callee who answers late can be reaped mid-ring as "lost contact with the call".
5. **`confirmed` sessions are never reaped.** If the dial job is lost (gateway down, `RUN_DIAL_WORKER=false`, Redis flushed), the row sits in `confirmed` forever. It counts toward the daily cap. A linked errand times out after 60 min.
6. **A workflow cancel cannot stop a confirmed call** (§3.12). `transition(confirmed→declined)` returns False silently (`errand_service.py:976-979`).
7. **Turning the gate off mid-call does nothing to the live call.** The gateway's `/cancel` docstring lists "gate toggled off" as a reason (`main.py:236`), but CC never sends it.
8. **An outcome after a terminal state is double-reported.** If the reaper already set `failed` and the gateway later posts `outcome`, CC overwrites `outcome_json`, posts a *second* card ("Call finished"), and schedules the errand resume again. It skips auto-save because the state is not `done`.
9. **`record_enabled` / notice-off mode cannot be reached.** CC never sends it (§3.6), so every call is recorded and transcripts are stored.

### Write-only or unused data

10. **Retention is unimplemented.** `audio_retention_days` is user-editable but has no reader. Transcripts (PII) and audio accumulate indefinitely.
11. **Transcripts have no reader.** No route, admin page or mobile screen shows `transcript_json`. Mobile does not use `audio_object_key` either (it is not referenced in `jarvis-node-mobile/src`). Both are write-only today.
12. **Unused columns and settings:** `constraints` (never written), `overlay_json`, `attempt_cap`, and the `source='web'` value.

### Gaps in the guards

13. **DNC is name-based only.** These paths all skip DNC:
    - a query that scores below 80 against the DNC'd contact goes to web search and can return its number;
    - typing a DNC'd number on the confirm card;
    - deleting the contact (any member can do it);
    - un-setting `do_not_call` (any member can do it).
14. **Fuzzy matching gives confident false positives** (§9). Examples:
    - "Walgreens pharmacy" matches "CVS Pharmacy" (85.5) when Walgreens is not in the book;
    - "Nail Salon" matches "Hair Salon" (80);
    - ties (e.g. "pharmacy" against two pharmacies, both 90) go to whichever row Postgres returned first, which is non-deterministic.
    
    The card title shows the matched contact name and says "This number is from your phonebook", so the human checkpoint is the only guard.
15. **`GET /internal/phone/sessions/{id}` returns PII to any app-to-app caller.** That includes the brief, `restricted_details` values and the number. `require_app_auth` accepts any registered app, not just the gateway, and the route has no state or household check.
16. **Anyone in the household can confirm.** The confirm card goes to the initiator, but `/callbacks` only requires membership, so any member holding the session id can confirm. `confirmed_by` records who did. This is probably intended.

### Smaller oddities

17. **Caps are counted in UTC** (`datetime.utcnow()` day and month boundaries), not the household timezone.
18. **The concurrency cap ignores `confirmed`.** Two quick confirms both pass. The gateway worker is serial (one `handle_job` at a time, `dial_worker.py:156-174`), so the second waits in the queue. Its reaper `over_time` clock (from `confirmed_at`) runs while it waits.
19. **The internal escalation and cancel routes go over the public internet.** `worker_url` is the public tunnel URL, so CC's calls to them leave the LAN.
20. **Stale docstrings:**
    - The gateway CLAUDE.md says the CC endpoints "do not exist yet" and calls the live loop "next phase". Both are stale; the live loop shipped, and the code dates live incidents to 2026-07-19..23.
    - `create_call_plan`'s docstring says it returns None on a number miss; the code says the opposite (`:349-357` vs `:561-565`). The code is right.
    - The cancel-card comment says "Early returns above — caps/DNC/number-miss"; number-miss is wrong.

---

## 9. Tests

**CC test files:**

| File | Lines | Covers |
|---|---|---|
| `tests/test_phone_call_service.py` | 755 | Normalisation, transitions, resolve, caps, confirm/cancel/escalation, reaper, scheduling placeholder, plan provenance, outcome facts, constraint extraction |
| `tests/test_phone_sessions_api.py` | 367 | Snapshot shape, check-time, restricted details, claim CAS once/409, lifecycle, turns/heartbeat, outcome → done, escalation, app-auth required |
| `tests/test_phonebook.py` | 365 | |
| `tests/test_phone_number_search.py` | 399 | |
| `tests/test_call_context.py` | 322 | |
| `tests/test_mobile_call_context.py` | | |
| `tests/test_make_phone_call_tool.py` | 100 | |
| `tests/test_time_window.py` | 177 | |

The errand and workflow tests (`test_errand_*`, `test_workflow_engine.py`) cover the deferred call step.

The gateway has its own contract-half tests: `tests/test_session_client.py` and `test_dial_worker.py`.

### Black-box contract tests (Go must pass)

1. **Claim CAS:**
   - two concurrent `claim_dial` calls → exactly one 200, the other 409;
   - claiming a `draft` → 409;
   - an unknown id → 404.
2. **Events:**
   - an illegal `state` → 409;
   - `turn` on a terminal session → 409;
   - `escalation` outside `in_call` → 409;
   - an unknown type → 400;
   - `outcome` from `in_call` → `done`, plus a card, plus the phonebook upsert.
3. **The snapshot's key set**, exactly as listed in §3.6. **Check-time response keys.**
4. **Confirm-card metadata JSON:**
   - `editor_schema`, `editable_fields[].data_key`, `interactive_elements[]` with command/callback/target/data;
   - the escalation card's fields.
   
   Mobile's `inboxEditors.ts` renders both.
5. **Phonebook CRUD:** 201 / 409 for a duplicate normalised name / 400 for an invalid number with the validator's message / 404 for a foreign household.
6. **Call-context PUT** canonicalisation:
   - a keyless row gets a slug;
   - a blank value is dropped;
   - a duplicate key: first wins;
   - an unknown tier → `if_asked`.

### Golden fixtures for fuzzy matching

`fuzz.WRatio(normalize(query), normalize(name))` with `score_cutoff=80`, rapidfuzz 3.14.6 (computed 2026-10-06 from CC's `.venv`).

| Phonebook name | Query | Score | Match? |
|---|---|---|---|
| Tony's Pizzeria | Tony's Pizzeria | 100.00 | yes |
| Tony's Pizzeria | Tonys Pizzaria | 92.86 | yes |
| Tony's Pizzeria | Tony's | 90.00 | yes |
| Tony's Pizzeria | Tony | 90.00 | yes |
| Tony's Pizzeria | pizzeria | 90.00 | yes |
| Tony's Pizzeria | tonys pizza | 88.00 | yes (existing unit test) |
| Tony's Pizzeria | Toni's pizza | 80.00 | yes (exactly on the cutoff) |
| Tony's Pizzeria | pizza | 76.00 | **no** |
| Tony's Pizzeria | completely unrelated dentist | 32.14 | no (existing unit test) |
| Tony's Pizzeria | `!!!` (normalises to "") | 0.00 | no |
| CVS Pharmacy | CVS | 90.00 | yes |
| CVS Pharmacy | Walgreens pharmacy | 85.50 | **yes (false positive)** |
| CVS Pharmacy | the pharmacy | 76.00 | no |
| CVS Pharmacy | CVS on Route 9 | 38.46 | no |
| Dr. Smith Dental | Dr Smith | 90.00 | yes |
| Dr. Smith Dental | Smith | 90.00 | yes |
| Dr. Smith Dental | Dr Smyth | 85.50 | yes |
| Dr. Smith Dental | dentist | 62.18 | no |
| Joe's Auto Body | auto body | 90.00 | yes |
| Joe's Auto Body | Joe | 90.00 | yes |
| Joe's Auto Body | joe's garage | 50.67 | no |
| Mario's | Marios Pizza | 90.00 | yes |
| Mario's | Mario's Italian Restaurant and Pizzeria of Freehold | 60.00 | no |
| Pizza Hut | Pizza | 90.00 | yes |
| Pizza Hut | Tony's Pizza | 67.86 | no |
| Hair Salon | Nail Salon | 80.00 | **yes (false positive)** |
| Main Street Vet | main st vet | 84.62 | yes |

**Tie cases.** These go to the first choice in iteration order:

| Book | Query | Scores | Winner |
|---|---|---|---|
| {Tony's Pizzeria, Tony's Barber Shop} | "Tony's" | both 90 | first |
| {CVS Pharmacy, Walgreens Pharmacy} | "pharmacy" | both 90 | first in either order |
| {CVS Pharmacy, Walgreens Pharmacy} | "Walgreens" | 90 vs 19 | Walgreens |

### Other golden candidates

- `normalize_us_number` table (from `TestNumberNormalization`).
- `extract_number` and `extract_address` against the scrape fixtures in `test_phone_number_search.py`, including the "2 yrs and am so pleased" regression.
- `location_mismatch`.
- `build_context_block` output text. The prompt wording is load-bearing and was tuned live.
- `extract_constraint_envelope`.

---

## 10. Questions for the user

1. **[scope] Does phone calling ship in jarvisd at all, and if so, is the gateway absorbed into the binary?**

   *Why it matters:* this is the only feature that needs a paid cloud vendor (Twilio) and public ingress (a Cloudflare tunnel). That sits against "no cloud dependencies by default". The gateway is about 5.7k lines of Python: Twilio WS, G.711, VAD, the turn pipeline, the spoken-output guard and recording. It is frozen, and today it talks to CC through Redis plus HTTP.

   *Options:*
   - (a) Cut it from jarvisd v1. Keep the tables in the schema but no routes.
   - (b) Port the CC half only. The gateway stays an external Python sidecar, frozen and unchanged.
   - (c) Absorb the gateway into jarvisd as `phone/` (a Go port). Live calls would use in-process STT, LLM and TTS (sherpa-onnx), with no Redis and no internal HTTP. It stays optional and off unless Twilio credentials and a public URL are configured.
   - (d) (b) now, (c) in a later phase.

   **My recommendation: (d).** Phase 5 ports the CC half behind a `Dialer` interface, so (c) later is a drop-in. Absorbing it also fixes oddities 1–4, 7 and 19 for free, because the call loop and the session row would live in one process.

2. **[change] If the gateway stays external for now (D8), how does jarvisd hand off dial jobs?**

   *Why it matters:* jarvisd has no Redis. The gateway BLPOPs `phone:dial`, and Python is frozen, so changing the gateway to HTTP breaks the freeze.

   *Options:*
   - (a) An optional `REDIS_URL` shim. jarvisd LPUSHes the same 2-field JSON to an external Redis that only the gateway needs. That is about 30 lines of RESP.
   - (b) jarvisd POSTs to a new gateway `/internal/dial` endpoint. This needs a gateway change.
   - (c) The gateway polls a new `GET /internal/phone/dial-jobs`. This needs a gateway change.

   **My recommendation: (a).** The CAS stays the authorisation, so the transport carries no trust, and it respects the freeze. Treat it as throwaway until (1c).

3. **[behaviour] What should "cancel" mean once a call is past draft?**

   *Why it matters:* today none of these do anything:
   - "End the call" on the escalation card does not hang up (oddity 1);
   - cancelling a workflow does not stop a confirmed call (oddity 6);
   - turning off `phone_calls.enabled` does not stop a live call (oddity 7).

   *Options:*
   - (a) Port as-is.
   - (b) `cancel_call` on an active session POSTs the gateway `/cancel` and marks it `failed` with "cancelled by user". Add `confirmed→declined`, so a cancelled confirmed job 409s at the claim. A gate toggle-off cancels active calls.
   - (c) As (b), but add a distinct `cancelled` terminal state.

   **My recommendation: (b).** It reuses the existing states. Errand fail-fast already treats any non-done state as failure.

4. **[behaviour] Should do-not-call also block by *number*?**

   *Why it matters:* today DNC only works when the spoken name fuzzy-matches the blocked contact (oddity 13). A weaker match falls through to web search and can surface the blocked business's number. A number typed on the card is never checked either.

   *Options:*
   - (a) Keep it name-only.
   - (b) Also refuse at confirm time when `dialed_number` equals any DNC contact's number in the household.
   - (c) As (b), and also suppress web results matching a DNC number at plan time.

   **My recommendation: (c).** It is cheap, and a DNC flag the user set should be absolute.

5. **[behaviour] Fuzzy matching: port rapidfuzz `WRatio@80` faithfully, or change it?**

   *Why it matters:* `WRatio` is a composite scorer (Indel ratio, token sort/set, partial variants, length-ratio scaling). Getting its exact numbers in Go means porting it carefully. It also produces confident wrong matches: "Walgreens pharmacy" → CVS Pharmacy at 85.5, and "Nail Salon" → Hair Salon at 80. Ties are non-deterministic.

   *Options:*
   - (a) Port faithfully and pin the §9 table as goldens.
   - (b) Port faithfully, but break ties deterministically and add an "I matched *CVS Pharmacy* for 'Walgreens pharmacy'" note on the card whenever the score is under 95.
   - (c) Replace it with a simpler token-overlap scorer and new goldens.

   **My recommendation: (b).** The confirm card is the real guard, so make the substitution visible rather than re-tuning the threshold.

6. **[behaviour] The monthly-minutes cap never counts anything, because the gateway never reports `duration_seconds` (oddity 2).**

   *Options:*
   - (a) CC computes the duration itself from its own timestamps: in_call to terminal. That needs an `in_call_at` column, or using `confirmed_at`.
   - (b) Rely on the gateway (requires changing a frozen repo).
   - (c) Drop the cap.

   **My recommendation: (a)**, using a new `in_call_at` set by the `in_call` transition. That makes Twilio spend actually bounded.

7. **[behaviour] Reaper windows.**

   *Why it matters:* `dialing` can be reaped while the phone is still ringing (oddity 4), and `confirmed` is never reaped (oddity 5).

   *Options:*
   - (a) Keep 60 s for every active state.
   - (b) Per-state windows: `dialing` 150 s (60 s for the stream to start, plus synthesis and calls.create margin); `in_call`/`wrapup` 60 s; `confirmed` 5 min → `failed` with "the call never started — is the phone gateway running?"

   **My recommendation: (b).**

8. **[scope] Retention, transcripts and notice-off.**

   *Why it matters:* every call stores a full transcript (PII) and audio forever. `audio_retention_days` is user-editable but does nothing. No UI reads transcripts or audio. `record_enabled`, the PRD's "notice-off" mode, cannot be reached.

   *Options:*
   - (a) Port as-is.
   - (b) Implement retention: a daily job that nulls `transcript_json`/`audio_object_key` and deletes the blob after N days.
   - (c) As (b), plus a household `phone_calls.record` setting sent as `record_enabled`. Off would mean no audio and redacted turns.
   - (d) Stop storing transcripts at all, since nothing reads them.

   **My recommendation: (b), with (c) if you still want notice-off.** Do you want a call-history screen? If not, (d) is simpler and safer.

9. **[change] Lock down the `/internal/phone/*` routes.**

   *Why it matters:* any registered app-to-app identity can GET a session's brief, PII and number (oddity 15).

   *Options:*
   - (a) Port as-is.
   - (b) Accept only the gateway's app id, from a configured `PHONE_GATEWAY_APP_ID`.
   - (c) (b), and also return 409 on GET unless the state is `confirmed|dialing|in_call|wrapup`.

   **My recommendation: (c).** If the gateway is absorbed (Q1c), the routes disappear altogether.

10. **[behaviour] Who may confirm a call plan?**

    *Why it matters:* today any household **member** who can post the callback can confirm, edit the number of, or cancel another member's plan. The card is pushed only to the initiator. Voice requests need an identified speaker, but confirmation does not need to be the same person.

    *Options:*
    - (a) Keep: any member.
    - (b) Only the initiator (`ctx.user_id == session.user_id`), or a household admin.

    **My recommendation: (b).** The call speaks "on behalf of {initiator}" and loads *their* PII.

11. **[minor] Late outcome after the reaper failed the session (oddity 8).**

    *Options:*
    - (a) Port as-is: overwrite and post a second card.
    - (b) Store the outcome or transcript for the record, but post no card and don't re-resume.
    - (c) As (b), but if `goal_achieved` is true, post a corrective "Actually, the call finished: …" card.

    **My recommendation: (c).** The first card told the user it failed, and that was wrong.

12. **[minor] Remove the dead knobs:** `phone_calls.attempt_cap` (no reader), `overlay_json`, the `constraints` column and `source='web'`. Also: should the per-call **category** selection on the confirm card ever ship? It was deferred on 2026-07-23, and all categories load today.

    **My recommendation:** drop the first four from the Go schema. Keep `category` on call-context fields, since it costs nothing.

---

## 11. Go port notes

**Package shape.** `internal/cc/phone`:

| File | Contents |
|---|---|
| `store.go` | sqlc queries for `cc_phone_call_sessions`, `cc_phone_contacts` |
| `machine.go` | Transition table and `Transition(s, to) bool`. The reaper should use it too: add `dialing|in_call|wrapup → failed`, which is already legal, and `draft → expired` |
| `plan.go` | `CreatePlan(ctx, PlanReq) (sessionID string, err error)` |
| `confirm.go` | The three server callbacks, registered in the 13 callback registry |
| `contacts_api.go`, `callctx_api.go`, `internal_api.go` | Routes |
| `reaper.go` | |
| `numbers.go` | `NormalizeUS`, regexes, `LocationMismatch` |
| `fuzzy/` | WRatio port with goldens |
| `callctx/` | Parse, coerce, select, block render, catalog |

**Dialer seam.**

```go
type Dialer interface {
    Enqueue(ctx, sessionID, householdID string) error
    Cancel(ctx, sessionID string) error
    Answer(ctx, sessionID, answer string, by int64) error
    LineType(ctx, e164 string) string
}
```

Implementations:
- `redisDialer` (Q2a): LPUSH, plus HTTP to `worker_url` for Cancel and Answer, plus HTTP to the gateway for LineType.
- `noopDialer`: phone disabled.
- Later, `inprocDialer` (Q1c).

The confirm handler keeps the same order: commit `confirmed`, then `Enqueue`. If `Enqueue` returns an error, mark the session `failed` and post an honest card.

**Atomic claim in SQLite.**

```sql
UPDATE cc_phone_call_sessions SET state='dialing', worker_url=?, heartbeat_at=? WHERE id=? AND state='confirmed'
```

Check `RowsAffected()==1`. jarvisd has a single writer connection, so this is serialised for free. Keep it as a CAS anyway; it is the security boundary.

**Transcript append.** Python does read-modify-write of a JSON list on every turn. In Go, either do it inside the writer transaction, or use a child table `cc_phone_call_turns(session_id, n, json)`. The child table is cheaper and append-only. Keep the JSON shape for any export.

**Plan creation.**
- Run it as a durable queue job (`phone.plan`, concurrency 1) instead of `create_task`. The spoken ack returns immediately, and a crash mid-plan still ends in a card on retry.
- The job must be idempotent: dedup key = conversation id + business + goal.
- The errand path calls `CreatePlan` directly and synchronously; it already runs in the workflow driver.
- The LLM draft goes to the background slot, with reasoning budget 0 and `max_tokens=200`.
- Web search calls the 04 quick-search package in-process.
- Availability calls the 05 context-provider client.

**Errand resume.** Replace FastAPI `BackgroundTasks` with a direct `workflow.DeliverSignal(runID, step, "phone_call", snapshot)`, posted to the queue after the session commits. Keep the 20 s sweep as the safety net, and have the reaper's and confirm's terminal transitions also deliver the signal. That removes the 20 s lag for declined, expired and reaped calls.

**Reaper.** A ticker goroutine every 30 s, or a recurring queue job, using the per-state windows from Q7. Use one transaction per session, so a slow `/cancel` POST to the worker never holds the writer: commit first, then call the Dialer.

**Times.** Store UTC everywhere. Caps should use the household timezone if you agree (oddity 17); otherwise keep UTC for parity. The card's `expires_at` format is `%Y-%m-%dT%H:%M:%SZ`.

**Fuzzy port.** WRatio is not Levenshtein ratio. It is the rapidfuzz `ratio` (normalised Indel distance, i.e. LCS-based) combined with:
- `token_sort_ratio` and `token_set_ratio`, scaled by 0.95;
- the `partial_*` variants, scaled by 0.9 when the length ratio is ≥ 1.5, or 0.6 when it is above 8.

An empty input scores 0. Write it from the rapidfuzz source, not from memory, and gate it on the §9 table (exact to 2 decimals). Load the household's contacts once per plan; there are tens of rows, so brute force is fine.

**Settings.** Declare the same keys, and keep `phone_calls.call_context` user-scoped and secret. Treat the call-context blob as a JSON **string** value (the Python code had a real repr-vs-JSON bug here, `mobile_call_context.py:94-97`).

**Inbox.** Use the in-process 13 inbox service: category `phone_call`, `push=true`, `target_type` user when there is a user, else household. Keep the card titles and emoji; mobile may key on them.

**Risks.**
- The wording of the brief and context prompts was tuned live. Port the strings byte-for-byte: `_draft_details` system prompt, `build_context_block`, the `_search_miss_note` texts, the refusal messages.
- The `/internal/phone/*` JSON shapes are a frozen contract with the Python gateway. Contract-test them against the gateway's `session_client.py` expectations: 200 / 409 semantics on claim, `allow_conflict` only there.
- Quick-search scraping (doc 04) must keep the SSRF guard.

**Simplifications if the gateway is absorbed (Q1c):**
- No Redis.
- No `/internal/phone/*` routes, no app-auth and no `worker_url`.
- Escalation answers become a channel send to the live call goroutine.
- Cancel becomes context cancellation.
- Heartbeats become in-process liveness.
- `duration` and `call_sid` are known locally.
- Check-time becomes a direct `timewindow.Check` call.
- The disclosure uses the in-process TTS.
- The remaining external surface is the Twilio REST client, the `/media/{token}` WebSocket (still needs public ingress and signature validation), and the blob store for recordings.
