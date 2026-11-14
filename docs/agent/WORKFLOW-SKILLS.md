# Workflow skills: Jarvis learns a task from a recording and can repeat it

Status: **spec in progress, 2026-11-12.** Nothing here is built. Questions are asked one at a time;
the queue is at the end. Builds on Teach Jarvis (C10 in [COMPUTER-CONTROL.md](COMPUTER-CONTROL.md))
and the direct-step / goal-loop rule of
[2026-10-27-direct-steps-goal-oriented-model.md](../decisions/2026-10-27-direct-steps-goal-oriented-model.md).

## Goal (user, 2026-11-11)

"Think of the potential like a skill.md, but the user just has to record the actions and Jarvis
learns it all and is able to repeat it." A taught workflow should hold what a person would write
down to hand the task to someone else: what it is for, when it applies, what to start from, what
each step is for and what should happen after it, and what to watch for.

## Intent (confirmed by the user, 2026-11-12)

- **Outcome:** record a task once; Jarvis learns a structured skill and repeats the task later
  with other inputs, on a screen that looks different.
- **Who:** household members at their own computers, by voice, from the phone or on a schedule,
  often with nobody watching.
- **Success:** a run ends in the goal's end state, verified from the screen and the files. On the
  way it achieves its prerequisites, verifies each step, and tries other routes before it asks.
- **Repeat first.** Learning know-how about an app, to do tasks never demonstrated, is added only
  if it turns out cheap; notes may belong to an app as well as a workflow so there is room.
- **First tasks:** a monthly budget or spending summary in a spreadsheet; downloading a statement
  and filing it under the right name; a regular report email built from a document; moving files
  between folders. Single-pass tasks with a few changing values (moving *every* matching file is
  repetition: see `control-flow`).

## What exists (survey, origin/main @ e84f2b73)

- **Learning** (`internal/modules/cc/workflows_learn.go`): one model call names and groups the
  recorded events and picks params from candidates the server seeded. The answer has `name`,
  `triggers`, `steps[].intent`, `params`, `success`. Nothing says why a step is there or what it
  should cause.
- **Narration** is only the name given to `start_recording` (`recording_tools.go:175`); nothing the
  user says while recording is kept.
- **Replay** (`internal/modules/cc/computer/replay.go`): a step acts directly when its element is
  found by role and name, else one model round whose goal is that step's intent alone ("do only
  this step"). The model never sees the task as a whole. One success check at the end.
- **Data** (`computer.Workflow`, `cc_taught_workflows`): name, triggers, params, a flat step list,
  success, version. No goal, no starting state, no per-step expectation, no notes.

## Principles (from the decisions below)

1. **Correct before fast** (user, 2026-11-11). Speed is not a goal. Nothing is done blind and
   nothing is reported done without a check.
2. **Identity, never position.** Windows move, pages scroll, other windows are open. A run focuses
   the app and finds each element fresh; recorded coordinates never drive an action.
3. **Model-led, with a narrow direct path** (Q1). The run is a model loop guided by the skill; a
   step skips the model only when its recorded element is found uniquely, by identity, in the right
   app. So the skill must be complete enough for the model to finish the task with no recording.
4. **Every step is look, act, verify**, judged by observed state, never the model's claim. A step
   that can't be verified stops and asks.
5. **Prerequisites first** (user, 2026-11-12). A workflow lists what must be true before it starts
   ("file abc is open"). A run checks each one and tries to achieve the unmet ones itself, under the
   same rules as any step; one it can't achieve stops the run before step 1 and asks.
6. **Try hard, then ask** (user, 2026-11-12). A run that meets something unexpected works toward
   the goal by other routes within its budget. It asks only when out of options or before a risky
   action.
7. **Guardrails per workflow: Strict or Creative** (user, 2026-11-12), Strict for a new workflow.
   *Strict:* only the apps and sites recorded in, any route inside them. *Creative:* any means
   (another app, a website, editing a file directly). Reaching the end state by another route is
   a success in both; neither repeats the recorded steps literally. Prerequisites, verification
   and risk confirmations are the same in both.
8. **Rules unchanged:** risk and confirmations, masked values, ownership and sharing, per-computer
   opt-in (C2, C11) apply as today.

## Capability map

| Module id | Responsibility | Depends on |
|---|---|---|
| `capture` | The recorder keeps the starting state and the result of each action (Q3, Q5): desktop agent and browser extension. | — |
| `skill-doc` | The learned knowledge: goal, when to use, prerequisites, per-step goal and expected result, notes. Data model, learn prompt, editor, Relearn. | `capture` (falls back without it) |
| `verified-replay` | The run under principles 2–4: model-led loop reading the skill, direct path by identity, prerequisites achieved before step 1, a check after every step. | `skill-doc` |
| `narration` | What the user says while recording, aligned to the events, feeding the learner. | `skill-doc` |
| `refine` | A workflow improves after teaching: a second recording, a spoken correction, answers given during runs kept as notes. | `skill-doc`, `verified-replay` |
| `control-flow` | Conditions and repetition, learned from narration or a second demonstration. | `verified-replay`, `narration` |
| `compose` | Workflows return values and call each other. | `skill-doc` |

Build order: `capture` and `skill-doc` together (the learner falls back to inference until a
recording has results, so neither blocks the other) → `verified-replay` → `narration` → `refine`.
`control-flow` and `compose` wait until a real task needs them (user, 2026-11-12).
Superseded order: `verified-replay` → `narration` → `refine` → `control-flow` → `compose`.
Out of scope: Pantry export (Q7 of COMPUTER-CONTROL, 2026-10-19).

## Decided

- **Q1 (2026-11-12): the model-led loop is the base; the recording acts directly only on a unique
  identity match; every step is verified.**
  [Decision](../decisions/2026-11-12-workflow-runs-are-model-led.md).
- **Q2 (2026-11-12): the skill is structured parts, not a document.** Goal, prerequisites, steps
  (purpose, expectation) and notes are separate fields; the page and the model's prompt render
  them. No free-text skill file (user: "I don't want another readme based approach").
  [Decision](../decisions/2026-11-12-workflow-skill-is-structured.md).
- **Q3 (2026-11-12): the recorder captures each step's result; the owner can also write a step's
  goal.** After each action settles the recorder keeps what changed (window title, address, the
  element list; a screenshot where there is no tree), and expectations are learnt from that.
  Recordings without it fall back to inferring from the next step. Every step also has a **goal**
  the owner can enter or correct on the workflow page.
  [Decision](../decisions/2026-11-12-step-results-are-recorded.md).
- **Q5 (2026-11-12): prerequisites are the recording's starting point.** The recorder keeps the
  state when recording began (what is open, which file or page, what is signed in); the learner
  turns it into the list, limited to the apps, files and pages the steps touch; the owner keeps,
  drops or adds on the workflow page.
  [Decision](../decisions/2026-11-12-prerequisites-from-the-starting-state.md).
- **Q6 (2026-11-12): prerequisites use the workflow's per-run values.** "File {file} is open", not
  the file that happened to be recorded (user: "they need to be dynamic and smart, otherwise
  they're not useful"). The learner proposes a value where the recorded name is, or looks like, a
  param; the owner can make it fixed again.
  [Decision](../decisions/2026-11-12-prerequisites-from-the-starting-state.md).
- **Q4 (2026-11-12): existing workflows stay as they are, with a Relearn button.** Relearn rebuilds
  the skill from the stored recording as a draft to review; when the recording is past retention
  the page says to record the task again. Nothing is relearnt on upgrade.
  [Decision](../decisions/2026-11-12-existing-workflows-relearn.md).

---

## Spec: `skill-doc`

### Objective

After learning, a workflow carries a **skill**: enough written knowledge that the model could do
the task from it alone. The owner can read and correct it on the workflow's page. This module
stores and learns the skill and shows it; it does not change how a run behaves
(`verified-replay` does).

What a skill holds, each a separate field (Q2):

| Part | Says | Today |
|---|---|---|
| Goal | Two parts: the **reason** for the task and the **desired end state**. Learnt, editable. The end state is what a run is checked against when it finishes, with `success` for what can be checked exactly, and what the model steers by when a step goes differently | — |
| Guardrails | Strict or Creative (principle 7); Strict when learnt | — |
| When to use | What the user would be asking for, beyond the trigger phrases | `triggers` only |
| Prerequisites | A list of what must be true before step 1 ("file abc is open", "signed in to the bank"), each one checkable. A run tries to achieve each unmet one before it starts (user, 2026-11-12) | — |
| Step goal | The point of each step. Learnt, and the owner can enter or correct it on the workflow page (Q3) | `intent` (what, not why) |
| Step expectation | What can be observed once the step worked, learnt from the result the recorder captured (Q3); editable | — |
| Notes | Things to watch for; empty when learnt, filled by `refine`. A note belongs to the workflow or to an app | — |
| Done | How to tell the whole task worked | `success` |

### Commands

```
scripts/check                                             # local change gate
scripts/check --all                                       # with the Go race suite
scripts/sandbox up --name workflow-skills --llm scripted --ui
scripts/e2e web                                           # admin regression
go run ./tools/ccsuite ...                                # teach cases, docs/dev/computer-suite.md
```

### Project structure

```
internal/modules/cc/computer/workflow.go      the Workflow type (the run reads it)
internal/modules/cc/workflows.go              storage, validation, routes
internal/modules/cc/workflows_learn.go        the learn prompt and the draft
internal/modules/cc/migrations/               one new migration
internal/agent/computer/recorder.go           `capture`: starting state and each action's result (desktop)
internal/agent/browser/                       `capture`: the same in the extension (a store release)
web/admin/src/pages/WorkflowEditPage.tsx      the editor
docs/decisions/                               one file per answer
```

### Code style

The existing pattern: the model proposes, the server keeps only what it can tie to the recording.
New learnt fields follow it, e.g. an expectation is dropped when it names an event that isn't there:

```go
// Expect is what can be observed once the step worked; empty means the run asks the model.
Expect *SuccessCheck `json:"expect,omitempty"`
```

Reuse `computer.SuccessCheck` for expectations; no second check type.

### Testing strategy

- Go unit tests beside the code: parsing and validating the learn answer (invented fields dropped,
  limits), storage round trip, an old row loading with an empty skill.
- Learn prompt goldens for one browser and one desktop trace (scripted model).
- Vitest for the editor; `scripts/e2e web` for the page.
- The teach cases in `docs/dev/computer-suite.md` must still pass unchanged.
- No wall-clock waits.

### Boundaries

- **Always:** existing workflows load and run as before; masked values never reach the skill text;
  skill text is length-capped for the local models.
- **Ask first:** a new model call
  per learn; changing the review card.
- **Never:** store recorded coordinates as something a run acts on; weaken a risk a step already
  carries; put secrets or masked input in the skill.

### Success criteria

1. Learning the suite's `bank_download` recording gives a workflow with a goal, its prerequisites,
   and a goal and expectation on every step, none naming something absent from the recording.
   With captured results the expectations come from them; without, from the next step.
1a. A recording begun with a file open whose name holds the month param learns the prerequisite
   "file {…} is open" with the param, not the recorded name; one begun with an unrelated window
   open does not list it.
1b. Relearn on a workflow whose recording is kept gives a draft to review and leaves the workflow
   unchanged until it is saved; with the recording gone it says so.
2. A person reading only the workflow page can say what the task does and what each step should
   cause.
3. The owner edits any part of the skill and the edit survives a reload; the version goes up.
4. A workflow stored before this change opens, edits and runs with no migration step by the user.
5. `scripts/check --all`, `scripts/e2e web` and the teach cases pass.

### Open questions for `skill-doc`

None: Q2–Q6 are decided. `capture`'s own questions (how long to wait for an action to settle, size
limits, what "signed in" is read from) are settled in its plan, with the user asked where a choice
is theirs.

## Later modules' questions (not yet ordered)

- `verified-replay`: how far a run may go to achieve a prerequisite (open a file, yes; sign in, ask);
  what a failed expectation does (retry, model continues, ask); whether a new
  workflow's first run is a supervised "watch me do it back".
- `narration`: always listening while recording or opt-in; where the audio is transcribed and
  whether it is kept.
- `refine`: whether a learnt note needs the owner's approval before a run uses it.

---

## Plan: `capture` and `skill-doc` (awaiting the user's review)

### Shape

- **Storage:** one migration adds `skill TEXT NOT NULL DEFAULT '{}'` to `cc_taught_workflows`
  (goal `{reason, end_state}`, `when_to_use`, `prerequisites[]`, `guardrails`, `notes[]`). A step's
  `goal` and `expect` go in the existing `steps` JSON. An old row reads as an empty skill.
- **A prerequisite is structured:** `{kind, text, app, site, file, url}` with kind `app_open`,
  `file_open`, `page_open`, `signed_in` or `other`; `file`, `url` and `text` may hold `{param}`.
  Only `other` needs the model to check.
- **Trace:** two new event kinds, ignored by servers and agents that don't know them.
  `start`: the windows (class, title, focused) or tabs (address, title) when recording began.
  `result`, after an action has settled: the window title or page address and title, and the
  elements (role, name) that **appeared and disappeared**, capped. A diff, not the whole tree: a
  trace must stay under `MaxTraceBytes` (768 KiB).
- **Signed in** is read from the recording itself: a site whose pages showed no password field
  and took no masked input was signed in. A recording that signs in keeps that as steps.
- **Learning:** the same call returns the new fields. If the local model's answer no longer fits
  `learnMaxTokens`, the skill moves to a second call (ask first, per Boundaries).
- **Inert until `verified-replay`:** this plan stores, learns and shows the skill. Runs don't read
  it yet, so the Strict/Creative radio ships with `verified-replay`, not here.

### Tasks (in order; each is one PR)

- [x] **S1 — skill in the data model.** `computer.Workflow`/`WorkflowStep` types, migration,
  `Validate` (limits, kinds, `{param}` declared), API round trip.
  - Acceptance: a workflow with a full skill saves and reloads; an old row loads empty and runs.
  - Verify: `scripts/check`; store and validation tests; the teach cases.
- [x] **S2 — learn the skill (inference path).** Prompt and answer gain goal, when to use, step
  goals, and step expectations inferred from the next step; invented references dropped.
  - Acceptance: success criterion 1 on the `bank_download` trace, without captured results.
  - Verify: prompt goldens (browser, desktop), build tests with a scripted model.
- [x] **S3 — the workflow page shows and edits the skill.** Goal, prerequisites, each step's goal
  and expectation, notes.
  - Acceptance: criteria 2 and 3.
  - Verify: Vitest, `scripts/e2e web`, `scripts/screenshots --sandbox workflow-skills`.
- [x] **S4 — Relearn.** A route that returns a draft from the stored recording, and the button.
  - Acceptance: criterion 1b.
  - Verify: route tests (kept, expired, another household 404), Vitest.
- [x] **C1 — trace format.** `start` and `result` kinds, caps, tolerant parsing on the server.
  - Acceptance: a trace with them learns as before; one without them still learns.
  - Verify: parse tests both ways.
  - Built with C2. `start` and `result` carry no `seq` (a result names its action in `of`), so
    the actions keep their numbers and their keyframes. The server already dropped kinds it
    doesn't know; the step count and the Recordings page now leave these two out as well.
- [ ] **C2 — desktop capture.** Starting windows and the result diff after each action settles.
  The three platforms share one recorder (`record_desktop.go`, a `DeskPlatform` table of
  functions per OS), and computer control already has what capture needs on each: the window
  list with PIDs (`Windows.List`) and the tree of a window (`ax.Snapshot`, `atspi.Snapshot`). So
  the capture is written once in the shared recorder and each OS wires two existing functions in
  (survey 2026-11-12). The settle wait uses the recorder's injected clock.
  - Acceptance: a recording on this box holds a `start` and a `result` per action, under the cap.
  - Verify: `record_desktop_test.go` with a fake platform; live recordings on this box (Wayland),
    an X11 session and the Mac test host.
  - Built (2026-11-13). A result is read when there has been no input for 1.5 s after a click or
    key, or after typing or a scroll that comes sooner, or at stop. A click or key followed at
    once by another gets none (the next result covers both): the new action's own element and
    keyframe are read first, never behind a tree read. It names the window of the app the action
    was in and at most 40 elements each way (role and label, 80 characters), from the first 1000
    elements on screen in that window; all results of one recording take at most 256 KiB, after
    which only the actions are kept.
  - The tree is read with `ax.Peek` / `atspi.Peek`, not a snapshot: no refs are kept and the ref
    generation doesn't move, so computer control and voice can act by ref while a recording is
    on. A name is a label only (on macOS never an `AXValue`, so a cell's contents stay out), and
    on Linux only what is showing is read. Elements are counted, so a second "Delete" shows as
    appeared; a read that hit the cap reports nothing as disappeared.
  - Live: sway on this box (start, and a result after every click and Enter); the MBP (macOS 26),
    where the first run found results read from another app's window, since fixed and rerun; X11
    only in a headless server with no window manager, which lists no windows. Those runs were
    before the review fixes above. A click with no window of its own (the Dock) read the
    first listed window on the Mac, which was not the one in use; it now reads the app that has
    the keyboard focus afterwards (fixed with C4, not rerun on the Mac).
- [x] **C4 — learn from the capture.** Expectations from `result`, prerequisites from `start`
  limited to what the steps touch, `{param}` where the recorded name is one.
  - Acceptance: criteria 1 (captured path) and 1a.
  - Verify: goldens; a recording made with a month-named file open, learnt in the sandbox.
  - Built for desktop recordings (2026-11-13); browser recordings wait for C3. Each action's
    result is put on its line of the learn prompt ("then: the window is …; new on screen: …",
    five names at most), so the model writes a step's expectation from what the screen showed.
    Prerequisites need no model: a window open at the start, of an app the task then acted in,
    is "this file is open" when its title holds a document name (a param's recorded value in the
    name becomes `{param}`), else "this app is open". Two limits: a file name with spaces gives
    only its last word, and a param matches only as the recorded text. "Signed in" is a browser
    prerequisite and comes with C3. The live recording of checkpoint 2 is still to do.
- [x] **C3 — browser capture.** The same in the extension, a version bump, the re-signed `.xpi`.
  - Acceptance: a browser recording holds `start` and `result` events; masked values still never
    travel.
  - Verify: `internal/agent/browser` tests and `jstest`; the `release_xpi` gate.
  - The stores have approved the extension (source was at 0.3.10; this work makes it 0.3.11); this needs a **new version**,
    because the capture is in `background.js`. It asks for no new permission (`tabs`, `scripting`
    and all sites are already granted), so users see no prompt. The next release tag submits it
    to both stores through `browser-ext-publish.yml`, after the user approves the
    `browser-stores` environment; Chrome publishes when its review of the update passes.
  - **Held for the user:** the signed `.xpi` and that approval.
  - Built (2026-11-13) as extension 0.3.11. A recording opens with the pages that were open; a
    click, submit or key gets a result once the user has done nothing more for 1.5 s: the page's
    title and the controls (role and name, never a value) that appeared and disappeared, 40 each
    way, from the first 1000 controls. Neither entry takes a seq. A page that navigates away
    takes its pending result with it: the address it led to is already in the trace.
  - **Signed in** is not read as first planned ("no password field and no masked input", which
    would call every public site signed in): a page counts as signed in when it shows a way to
    sign out, and the prerequisite is listed only for a site the task then used. To confirm with
    the user.
  - Verified in a real headless browser by the suite's two teach cases on qwen3.5-9b: the
    result reached the learn prompt ("then: the page is …") and both replays passed.

### Checkpoints

1. After S3: a sandbox with the bank download learnt, and screenshots of its page, for you to read.
2. After C4: a recording on this box with a file open, and the prerequisites it learnt.

### Risks

| Risk | Mitigation |
|---|---|
| The local model's answer grows past its budget or loses accuracy | Goldens on the real model before S2 merges; a second call if needed |
| Result diffs blow the trace cap on busy pages | Cap per result and in total; past the total, later results are dropped (the trace is written as it goes), never events |
| "Settled" is wrong (slow pages, animations) | The result is taken at quiet or just before the next action, whichever is first; the learner treats a result as evidence, and the owner can edit |
| The extension update waits on Chrome's review of the new version | C3 is last; browser recordings use inference until it is out |
| Skill parts shown but not yet used by runs | Nothing here is released before `verified-replay` |
