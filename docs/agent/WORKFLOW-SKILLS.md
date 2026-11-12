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
6. **Rules unchanged:** risk and confirmations, masked values, ownership and sharing, per-computer
   opt-in (C2, C11) apply as today.

## Capability map

| Module id | Responsibility | Depends on |
|---|---|---|
| `skill-doc` | The learned knowledge: goal, when to use, prerequisites, per-step purpose and expected result, notes. Data model, learn prompt, editor. | — |
| `verified-replay` | The run under principles 2–4: model-led loop reading the skill, direct path by identity, prerequisites achieved before step 1, a check after every step. | `skill-doc` |
| `narration` | What the user says while recording, aligned to the events, feeding the learner. | `skill-doc` |
| `refine` | A workflow improves after teaching: a second recording, a spoken correction, answers given during runs kept as notes. | `skill-doc`, `verified-replay` |
| `control-flow` | Conditions and repetition, learned from narration or a second demonstration. | `verified-replay`, `narration` |
| `compose` | Workflows return values and call each other. | `skill-doc` |

Build order: `skill-doc` → `verified-replay` → `narration` → `refine` → `control-flow` → `compose`.
Out of scope: Pantry export (Q7 of COMPUTER-CONTROL, 2026-10-19).

## Decided

- **Q1 (2026-11-12): the model-led loop is the base; the recording acts directly only on a unique
  identity match; every step is verified.**
  [Decision](../decisions/2026-11-12-workflow-runs-are-model-led.md).
- **Q2 (2026-11-12): the skill is structured parts, not a document.** Goal, prerequisites, steps
  (purpose, expectation) and notes are separate fields; the page and the model's prompt render
  them. No free-text skill file (user: "I don't want another readme based approach").
  [Decision](../decisions/2026-11-12-workflow-skill-is-structured.md).

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
| Goal | What the task achieves, in a sentence or two | — |
| When to use | What the user would be asking for, beyond the trigger phrases | `triggers` only |
| Prerequisites | A list of what must be true before step 1 ("file abc is open", "signed in to the bank"), each one checkable. A run tries to achieve each unmet one before it starts (user, 2026-11-12) | — |
| Step purpose | Why each step is there | `intent` (what, not why) |
| Step expectation | What can be observed once the step worked | — |
| Notes | Things to watch for; empty when learnt, filled by `refine` | — |
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
- **Ask first:** an agent or extension release (capturing more at recording time); a new model call
  per learn; changing the review card.
- **Never:** store recorded coordinates as something a run acts on; weaken a risk a step already
  carries; put secrets or masked input in the skill.

### Success criteria

1. Learning the suite's `bank_download` recording gives a workflow with a goal, its prerequisites,
   and a purpose and expectation on every step, none naming something absent from the trace.
2. A person reading only the workflow page can say what the task does and what each step should
   cause.
3. The owner edits any part of the skill and the edit survives a reload; the version goes up.
4. A workflow stored before this change opens, edits and runs with no migration step by the user.
5. `scripts/check --all`, `scripts/e2e web` and the teach cases pass.

### Open questions for `skill-doc` (asked one at a time, in this order)

1. **Q3 — expectations:** a recording holds events, not what the screen showed after each. Are
   step expectations inferred from the trace, or does the recorder capture the screen after each
   step?
2. **Q5 — prerequisites:** where they come from: inferred from how the recording begins, said by
   the user, or both; and whether one can use a param ("file {name} is open").
3. **Q4 — existing workflows:** left with an empty skill, or relearnt from their recording when it
   is still kept?

## Later modules' questions (not yet ordered)

- `verified-replay`: how far a run may go to achieve a prerequisite (open a file, yes; sign in, ask);
  what a failed expectation does (retry, model continues, ask); whether a new
  workflow's first run is a supervised "watch me do it back".
- `narration`: always listening while recording or opt-in; where the audio is transcribed and
  whether it is kept.
- `refine`: whether a learnt note needs the owner's approval before a run uses it.
