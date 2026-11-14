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

## Spec: `verified-replay` (approved by the user, 2026-11-13)

### Objective

A run uses what the workflow learnt. Today a replay does each recorded step (directly when its
element is found, else one model round "do only this step") and checks once at the end. After
this module a run makes sure of the prerequisites, checks every step against its expectation,
works toward the goal when something is off, and confirms the end state; and a new workflow is
watched through its first run, with the owner's corrections turned into a revision.

For the owner: a workflow that either does the task and says what it checked, or stops at the
point it went wrong and says what it saw. Never "done" on a wrong screen.

### Behaviour

A run has four phases. Risk rules, confirmations, masked values and household blocks apply in
every phase as today (principle 8).

1. **May it run?** An unproven workflow (no verified run of its current version) runs only when
   its owner starts it and is at the computer (computer control's attended check: screen
   unlocked, recent input). A schedule or a request from another member is refused with the
   reason. It pauses before its first risky step even where that step isn't "Ask me first".
2. **Prerequisites** (Q1), each checked, an unmet one achieved, in the order listed:
   - *App open:* in the window list, else launched from the catalogue.
   - *File open:* a window whose title holds the name (after `{param}`), else found by name in
     the user's document folders and opened when exactly one matches; none or several asks. The
     node finds the file itself: the model names it and never supplies a path.
   - *Page open:* a tab on that address, else opened.
   - *Signed in:* the site's page shows a way to sign out; if not, the run asks the user to
     sign in and continues when they say so. It never types a password.
   - *Other:* the model judges it on screen; unmet, the model works toward it, then asks.
   One it can't achieve stops the run before step 1.
3. **Steps**, each look, act, verify (principles 3 and 4):
   - *Act:* directly when the step's recorded element is found exactly once, by role and name,
     in the right app; otherwise the model does the step, given its intent and goal, the
     workflow's goal, the notes for the app, and the recorded element as a hint.
   - *Verify:* the step's expectation. An address, text, download or file check is observed;
     a screen sentence is judged by the model on a fresh look. No expectation: the step counts
     as done when its action succeeded, as today.
   - *A failed check* (Q2): wait and look again; still failing, the model chooses to redo the
     step, reach the result another way, or carry on because the result is there in another
     form. A risky step is never redone without asking. After 3 attempts on one step the run
     asks, showing the step, what was expected and what it sees.
4. **End state:** the workflow's goal end state and success check are verified. Reached: done,
   saying what was checked. Not reached after every step: the model works toward it within the
   run's remaining budget, then asks.

**Opening files** (in a run and by voice). A document opens without asking. A program, script
or shortcut opens after a confirmation that names it and its folder: a risky action like paying
or sending. A workflow step that runs one is learnt as "Ask me first", which its owner can turn
off for that step. `desktop_open` takes a file's name the same way outside a run ("open my
budget spreadsheet", "run my backup script").

**Strict or Creative** (principle 7) bounds "another way" in phases 2 to 4. Strict: only the
apps and sites the workflow's steps and prerequisites name. Creative: any means the node offers.
The workflow page gets the radio, Strict by default. A Creative action in an app or site the
workflow doesn't name is risk-classified like any other and reported in the run's summary.

**Proven.** A run of an unproven workflow that finishes with the end state verified marks that
version proven. Saving a change to the steps, prerequisites, goal end state, success check or
guardrails makes it unproven; renaming, sharing, triggers and notes don't. Relearn's saved
draft makes it unproven. Step healing (a moved element) doesn't.

**The watched run and its revision** (Q3, Q3a). The workflow page shows a run of that workflow
as it goes: each prerequisite and step with its state (waiting, doing, verified, failed), what
was expected and, on a failure, what was seen. Beside it, a box: "What went wrong?" Submitting
it, during a pause or after the run, sends the text and the run's record to the model, which
answers proposed edits to that workflow; the page loads them as a draft with the changed fields
marked, as Relearn's draft is. Save, then Run again. Text the model can't turn into an edit is
proposed as a note.

### Project structure

```
internal/modules/cc/computer/replay.go       the run: phases 1 to 4 (replaces today's step loop)
internal/modules/cc/computer/prereq.go       checking and achieving a prerequisite (new)
internal/modules/cc/computer/verify.go       a step's expectation uses the existing checks
internal/modules/cc/workflows_run.go         may-it-run, proven, the run's record
internal/modules/cc/workflows_revise.go      feedback + run record -> proposed edits (new)
internal/modules/cc/slots.go                 the "running a workflow" model slot (Models page, per feature)
internal/modules/cc/migrations/              proven version; the last run's record per workflow
internal/agent/desktop/, internal/agent/apps/  the open policy: a file by name (documents freely, programs after asking)
web/admin/src/pages/WorkflowEditPage.tsx     the radio, proven state, the run panel, the feedback box
```

### Testing strategy

- Unit, with the fake executor and a scripted model: each prerequisite kind met, achieved and
  unachievable; a step verified, a slow screen passing on the second look, the three model
  choices, the risky step that isn't redone, the attempt limit; the end state reached by
  another route; Strict refusing an app the workflow doesn't name.
- Routes: may-it-run refusals (schedule, another member, owner away), proven set and cleared,
  the revise route (owner only, another household 404), with tier expectations.
- Computer suite on the real model: the two teach cases still pass; new cases for a
  prerequisite achieved (file not open), a step whose expectation fails once, and a run that
  must stop and ask.
- Web: Vitest and `scripts/e2e web` for the radio, the run panel and the feedback draft;
  screenshots and the design audit.

### Boundaries

- **Always:** verify by observed state; ask before redoing a risky step; keep a workflow
  unchanged until its owner saves a draft.
- **Ask first:** any new `/api` route (revise, the run's record); more than one model call per
  step on the happy path.
- **Never:** open a path the model supplied; run a program, script or shortcut without the
  confirmation (or its owner's standing choice on that workflow step); type a password or a
  masked value; act on recorded coordinates; mark a workflow
  proven from the model's claim; let Creative skip risk confirmation.

### Success criteria

1. The bank download with the statement page not open: the run opens it (prerequisite), does
   the steps, and reports the download verified.
2. A step whose expectation fails because the page is slow passes on the second look with no
   extra action.
3. A step whose button was renamed is done by the model, verified, and healed.
4. A "send" step whose check fails is not sent again: the run asks.
5. An unproven workflow refuses a scheduled run and says why; after one verified owner run it
   accepts it. Editing a step makes it unproven again; renaming doesn't.
6. After a failed watched run, "it picked the wrong account at step 4" produces a draft that
   changes step 4 or adds a note; nothing is stored until Save.
7. Strict: a run that could only finish in an app the workflow doesn't name asks instead.
7a. "Open my budget spreadsheet" by voice opens the one matching document; "run my backup
   script" asks first, then runs it; a path given by the model is refused.
8. The two existing teach cases pass unchanged on qwen3.5-9b.

### Costs and risks

| | |
|---|---|
| Runs are slower | A step with a screen expectation costs one more model call. Address, text, download and file checks cost none. Principle 1 accepts this. |
| A wrong expectation stops a good run | The model may carry on when the result is there in another form; the watched first run is where wrong expectations get corrected. |
| Small models plan poorly | Running a workflow has its own entry in the Models page's per-feature section, so the owner picks the model for it (auto: what a computer task uses). The suite gates on qwen3.5-9b; what a smaller model does is reported, not promised. qwen3-4b already picks the wrong tool for taught workflows. |
| The run's record holds screen text | It is kept per workflow for the owner only, replaced by the next run, and never holds a masked value. |

### Open questions

None blocking. Decided in review (2026-11-13): documents open freely and a program after a
confirmation, by voice too
([decision](../decisions/2026-11-13-programs-open-after-a-confirmation.md));
the extra model call per screen-checked step is accepted; running a workflow gets its own
per-feature model slot
([decision](../decisions/2026-11-13-runs-open-documents-and-have-their-own-model-slot.md)).
Chosen as defaults, to change if the user says so: 3 attempts per step; a
2 second wait before the second look; the run is shown on the workflow page (the errand card
keeps its summary); the document folders are Documents, Downloads and Desktop.

## Plan: `verified-replay` (approved by the user, 2026-11-13)

The user approved the plan and, for R6 to R9, said in advance that those PRs need no separate
approval from them ("i don't think you need my approval on any of those"; R8 and R9 "as long
as you have the proper authorization/authentication on them"). Each of those PRs still states
its access rule at the top and carries its tier expectations.

### Shape

- **One loop, grown in place.** `Replay` keeps its outer shape (bind params, open the run,
  resume after a confirmation or a question, budget of rounds and minutes). Each task adds one
  phase to it, so the two teach cases pass after every task.
- **A step's check is the existing verifier.** `Loop.Verify` already checks an address, text, a
  download, a file or a screen sentence for the end of a run; a step's `expect` is the same
  `SuccessCheck`, so step checks need no new checker.
- **The model's choices are tools it already has.** On a failed check the model round gets the
  step, its goal, what was expected and what is seen, and answers as a computer task round
  does: actions, done, or a question. "Redo", "another way" and "carry on" are those three.
- **State per workflow:** the version last proven, and the last run's record (per prerequisite
  and step: state, expected, seen). One migration each, in the task that first needs it.
- **Model:** runs read a new slot, `workflows.run_model` (auto: what a computer task picks).

### Tasks (in order; each is one PR)

- [x] **R1 — a model slot for running.** `workflows.run_model` in `slots.go`, read by `Replay`;
  its entry in the Models page's per-feature section.
  - Acceptance: choosing a label there changes the model a run uses; auto is today's.
  - Verify: unit test on the pick; Vitest for the entry; screenshots and the design audit.
- [x] **R2 — every step is checked.** After a step acts, its `expect` is verified. A failed
  check waits 2 s and looks again. Still failing, the run asks, showing the step, what was
  expected and what it sees (the model's choices come in R3).
  - Acceptance: criterion 2; a failed check never reports the step done; a step with no
    expectation behaves as today; criterion 8.
  - Verify: unit tests with the fake executor (verified, slow screen, failing); the two teach
    cases on qwen3.5-9b; a new suite case with a slow page.
  - Built (2026-11-13). On the user's word after a failed check the run carries on and never
    does the step again. A screen sentence is skipped for a model that can't see (the rest of
    the expectation is still checked). Found on the real model and fixed here: a learnt
    expectation kept the recorded value ("…/statements/2026-09"), so learning writes the param
    into it and a run binds its own; and a step that causes a download was given a sentence no
    screen shows ("the browser begins downloading"), so it now expects the file in Downloads.
    The suite's slow-page case is not written: the fixture sites have no delayed-content page,
    and the path is covered by a unit test; it belongs with R3's cases.
- [x] **R3 — the model decides on a failed check; Strict or Creative.** The model round: redo,
  another way, or carry on. A risky step is not redone without asking. Three attempts per step,
  then ask. Strict bounds the round to the apps and sites the workflow names; the radio on the
  workflow page.
  - Acceptance: criteria 3, 4 and 7.
  - Verify: unit tests for each choice, the risky step and the limit; Strict refusing another
    app; a suite case with a renamed button and one with a "send" step; Vitest, screenshots.
  - Built (2026-11-13). The model's round is the step's own fallback round, told the step ran
    and what its check found. A risky step goes straight to the question, with no model round.
    An answer that isn't a plain "continue" is given to the model as what to do. Strict holds a
    model round to the workflow's sites (a subdomain of one counts as inside); a round already
    acts only in its step's app or browser window, so **Creative for now lifts the site bound
    only**: reaching the result in another app or by editing a file needs the run to switch
    targets, which comes with R4's work toward the end state. No new suite case: the renamed
    button is `replay_moved_button`, and the fixture sites have no "send" step.
- [ ] **R4 — the end state.** The goal's end state is verified with the success check; not
  reached, the model works toward it within the run's budget, then asks.
  - Acceptance: a run whose steps all "worked" but whose result is missing is not done.
  - Verify: unit tests; a suite case where the download never lands.
- [ ] **R5 — prerequisites: app, page, signed in, other.** Each is checked and an unmet one
  achieved before step 1; signed out asks the user and continues on their word.
  - Acceptance: criterion 1; a signed-out site stops before step 1 without typing anything.
  - Verify: unit tests per kind (met, achieved, unachievable); a suite case with the page closed.
- [ ] **R6 — open a file by name.** The agent's open policy takes a file's name: the node finds
  it in Documents, Downloads and Desktop, opens a document on a single match, and a program,
  script or shortcut after a confirmation. `desktop_open` by voice, and the file prerequisite.
  A step that runs a program is learnt as "Ask me first", which its owner can turn off.
  - Acceptance: criterion 7a; a file prerequisite with one match opens, with two asks which.
  - Verify: agent tests on fake folders (one, none, several, a script, a path refused); the
    open-policy guard tests updated, not weakened; a voice pipeline scenario.
  - Changes `internal/agent/CLAUDE.md` invariant 9 (approved in advance, 2026-11-13).
- [ ] **R7 — proven, and who may start an unproven run.** The proven version (migration); an
  unproven workflow refuses a schedule, another member, or its owner away from the computer,
  and pauses before its first risky step; a verified run proves it; which edits unprove it;
  the state on the workflow page.
  - Acceptance: criterion 5.
  - Verify: unit and route tests for each refusal and each edit kind; Vitest; screenshots.
  - Adds a membership and presence check to starting a run (approved in advance, 2026-11-13).
- [ ] **R8 — the run on the workflow page.** The last run's record (migration), a route to
  read it, and the panel: each prerequisite and step with its state, what was expected and, on
  a failure, what was seen; live while a run goes.
  - Acceptance: a failed run shows where it failed and what it saw; another member can't read it.
  - Verify: route tests with tier expectations; Vitest; `scripts/e2e web`; screenshots, audit.
  - A new `/api` route: owner only, another household 404, tier expectations in the PR (approved in advance, 2026-11-13).
- [ ] **R9 — revise from what the owner writes.** The "What went wrong?" box; a route that
  sends the text and the run's record to the learning model and answers proposed edits; the
  draft with the changed fields marked, saved or discarded like Relearn's.
  - Acceptance: criterion 6.
  - Verify: route tests (owner only, another household 404, nothing stored); a golden of the
    revise prompt; the real model on three kinds of feedback; Vitest; screenshots.
  - A new `/api` route: owner only, another household 404, tier expectations in the PR (approved in advance, 2026-11-13).

### Checkpoints

1. After R4: the two teach cases and the new suite cases on qwen3.5-9b, with the time a run
   takes before and after.
2. After R7: the user teaches one real task (the monthly budget sheet) and runs it once, watched.
3. After R9: the user corrects a run with the feedback box and runs it again until it is proven.

### Risks

| Risk | Mitigation |
|---|---|
| Step checks make good runs fail (a too-strict learnt sentence) | R2 lands before R3 on purpose: the suite shows how often it happens on the fixtures; the model's "carry on" in R3 and the watched run in R7 to R9 are the answer |
| The loop change breaks resume after a confirmation | Each task keeps the teach cases and the existing resume tests green; resume gets a test per new phase |
| Rounds budget runs out on long workflows | A screen check counts as a round; the budget is reported in checkpoint 1 and raised only with the user |
| File search opens the wrong document | Single match only, by full name; otherwise ask |

## Later modules' questions (not yet ordered)

- `verified-replay`, asked one at a time:
  - **Q1 (decided 2026-11-13):** a run opens apps and files to meet a prerequisite (a file only
    when exactly one matches, else it asks) and never signs in: signed out, it asks the user to.
    Signing in for the user is punted.
    [Decision](../decisions/2026-11-13-prerequisites-open-freely-never-sign-in.md).
  - **Q2 (decided 2026-11-13):** a failed step check waits and looks again, then the model
    decides (redo, another way, or carry on); a risky step is never redone unasked; after a
    bounded number of attempts it asks.
    [Decision](../decisions/2026-11-13-a-failed-step-check-goes-to-the-model.md).
  - **Q3 (decided 2026-11-13):** a new workflow is unproven until one verified run: owner-started,
    attended, pausing before its first risky step. The user added a revision loop: while watching,
    the owner can write what went wrong, and the workflow is revised and run again.
    [Decision](../decisions/2026-11-13-first-run-is-watched-with-feedback.md).
  - **Q3a (decided 2026-11-13):** the owner's written feedback and the run's record become
    proposed edits shown as a draft to review, as Relearn's is; then run again.
    [Decision](../decisions/2026-11-13-watched-run-feedback-becomes-a-draft.md).
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
