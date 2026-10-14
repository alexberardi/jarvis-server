# Autonomy: Custom Rules, watches, goals (plan)

Status: **planned, 2026-10-14.** Nothing here is built yet; [STATUS.md](../STATUS.md) tracks the phases. These features are what jarvisd can borrow from OpenAI Dots (always-on agents with Custom Rules plus a pre-action reviewer) and Meta Muse (Goals, monitoring), done locally and with nothing hidden from the household.

Paths are relative to `internal/modules/cc/` unless they start with `internal/`, `web/` or `docs/`.

## Decisions

Each has its own file in [decisions/](../decisions/). Below they're referred to as A1–A9.

| # | Decision |
|---|---|
| A1 | [Household rules may let background actions run without a tap](../decisions/2026-10-13-household-rules-may-let-background-actions-run-without-a-tap.md) (lifts D9's "no new callers") |
| A2 | [Custom Rules govern background work only](../decisions/2026-10-13-custom-rules-govern-background-work-only.md) |
| A3 | [Watches are built on the signal bus](../decisions/2026-10-13-watches-are-built-on-the-signal-bus.md) |
| A4 | [Autonomy is built in worktrees beside the release](../decisions/2026-10-13-autonomy-is-built-in-worktrees-beside-the-release.md) |
| A5 | [Autonomy phases merge before v0.1.0](../decisions/2026-10-14-autonomy-phases-merge-before-v0-1-0.md) |
| A6 | [Home state reaches watches as node signals](../decisions/2026-10-14-home-state-reaches-watches-as-node-signals.md) |
| A7 | [Every autonomous action asks by default](../decisions/2026-10-14-every-autonomous-action-asks-by-default.md) |
| A8 | [Any identified member creates watches and goals](../decisions/2026-10-14-any-identified-member-creates-watches-and-goals.md) |
| A9 | [Watch fetches honour web_scraping.allow_external](../decisions/2026-10-14-watch-fetches-honour-web-scraping-allow-external.md) |

## What exists (survey, 2026-10-13)

- **No policy layer.** `servertools/servertools.go:7` says "the framework imposes no policy", and `Registry.Execute` (`:151`) has no gate.
  - Existing risk notions: node commands carry `is_risky` (`internal/agent/node/tools.go:132`), and server tools can implement `errands.RiskyTool` (`errands/planner.go:176`).
  - Approval cards exist in four places: errands (plan and delta cards), phone `confirm_call`, voice `actions` and signal-automation confirm. All four use the server-callback map `buildServerCallbacks` (`callbacks.go:86`).
- **The errand engine is fully ported** (plan → card → run → step → replan → done, `errands/`).
  - `WidensEnvelope` is live (`errands/replan.go:71`).
  - `IsAutorunEligible` (`errands/autorun.go:25`) has a hard-coded allowlist (`get_drive_time` and `reminder`, at most 3 steps, nothing risky, no `business` argument). Only tests call it.
  - `AutonomousEnabled` (`errands/service.go:197`) is never called.
  - `cc_workflows.kind` is an open string column, and its `waiting_on='timer'` and `wake_at` columns exist but are never used.
- **Scheduler.** It is a fixed set of trigger kinds (interval, cron, once) on the durable queue; a module registers a job type. The pattern to copy for user-owned rows is errand schedules: a `KindOnce` trigger per row, a claim, then re-arm and reconcile (`schedules.go:101-112, 205, 248, 275`). Missed runs fire once, late (D26).
- **Signal bus.**
  - Storage: `cc_signals` is unique on (household, source_key), with a TTL of at most 7 days.
  - Ingest: `POST /api/v0/signals`, which node packages such as `calendar_alerts` already post to.
  - Fan-out: `dispatchSignal` → `cc.signal_reaction` jobs (`signals_reactions.go:113`).
  - Change state: `cc_reaction_claims` with `claimValue`/`setClaimValue` (`:206-226`) already tracks "did this change?".
  - User automations (`signals.automations`) make one background-LLM tool pick, with `delivery` set to `automatic` or `notification` (D7 cards).
  - The catalog is static: `presence.left`, `presence.seen`, `appt.upcoming`.
- **Attention broker.**
  - Gates: dedupe, budgets, quiet hours (`attention.go:117-279`).
  - **It only gates the three node routes.** Notifications that jarvisd originates itself go straight to `postInboxItem` (`nodeplugin.go:133`) with no gate.
- **Deep research is the closest existing analogue to a watch**: a durable job with a 10-minute deadline that searches, scrapes and summarises in the background, then posts an inbox item and a push (`servertools/deepresearch.go`). It is one-shot and keeps no state between runs.
  - Fetching goes through `internal/platform/ssrf` (8 s timeout, 4 MB cap, private addresses blocked).
  - Search is DuckDuckGo HTML.
  - `web_scraping.allow_external` is declared but nothing reads it.
- **LLM contention, which must be fixed first.**
  - `llm.Service.acquire` (`internal/modules/llm/service.go:257`) only limits background calls. Live calls are never queued or prioritised.
  - With `background.engine=shared` and `parallel=1`, one background call holds the only llama-server slot, so a voice turn waits behind it.
  - Calls through the LLM HTTP API resolve as `live` (`internal/modules/llm/http.go:330`), so sandbox and pipeline traffic from worktrees competes with the house at live priority.
  - Nothing sends logprobs or a grammar to llama-server. JSON mode is a prompt instruction plus validation (`jsonmode`).

## Design

### 0. Foundations

1. **Live-first admission in `llm.Service`.**
   - When live and background share one engine with one slot, a background call waits while any live call is in flight, plus a short grace period after it (follow-ups arrive within seconds).
   - A running call can't be pre-empted, so background callers also get a max-tokens cap.
   - Requests to the HTTP API from app clients get a `priority` (default `live` for compatibility; the harness tap sends `background`). That stops worktree pipeline runs from delaying the house.
   - This is worth shipping on its own, before v0.1.0.
2. **`notifyGated`.** One helper that sends any notification jarvisd originates through `m.attentionGate` (dedupe key, category, budget, quiet hours), then `postInboxItem`/push. Watches, goals and autorun reports all use it. Deep research can move onto it later.
3. **Autonomy activity log** (`cc_autonomy_events`). An append-only record of what ran without a tap, what was asked and what was blocked, by which rule, with the source (errand, goal, watch, automation). This is the "inspect what it did" surface. Unlike Dots, nothing is hidden.

### 1. Custom Rules

- **Data:** `cc_tool_rules(household_id, scope_kind household|goal|watch, scope_id, tool, effect allow|ask|block, note, updated_by, updated_at)`, unique on (household, scope, tool). This is a table rather than a setting, because household-setting writes only accept bool, int and string (`household_settings.go:295`).
  - A goal- or watch-scoped rule is the per-task opt-in, for example "for this goal you may book without asking". It is granted when the goal or watch is created, or later from its card or page.
  - An optional `cc_household_instructions` holds free text such as "never spend money, never contact my boss". Only the reviewer (§1.4) uses it.
- **Package `policy`:** `Decide(ctx, hh, Action) Verdict`, where `Action{Tool, Args, IsRisky, Source}` and `Verdict{Effect, Rule, Reason}`.
  - **Resolution, first match wins (A7):**
    1. A goal or watch rule for this tool.
    2. A household rule for this tool.
    3. The tool's own declaration: `run_without_confirmation` means allow. A tool that is `is_risky`, or has a `business` counterparty (phone calls), ignores its own declaration and gets a warning in the log.
    4. Otherwise **ask**.
  - Automations keep their `delivery` value as the user's explicit per-automation choice. It counts as an automation-scoped rule.
  - D21 refusals with an unknown speaker still apply whatever the rules say.
  - The resolution replaces `autorunAllowlist` as the trust boundary. `IsAutorunEligible` becomes: every step resolves to allow, and the step count is at or under the cap.
- **The tool declaration (SDK change, EXTERNAL-CHANGES):**
  - `IJarvisCommand.run_without_confirmation: bool`, default False, sits next to `is_risky` (`jarvis-command-sdk/jarvis_command_sdk/command.py:293`). The node sends it in the tool metadata, the same way it sends `is_risky` (Python node-setup and the Go node, `internal/agent/node/tools.go:132`).
  - Server tools get the same thing through an optional Go interface, `ConfirmFreeTool`.
  - Candidates: read-only lookups (weather, drive time, search) and reversible node-local writes (reminder, timer, list add).
  - Old nodes that don't send the field are treated as False, which means ask.
- **Checkpoints, all background (A2):**
  1. **Errand plan, `CreatePlan`** (`errands/plan.go:137`).
     - If `AutonomousEnabled` is on and the plan is autorun-eligible, it launches straight away (the same transaction as `handleApprove`).
     - Instead of a plan card, it posts a "Ran on its own: …" card with a Stop button.
     - Any step that is `block` → no plan card, plus a card explaining the block.
  2. **Each errand step, `runStep`** (`errands/runner.go:66`). A step added by replan is checked again. `block` fails the step and `ask` suspends on `approval`, reusing the delta card.
  3. **Signal automations**, around `signals_reactions.go:752`. `allow` lets `automatic` run, `ask` forces `notification`, and `block` drops the action with a log line.
  4. **Goal advances and watch actions**, which go through the same two calls (§2, §3).
- **Auto-review (later, optional):** before an autorun step, one background yes/no call asks whether the step goes against the household instructions or the user's request. A "no" demotes the step to ask; it never promotes anything. This is the slot for the Jev-style decision model: start with llama-server and one closed question, and measure it with `tools/pipeline`.
- **Surface:**
  - Admin "Autonomy" page: a rules table over the full tool catalogue (server tools plus the node commands seen at warmup), the instructions box, and the activity log.
  - Mobile is a later external change (EXTERNAL-CHANGES).
  - A voice tool for editing rules is out of scope: rules are an admin decision.

### 2. Watches

A watch is a **source** that publishes signals, a **condition** over those signals, and an **action**, which is always notify. A watch can also kick off an errand, and the rules decide whether that asks.

- **Data:** `cc_watches(id, household_id, user_id, title, source_kind, source_spec JSON, condition JSON, cadence, state active|paused|done, expires_at, last_value, last_checked_at, last_fired_at, fire_policy once|every_change|cooldown)`.
- **Sources → signals.** Each source writes `cc_signals` rows with `kind='watch.<source>'` and `source_key='watch:<id>'`, so watch observations show up in the ambient context and the existing reaction fan-out.
  - `web`: gated by `web_scraping.allow_external` (A9) and `web_search`. Fetch a URL through the SSRF fetcher, or run a search query. A background LLM extracts the observed value (a price, in stock or not, a summary). Uses the scheduler's `KindOnce` re-arm pattern on `cadence`, with a per-household rate limit.
  - `signal`: needs no poller. A condition over existing signal kinds, evaluated in a new reaction registered with `dispatchSignal`.
  - `calendar` and `inbox`: these already arrive as signals or inbox items from node packages, so they work like `signal` with kind filters. Inbox data needs a signal emitted on inbox-item creation; that is the only new hook.
  - `home` (A6): nodes publish device-state changes as signals (`device.state`, with `source_key='device:<node>:<entity>'`, a TTL and the facts `{state, attributes}`). This needs a node change in Python node-setup and the Go node, plus the external device-manager packages such as HA. Add it to the static catalog. Watches then need no poller; `absent_for` uses a `KindOnce` trigger to re-check.
- **Conditions:**
  - Deterministic when possible: `lt`, `gt`, `changed`, `equals`, `contains`, `absent_for` (for example, the garage is still open 10 minutes later).
  - An LLM yes/no for fuzzy conditions ("is there news about X that matters?").
  - "Changed?" state uses the same claim mechanism as reactions.
- **Firing:** through `notifyGated` (dedupe key `watch:<id>:<value-hash>`) to the watch's owner, or the household if it has no owner.
  - `fire_policy` decides whether it repeats.
  - Watches expire by default (30 days, or when a condition marked `once` fires) so they don't pile up.
- **Creation:**
  - Voice tools `create_watch` / `list_watches` / `cancel_watch`, which are server tools, so speaker rules apply.
  - Admin page section.
  - Goals can create watches.
  - This is not the cut D17 matcher: every watch is something a person asked for, with an explicit condition, and nothing proposes on its own.

### 3. Goals

A goal is a long-running objective that **owns errands and watches** and advances on wake-ups.

- **Data:**
  - `cc_goals(id, household_id, user_id, title, objective, plan_json (milestones), state active|paused|done|abandoned, cadence, next_wake_at, budget JSON (max errands per week, max phone calls), progress_log JSON, created_at, updated_at)`.
  - Add `goal_id` to `cc_workflows` and `cc_watches`.
- **Wake loop:** one `KindOnce` trigger per goal (the `armSchedule` pattern) at `next_wake_at`. On wake:
  1. Gather progress: child run results, watch findings, relevant signals.
  2. Make one background planner call (reusing `ReplanFromProgress` and its prompt style). The answer is one of: no action yet, propose an errand, create a watch, mark a milestone, or ask the user a question.
  3. A proposed errand goes through the normal errand path, so Custom Rules decide between a Run card and autorun. The goal's `budget` is checked first.
  4. Set `next_wake_at`.
  - Child completion: add an `onRunFinished` hook beside `finish` (`errands/runner.go:689`) that wakes the goal early.
- **Creation:** explicit only in v1, via a voice tool `set_goal` ("make it a goal to …") and the admin page. This leaves D13's question (when should a request become an errand or goal?) for later, on purpose.
- **Visibility:** a weekly progress card per active goal (cron per household, like the attention journal card), plus the admin view of the goal's timeline built from the activity log.
- **Model limits:** local Qwen with one slot will be weaker at long plans than GPT-6 or Muse. Keep each wake a small step, cap the budget, and default to asking.

## Build plan

All new behaviour defaults off: `errands.autonomous_enabled` (exists), plus new `watches.enabled` and `goals.enabled`. Each phase is a PR, written test-first, and checked with `scripts/check` plus `CGO_ENABLED=1 go test -race ./...`. Voice tools get pipeline scenarios, and new routes get jarvisd-only contract tests (the `wire-change` skill).

| Phase | Content | Depends on | Lane |
|---|---|---|---|
| **P0a** | Live-first LLM admission, API `priority`, harness tap sends `background` | none | serial, first |
| **P0b** | `notifyGated`, `cc_autonomy_events`, the `policy` package (resolution order, `ConfirmFreeTool`, everything resolves to ask until P1 adds rules) | none | serial |
| **P1** | Custom Rules: table, `Decide`, checkpoints 1–3, autorun via rules (lifts D9), "Ran on its own" card with Stop | P0b | A |
| **P2** | Watches v1: table, `web` and `signal` sources, deterministic conditions, voice tools, `notifyGated` | P0b | B |
| **P3** | Admin Autonomy page: rules editor, watches list, activity log | P1, P2 APIs | C |
| **P4** | Watches v2: `calendar`/`inbox` (inbox→signal hook), fuzzy LLM conditions, `home` via node `device.state` signals (A6; needs the node change) | P2 | B |
| **P5** | Goals: table, wake loop, child hook, `set_goal`, budget, weekly card, admin timeline | P1 (P2 optional) | A |
| **P6** | Auto-review (yes/no pre-check), measured with the pipeline | P1 | any |
| **P7** | Mobile: rules, watches and goals screens (external change) | P3 | node-mobile |

### Building it alongside the release

- **Worktrees.**
  - Lanes A, B and C each get a worktree under `.claude/worktrees/` on a `feat/autonomy-*` branch from `main`, with their own sandbox (`scripts/sandbox up --name autonomy-a`, and so on).
  - A fresh worktree needs `scripts/fetch-sherpa-libs.sh` once.
  - `down --purge` when a lane finishes.
- **Model contention.** The sandboxes share the installed model's single slot. Until P0a lands, pipeline runs from the lanes queue behind the house *at live priority*, and so does the house behind them. **P0a goes first for that reason.**
- **Migrations.**
  - cc migrations are numbered in steps of 10 (`00170_node_kind.sql` is the latest), and goose applies them in order.
  - Each lane takes the next free number when it rebases onto `main` just before merging (the `add-migration` skill).
  - Lanes don't reserve ranges, because out-of-order versions break goose.
- **Shared hot spots** that need small, mechanical merges: `cc.go` definition lists, `buildServerCallbacks`, `householdControllable`, `servertools/definitions.go`, and admin `bff.go`/`App.tsx`.
- **Merge order (A5).** Phases merge into `main` as they are ready, off by default, including before v0.1.0. P0a goes first.
- **External changes** to add to `docs/EXTERNAL-CHANGES.md` as each phase lands:
  - SDK `run_without_confirmation` (P1).
  - Node `device.state` signals in Python node-setup, the Go node and device-manager packages (P4).
  - Mobile screens (P7).

## Open questions

None at the moment.
