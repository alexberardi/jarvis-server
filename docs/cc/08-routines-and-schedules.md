# 08 — Routines and schedules

Source: `/home/alex/jarvis/jarvis-command-center` at `3b32b73`. All `file:line` cites are in that repo unless prefixed with another repo name (`node-setup/`, `node-mobile/`, `pantry/`). CC's CLAUDE.md is stale here: it describes `/api/v0/mobile/routines` and a `routine_builder.py`. Neither exists. The real router is `app/api/routines.py`, mounted at `/api/v0` (`app/main.py:824`).

Two of the files assigned to this chapter belong somewhere else:

- `time_window.py` is used only by the phone gateway's check-time route (doc 11).
- `step_value_resolver.py` is used only by the errand executor (doc 09).

Both are specified here (§3.6) because the split assigned them here, and the owning docs should cross-link.

---

## 1. Purpose

Three different "do something later or repeatedly" concepts share the word *schedule*. A port must keep them distinct.

| Concept | What the user gets | Who builds it | Where it runs | Confirmation |
|---|---|---|---|---|
| **Routine** | A named bundle of node commands ("good morning": lights, weather, calendar) and one LLM-composed spoken summary | Mobile routine builder (CRUD in CC) | **On the node**, via `RoutineCommand` (`node-setup/commands/routine_command.py:307`) | None. Runs autonomously when triggered. |
| **Routine schedule** | Optional cron/interval on a routine (`routines.schedule` JSON) | Mobile editor | CC's scheduler fires the routine on a target node | None. Fires unattended and posts a completion card. |
| **Schedule** (`schedules` table) | "Tomorrow at 9am, call the dentist" / "every weekday at 8, check traffic" | Voice tool `schedule_errand` only (`app/core/tools/schedule_errand_tool.py:161`) | CC re-plans the *intent* into an errand plan (doc 09) and posts a plan card | **Always.** The user must tap Run. A schedule never acts on its own (`app/services/schedule_service.py:3-6`). |
| **Errand / workflow** (doc 09) | LLM-planned mix of node commands and server tools | `run_errand` tool, or a schedule firing | CC executor, step by step across planes | Plan card, Run tap |

Routine triggers:

1. **Voice.** The trigger phrase is matched on the node with no LLM (`node-setup/commands/routine_command.py:241`). CC sees only the composition call.
2. **Run-now.** Mobile button → CC → MQTT → node.
3. **Schedule.** CC loop → MQTT → node.

Users:

- **mobile:** CRUD, run-now, Schedules screen
- **node:** pull, execute, POST the result
- **voice:** `schedule_errand` and `list_scheduled_errands` tools
- **background loops:** routine scheduler, execution TTL, schedule sweep

---

## 2. Entry points

### Routes

| Method + path | Auth | Live caller | Disposition |
|---|---|---|---|
| `GET /api/v0/households/{hh}/routines` | `verify_provisioning_auth`: admin `X-API-Key` **or** user JWT; membership is checked for JWT only (`app/provisioning.py:84-119`) | mobile `routineApi.listRoutines` | keep |
| `POST /api/v0/households/{hh}/routines` (201) | same | mobile `createRoutine` | keep |
| `GET /api/v0/households/{hh}/routines/{id}` | same | mobile `getRoutine` | keep |
| `PATCH /api/v0/households/{hh}/routines/{id}` | same | mobile `updateRoutine` | keep |
| `DELETE /api/v0/households/{hh}/routines/{id}` (204) | same | mobile `deleteRoutine` | keep |
| `POST /api/v0/households/{hh}/routines/{id}/run-now` | same | mobile `runRoutineNow` (`node-mobile/src/screens/Routines/RoutineListScreen.tsx:125`) | keep |
| `GET /api/v0/nodes/{node_id}/routines` | node `X-API-Key` (`verify_api_key`); 403 when the path id ≠ the caller (`app/api/routines.py:377`) | node `pull_routines` (`node-setup/services/routine_sync_service.py:60`), on boot (`node-setup/scripts/main.py:606`) and on nudge | keep |
| `POST /api/v0/routines/run-background` | node | **none** (Appendix A) | **cut** |
| `GET /api/v0/mobile/household/{hh}/schedules` | user JWT, member role (`app/api/mobile_schedules.py:32-45`) | mobile `schedulesApi.listSchedules` | keep |
| `POST /api/v0/mobile/household/{hh}/schedules/{id}/cancel` | same | mobile `cancelSchedule` | keep |
| `POST /api/v0/device-control-results/{request_id}` | **none** (`app/api/smart_home.py:1200`) | node `_post_tool_call_result` (`node-setup/scripts/mqtt_tts_listener.py:991`) | owned by doc 05/07; it carries routine results |

### MQTT

| Topic | Direction | Payload |
|---|---|---|
| `jarvis/nodes/{node_id}/routines/sync` | CC → node | `{"event":"routines_changed","household_id":…}`. A nudge only; it carries no routine data (`app/api/routines.py:237-240`). The node reacts by GET-pulling (`node-setup/scripts/mqtt_tts_listener.py:2411`, `:1787`). |
| `jarvis/nodes/{node_id}/commands` | CC → node | `[{"command":"routine","details":{routine_name:<slug>, reply_request_id, tool_call_id, trusted:true, voice_command:"routine: <slug>", request_id}}]` (`app/api/routines.py:537-548`, `app/services/node_command_service.py:64-68`). The node dispatches it to `handle_routine` (`node-setup/scripts/mqtt_tts_listener.py:1388`, `:926`). |

### Background loops (in `app/main.py`)

| Loop | Cadence | Gate |
|---|---|---|
| `_periodic_routine_scheduler` (`main.py:531-553`) | Sleeps `max(10, routines.scheduler_interval_seconds)` (default 30) **before** each tick | `routines.scheduler_enabled` (**default False**, `app/services/settings_definitions.py:675-680`) |
| `_periodic_routine_execution_cleanup` (`main.py:556-575`) | Every 86400 s; the first run is 24 h after boot | none |
| `fire_due_schedules`, inside `_periodic_errand_resume` (`main.py:203-224`) | Every 20 s, after `resume_waiting_errands` and `resume_due_timer_workflows` | none (always on) |
| `_periodic_attention_journal` (`main.py:578-610`) | 60 s | Owned by doc 10, but it **reuses `routine_scheduler.is_due`** with an in-memory `last_fired` dict |

### Server tools and internal callers

- `schedule_errand` creates `schedules` rows (`schedule_errand_tool.py:271-279`). `list_scheduled_errands` posts the list card (`app/core/tools/list_scheduled_errands_tool.py:60-77`). Both are doc 09 tools, but they are the only producers and readers of `schedules`.
- The server callback `("schedule","cancel_schedule")` is registered at startup (`schedule_service.py:361-367`, `main.py:493-496`) for the Cancel buttons on the list card.
- `execute_routine_on_node` is also named as the executor in `errand_planner.py:5`'s docstring. That is stale: errands use `errand_executor.run_step`, not routines (`errand_service.py:13-16`).

---

## 3. Behaviour

### 3.1 Routine CRUD and sync (`app/api/routines.py`)

- **Create** (`:265-293`):
  1. Name required, else 422.
  2. `response_length ∈ {short, medium, long}`, else 422.
  3. Slug = `[^a-z0-9]+ → _`, lowercased and stripped, fallback `"routine"`. A per-household collision gets `_2`, `_3` and so on (`:105-121`).
  4. `steps` is stored in the **mobile-native** shape (`args: [{key,value}]`).
  5. `schedule` is validated (`:206-212`). `target_node_id` defaults to the setting `smart_home.primary_node_id` **at save time** (`:184-195`).
  6. Commit, then nudge.
- **Slug is immutable.** A rename keeps the slug (`:330-331`), because the slug is the node's data key.
- **PATCH** (`:318-349`) uses `exclude_unset`. `schedule: null` clears the schedule. Any provided `schedule` is re-serialized from the request body, so a `last_fired_at` the client omits becomes `null`. The mobile editor always sends the whole body, including a schedule rebuilt without `last_fired_at` (`node-mobile/src/screens/Routines/RoutineEditScreen.tsx:372-379,405-412`). So **every save resets the schedule's firing history**, even a rename (see §8).
- **Delete:** hard delete. `routine_executions` cascades through its FK (`app/models.py:849`).
- **Nudge** (`:224-243`): publish `routines/sync` to every `is_active` node in the household. MQTT failure is logged and never fails the request. When MQTT is unavailable the nudge is skipped entirely, and nodes catch up on their next boot only.
- **Node pull** (`:369-390`):
  - Returns `{"routines": {slug: {trigger_phrases, steps:[{command, args:{k:v}, label}], response_instruction, response_length}}}`, **enabled routines only**.
  - Args are flattened by `_flatten_args` (`:124-145`): a string value starting with `[` or `{` is `json.loads`-ed, falling back to the raw string. Everything else stays a string, and the node coerces it.
  - A node with no household gets `{"routines": {}}`.
  - The node-facing payload does **not** include `schedule`, `name`, `enabled` or `id`.
- **Node apply** (`node-setup/services/routine_sync_service.py:44-99`):
  - Writes each server routine into `CommandDataRepository("routine", slug)`.
  - Prunes DB rows that the server no longer has, unless the slug belongs to a hardcoded default or a Pantry custom file.
  - If CC is unreachable, keeps the local data and does not prune.

### 3.2 Node-side routine store and execution (reference; not ported, but it is the contract)

- **Precedence:** DB > `routines/custom_routines/*/routine.json` (Pantry) > hardcoded defaults (`good_morning`, `good_night`, `morning_briefing`, `nightly_briefing`). See `node-setup/commands/routine_command.py:81-117`, `:463-524`. `_load_routines` also **seeds** defaults and custom files into the DB on every load (`:106-110`).
- **Routine definition schema** (node-native, the union of all sources):
  ```json
  {
    "trigger_phrases": ["good morning", "start my day"],
    "steps": [{"command": "get_weather", "args": {"resolved_datetimes": ["today"]}, "label": "weather"}],
    "response_instruction": "Give a cheerful morning briefing…",
    "response_length": "short|medium|long",
    "type": "routine|briefing"
  }
  ```
  - `type` exists only on node defaults and Pantry files. It sets the default length (`routine_command.py:42-45`).
  - **There are no conditions, no step branching and no inter-step data flow in routines.** `$`-directives (§3.6) are errand-only.
- **Execution** (`routine_command.py:307-384`):
  1. Steps run **sequentially and are error-resilient**.
  2. `resolved_datetimes` keywords (`today`, `tomorrow`, `yesterday`, `day_after_tomorrow`) resolve against the **node's local clock** (`:446-457`).
  3. A missing command becomes a recorded error and the run continues.
  4. If every step fails, the result is `error_response("All routine steps failed.")` with `passed:0`.
  5. Otherwise the results are composed through CC `POST /api/v0/chat` (`chat_text`) with a `/no_think` prompt (`:386-421`). If that fails, the fallback concatenates each step's `message`.
  6. The run returns `{message, passed, failed}`.
- **MQTT run** (`handle_routine`, `node-setup/scripts/mqtt_tts_listener.py:926-977`):
  - Builds a `RequestInformation` with `user_id = details.get("user_id")`. **CC never sends `user_id`** (`app/api/routines.py:537-543`), so run-now and scheduled runs execute with no user.
  - POSTs `{"output": {...context_data, success, error?}}` to `/device-control-results/{reply_request_id}`.
  - **Nothing is spoken**: the composed message goes only to CC (see Q2).

### 3.3 Run-now and scheduled execution (`execute_routine_on_node`, `app/api/routines.py:512-575`)

1. `request_id = uuid4`. Publish the `routine` command through `node_command_service.publish_command_with_id`, which records the pending command with a 5-minute expiry (`node_command_service.py:49-59`).
   - On a publish exception: record `failed`, return `{success:false, status:"failed", …, error}`.
   - When the MQTT client is `None`, `_publish` returns silently (`node_command_service.py:72-74`), so the run falls through to a **timeout**.
2. Poll `$TMP/jarvis-device-control/{request_id}.json` every 100 ms for **20 s** (`:401-420`). Read it and unlink it. A malformed file is retried until the deadline.
3. On timeout: record `timeout`, return `status:"timeout"`. A result POSTed later is written to the directory and **never cleaned up**.
4. Otherwise read `output.passed/failed/success/message`:
   - `status = failed` if `!success`
   - else `partial` if `failed > 0`
   - else `success`
5. Record a `routine_executions` row (`:423-454`). An audit failure is swallowed.
6. If `notify_on_complete`, post ONE inbox card with push:
   - title `"{icon} '{name}' {headline}"`, category `routine`, metadata `{household_id, routine_id, status}`
   - targets a user if `notify_user_id` is set, otherwise the household
   - the status → icon/headline table is at `:460-465`, and the summary rules at `:483-491`
   - non-fatal by construction

**Run-now** (`:578-594`): `node_id = body.node_id ?? primary node`. If neither exists → 400. It awaits the whole run, so the HTTP request blocks for up to about 20 s. It does **not** notify. It does not check `routine.enabled`; the node only holds enabled routines, so a run-now of a disabled routine comes back as `failed` "Unknown routine".

**Run-background** (`:597-668`) is cut. It is the only producer of `trigger="background"` and of `notify_user_id`.

### 3.4 Routine scheduler (`app/services/routine_scheduler.py`)

The tick runs `run_due_routines(db)` (`:99-167`):

1. Load every `enabled` routine with a non-null schedule. Skip unparseable JSON, and skip `schedule.enabled == false`.
2. `last = schedule.last_fired_at`, parsed by `fromisoformat` and coerced to aware UTC.
3. **Interval with no `last`:** write `last_fired_at = now` as a baseline and do **not** fire (`:127-129`). The first fire is one interval after the scheduler first sees the routine, not after it was saved.
4. `is_due` (`:56-89`):
   - **interval:** `now − last ≥ interval_seconds`
   - **cron:**
     - Evaluate in `ZoneInfo(schedule.timezone)`. An unknown or empty zone silently becomes UTC (`:30-36`).
     - Base = `last` in local time, or `now_local − 1 min` when there is no `last`.
     - Due iff `croniter(cron, base).get_next() ≤ now_local`.
     - A missing croniter or a bad cron → not due, with a warning on every tick.
5. Due → check the target node: it must have the same household, `is_active`, and be `is_online()` (`last_seen` within 15 min; `app/models.py:16,57-62`).
   - Offline or missing → post a `failed` card ("the target node was offline") to the **household**.
   - Then mark it fired anyway: "respect cadence, don't pile up" (`:146-158`).
6. Otherwise `await execute_routine_on_node(…, "scheduled", notify_on_complete=True)`, then `_mark_fired(now)` (`:160-165`). `_mark_fired` writes the in-memory `schedule` dict snapshot plus `last_fired_at` back into `routine.schedule` (`:92-96`).

**Semantics a port must decide on** (current behaviour first):

| Concern | Today |
|---|---|
| Missed runs across a restart or outage | **Fire once, late, with no staleness cap.** For cron, the next occurrence after `last` is ≤ now, so it fires on the first tick, even 23 h late (an 8am "good morning" at 7pm). Then `last = now`, so later occurrences are not replayed. An interval fires once and re-anchors to the fire time. |
| First cron fire | Only if a tick lands within 60 s after the occurrence (base = now − 1 min). With `scheduler_interval_seconds > 60`, a never-fired cron routine can **miss its occurrences** while `last` stays null. |
| Drift | Interval cadence is anchored to the *fire* time, which is tick time plus up to 20 s of execution, so it drifts by up to one tick each period. Cron does not drift. |
| Overlap | One coroutine, sequential. Due routines run serially, up to 20 s each, so a tick can take N×20 s and delay later routines. A routine cannot overlap itself through the scheduler. A run-now can overlap a scheduled run on the node, which handles both. |
| Crash mid-run | `_mark_fired` runs after execution, so a crash during the 20 s wait re-fires on restart (**at-least-once**). |
| Multi-process | None. A single uvicorn worker (`Dockerfile:16`), and no claim. |
| DST | Delegated to croniter with an aware local datetime, and untested. In the spring-forward gap and the fall-back repeat, croniter's behaviour defines whether a 02:30 job is skipped, shifted or doubled. |
| Timezone source | Mobile sends the **phone's** IANA zone on every save (`RoutineEditScreen.tsx:63-66,374`), not the node's or the household's. |

### 3.5 Schedules (errand time triggers; `app/services/schedule_service.py`)

- **Create** (`:94-120`): only from `schedule_errand`. The row holds:
  - `intent`: the user's goal text
  - `next_fire_at`: naive UTC
  - `timezone`: the node's IANA zone (`schedule_errand_tool.py:238-242`)
  - `recurrence`: JSON, or NULL for a one-shot
  - `node_id`, and `user_id` (the speaker)
  - `state = "active"`

  `title` is never set. The tool creates the row in a **fire-and-forget task after acking** (`schedule_errand_tool.py:273-281`), so a failed insert is invisible to the user.
- **Recurrence spec:** `{"type":"interval","interval_seconds":N}` | `{"type":"cron","cron":"m h dom mon dow"}`.
  - The tool only ever emits these forms (`schedule_errand_tool.py:118-147`): every N min/hours, hourly, `m h * * 1-5`, `m h * * D`, `m h DAY * *`, `m h * * *`. The time is taken from the first fire, in node-local time.
  - The schedule's `timezone` is stored separately; it is not inside the spec.
- **Sweep** `fire_due_schedules` (`:420-462`), every 20 s:
  1. Snapshot the rows with `state='active' AND next_fire_at ≤ utcnow()`.
  2. For each row:
     - **recurring:** `next = compute_next_fire(recurrence, now, tz)`, which is *strictly after now*. Claim with a conditional `UPDATE … SET next_fire_at=next, last_fired_at=now WHERE id=? AND state='active' AND next_fire_at ≤ now` (`:391-417`).
     - **one-shot**, or a recurrence that fails to parse: claim `active → done` (`:370-388`).
     - A lost claim → skip.
  3. Then `draft_errand_plan_detached(household, node, intent, user_id)` (doc 09). It plans with the LLM and posts a plan card; on failure it posts its own failure card.

  The claim happens **before** drafting, so delivery is **at-most-once** per occurrence.
- **Missed runs:** collapse to one late fire. The recurring re-arm is computed from `now`, so skipped occurrences are dropped. A one-shot fires whenever CC comes back, however late. That is low risk, because the user still has to approve the plan.
- **Unparseable recurrence:** a recurring row whose spec stops parsing degrades to a one-shot (`next_fire=None`, so `_claim_one_shot`) and becomes `done` after one fire.
- **List / cancel:**
  - `list_schedules` returns `active`/`paused` ordered by `next_fire_at` (`:123-150`).
  - `cancel_schedule` is a household-scoped conditional update to `cancelled`, and is idempotent (`:153-174`).
  - The management card holds at most 8 Cancel buttons, each `target:"server"`, `command:"schedule"`, `callback:"cancel_schedule"`, `data:{schedule_id}` (`:290-309`). A tap cancels, then **posts a fresh list card**, then returns a result inbox (`:338-358`).
- **Display strings** (`schedule_view`, `:258-269`): `is_recurring`, `cadence`, `next_local`, `description`. The vocabulary lives on the server so mobile and voice read cadences the same way (`_describe_recurrence`, `:196-229`). The mobile renders these strings as-is, so they are **contract** (golden-test them).

### 3.6 The two misfiled modules

- **`time_window.check_time(envelope, utterance)`** (`app/services/time_window.py:278-314`): pure function, called by `POST /internal/phone/sessions/{id}/check-time` (`app/api/phone_sessions.py:174-202`).
  - Parses `Acceptable times:` and `Do not book:` lines into per-weekday minute intervals. The end is exclusive, and a start with no am/pm inherits the end's (`:185-216`).
  - Finds a weekday plus a time in the utterance. Both are required. Noon and midnight are handled; a bare hour gives both am and pm candidates (`:106-123,143-174`).
  - **Order matters:** if any candidate lands in a blocked window → `available=false`, even with no acceptable windows. Otherwise, with no acceptable windows → `None` (the model decides). Otherwise, any candidate open → `true`.
  - Output: `{time_detected, available, proposed_label, acceptable_summary}`.
  - Belongs in doc 11. It has an excellent golden-fixture target in `tests/test_time_window.py`.
- **`step_value_resolver.resolve_step_args(args, prior_results, now)`** (`app/services/step_value_resolver.py:104-125`): called once per errand step (`app/services/errand_executor.py:294-301`).
  - A `$`-directive is a single-key dict whose key starts with `$`.
  - `$from_step{step, field}` → `prior[step].data[field]`, else the top-level field.
  - `$leave_by{event_start, drive_from_step, drive_field="duration_minutes", buffer_minutes=5}` → `round((start − drive − buffer − now)/60)` in minutes. It skips if the result is ≤ 0, if the drive step failed, or if the field is non-numeric. A naive `event_start` is treated as UTC (`:51-60`).
  - An unknown `$key` → skip. The first skip aborts resolution of the remaining args, and the executor records the step as `success:true, data:{skipped:reason}`.
  - Belongs in doc 09.

---

## 4. Data

| Table (Go: `cc_…`) | Columns that matter | Lifecycle |
|---|---|---|
| `routines` (`app/models.py:806-840`; migration `alembic/versions/d9c8b7a6e5f4_add_routines.py`) | `id` uuid; `household_id`; `slug` (unique per household, immutable); `name`; `trigger_phrases` TEXT JSON array; `steps` TEXT JSON `[{command, args:[{key,value}], label}]`; `response_instruction`; `response_length`; `schedule` TEXT JSON or NULL; `enabled`; `created_at`/`updated_at` (naive UTC) | Hard delete |
| `routine_executions` (`app/models.py:843-859`) | `routine_id` FK CASCADE; `household_id`; `node_id`; `trigger` ∈ {`run_now`, `scheduled`, `background`}; `voice` is in the comment but is **never written**; `status` ∈ {`success`, `partial`, `failed`, `timeout`}; `passed`, `failed`; `error`; `started_at`, `finished_at` | Deleted where `started_at < now − routines.execution_ttl_days` (default 7), daily. **No route reads this table.** It is write-only audit. |
| `schedules` (`app/models.py:1154-1182`; `alembic/versions/j2f3sched006_add_schedules.py`) | `id` = `sch_<hex32>`; `household_id`; `user_id`; `node_id`; `intent`; `title` (unused); `timezone`; `next_fire_at` (naive UTC, indexed); `recurrence` TEXT JSON or NULL; `state` ∈ {`active`, `done`, `cancelled`, `paused`}, and **nothing sets `paused`**; `last_fired_at` | Never deleted. Done and cancelled rows accumulate. |

**Routine `schedule` JSON** (the mobile contract; `app/api/routines.py:68-77`, `node-mobile/src/types/Routine.ts:30-38`):

```json
{
  "type": "cron|interval",
  "cron": "0 8 * * 1,2,3,4,5",
  "interval_seconds": null,
  "timezone": "America/New_York",
  "target_node_id": "node-abc",
  "enabled": true,
  "last_fired_at": "2026-10-06T12:00:00.123456+00:00"
}
```

- Mobile produces cron strings only as `M H * * <*|d,d,…>` (`RoutineEditScreen.tsx:77-86`). The server accepts any string, and an invalid cron is caught only at evaluation time (silently never due).
- `last_fired_at` is server-written. Its format comes from `datetime.isoformat()` on an aware UTC value.
- **In-memory state:**
  - `node_command_service._pending_commands` (5-minute TTL)
  - the attention journal's `last_fired` dict
- **On disk:** `$TMP/jarvis-device-control/*.json` result files.

---

## 5. Settings

| Key | Default | Effect |
|---|---|---|
| `routines.scheduler_enabled` | **False** (`settings_definitions.py:675-680`) | When off, routine schedules **never fire**. The mobile UI still lets users set them. Truthy strings are accepted (`main.py:539-541`). |
| `routines.scheduler_interval_seconds` | 30 | Tick period, floored at 10. Above 60 it breaks the first-fire window (§3.4). |
| `routines.execution_ttl_days` | 7 | Audit retention. |
| `smart_home.primary_node_id` (household) | — | Default target for run-now and scheduled runs. Resolved at **save** time for schedules and at request time for run-now. |
| `attention.journal_card_cron` / `attention.timezone` | `0 21 * * *` / UTC | Doc 10's consumer of `is_due`. |

Schedules have no settings, and their sweep is always on.

---

## 6. Dependencies

**Other CC subsystems:**

- **nodes (doc 05):**
  - the `Node` row, `is_active`, and `is_online()` with its 15-minute `last_seen` threshold
  - `node_command_service` for publishing commands
  - the MQTT client for the nudge
  - the `/device-control-results` reply path
- **settings framework (doc 00):** `routines.*` and `smart_home.primary_node_id`.
- **inbox (doc 13):** `post_inbox_item_sync` for completion and list cards with push. The server-callback registry dispatches the Cancel taps.
- **errands (doc 09):** `draft_errand_plan_detached`, which is the schedule's whole payload. The `schedule_errand` and `list_scheduled_errands` tools are the only producers and readers of `schedules`.
- **phone (doc 11):** consumes `time_window`.
- **attention (doc 10):** borrows `is_due`.

**Other services:**

- **jarvis-auth:** JWT and household membership through `verify_household_role`, and node key validation.
- **notifications:** push, via the inbox.
- **There are no direct LLM calls in this subsystem.** Routine composition happens on the node through `POST /api/v0/chat` (doc 02, the live slot). Schedule firing reaches the LLM only through the errand planner (doc 09).

**Libraries and third parties:**

- `croniter` (optional import; when it is missing, cron routines and schedules are silently disabled)
- `zoneinfo`
- **Pantry** (the cloud service, out of scope), only through node-installed `routine.json` files and the unused generate API

---

## 7. Invariants and non-obvious behaviour

1. Routine JSON goes **only** over the node's authenticated HTTP pull, never over MQTT (`app/api/routines.py:10-13`). The MQTT payload is `{"event":"routines_changed","household_id":…}`.
2. The pull is **slug-keyed** and **enabled-only**. Args are flattened, with JSON-looking strings decoded (`:124-145`). Mobile round-trips args as `[{key, value: string}]`.
3. The slug is assigned once, de-duplicated with `_N`, and never changes on rename.
4. Every mutation nudges every **active** household node. MQTT errors never fail the request.
5. A scheduled run that can't happen (offline node) **still posts a card and advances** `last_fired_at` (`routine_scheduler.py:146-158`). Every detached terminal state posts exactly one card ("never vanish").
6. Run-now returns `{success, status, message, passed, failed}`, with `error` only on a publish failure. The status is derived from `output.success` and `output.failed` (`app/api/routines.py:565-572`).
7. An interval routine's first sighting writes a baseline and does not fire.
8. Schedules: the conditional UPDATE claim is the serialization point. A recurring schedule stays `active` and re-arms strictly after `now`. A one-shot goes `done` **before** planning.
9. A schedule never executes anything. It only re-plans and posts an approval card.
10. The cadence and next-fire display strings (`schedule_view`) are rendered verbatim by mobile and voice. `_local_when` strips `:00` with `.replace(":00","")`, which also turns `10:00` into `10`. Preserve this byte-for-byte or change it deliberately.
11. The household scope of cancel is enforced in the UPDATE's WHERE clause, not by a prior read.

---

## 8. Oddities

1. **Scheduler off by default.** Users can create scheduled routines that never fire, with no UI hint (Q1).
2. **Scheduled and run-now routines are silent on the node.** `handle_routine` returns the composed briefing to CC, which only posts an inbox card. A scheduled "good morning" never speaks (Q2).
3. **20 s result timeout vs LLM composition.** A `medium` briefing (weather, calendar, news, then `/api/v0/chat`) can exceed 20 s. CC then records `timeout` and posts "didn't finish" while the node actually succeeds. The late result file leaks in `$TMP` forever.
4. **Every mobile save wipes `last_fired_at`.** The editor always resends the schedule. Interval routines re-baseline, and cron routines fall back to the 60 s window. Saving in the minute after a cron fire can double-fire it.
5. **Lost-update race:** `_mark_fired` writes back the schedule dict it read before a run of up to 20 s, which clobbers any PATCH made in between (`routine_scheduler.py:92-96,117,164`).
6. **First cron fire is missed** when the tick interval exceeds 60 s (§3.4).
7. **No user context on node runs.** CC doesn't send `user_id`, so per-user commands (calendar, email) run as nobody. Run-now has `auth.user_id` available but drops it.
8. **Shadowing is permanent.** A server routine whose slug equals a node default (e.g. a user-created "Good Morning" becomes `good_morning`) overwrites the node's DB row. When it is deleted on the server, the prune skips it as "protected" (`routine_sync_service.py:29-36,92`), so the user's version **survives deletion** on every node. Disabling it behaves the same way.
9. **Two invisible routine populations:** the node defaults and Pantry `custom_routines`. Nodes seed them into the local DB, but CC never knows about them. They are absent from the mobile list and cannot be run-now'd or scheduled. Pantry's `/v1/routines/generate` exists (`pantry/app/api/routines.py:40`), and mobile has client functions for it (`node-mobile/src/api/pantryApi.ts:118-136`), but **no screen calls them**. `GeneratedRoutine.background: null` refers to a removed field.
10. **The stale-copy problem:** run-now and scheduled runs execute the node's **local** copy by slug. If the nudge was missed (MQTT down, node offline), the node runs an old definition or reports "Unknown routine". There is no version check.
11. **Two cron engines.** `routine_scheduler` uses a `last_fired_at`-based "is due", and `schedule_service` uses a `next_fire_at` claim. They share the vocabulary but differ in semantics: first-fire, catch-up, claim/at-least-once vs at-most-once. The attention journal adds a third use, with in-memory state.
12. `routine_executions.trigger="voice"` is documented but never produced. Voice routines are invisible to CC except through their `/api/v0/chat` composition call. No route reads the table.
13. `schedules.paused` and `schedules.title` are never used, and nothing calls `list_schedules(include_done=True)`. `mobile_schedules.py:39-40` claims the list "degrades to an empty list on a DB hiccup"; there is no try/except, so it 500s.
14. A recurring schedule whose spec becomes unparseable silently turns into a one-shot (§3.5).
15. `_describe_recurrence` reads a multi-day `dow` (`1,3,5`) as "every day". Only schedule-tool output reaches it today, and that never emits multi-day.
16. Node trigger matching is very loose (`routine_command.py:264-301`): a reverse substring with `len ≥ 3`, so "day" matches "start my day", plus an 80% token overlap. It is not ported (it runs on the node), but it explains odd voice routing.
17. `/device-control-results/{id}` is unauthenticated and writes arbitrary JSON to a temp file named by a client-supplied id. That id is a path component; check it for traversal (doc 05/07).
18. CC's CLAUDE.md routes (`/mobile/routines`, `routine_builder.py`) don't exist.

---

## 9. Tests

| File | Covers |
|---|---|
| `tests/test_routines_api.py` (207 lines) | create/slugify/dedupe, 422 on length, list/get, PATCH clears schedule, delete, nudge fan-out to active nodes, pull flattening (array round-trip), enabled + household scoping, node mismatch 403 |
| `tests/test_routine_completion_notify.py` | the card table per status, user vs household target, non-fatal notify, `notify_on_complete` flag |
| `tests/test_routine_background_dispatch.py` | the cut route; drop it |
| `tests/test_schedule_service.py` (267 lines) | one-shot fire/claim idempotency, failed draft, `compute_next_fire` interval/cron/junk, recurring re-arm and claim, list order and scope, cancel idempotency and scope, `_describe_recurrence` phrases, card metadata and button cap, cancel callback re-post |
| `tests/test_mobile_schedules.py` | enriched list, live-only, cancel returns fresh list, cross-household no-op, 401 |
| `tests/test_schedule_errand_tool.py`, `tests/test_list_scheduled_errands_tool.py` | doc 09 tools that feed `schedules` |
| `tests/test_time_window.py` (177 lines), `tests/test_step_value_resolver.py` | pure functions → **table-driven golden fixtures** |
| node-setup `tests/test_routine_sync.py`, `tests/test_routine_command.py` | the node contract: pull, prune and protect |

**Gap:** `routine_scheduler.py` (`is_due`, `run_due_routines`) has **no tests at all**.

Golden and contract candidates:

1. The `_routine_to_mobile` and `_routine_to_node` shapes, including `_flatten_args` edge cases (`"[bad"`, `""`, `{}`), the slug algorithm, and the 422 detail strings.
2. A run-now black-box test with a fake node that answers on MQTT: success, partial, failed, no answer (timeout) and publish failure. Assert the status mapping and the card titles.
3. `is_due` and `compute_next_fire` tables across DST transitions in `America/New_York` and `Europe/London`, generated from croniter so that Go matches or deliberately differs.
4. `schedule_view` strings for every recurrence the tool can emit, and `_local_when` formatting.
5. `check_time` and `resolve_step_args` tables.

---

## 10. Questions for the user

1. **[behaviour] Should a scheduled routine *speak* on the target node?** Today a scheduled or run-now routine runs on the node, but its composed briefing is only sent back to CC as an inbox card and push (`node-setup/scripts/mqtt_tts_listener.py:926-977`). A 7am "good morning" fires the lights and posts a card, but says nothing.
   - *Why it matters:* this decides whether the Go port needs a "speak this on node X" path for routines, and what "success" means.
   - Options:
     - (a) the card only, as today
     - (b) speak on the target node and also post the card
     - (c) a per-routine `announce: bool` flag
   - **Recommendation: (c), defaulting to true for scheduled runs and false for run-now.** The phone already shows the run-now result inline.
2. **[scope] Is `routines.scheduler_enabled` still meant to default to off?** What is it set to in prod?
   - *Why it matters:* with the default, the mobile schedule picker is a no-op. Users get silent non-firing, unless prod has flipped the setting.
   - Options:
     - (a) keep the gate, default off
     - (b) default on
     - (c) remove the setting; a routine with a schedule is the opt-in
   - **Recommendation: (c).** The per-routine `schedule.enabled` already exists.
3. **[behaviour] What should missed scheduled occurrences do after downtime?** Today a routine fires **once, however late**: an 8am briefing replays at 7pm after an outage. Errand schedules also fire once, late, and drop the rest.
   - Options:
     - (a) fire once, late, as today
     - (b) skip if later than a grace window (e.g. 15 min or 10% of the period), and post a "skipped while offline" card
     - (c) replay every missed occurrence
   - **Recommendation: (b) for routines,** which act autonomously, with a 15-minute default grace. Keep (a) for errand schedules, since the user approves them anyway.
4. **[scope] Should routines and errand schedules share one scheduler engine?**
   - Today there are two loops and two semantics (§8.11). A third consumer, the attention journal, borrows `is_due`. Doc 09 workflows add `wake_at` timers.
   - Options:
     - (a) port both as they are
     - (b) one Go `scheduler` module: a `next_fire_at`-based durable trigger table with claims and enqueue into the embedded job queue. Routines, errand schedules, the journal and workflow wakeups are trigger *kinds*.
   - **Recommendation: (b).** Keep the routine `schedule` JSON (including `last_fired_at`) as a projection for the mobile contract.
5. **[behaviour] Where should routines execute in the Go world: still on the node, or on the server?**
   - Today CC only relays to the node. The node composes through CC `/api/v0/chat` anyway, and a run silently depends on the node's possibly stale local copy (§8.10).
   - Options:
     - (a) keep node execution and fix staleness: include `updated_at`/hash in the command so the node re-pulls on a mismatch
     - (b) CC sends the full definition in the `routine` MQTT command (still node-executed, never stale)
     - (c) CC executes server-side, dispatching node-plane steps like the errand executor
   - **Recommendation: (b).** It needs a node change, but a tiny one. Otherwise (a) needs no node change at all.
6. **[behaviour] Which timezone should a routine schedule use?**
   - The mobile editor stamps the **phone's** zone on every save. A user travelling, or a family member in another zone, silently shifts a household's 7am routine.
   - Options:
     - (a) the phone's zone, as today
     - (b) the target node's zone
     - (c) a household timezone setting
   - **Recommendation: (b), falling back to (c).** Schedules for errands already use the node's zone (`schedule_errand_tool.py:238`). Also decide whether an unknown zone falls back to UTC silently (today) or is rejected with a 422. I recommend 422.
7. **[behaviour] What should the run-now / scheduled result timeout be?**
   - 20 s regularly loses to the LLM composition of a medium briefing, producing false "didn't finish" cards (§8.3).
   - Options:
     - (a) keep 20 s
     - (b) about 90 s, with run-now returning `202 {execution_id}` while mobile polls
     - (c) a longer synchronous wait of about 60 s
   - **Recommendation: (c) for now.** Mobile expects a synchronous result. In Go the wait is an in-process MQTT reply channel, so nothing leaks after a timeout.
8. **[behaviour] Should run-now and scheduled routines run as a user?**
   - CC never passes `user_id`, so calendar and email steps run with no user (§8.7).
   - Options:
     - (a) none, as today
     - (b) run-now passes the caller's user id; scheduled runs pass the routine's creator
     - (c) add an explicit per-routine "run as" field
   - **Recommendation: (b).** It needs a `created_by_user_id` column, which the port can add.
9. **[behaviour] Pantry and default routines live only on nodes. Should CC own them?**
   - They are invisible to mobile, can't be run-now'd or scheduled, and a same-slug user routine permanently shadows the default even after deletion (§8.8–9). Pantry's `/v1/routines/generate` client code is unused.
   - Options:
     - (a) leave it as is
     - (b) seed the defaults as real CC rows per household, and have the node report installed custom routines up to CC
     - (c) drop the node defaults and the Pantry routine component
   - **Recommendation: (b) for the defaults,** which also fixes the shadowing. Ask separately whether the Pantry `routine` package type is still wanted. If it isn't, the dead `generateRoutines` client goes too.
10. **[behaviour] Overlap: if a routine is still running (or awaiting its result) when its next occurrence, or a run-now, arrives, what should happen?**
    - Options:
      - (a) allow concurrent runs, as today
      - (b) skip and note it on the card
      - (c) queue it
    - **Recommendation: (b),** with a per-routine single-flight lock in the job queue (dedup key `routine:{id}`).
11. **[minor] Should `routine_executions` stay write-only?**
    - Nothing reads it.
    - Options:
      - (a) drop it
      - (b) keep it and expose `GET …/routines/{id}/executions` for a "last run" line in mobile
    - **Recommendation: (b),** if a "last ran at / status" line is wanted. Otherwise (a).
12. **[minor] Schedules housekeeping.**
    - `paused` and `title` are unused, and `done`/`cancelled` rows are never purged.
    - Options:
      - (a) port as is
      - (b) drop `paused`/`title` and add a TTL purge of terminal rows (e.g. 30 days)
    - **Recommendation: (b).** Also include active `schedules` in `import-legacy`; PLAN §5 lists only routines.

---

## 11. Go port notes

**Shape.** Package `cc/routines`:

- `store` (sqlc on `cc_routines`, `cc_routine_executions`, `cc_schedules`)
- `api` (the 7 kept routine routes and 2 schedule routes)
- `sync` (the nudge publisher)
- `runner` (`RunOnNode(ctx, routine, nodeID, trigger, notify)`)
- a shared `scheduler` (Q4)

`timewindow` moves to `cc/phone`, and `stepvalues` moves to `cc/errands`. Both are pure functions, so they are easy table-test ports.

**Execution path.** Replace publish-then-poll-a-temp-file with the doc 05 embedded-broker request/response:

1. Register a reply channel keyed by `request_id`.
2. Publish on `jarvis/nodes/{id}/commands`.
3. Have the `/device-control-results/{id}` handler deliver into the channel. The route stays for nodes, but there is no filesystem.

A timeout cancels the waiter, and a late reply is dropped and logged. Keep the details payload byte-compatible: `routine_name`, `reply_request_id`, `tool_call_id`, `trusted`, `voice_command`, `request_id`. Add `user_id` only if Q8 says so; the node already reads it.

**Scheduler.**

- Store an explicit `next_fire_at` per trigger in a `cc_triggers` table (or a column on `cc_routines`). Compute it on save and on fire.
- Claim with a conditional UPDATE, as `schedule_service` does, and enqueue a job with dedup key `(kind, id, occurrence)` into the embedded queue. Durable delivery comes from the queue, and the claim gives exactly-once enqueue.
- Keep writing `schedule.last_fired_at` inside the routine JSON, because mobile reads it.
- The grace window for missed runs is per Q3.
- Mobile resaving a schedule must **not** reset `next_fire_at` unless `type`, `cron`, `interval_seconds` or `timezone` actually changed. This fixes §8.4.

**Cron and timezones.**

- Use `robfig/cron/v3`'s standard 5-field parser with `time.LoadLocation`. Croniter accepts some extensions (e.g. `@daily`, `L`, `#`). Mobile and the tool never emit them, but stored data might, so validate on save (422) and run the import through the parser.
- **Windows has no system tzdata.** Import `time/tzdata` (about 450 KB) into the binary.
- Define DST explicitly and test it with the fixtures in §9.3. Recommended:
  - a spring-forward time that doesn't exist fires at the first valid instant after it
  - a fall-back repeated time fires once, on its first occurrence

  Verify what robfig does and match it in a wrapper.

**Data.**

- Routines keep TEXT JSON columns, per the convention. The node pull flattens at the boundary.
- Timestamps: today they are naive UTC everywhere. Store RFC3339 UTC text in SQLite, and keep the `isoformat()` style (`+00:00`, microseconds) for `last_fired_at`, since mobile parses it.
- `import-legacy`: routines are covered. Add `schedules` with `state IN ('active','paused')`, and skip `routine_executions`.

**Cut.**

- `POST /routines/run-background`, `_run_routine_background_task`, `trigger="background"`, and `tests/test_routine_background_dispatch.py`.
- The `_RESULT_DIR` file plumbing.
- The croniter-missing branches.

**Risks.**

- Schedule display strings are a mobile/voice contract with quirks such as the `:00` stripping. Port them byte-exact with golden tests.
- Behaviour changes that answers to Q1, Q3, Q6 and Q7 introduce are user-visible. Land them deliberately, not as port accidents.
- `routines/sync` nudges come from the embedded broker. Nodes connect with CC-issued credentials (doc 05), so the nudge works only after the broker cutover. During the strangler phase, the node must still be subscribed on whichever broker CC publishes to.
