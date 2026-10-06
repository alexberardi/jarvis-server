# 09 — Errands and workflows

Source: `jarvis-command-center` at `3b32b73`. Paths are relative to `app/` unless they start with `tests/`, `alembic/` or `../`. The `prds/errand-runner.md` PRD that the code cites everywhere (§2–§3, §2.4–§2.6) **is not in the repo** (`ls prds/`), so the code and its commit history are the only spec.

Cross-references: doc 08 (the `schedules` table and the schedule sweep, which re-plans an errand on a clock), doc 10 (signals and proposals; its leave-by reaction once ran *through* this engine and now doesn't), doc 11 (phone calls: the only deferred step that's actually used), doc 13 (inbox cards and `POST /callbacks`, the transport for every tap), and doc 05 (`dispatch_node_command` over MQTT).

---

## 1. Purpose

**What the user sees.** You say *"Run an errand: check the weather and remind me to buy milk tomorrow"* or *"Call the pharmacy about my refill, then call the doctor's office"*. The node replies "On it, I'll send a plan to your phone". A minute later a **plan card** reaches your phone, listing numbered steps with **Run**, **Revise** (a free-text "tell me what to change" box) and **Cancel**. Tap Run and CC carries out the steps in the background. Nothing is ever spoken back on the node. When it finishes, one **completion card** arrives ("✅ Errand done: …", "⚠️ … finished with issues", and so on).

### Vocabulary: the four things that look alike

| Term | What it is to the user | What it is in code | Who plans it | Where it runs | Example |
|---|---|---|---|---|---|
| **Routine** (doc 08) | A saved, named macro that the user writes and reuses ("good morning") | `routines` rows; the node's `RoutineCommand` executes it | The user (mobile CRUD) | On the **node** | "good night" → lights off, lock door |
| **Errand** | A one-off background task given as a goal in plain language | An `errand_plans` row (the DRAFT) plus a `workflows` row (the RUN) | An **LLM planner**, reviewed on a card | In **CC**, one step at a time, routed to node, server or phone | "check the weather and remind me to buy milk tomorrow" (`core/tools/run_errand_tool.py:42`) |
| **Scheduled errand** | "Tomorrow at 9am, call the dentist to book a cleaning" (`core/tools/schedule_errand_tool.py:3`) | A `schedules` row whose `intent` is re-planned at fire time | The LLM again **at each fire**, and confirmed again each time | Same as an errand | "every weekday at 8, check traffic" |
| **Workflow** | Never visible to the user | The engine (`services/workflow_engine.py`) and the `workflows` table. `kind="errand"` is the **only** kind | n/a | n/a | n/a |

The workflow engine was pulled out of the errand runner as a "general durable engine" (`services/workflow_engine.py:1-36`), with three plug-in seams: a handler registry, signals, and progress. It has exactly one consumer. Every `Workflow` row is an errand run.

### Who drives it

| Driver | What it does |
|---|---|
| A **node**, through the voice tool loop | Calls `run_errand`, `schedule_errand` and `list_scheduled_errands` |
| The **20 s background loop** | Fires schedules, resumes phone and timer waits, times out stuck waits |
| **Mobile** | Card taps via `POST /api/v0/callbacks` (server plane) |
| **jarvis-phone-gateway** | `/internal/phone/*` session updates, which resume an errand waiting on a call |

### How finished and how used it looks (candid)

- **When it was built:** in about two weeks, from 2026-07-28 (`6c4597e`, POC chunk 3) to about 2026-08-12. Over that time it gained the durable engine (`4bc0052`), Revise, pause-and-replan (`8cf1c51`, `e491c1a`), scheduling slices 1–2 and list/cancel.
- **Since then:** nothing errand-specific has landed. The newest commits touching `errand_service.py` and `workflow_engine.py` date from 2026-08-05.
- **Testing:** heavy. About 3.1k lines of tests (§9) cover about 3.9k lines of source. Live fixes such as "Qwen3.5 dropped run_errand", "replan returned empty" and "card invented a joke was told" show it was exercised by hand on dev.
- **Real use is unknown.** No client except voice creates errands. The mobile app only renders generic interactive cards plus the Schedules screen (`../jarvis-node-mobile/src/screens/Schedules/`). No install-e2e or behaviour-corpus case mentions `run_errand`. There is no feature flag; see Q12.
- **Abandoned scaffolding** sits next to the live code: autorun, `$`-directives, `wait_for`, the edit-goal re-plan callback and draft TTL (§8).

---

## 2. Entry points

### Routes

| Method and path | Auth | Live caller (PLAN Appendix A) | Notes |
|---|---|---|---|
| `POST /api/v0/errands` | Node `X-API-Key` (`api/errands.py:41-46`) | **None. Cut.** | Plans a goal into a draft and card, and returns `{errand_plan_id, state, summary, steps}` (`api/errands.py:87-92`). Supports a `node_id` override inside the household (`:63-78`). Confirmed: nothing in node-setup, mobile, web or the packages posts here. The mounting comment at `main.py:826-827` is stale: it still says "transient routine". |
| `POST /api/v0/callbacks` | User JWT | Mobile | The **real** errand control surface. Each `(command="errand", callback=…)` pair (`services/errand_service.py:69-78`) is dispatched by `api/callbacks.py:232-280` → `_run_server_callback_job` (`:289-356`) as a FastAPI background task. Doc 13 owns this route. |
| `/internal/phone/*` session transitions | App-to-app (phone-gateway) | phone-gateway | `api/phone_sessions.py:34-51` schedules `resume_errand_after_call` on a terminal call (`:279` fail paths, `:345` done). Doc 11. |
| `GET/DELETE /mobile/schedules…` | JWT | Mobile Schedules screen | Doc 08 (`api/mobile_schedules.py`). |

### Server tools (voice)

| Tool | File | What it does | Offered |
|---|---|---|---|
| `run_errand(goal)` | `core/tools/run_errand_tool.py` | Fires `draft_errand_plan_detached` as a detached task (`:101-119`) and returns a spoken ack (`:124-130`) | Always. Hard-coded into the text-path allow-list (`core/conversation_handler.py:331-334`); native-tool providers get every registered tool (`:370-371`) |
| `schedule_errand(goal, fire_at, recurrence?)` | `core/tools/schedule_errand_tool.py` | Parses the time in the node's timezone, then `create_schedule` (doc 08) | Always (`conversation_handler.py:335-340`) |
| `list_scheduled_errands()` | `core/tools/list_scheduled_errands_tool.py` | Speaks a count and posts a management card with Cancel buttons (`:60-86`) | Always (`conversation_handler.py:341-345`) |

### Server callbacks (registered at startup, `main.py:490-496`, `services/errand_service.py:1367-1376`)

| `callback` | Card / button | Handler |
|---|---|---|
| `approve_errand_plan` | Plan card **Run** | `_handle_approve_errand_plan` (`:627-707`) |
| `refine_errand_plan` | Plan card **Revise** (carries `instruction`) | `_handle_refine_errand_plan` (`:1173-1231`) |
| `discard_errand_plan` | Plan card **Cancel** | `_handle_discard_errand_plan` (`:892-957`) |
| `replan_errand_plan` | **No card emits it any more** (edit-goal; replaced by Revise in `77c80ad`) | `_handle_replan_errand_plan` (`:1084-1146`) |
| `approve_replan` | Mid-run delta card **Approve** | `_handle_approve_replan` (`:989-1035`) |
| `stop_errand` | Mid-run delta card **Stop** | `_handle_stop_errand` (`:1038-1081`) |

### Background loop

`_periodic_errand_resume` (`main.py:203-226`):

- **Once at boot:** `recover_orphaned_running_workflows()`.
- **Then every 20 s:**
  1. `resume_waiting_errands()`: phone wakeups and the 60-min timeout.
  2. `resume_due_timer_workflows()`
  3. `fire_due_schedules()`: doc 08, which calls back into `draft_errand_plan_detached`.

All three run serially in one try. A raise in step 1 skips steps 2–3 for that tick (`main.py:213-224`).

### Import-time side effect

Importing `errand_service` registers the store factory and the `"errand"` consumer (`services/errand_service.py:1351-1365, 1381`). Importing `workflow_engine` registers the five handlers (`services/workflow_engine.py:930-934`).

---

## 3. Behaviour

### 3.1 Lifecycle overview

```
voice "run an errand…" ─┐
schedule fires (08) ────┼─▶ draft_errand_plan_detached ─▶ create_errand_plan
(POST /errands — cut) ──┘        │                          ├ menu = node cmds ∪ server tools
                                 │                          ├ plan_errand (LLM, background slot)
                                 │                          ├ INSERT errand_plans(state=draft, rev=1)
                                 │                          └ post plan card (inbox + push)
                                 └ on ANY failure → "Couldn't plan that errand" card
plan card ── Revise ─▶ refine (LLM) → rev+1, card updated IN PLACE
          ── Cancel ─▶ draft→cancelled
          ── Run ────▶ INSERT workflows(state=running,cursor=0); plan→launched; run_workflow
                          └ execute_errand loop (fail-fast):
                               node step   → MQTT tool_call, await ≤30s
                               server tool → tool_executor in-process
                               make_phone_call → create_call_plan → SUSPEND (waiting/phone_call)
                               wait_for    → SUSPEND (waiting/timer)        [unreachable from planner]
                               request_replan → SUSPEND (waiting/approval) → replan LLM →
                                     widens? delta card (Approve/Stop) : auto-continue
                          └ finished → LLM-compose summary → ONE completion card → done|partial|failed
```

### 3.2 Planning (`services/errand_planner.py`)

**1. Build the menu: `build_full_errand_menu` (`:190-210`).**

- **Node commands:**
  - On the voice path, they come from the live conversation's `available_commands` (`core/tools/run_errand_tool.py:93-99`).
  - Otherwise `_resolve_node_menu` makes an MQTT round-trip, `_request_tools_from_node(node_id, timeout=10.0)` (`services/errand_service.py:85-105`).
  - They are filtered by `ERRAND_MENU_DENY`: `routine`, `chat`, `answer_question`, `tell_joke`, `act_on_items`, `send_link`, `control_node`, `run_errand`, `request_validation`, `identify_speaker`, `resolve_relative_date` and `get_command_examples` (`:43-61`).
  - `is_risky` comes from the SDK's command definition (`../jarvis-command-sdk/jarvis_command_sdk/command.py:871`).
- **Server tools (`enabled_errand_server_tools`, `:114-152`):**
  - `make_phone_call` **only if** `phone_calls_enabled(hh)`. The voice path differs: it always offers the tool.
  - `deep_research` and `quick_search` if `web_search.enabled`. Fails closed.
  - `remember` and `forget` if there is a speaker and `memory.enabled`. Fails open. `recall` also needs `memory.recall_enabled`.
- **Name collisions:** the server tool wins de-dup (`:201-210`), mirroring the executor's `has_tool` routing.
- **Empty menu:** a static `COMMAND_MENU` of 6 node commands is used (`:25-38`, `:474`). Its `set_reminder` arg names (`text`, `when`) are invented and may not match any real package.

**2. Make one LLM call: `_run_planner` (`:388-437`).**

- Settings: `model="background"`, `temperature=0`, `max_tokens=6000`, **thinking on**. `<think>` is stripped (`:378-385`), then fences and prose are stripped to the outer `{…}` (`:338-349`).
- The prompt (`:306-324`) has these parts:
  - the decomposition rule (`:264-270`)
  - the rare `request_replan` checkpoint rule (`:287-298`)
  - the menu
  - the goal
  - the fabrication guardrail: never invent phone, email or address details (`:251-258`)
  - the output shape `{"summary", "steps":[{command,args,label}]}`
- Failures:
  - An empty response raises `ValueError` and logs `finish_reason` / `reasoning_len` (`:415-432`).
  - Bad JSON raises `ValueError` (`:433-437`).

**3. Validate: `_plan_from_data` (`:440-462`).**

- A step whose command isn't in `menu ∪ CONTROL_COMMANDS{request_replan}` is **dropped**, not run.
- Each step is stamped with `is_risky` from the menu.
- If no steps remain, it raises `ValueError`.
- Steps are stored as `[{command, args, label, is_risky}]` (`:228-234`).

**4. Persist** (`services/errand_service.py:443-503`).

- Inserts `ErrandPlan(state="draft", revision=1)` and commits **before** the card.
- The card is posted best-effort, and its `inbox_item_id` is stored so that later revisions update it in place (`:183-220`, `:489-497`).

**5. Failure on the detached path** (`:527-563`).

- `ValueError` → a "🗒️ Couldn't plan that errand … try rephrasing" card.
- Any other exception → a "hit a snag … try again" card.

  The voice ack already promised a card, so the user always gets one (the "never-vanish" rule).

### 3.3 The plan card contract (mobile-facing; must be byte-compatible)

**Built by:** `build_plan_card_metadata` (`services/errand_service.py:121-180`).

**Envelope:**

- Title: `🗒️ Errand plan: {summary}`.
- Category: `errand_plan`.
- `push=True`.
- `target_type`: `user` if there is a speaker, otherwise `household` (`:210-220`).
- Body: markdown with a numbered list of labels and instructions (`:108-118`).

**Metadata:**

- `household_id` (mobile copies it into `POST /callbacks`), `plan_id`, `revision`, and `steps`.
- `editable_fields`: one required text field, `data_key="instruction"`. The comment warns: **do not set `editor_schema`** (more than 2 disables the buttons).
- `interactive_elements`: Run, Revise and Cancel, all with `command:"errand"` and `target:"server"`.
  - Run and Cancel `data`: `{plan_id, revision}`.
  - Revise `data`: also seeds `"instruction": ""`. Mobile merges the typed text **only** into buttons whose data already has that key (`:132-137`).

### 3.4 Run → execution (`_handle_approve_errand_plan`, `:627-707`; engine)

**Guards, in this order:**

1. `plan_id` is present.
2. The row exists in `ctx.household_id`.
3. `state != draft` → a friendly "already {state}" inbox reply, `success=True`.
4. Revision is stale → failure "This plan was updated…". The comparison is lenient: an unparseable revision is treated as a match (`:614-624`).
5. There are no steps → the plan is marked `failed`.

**Then:**

- Insert `Workflow(kind="errand", state="running", cursor=0, steps=<copy of plan.steps>, title=summary)`.
- Set the plan to `launched`, link `workflow_id` and set `confirmed_at`, then commit.
- `await run_workflow(id)`.

  The **whole synchronous portion of the errand runs inside the callback background task**, so the mobile `/callbacks/{id}/status` poll stays pending until the run finishes or suspends.

**`run_workflow` → `_drive_run` → `execute_errand`** (`services/workflow_engine.py:808-846, 765-805`; `services/errand_executor.py:193-335`):

- **Timezone.** It reads `attention.timezone` for the household, falling back to UTC (`errand_executor.py:224-230`). The date context is built lazily, only if a node step has `resolved_datetimes` (`:232-244`; `workflow_engine.py:178-209`).
- **Synthetic conversation.** It seeds a `conversation_cache` entry `errand-<uuid>` with `node_context{household_id,node_id,speaker_user_id,timezone}` so server tools resolve their context as they would on a real turn. It is removed in `finally` (`errand_executor.py:246-259, 318-319`).
- **The loop, from `start_index` (`:280-317`):**
  1. Skip a step with an empty command, appending no result.
  2. `resolve_step_args`: `$`-directives (§8). A "skip" result is recorded as `success=True, data.skipped`.
  3. `run_step` → `resolve_handler(command)`, first match in registration order (`workflow_engine.py:598-637`):

     | Order | Handler | Matches | Kind | Does |
     |---|---|---|---|---|
     | 1 | `PhoneCallHandler` | `make_phone_call` | deferred / `phone_call` | `create_call_plan(..., errand_id=workflow_id, errand_step=i, prior_context=…)` → `Suspend(cursor=i+1, key=session_id)`; no session returns a failed step (`:328-350, 237-258`) |
     | 2 | `WaitForHandler` | `wait_for` | deferred / `timer` | `delay_seconds` or ISO `until` → `Suspend(wake_at)`; a time already past succeeds immediately (`:427-456`) |
     | 3 | `ReplanHandler` | `request_replan` | deferred / `approval` | `Suspend(cursor=i+1)` (`:510-532`) |
     | 4 | `ServerToolHandler` | `tool_registry.has_tool(cmd)` | sync | `tool_executor.execute_tool(cmd, args, conversation_id=conv_id, user_utterance=goal)`, a **synchronous call on the event loop**; a result dict with `error` counts as failure (`:548-568, 136-158`) |
     | 5 | `NodeCommandHandler` | everything else | sync | `dispatch_node_command(node_id, cmd, args, user_id, voice_command=goal, timeout=30)`: the MQTT `tool_call` verb with `trusted:True`, correlated by polling a **temp file** (`services/node_command_service.py:145-196, 118`); a timeout returns `{success:false, error:"the node didn't respond in time"}` |

  4. A handler crash becomes a failed outcome (`workflow_engine.py:629-632`).
  5. **Fail-fast:** the first failed step `break`s the loop (`errand_executor.py:315-317`).
  6. On a `Suspend` the engine attaches the results and returns it.
- **Suspend.** `save_waiting(cursor, results, waiting_on=signal_kind, wake_at)` (`errand_service.py:1284-1298`). If the kind is `approval`, it emits `needs_a_tap` (`workflow_engine.py:784-797`).
- **Finish.** `aggregate_and_compose` (`errand_executor.py:156-190`):
  - Status is `success` if every step passed, `failed` if none did or the list is empty, and `partial` otherwise.
  - Control steps (`control:True`) are excluded from the tally.
  - The message is LLM-composed (`:93-153`):
    - the prompt is anti-fabrication (`:111-130`): the goal may *describe* actions that weren't performed
    - on the **live** slot (the client default `model="live"`, `core/llm_proxy_client.py:57`), `temperature=0.3`, `max_tokens=400`, `reasoning_budget=0`
    - any failure falls back to the plain `"label: msg • …"` join (`:53-70`)
- **Report.** `emit_progress("complete")` → `_post_errand_completion_card` (`errand_service.py:585-611`):
  - Title: `"{icon} Errand {headline}: {title}"`, with icon and headline from `_ERRAND_STATUS_CARD` (`:577-582`).
  - Category `errand`, `push=True`, and metadata `{household_id, status}`.
  - **No buttons.**
- **Persist.** `save_terminal(terminal_state)`: `success→done`, otherwise the status passes through; `error` is the message truncated to 500 characters (`workflow_engine.py:758-762, 800-804`).
- **A crash anywhere** produces a "The workflow couldn't run." card and `failed` (`:833-841`).

### 3.5 Resume: `deliver_signal` (`services/workflow_engine.py:850-923`)

This is the single resume primitive. The steps:

1. **Load.** Proceed only if the run is `waiting` and `cursor == step+1`; otherwise it's a stale signal (`:876-880`).
2. **Claim.** An atomic `UPDATE … SET state='running' WHERE state='waiting'` (`errand_service.py:1273-1282`), so the immediate hook and the sweep can't both proceed.
3. **Interpret.** The handler interprets the signal:
   - **Phone** (`:352-394`):
     - `done` with `goal_achieved is True` → success. The gateway's wrap-up `summary` and `facts` are threaded into later call briefs (`:308-325`, `:221-234`).
     - `done` with `goal_achieved` False or **None** → **failure**. Calls to strangers chain only on explicit success.
     - `failed`, `declined` or `expired` → failure.
   - **Timer:** always success.
   - **Approval:** success, with `control:True`.
4. **Then:**
   - Failure → `finalize_fn`, the completion card, and terminal (fail-fast).
   - Success → `_drive_run` from the cursor, which may suspend again.
   - A crash → a "hit a snag after the last step" card and `failed`.

**What delivers signals:**

| Signal | Source |
|---|---|
| Phone outcome, immediate | `api/phone_sessions.py:34-51` (a snapshot including `outcome_json`) → `resume_errand_after_call` (`errand_service.py:710-725`) |
| Phone outcome, safety net | The 20 s sweep `resume_waiting_errands` (`:792-850`): for every `waiting` run, looks up the latest `PhoneCallSession` with `errand_id=run.id` and `errand_step=cursor-1` |

The safety-net sweep:

- If that call is terminal, it resumes the run.
- If the session's `created_at` is older than **60 min** (`:731`), it synthesizes `state="failed"` ("the call wasn't completed in time").
- It queries every waiting run, including timer and approval waits, every tick.

| Signal | Source |
|---|---|
| Timer | `resume_due_timer_workflows` (`:853-889`): `waiting_on='timer' AND wake_at<=utcnow()` |
| Approval | A tap or the auto-continue (§3.6) |

### 3.6 Mid-run pause-and-replan (the `request_replan` checkpoint)

**What triggers it.** The planner may end a plan with `{"command":"request_replan","args":{"reason":…}}` when later steps depend on earlier results ("check the weather and IF rain remind me…", `errand_planner.py:287-298`). The run suspends on `approval`, and `needs_a_tap` launches the detached `_resolve_and_decide_replan` (`errand_service.py:333-350, 383-440`).

1. **Re-plan.** If the step already carries `add_steps`, those are used. Otherwise `replan_from_progress` runs: an LLM call on the background slot that is given the done steps and their real result data (`errand_planner.py:516-578`). It **may return zero steps**.
2. **Persist the continuation.** It is written into the checkpoint step's `args.add_steps` (`errand_service.py:353-380`), so the card, Approve and auto-continue all splice the same steps.
3. **Gate with `widens_envelope(steps, steps+add)`** (`workflow_engine.py:468-507`). It widens if any of these hold:
   1. the amended plan has more `is_risky` steps
   2. it adds a command not already approved
   3. it adds a `business` counterparty that is new
   4. *(stub, always false)* it loosens a guardrail
4. **Decide:**
   - **Widens:** post a delta card (`🔀 Errand update: …`, Approve / Stop, `data {workflow_id, revision}`; `:230-312`).
     - Approve: `_apply_workflow_replan` splices the steps at `at_step+1`, bumps `workflows.revision` and calls `deliver_signal(approval)` (`:263-281, 989-1035`).
     - Stop: cancels the run, declines pending calls and cancels the plan (`:1038-1081`).
   - **In envelope, or nothing to add:** `_auto_continue_replan` splices and resumes with no tap (`:315-330`).
   - **Any failure:** auto-continue, so the run never parks forever (`:435-440`).

### 3.7 Revise and cancel

- **Refine** (`:1173-1231`):
  - The prompt contains the current plan, the instruction and the menu (`errand_planner.py:352-375, 480-495`).
  - The allowed commands are `menu ∪ commands already in the plan`, so a degraded fallback menu can't strip existing steps.
  - On success, `_apply_plan_revision` updates the steps in place, sets `revision+1` and **updates the same inbox item** (`errand_service.py:1149-1170`).
  - On an infra failure the row is untouched, so the old card stays valid.
- **Cancel** (`:892-957`):
  - A `draft` plan becomes `cancelled`.
  - A `launched` plan whose run is **`waiting`** becomes `cancelled` on both rows, and `_decline_pending_workflow_calls` declines `draft`/`confirmed` phone sessions (`:960-986`).
  - A **`running`** run can't be cancelled; the reply is an honest "already running" no-op. CC has no cancellation token for an executing drive.
- **Every callback reply** uses `context_data.inbox`. With `navigation_type=new_notification`, that becomes a new inbox item (`api/callbacks.py:478-500`).

### 3.8 Restart behaviour

- **`running` at boot.** Recovery fails **every** `running` workflow, whatever its kind: `partial` if any step passed, otherwise `failed`. It posts "This errand was interrupted by a restart…" (`errand_service.py:735-789`). This is correct only because CC is single-instance.
- **`waiting` rows** survive and resume via their signal.
- **Detached `asyncio` tasks are lost on a restart:** the draft from `run_errand`, the replan resolve and the auto-continue.
  - A lost draft means the voice user was promised a card that never comes.
  - A lost replan task leaves the run `waiting/approval` **forever**. Nothing sweeps approval waits (acknowledged at `:318-319`).
- **Drafts never expire.** `expires_at` is never set, and nothing writes `expired` to `errand_plans`.

---

## 4. Data

| Table | Key columns | Lifecycle |
|---|---|---|
| `errand_plans` (`models.py:1048-1099`; migrations `f7a8errnd001`, `g8b9errnd002`, `h9c0errnd003`, `i0d1wkfl004`) | `id pl_<hex>`, `household_id`, `user_id?`, `node_id?`, `goal`, `summary`, `steps` (JSON text), `inbox_item_id`, `state`, `revision`, `workflow_id`, `confirmed_at` | **States actually written:** `draft → launched \| cancelled`, `draft → failed` (no steps), `launched → cancelled` (via cancel or stop). The column comment lists `running/waiting/done/partial/timeout/expired`, which is wrong after the split; `launched` isn't in it. **Vestigial:** `routine_slug`, `cursor`, `results_json`, `error` (mostly) and `expires_at`. Never deleted. |
| `workflows` (`models.py:1102-1151`; `i0d1wkfl004`, `i1e2wait005`, `l4h5wfrev008`) | `id wf_<hex>`, `kind`, `household_id`, `user_id`, `node_id`, `goal`, `title`, `steps` (JSON, **a copy** that is mutated by replan splices), `cursor`, `results_json`, `revision`, `inbox_item_id` (delta card), `state`, `waiting_on`, `wake_at` (naive UTC), `error` | `running ⇄ waiting{phone_call\|timer\|approval}`, ending in `done \| partial \| failed \| timeout* \| cancelled`. (*`timeout` is reachable only if a `finalize_fn` emits it, and none does.) Never deleted. Indexed on kind, household, state and wake_at. |
| `phone_call_sessions.errand_id` / `errand_step` (`models.py:992-995`) | `errand_id` holds the **workflow** id despite its name | Doc 11 |
| `schedules` (`models.py` `Schedule`; `j2f3sched006`) | `intent`, `next_fire_at`, `recurrence`, `state` | Doc 08 |

**Result-entry shape** (in `results_json` and the step outcomes): `{command, label, success, message, error, data, summary?, control?}` (`workflow_engine.py:150-175`).

**In-memory state:**

- module registries: `_HANDLERS`, `_CONSUMERS`, `_STORE_FACTORY`
- `_ERRAND_TASKS` and `_SCHEDULE_TASKS`: sets of strong references to tasks
- the synthetic `conversation_cache` entries
- **On disk:** temp result files under `jarvis-device-control/` (node correlation, doc 05)

---

## 5. Settings

| Key | Default | Effect |
|---|---|---|
| `phone_calls` gate (`phone_calls_enabled`, doc 11) | off (fails closed) | `make_phone_call` appears in the errand menu only if this is on (`errand_planner.py:134-140`) |
| `web_search.enabled` | `False` | Adds `deep_research` and `quick_search` to the menu |
| `memory.enabled` / `memory.recall_enabled` | `True` / `True` | Add `remember`, `forget` and `recall`, and only with an identified speaker |
| `attention.timezone` | `"UTC"` | Household zone for date-key resolution in node steps (`errand_executor.py:228`) |
| `errands.autonomous_enabled` | `False` | **Defined but read by nothing** (`services/settings_definitions.py:365-380`); see §8 |

No setting enables or disables errands, and the following are all constants: the 20 s cadence, the 60-min phone deadline, the 30 s node timeout, the 10 s menu fetch, and the planner's 6000 `max_tokens`.

---

## 6. Dependencies

**Other CC subsystems:**

| Subsystem | Used for |
|---|---|
| Tool loop (02) | `tool_registry`, `tool_executor` and `conversation_cache` |
| Dates (03) | `flatten_date_context`, `generate_date_context_object`, `normalize_date_key`; and `parse_time_string` in `schedule_errand` |
| Nodes (05) | `_request_tools_from_node`, `dispatch_node_command` |
| Schedules (08) | Creates, fires and lists errands |
| Phone (11) | `create_call_plan`, `transition`, `phone_calls_enabled`, and the session hooks |
| Inbox and callbacks (13) | `post_inbox_item_sync`, `update_inbox_item_sync`, `server_callback_registry` |
| Proposals (10) | **Imports the private `_run_planner`** (`services/proposal_matcher.py:233`) to reuse it as a generic LLM-to-JSON call |

**LLM calls (all through llm-proxy `chat_completion`):**

| Call | Slot | Params | Prompt |
|---|---|---|---|
| plan / refine / replan | `background` | temp 0, max 6000, thinking ON | `errand_planner.py:306-324, 352-375, 531-559` |
| completion compose | `live` (default) | temp 0.3, max 400, `reasoning_budget:0` | `errand_executor.py:111-130` |

None of these use the prompt providers; they are raw user-message prompts.

**Other services and third parties:**

- **jarvis-notifications** for inbox and push. In Go this becomes in-process.
- **jarvis-phone-gateway** through doc 11.
- **Third parties:** none directly.

---

## 7. Invariants and non-obvious behaviour (preserve)

1. **Nothing runs without a Run tap.** This covers drafts and every scheduled fire (`schedule_service.py:1-6`). The one exception: an in-envelope mid-run replan auto-continues.
2. **The planner never executes**, and unknown commands are dropped, not run (`errand_planner.py:453-455`).
3. **Server tool wins over node command on a name collision**, both in the menu and at dispatch (`errand_planner.py:201-210`; `workflow_engine.py:595-597`). Handler order is phone → wait → replan → server → node.
4. **Fail-fast:** the first failed step ends the run, and a `done` call chains **only** when `goal_achieved is True`.
5. **Never-vanish:** every terminal path posts exactly one honest card, including:
   - planning failure
   - a crash
   - a restart (orphan recovery)
   - a phone timeout
6. **Anti-fabrication in the summary:** steps whose data is empty are described as "returned no information" (`errand_executor.py:105-110`), and control steps are excluded.
7. **Stale-card guards:** `revision` on both `errand_plans` and `workflows`. Comparison is lenient: a non-int revision passes (`errand_service.py:614-624`). Every tap is household-scoped through `ctx.household_id`.
8. **Single-use taps** return `success=True` with an "already handled" inbox note, never an error.
9. **Card updates happen in place** through the stored `inbox_item_id`. If the update fails, a new card is posted (`:200-220`).
10. **Atomic claims:**
    - `waiting→running` before any resume.
    - Schedule claims (doc 08).
    - Only `waiting` runs are cancellable.
11. **Errands are headless.** They never speak on the node. The voice tool's spoken ack is the only audio.
12. **Detached runs need a synthetic conversation context** so that server tools find household and speaker. In Go, pass the context explicitly instead (§11).
13. **`replan` splices at `at_step+1`, not at the end**, and the continuation is persisted before the decision so every path splices the same steps.
14. **Timezones:** `schedule_errand` resolves times in the **node's** zone from the conversation (`schedule_errand_tool.py:238-242`). Execution uses the **household** `attention.timezone`, and these can differ.

---

## 8. Oddities

1. **Autonomy scaffolding with nothing calling it.**
   - `services/autorun_gate.py`: an allowlist of `{get_drive_time, reminder}`, at most 3 steps, nothing risky and no `business` arg.
   - The `errands.autonomous_enabled` setting.
   - `services/step_value_resolver.py`, providing `$leave_by` and `$from_step`.

   All three were added in `9b36818` so that `appt.upcoming` could **autorun** a workflow. `ea66619` replaced that with a deterministic leave-by *card* (`services/signal_reaction_bridge.py:1-24`). Now:

   - `is_autorun_eligible` is called only by tests.
   - The setting is read by nothing, yet it is still exposed in settings definitions.
   - `resolve_step_args` still runs on every step (`errand_executor.py:294-301`), but no producer emits `$` directives. The planner prompt never mentions them.
2. **`wait_for` is unreachable.** It is not in `CONTROL_COMMANDS` (`errand_planner.py:218`) and not in any menu, so the planner can't emit it, and `_plan_from_data` would drop it anyway. The timer handler, the `wake_at` column and the timer sweep exist only to "prove the engine is general" (`errand_service.py:856-858`).
3. **The `replan_errand_plan` callback** (edit the goal and re-plan) is registered, but no card renders its button.
4. **`POST /errands` is unused**, and its docstring and `main.py:826-827` describe the removed "transient routine" path. Likewise the planner module docstring still says "executor (execute_routine_on_node)" (`errand_planner.py:3-8`), and `ErrandPlan.routine_slug` is vestigial.
5. **Stale state comments.** The comment in `ErrandPlan.state` (`models.py:1082`) and the docstring of the `ErrandPlan` class both disagree with the states actually written.
6. **Contradictory comment about running runs.** The cancel-handler comment says "a stuck 'running' is recovered→'waiting' by the sweep, then cancellable" (`errand_service.py:925-926`). In fact recovery happens only at startup and lands the run **terminal**; the sweep never touches `running`. A run hung mid-drive (for example a blocked server tool) can't be cancelled until a restart.
7. **Approval waits and drafts never time out.** A lost auto-continue task leaves the run parked forever (`:318-319`). The phone deadline counts from `session.created_at`, which includes the time the user takes to confirm the call card. A user who confirms after 60 minutes fails the errand while the call may still be placed.
8. **Compose runs on the live slot** while every planning call runs on background, so a completion summary competes with voice turns.
9. **`ServerToolHandler` blocks the event loop.** It calls the synchronous `execute_tool` from inside the async drive.
10. **`_summarize_progress` pairs `done_steps[i]` with `results[i]`** (`errand_planner.py:522-524`). Empty-command steps append no result, so the two can drift out of alignment.
11. **The fallback `COMMAND_MENU`** advertises invented argument specs, such as `set_reminder(text, when)`.
12. **`resume_waiting_errands` runs a phone-session query for every waiting run** each tick, including timer and approval waits. Harmless, but wasteful.
13. **`proposal_matcher` reaches into `errand_planner._run_planner`**, a private function, as a shared utility.

---

## 9. Tests

| File | Lines | Covers |
|---|---|---|
| `tests/test_errand_service.py` | 1246 (55 tests) | card metadata; in-place update; create/draft-detached failure cards; the `POST /errands` route; approve, refine, replan, discard; resume and fail-fast, including `goal_achieved=False`; the timer sweep; the phone sweep with timeout; startup recovery; the atomic claim; replan widen versus auto-continue |
| `tests/test_workflow_engine.py` | 529 | handler resolution order; outcome normalizers; `run_workflow`; `deliver_signal`, including no-op, stale cursor and idempotency; the timer and approval signals; the `widens_envelope` truth table |
| `tests/test_errand_executor.py` | 328 | fail-fast; suspend; synthetic context; compose fallback; control-step filtering |
| `tests/test_errand_planner.py` | 349 | menu building and the deny list; server-tool gating; validation and drop; think-stripping; refine and replan |
| `tests/test_run_errand_tool.py`, `tests/test_schedule_errand_tool.py`, `tests/test_list_scheduled_errands_tool.py`, `tests/test_schedule_service.py`, `tests/test_mobile_schedules.py` | 116 / 210 / 56 / 267 / – | tools, time parsing and the schedule sweep (doc 08) |
| `tests/test_autorun_gate.py`, `tests/test_step_value_resolver.py` | – | dead code (§8.1) |

**Golden and contract candidates:**

1. **Card metadata** (plan card, delta card, completion card, couldn't-plan card), as JSON fixtures. Mobile renders these generically, so key names, `target:"server"`, the `instruction` seeding and the absence of `editor_schema` are all contract.
2. **The `errand.*` callback pairs and their reply envelopes** (`context_data.inbox`).
3. **A fake-LLM black-box run:**
   - goal → canned planner JSON → draft row and card
   - Run tap → fake node replies → completion card text, using the fallback join with compose disabled
   - phone suspend → a terminal session with `goal_achieved` True / False / None → continue or fail-fast
4. **`widens_envelope`**, as a pure truth table that can be ported verbatim.
5. **`_resolve_fire_at` / `_build_recurrence`** time-parsing tables (doc 08).
6. **The planner, refine, replan and compose prompt strings**, if byte-exact prompts are wanted. These aren't provider prompts, so the user decides; see Q11.

---

## 10. Questions for the user

**1. `[scope]` Do errands ship in the first Go release at all, and how much of them?**

*Why it matters:* About 3.9k lines of source plus a generic engine, built in two weeks in July–August with no errand commits since. Real use is unknown: no client but voice creates errands, and nothing in e2e covers it. It also pulls in phone (doc 11), schedules (doc 08) and the inbox-callback plane.

*Options:*

- **(a)** Port everything as-is in 5c.
- **(b)** Port the core: plan, card, Run, sync steps, phone suspend/resume, Revise/Cancel, scheduled re-plan. Defer pause-and-replan.
- **(c)** Defer errands until after the port, keeping only the tables for import.

***My recommendation:* (b).** Phone errands are the headline capability and the core is well specified by tests. Pause-and-replan is the most complex and least proven part (see Q4). A quick check of prod `errand_plans` and `workflows` counts would settle this. Can you tell me whether you actually use errands day to day?

**2. `[change]` Keep the generic "workflow engine" abstraction, or build errands directly on the embedded job queue?**

*Why it matters:* The engine's seams (handler registry, store factory, consumer registry, progress emitter) exist for a second consumer that never came. In Go the durable queue already provides leases, retries, delays (timers) and dedup.

*Options:*

- **(a)** A one-to-one port of the seams.
- **(b)** An errand runner in which each drive/resume is a queue job keyed by workflow id (dedup = the atomic claim), timers are delayed jobs, and signals are "enqueue resume(workflow, step)".
- **(c)** Hybrid: keep the `StepHandler` interface (it's a natural Go interface) and drop the store, consumer and emitter indirection.

***My recommendation:* (c) on top of (b).**

**3. `[behaviour]` What should happen to an errand that was mid-step when the process restarted?**

*Why it matters:* Today every `running` run is failed at boot with an "interrupted" card. With a durable queue, Go could resume from the cursor. But a node or server step may already have executed (a device toggled, a memory saved), so re-running it is not idempotent.

*Options:*

- **(a)** Keep the behaviour: fail and send a card.
- **(b)** Resume from the cursor, re-running the in-flight step.
- **(c)** Mark the in-flight step "unknown" and stop, with a card offering "Run the rest?".

***My recommendation:* (a) for v1.** It's honest and simple, and restarts are rare. (c) is a nice follow-up.

**4. `[scope]` Keep mid-run pause-and-replan (`request_replan` → LLM continuation → `widens_envelope` → delta card or silent auto-continue)?**

*Why it matters:* It is what makes "check the weather and IF rain remind me" work. It is also the part with:

- the most moving parts (three detached tasks)
- a known park-forever hole (§8.7)
- a silent auto-continue that executes LLM-chosen steps the user never saw, whenever they fall "in envelope"

*Options:*

- **(a)** Port as-is.
- **(b)** Port it, but always post the delta card (no silent auto-continue), and put a deadline on approval waits.
- **(c)** Cut it for v1; the planner prompt drops the checkpoint rule.

***My recommendation:* (b).** Silent continuation of unseen steps contradicts "nothing runs without a tap". Do you want in-envelope auto-continue?

**5. `[scope]` Cut the orphaned autonomy code: `autorun_gate.py`, `errands.autonomous_enabled` and the `$leave_by`/`$from_step` step-value resolver?**

*Why it matters:* These were superseded by the deterministic leave-by card (`ea66619`) and have no producer or caller. But they encode a trust boundary you may want back: a signal autorunning a low-blast plan.

*Options:*

- **(a)** Cut all three.
- **(b)** Port them as dormant code.
- **(c)** Cut the code but record the design in the doc 10 backlog.

***My recommendation:* (c).** Is signal-triggered autorun still on your roadmap?

**6. `[behaviour]` Timeouts and expiry.**

*Why it matters:*

- Drafts never expire, and nothing ever writes `expires_at` or the `expired` state.
- Approval waits never time out.
- The 60-minute phone deadline is measured from when the call was *drafted*, so it includes the user's own time to confirm the call card. A late confirm fails the errand while the call may still dial.

*Options:* pick values for:

- **(i)** a draft TTL that marks the plan `expired` and updates the card in place
- **(ii)** an approval-wait deadline that fails the run with a card
- **(iii)** a phone deadline measured from `confirmed` or `dialing` rather than draft

***My recommendation:* (i) 24 h; (ii) 24 h; (iii) yes, measured from confirm, still 60 min.** When an errand is cancelled or times out, should its unconfirmed call card also be auto-declined (as Cancel already does)?

**7. `[behaviour]` Recurring scheduled errands re-plan and re-ask on every fire. Is that really the experience you want for e.g. "every hour" or "every weekday at 8"?**

*Why it matters:* Each fire costs a background-slot LLM call (up to 6000 tokens) and sends a push that needs a tap. Unapproved drafts pile up forever (Q6). This overlaps doc 08.

*Options:*

- **(a)** Keep re-plan and re-confirm on every fire.
- **(b)** Plan once at schedule time; each fire then re-confirms the *same* plan, with no LLM call.
- **(c)** Add a per-schedule "run without asking" option, limited to an allowlist (this is Q5's gate).

***My recommendation:* (a) for v1, plus the draft TTL**, so stale cards expire. Revisit (c) together with Q5.

**8. `[scope]` Confirm the cuts.**

The cuts are:

- `POST /errands`, which has no caller (Appendix A)
- the `replan_errand_plan` (edit-goal) callback, which no card renders
- the unreachable `wait_for` step and the timer sweep (§8.2)
- the vestigial `errand_plans` columns: `routine_slug`, `cursor`, `results_json`, `expires_at` (unless Q6 reuses it)

*Why it matters:* Each item is surface area to port and test with zero users. `wait_for` is nearly free on a queue with delayed jobs, but it also needs planner support to be reachable.

***My recommendation:* cut all of them.** Keep "timer" only as a queue capability, not as an errand step.

**9. `[behaviour]` Who may see and approve an errand when the speaker wasn't identified?**

*Why it matters:* Without a speaker:

- the plan card and the completion card go to the **whole household**
- any household member can tap Run, including on errands that place phone calls
- memory tools are silently left out of the menu

*Options:*

- **(a)** Keep household broadcast.
- **(b)** Require an identified speaker for errands that contain risky or phone steps.
- **(c)** Always target the household, but restrict Run to the household owner or admins.

***My recommendation:* (a), plus (b) for any plan containing `is_risky` steps.**

**10. `[behaviour]` Keep the strict fail-fast semantics exactly?**

The current rules:

- any failed step ends the errand
- a phone call that connected but whose `goal_achieved` is *unknown* (None) counts as failure
- a step the resolver "skipped" counts as success

*Why it matters:* "Check the weather, read the news, remind me X" stops at the first flaky node step, even when the steps are independent.

*Options:*

- **(a)** Keep it as-is.
- **(b)** Fail-fast only after risky or phone steps, and continue past a failed read-only step.
- **(c)** Let the planner mark dependencies.

***My recommendation:* (a) for parity;** (b) is a small, safe improvement if you want it.

**11. `[minor]` Prompt handling.**

*The question:* Should the planner, refine, replan and compose prompts be ported **byte-exact**, as the kept providers are, and should compose move to the background slot?

*Why it matters:* These prompts were tuned against Qwen3.5-9B live failures ("joke was told", empty replan).

***My recommendation:* byte-exact prompts, and compose on background** with the per-type concurrency cap. It isn't latency-critical, and today it competes with voice.

**12. `[minor]` Feature flag and legacy import.**

*Why it matters:*

- **Flag:** there's no `errands.enabled` setting. `run_errand`, `schedule_errand` and `list_scheduled_errands` are always in the LLM's tool list, which costs prompt tokens and risks misfires on every household.
- **Import:** `import-legacy` lists routines but not `schedules`, `errand_plans` or `workflows`.

***My recommendation:* add `errands.enabled` (default on, to match today). Import active `schedules` only. Don't import drafts or runs.**

---

## 11. Go port notes

### Shape

```
cc/errands/
  planner.go     // menu build (node cmds ∪ gated server tools, deny list, server-wins dedup),
                 // plan/refine/replan prompts (byte-exact), validate+stamp is_risky
  service.go     // CreatePlan, card builders (plan/delta/completion/couldn't-plan), callback handlers
  runner.go      // Drive(ctx, wfID) — the execute_errand loop; fail-fast; aggregate+compose
  handlers.go    // type StepHandler interface { Match(cmd) bool; Run(...) (Outcome, *Suspend, error);
                 //   Interpret(signal) Outcome } — phone, (replan), server, node
  envelope.go    // WidensEnvelope — pure, port the truth-table test verbatim
  store.go       // sqlc: cc_errand_plans, cc_workflows (claim = UPDATE … WHERE state='waiting')
```

### Use the platform instead of the Python workarounds

- **Embedded queue.** Each Run tap enqueues `errand.drive{wf_id}` with dedup key `wf_id`. A phone outcome, a replan approval or an auto-continue enqueues `errand.resume{wf_id, step, kind}`. This removes:
  - the detached `asyncio` tasks that are lost on restart (draft, replan resolve, auto-continue)
  - the 20 s phone-resume polling; keep a cheap deadline sweep only for timeouts
  - running the whole errand inside the `/callbacks` background task

  The callback handler should only validate, create the row and enqueue, then return. That changes when the mobile `/callbacks/{id}/status` poll completes (today it waits for the first suspension or for completion). **Verify the mobile UI doesn't rely on that.** Most likely it only needs `completed`.
- **Planning job.** Run `run_errand` / the schedule fire as a queue job `errand.plan{hh,node,user,goal,menu}`, so a promised card survives a restart.
- **Concurrency.** Planner and compose calls go through the background-LLM job type with its concurrency cap (PLAN §3.2). Plan for a 6000-token think budget per plan.
- **No synthetic conversation-cache entry.** In-process server tools should take an explicit `ToolContext{HouseholdID, NodeID, SpeakerUserID, TZ}`. Doc 02 should make that the server-tool signature anyway.
- **Node steps** use the embedded MQTT request/response (doc 05) instead of temp-file polling. Keep the 30 s step timeout and the `{success:false,error:"the node didn't respond in time"}` shape. That error text reaches the completion card and the compose prompt.
- **Phone resume** is a direct function call from the phone module's terminal transition: `errands.OnCallTerminal(session)`. Keep the snapshot of `outcome_json` so that `goal_achieved` and `summary/facts` are available.
- **Restart.** On boot, fail `running` workflows as today (Q3), unless the queue lease model makes "in-flight step unknown" precise.

### Risks

- **Card JSON is a mobile contract.** Golden-test every metadata block. Preserve:
  - lenient revision comparison: mobile may stringify the int
  - `instruction` seeding only on Revise
  - no `editor_schema`
- **Timestamps.** `wake_at` and `next_fire_at` are naive UTC. Store them as UTC in SQLite and compare in UTC.
- **`steps` JSON passes `args` through untyped**, as `map[string]any`. Node commands receive whatever the LLM produced, after `resolved_datetimes` resolution. Don't impose a schema the Python never enforced.
- **The two timezone sources** (node zone at scheduling, household `attention.timezone` at execution) must be preserved, or deliberately unified.

### Simplifications if the recommendations are taken

Cut:

- the autorun gate, step-value resolver and `wait_for`/timer sweep (Q5, Q8)
- `POST /errands` and the `replan_errand_plan` callback
- the vestigial columns
- the store, consumer and emitter indirection (Q2)

Together that removes about a third of the Python surface without changing any live behaviour.
