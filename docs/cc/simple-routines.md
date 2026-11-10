# Simple routines: saved instructions behind a shortcut

**Status: implemented; release held for the model clarification failure below.** The user
confirmed the interaction and authorized implementation on 2026-11-09.
Decision: [Simple and Advanced routines](../decisions/2026-11-09-simple-routines.md).
Existing behavior: [Routines and schedules](08-routines-and-schedules.md).

## 1. Objective and confirmed scope

Make a routine easy to create without knowing command names, arguments or step labels.
Keep the current builder available as **Advanced**, and add **Simple** beside it at
the top of the mobile routine editor. Keep one shared routines list.

A Simple routine has a name, its usual trigger phrases, and one multiline instruction:

| Field | Example |
| --- | --- |
| Name | Good morning |
| Trigger phrase | Good morning |
| What should Jarvis do? | Turn on the living room lights and play Room Lights. |

On a matching voice request, Jarvis substitutes the saved instruction for the request
sent into the ordinary model/tool flow. The model interprets it **on every run**.
Saving does not generate a fixed sequence of commands. Simple also supports the
existing **Run** button and **schedules**. Existing routines remain Advanced.

Success means the example can be created without opening a command picker, then run
through any of those three entry points on a compatible node. Actual tool results
determine completion; generating a reassuring sentence is not evidence of execution.

## 2. Mobile experience

- Put a **Simple | Advanced** segmented control immediately below the editor title.
  Reuse the app's React Native Paper controls, theme and accessibility patterns.
- New routines open in Simple; saved routines open in their saved mode.
  Show a small mode label on list items and an instruction preview for Simple.
- Both modes retain the existing name, trigger phrases and optional schedule controls.
  A Simple routine needs a nonblank instruction. Advanced retains its step builder,
  command source selection, response instruction and response length controls.
- Simple shows the label **What should Jarvis do?**, a multiline textbox, and helper
  text: “Write what you'd normally ask Jarvis. It will interpret this each time.”
  Hide technical command/argument fields and response tuning in this mode.
- Creating or editing Simple does not require loading/decrypting the node's command
  catalogue. A node still needs to be chosen for Run and for a schedule as today.
- Keep separate content for the two modes. Switching never converts instructions
  into steps or steps into prose. **Save makes the currently selected mode active**
  for subsequent voice triggers, Run and schedules, subject to the existing enabled
  controls. Saving with Simple selected uses its instruction; saving with Advanced
  selected uses its steps. Preserve the other mode's content for a later switch.
  Changing the editor's selection alone does not change the live routine; Cancel
  leaves the previously saved mode and content in place. There is no separate
  Publish action. Shared fields stay shared. Only the selected mode executes, and
  only its execution requirements block saving; inactive content must still be
  structurally valid. The user confirmed this Save behavior after clarification.
- Save/Cancel and unsaved-change behavior follow the existing editor. Saving itself
  never runs a routine. Blank text is rejected inline and by the server.
- Ensure the textbox remains usable with the keyboard open, large text and screen
  readers. Selected mode and validation errors must be announced accessibly.

## 3. Execution behavior

The unit of substitution is the **matched routine**, not an arbitrary substring in
a sentence. Preserve current trigger normalization and selection semantics in this
version; test punctuation, capitalization, existing overrides and collisions with
built-in routines. Expanding a shortcut must not itself invoke another round of
shortcut matching. Do not reinterpret a validation answer as a new shortcut.

The expanded instruction enters the normal conversation pipeline as **user input**,
with the current node, household, tools, time/date context and applicable identity.
It is not a system prompt or an invocation of the summary-only LLM endpoint.
Normal tool validation, permissions, cancellation and turn limits still apply.
Use the configured model/provider; no additional cloud service is required.

| Entry point | Context | Response |
| --- | --- | --- |
| Voice | Triggering node, current conversation and identified speaker | Ordinary spoken response and follow-up questions |
| Run in mobile | Selected node, otherwise existing primary-node fallback | Silent on the node; result in the app |
| Schedule | Schedule's target node and timezone, with existing fallback rules | Silent on the node; existing completion notification |

Voice follow-up questions are explicitly confirmed by the user: if the instruction
needs more information, use the ordinary conversation flow to ask and continue.

Preserve D41 from the existing routine spec: Run and schedules have no inferred user
identity. Do not impersonate the routine's creator or the phone's authenticated user
when dispatching tools. Voice retains its normal speaker context. Personal commands
continue to follow their existing user/node credential rules.

**Unattended behavior:** if Run or a schedule needs clarification or an
interactive confirmation, finish with an explanatory incomplete result. Ask the user
to make the saved instruction more specific or run it by voice. Do not silently
choose a missing device/song, open a microphone session, or wait indefinitely.
No resumable chat UI is added in this version. Already completed actions remain done.

Each run takes one snapshot of the active definition. Editing it affects later runs.
Keep existing schedule overlap suppression, missed-run handling and bounded waits.
The current server wait is 60 seconds and the mobile request timeout is 90 seconds;
do not silently increase them to hide model/tool failures. A timeout is an unknown
outcome and must not cause an automatic replay of side effects.

Preserve deferred action callbacks, including music playback after voice output.
For silent runs, callbacks still execute without waiting for speech. Track callback
failures and distinguish resolving a track from actually attempting playback.
Simple must not regress the routine playback fix.

## 4. Data and compatibility contract

Extend the existing household-owned routine resource; retain the current CRUD,
run-now, schedule and node synchronization entry points. Keep `response_instruction`
for Advanced's response formatting; do not reuse it as executable instruction text.

Additive fields:

| Field | Meaning |
| --- | --- |
| `mode` | `simple` or `advanced`; selects the only executable payload |
| `utterance` | Saved Simple instruction, retaining internal punctuation/newlines |
| Existing `steps` and response fields | Advanced content; inactive while mode is Simple |

The SQLite migration defaults existing records to Advanced without altering their
steps, triggers, schedules or enabled state. A legacy create without `mode` creates
Advanced. On PATCH, omitted fields remain unchanged, including mode. Reject unknown
modes and activation of Simple with blank text. **Proposed:** limit instructions to
5,000 characters, consistent with the current mobile chat limit and its counting
semantics. Apply the same validation in the app and server.

Keep inactive content through mode changes, but never dispatch it. Node projections
must include the active mode and its required payload. A new executor selects by
mode, never by whether `steps` happens to be empty. There is no fallback from a
Simple execution failure to stored/default Advanced steps.

Compatibility is a delivery requirement:

- A new phone connected to an older server keeps Advanced usable and clearly explains
  that Simple requires an update. An old server must not silently save a Simple
  request as an empty Advanced routine.
- Unsupported Go or Python nodes must not receive a Simple definition as an ordinary
  executable step routine. Check support before enabling a target and again before
  dispatch; a scheduled target that loses support yields an explicit failure.
- Protect voice synchronization too: old nodes must not activate a Simple definition
  or keep running its stale Advanced version after a mode change. Handle default
  routine overrides, whose local definitions currently have deletion exemptions.
- An older phone must not silently turn a Simple routine into Advanced or overwrite
  its active payload. Reject incompatible edits with a useful update-required error;
  preserve existing Advanced contracts.

The routines list advertises `supported_modes: ["advanced", "simple"]`. The Go agent
advertises `capabilities.simple_routines: true` and pulls with `?simple_routines=1`.
Mobile uses these signals to enable the editor and Run/schedule targets. Saving an
enabled Simple schedule validates its target on the server, including a mode-only PATCH.
Older nodes receive an inert definition with no triggers and one unavailable sentinel
command. This overwrites stale built-in definitions even where deletion or empty steps
would otherwise restore a default. Disabled Simple definitions also produce this sentinel.
Older clients may edit shared metadata, but active-payload edits without `mode` return 409.
Initial execution support targets the Go agent; legacy Python nodes require an update
before they can execute Simple routines.

Keep the existing run-now result fields and statuses (`success`, `partial`, `failed`,
`timeout`). For Simple, `passed`/`failed` count attempted tool calls, not model messages
or imagined steps; the status/message also accounts for unresolved requested work.
A successful text-only request can legitimately have zero tool calls. Clarification
with no completed actions is failed; clarification after completed actions is partial.
An explicit tool failure cannot become success solely because the model says “Done.”

## 5. Boundaries and observability

**Always:** preserve household isolation, existing tool permissions, local operation,
normal provider selection, enabled/trigger controls and Advanced execution behavior.
Use existing Go modules/interfaces and the mobile API/state patterns. Add correlated
routine/run IDs, entry point, mode, node, tool outcomes and elapsed time to execution
diagnostics. Keep original voice input and the expanded request distinguishable in
the existing conversation trace without duplicating full instructions in routine logs.

The implementation uses the existing routes and access checks. Saved instructions are
ordinary user input, never a privileged prompt. Unattended runs reuse `chatStream`
without an inferred creator, phone user or desktop owner; a private conversation and
one deadline bound the run. Voice carries original speech plus `routine_name` and
`routine_utterance`, applies speech gates first, and expands once before model input.
Compound shortcuts bypass single-device/island completion shortcuts until the model
finishes the whole request. Validation answers retain the same conversation.

**Out of scope:** text-to-step generation, automatic mode conversion, nested routine
execution, new template syntax, a new scheduler, undo/rollback of completed actions,
or redesigning general trigger matching. There is no second independent tool loop.
Production, mobile publishing and agent releases remain outside this implementation task.

## 6. Source map and conventions

Paths in this table are relative to their owning repository, not to this worktree's
parent. Mobile and server are independent Git repositories.

| Repository | Entry points to extend or reuse |
| --- | --- |
| `jarvis-node-mobile` | `src/screens/Routines/RoutineEditScreen.tsx`, `RoutineListScreen.tsx`; `src/types/Routine.ts`; `src/api/routineApi.ts` |
| `jarvis-server` — definitions | `internal/modules/cc/routines.go`, `routines_run.go`, `internal/modules/cc/migrations/` |
| `jarvis-server` — execution | `internal/agent/builtins/routine.go`; `internal/agent/nodesettings/routines.go`; `cmd/jarvis-agent/builtins.go` |
| `jarvis-server` — ordinary turns | `internal/agent/voice/pipeline.go`, `followup.go`, `converse.go`; `internal/modules/cc/turn.go`, `mobile_chat.go` |
| Tests | Mobile `__tests__/screens/`, `__tests__/flows/`, `__tests__/api/`, `.maestro/`; server colocated Go tests, `contract/`, `tools/pipeline/`, `e2e/coverage.json` |

Use the versions in the repositories' manifests: Go `go.mod`, and mobile's Expo,
React Native, TypeScript and React Native Paper dependencies. No new dependency is
currently needed. Go uses `gofmt`, explicit errors/contexts and the existing logger.
TypeScript uses the current interfaces and snake_case API fields, for example this
existing type in `src/types/Routine.ts`:

```typescript
export interface RoutineStep {
  command: string;
  args: RoutineStepArg[];
  label: string;
}
```

Extend those contracts explicitly; keep unsaved form state distinct from the validated
active execution payload. Do not introduce unrelated API naming changes.

## 7. Acceptance and verification

| Scenario | Required observable result |
| --- | --- |
| Create Simple | Save with name, phrase and instruction; no step or command-catalogue requirement |
| Edit existing routine | Opens Advanced with all existing data and behavior preserved |
| Switch modes | Each mode's content survives switching and Save/reopen; only the mode selected at Save runs; switching then Cancel preserves the previous live mode |
| Voice shortcut | Expanded instruction reaches ordinary model/tool processing once with the right speaker/node |
| Run / scheduled shortcut | Same saved instruction runs on the selected node; no speech; correct inline/notification outcome |
| Lights plus music | Both tool results are consumed and the deferred playback callback runs exactly once |
| Runtime interpretation | A later run uses current date/context; no tool plan is compiled during Save |
| Follow-up | Voice can clarify; unattended execution reports incomplete work without guessing |
| Failure / timeout | Tool, model, unsupported-target and callback errors cannot produce false success or automatic replay |
| Isolation / disable | Another household cannot read/run it; disabled trigger/schedule controls remain effective |
| Mixed versions | Old server/phone/node behavior matches §4, including stale default overrides |

Use Jest/React Native Testing Library for form, validation, API and mode persistence
behavior. Use Go tests for migration, PATCH semantics, synchronization, execution
context, outcomes and callbacks. Cover all three real entry paths with a scripted
model and simulated tools; assert requests and side effects, not just response text.
Add focused real-model pipeline scenarios for instruction expansion, multiple tools
and clarification. Verify voice callbacks with the native pipeline harness; do not
claim hardware playback from a mocked callback test.

Extend black-box HTTP/MQTT contracts for additive fields and compatibility failures
following the [wire-change skill](../../.claude/skills/wire-change/SKILL.md). Preserve
legacy checks and mark jarvisd-only cases explicitly. Exercise mobile flows in the
isolated emulator and capture both modes, keyboard/validation states, light/dark and
large text. Update coverage mapping when flows change. Follow existing coverage and
quality gates; do not relax them for this feature.

Commands for implementation verification, from the indicated checkout:

```bash
# jarvis-node-mobile (implementation worktree)
npm ci
npx tsc --noEmit
npm test -- --ci

# jarvis-server (implementation worktree; native prerequisites per AGENTS.md)
CGO_ENABLED=0 mise exec go@1.27 -- go build ./cmd/jarvisd ./cmd/jarvis-agent
scripts/check --all
scripts/sandbox up --name simple-routines --llm scripted --ui
scripts/sandbox node-sim --name simple-routines
scripts/sandbox down --name simple-routines --purge

# Run the mobile lane against the intended mobile commit, not origin/main.
# Set MOBILE_ROUTINES_CHECKOUT to that checkout's absolute path first.
scripts/e2e mobile --name simple-routines-e2e --mobile-repo "$MOBILE_ROUTINES_CHECKOUT"
```

The sandbox commands are lifecycle entry points, not a claim that a simulated node
already implements the necessary music/lighting fixtures. Add those fixtures as part
of the implementation tests. Contract credentials/targets and real-model commands
follow the [contract guide](../contract/README.md) and
[pipeline-test skill](../../.claude/skills/pipeline-test/SKILL.md). Never run fixtures
against production. Verification evidence is recorded with the coordinated PRs.

## 8. Operational details and verification

Silent Simple tool calls include `wait_for_completion: true`. The Go dispatcher waits
for authorized deferred effects before posting their result, returning callback errors.
It preserves `wait_for_input` as context: SDK success responses default to true, so it
alone does not mean unfinished work. Explicit choices, parked confirmations and action
buttons produce `requires_follow_up`, suppress pending callbacks and stop unattended
execution. A question from the model without an exchange-complete marker is incomplete.
These model completion semantics cannot independently prove a physical device changed;
tool results remain the execution evidence. Timeouts are never automatically replayed.

Trigger precedence remains unchanged: defaults before additional routines. To replace
“Good morning,” edit that existing routine and save Simple; adding a second routine with
the same phrase does not change precedence. Advanced content remains available when
switching back.

Verified so far: focused Go tests for CRUD, old/current pulls, voice expansion and
clarification, multiple tool rounds and deferred effects, scheduled execution, pending
input and timeout handling; mobile TypeScript and all Jest suites; jarvisd-only Simple
contracts plus the unchanged Advanced MQTT contract; two real-model pipeline scenarios
(expansion and device-then-timer), each passing twice. The Android Simple flow saved,
reopened and switched modes with retained contents, with screenshots of both layouts.
It passed after two harness transport retries. The separate Advanced create/edit/delete
flow remains unverified: both attempts lost the emulator transport, including the
last attempt before app launch. No physical Pi speaker was exercised;
iOS, dark mode and large-text runtime checks have not been performed.

### Release hold: clarification is not reliable on the tested model

The installed `qwen3-4b` text/suffix model interpreted “Set a timer” as 3,600 seconds,
rather than asking for the missing duration. The voice diagnostic in
`tools/pipeline/scenarios/simple_routines.json` remains failing (0/2); it is tagged
`known-model-failure`, not skipped or changed to accept the guess. A transient instruction
to ask for missing parameters did not correct it.

The separate unattended probe used an isolated real Go Pi agent with voice disabled.
“Set a timer” dispatched an actual 3,600-second timer and returned `partial` because the
model omitted the completion marker. “Ask me how long I want the timer, then set it for
that duration” dispatched a zero-duration timer, which the node rejected, returning
`failed`. The disposable agent and its timer data are removed after verification.

Explicit validation, confirmation, choice and action-button signals stop unattended
execution, and scripted tests prove voice can ask and continue. They do **not** prove
this model reliably detects missing information. Keep both PRs in draft for review;
do not deploy or describe the no-guessing expectation as satisfied. Resolve model/parameter
grounding or explicitly revise that product expectation before releasing unattended Simple.


Android review images: [Simple](assets/simple-routines/simple-routine-editor.png),
[Advanced](assets/simple-routines/advanced-routine-editor.png).
