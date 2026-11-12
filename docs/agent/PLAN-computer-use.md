# Plan: Computer use, vision-first

Spec: [SPEC-computer-use.md](SPEC-computer-use.md) (approved by the user 2026-11-11). Branch:
`feat/computer-use`, worktree `.claude/worktrees/computer-use`. Status: **draft, awaiting review.**

## What the code already does (read 2026-11-11, `cc/computer/loop.go`, `prompt.go`)

- A desktop target **already gets a screenshot every round** (`observe`: `Screenshot: desktop ||
  r.wantShot`), plus the element snapshot. A browser target gets a screenshot only when the model
  asks or the snapshot has fewer than 5 elements.
- The element-first lean is in the **system prompt**: elements are the main view, coordinates are
  "only on a screenshot", "Prefer elements by ref over coordinates", "a click on a small button can
  miss", and with no elements it steers to shortcuts and menus.
- The model answers with **one JSON object in text** (not function tools). That is the single
  schema of V5; the spec's "function tools" wording is corrected in this plan's first PR.
- The model is picked by `computer.model` (`pickLabel`), so the cloud slot already works for
  computer tasks; `tools/ccsuite` knows only `--llm shared|scripted`, so the suite can't use it yet.

So the work is mostly prompt, suite and fixtures, measured against cloud first (V4), not a new
loop.

## Order and checkpoints

```
1 suite on cloud ─→ 2 baseline (no code) ─→ CHECKPOINT A (user reviews numbers)
                         │
3 canvas fixture ────────┘ (parallel with 1–2)
                         ↓
4 vision-first prompt ─→ 5 fixes from the baseline ─→ CHECKPOINT B (cloud passes 3/3)
                         ↓
6 T1 Writer case     7 stop / take-over case + MVP B gaps
                         ↓
8 live on this box (user) ─→ CHECKPOINT C (does it feel right?)
                         ↓
9 local models: same matrix, prompt fixes ─→ CHECKPOINT D (user judges the table)
                         ↓
later: macOS on the MBP (own plan), Windows
```

## Tasks

### 1. The suite runs on the cloud slot

`tools/ccsuite run --llm cloud`: the suite's sandbox gets a cloud label from a local env file
(`JARVIS_TEST_ANTHROPIC_KEY`, model `JARVIS_TEST_ANTHROPIC_MODEL`, default `claude-sonnet-5-5`;
the same names as the live tests) and sets `computer.model` to `cloud`. The key is never written
to the run's report or logs.

- Acceptance: `go run ./tools/ccsuite run --llm cloud --filter notes_x11` reaches Anthropic (the
  run's `llm.jsonl` names the model) and scores the case; without the key it exits 2 with the
  reason. Report shows model and slot.
- Verify: `ccsuite` unit tests (env/flag parsing, key redaction), one real run.
- Files: `tools/ccsuite/env.go`, `main.go`, `docs/dev/computer-suite.md`.

### 2. Baseline on cloud (no code)

Run the existing desktop cases (`notes_x11`, `notes_wlroots`) and the browser cases on Sonnet 5.5,
3 repeats each. For every failure, name the cause: loop, prompt, agent/platform, or model.

- Acceptance: a results table in the plan (case × pass/3 × cause), shown to the user.
- Verify: the suite's own report files under `.ccsuite/<run>/`.
- Files: this plan only.

**CHECKPOINT A:** the user sees the cloud numbers before prompt work starts.

### 3. A canvas fixture app (no accessibility tree)

`tools/ccsites/canvasapp`: a small desktop app that draws its UI itself (buttons, a text field, a
list) with no accessibility tree, and writes its state to a file the suite scores. Cases
`canvas_x11` and `canvas_wlroots` ("add an item called … and save"), `pending` until task 4.

- Acceptance: the app runs under Xvfb and headless sway; the agent's snapshot of it has 0
  elements; a scripted run that clicks the right coordinates passes the case, a wrong one fails.
- Verify: `ccsuite run --llm scripted --include-pending --filter canvas_` with a scripted answer.
- Files: `tools/ccsites/canvasapp/`, `tools/ccsuite/cases/canvas_*.json`, suite doc.
- Open: the toolkit. Recommendation: Go + a pure-Go window library that already builds here, or
  Tk if that's what the image has; whichever has no AT-SPI bridge.

### 4. Vision-first prompt for desktop tasks

A desktop-target system prompt where the screenshot is the main view and coordinates are
first-class (`click`/`double_click`/`drag`/`scroll`/`type`/`keypress`, `zoom` for small text),
elements as optional hints (capped at 40, with their position), and "verify with screen" as the
normal done-check. The browser prompt is unchanged (settled question 2). One golden per prompt.

- Acceptance: `canvas_*` flipped to `required` and passing on cloud; `notes_*` no worse than the
  baseline; prompt goldens updated on purpose.
- Verify: `go test ./internal/modules/cc/computer/...`, suite on cloud ×3.
- Files: `cc/computer/prompt.go`, `prompt_test.go`, `testdata/`, `loop.go` (hint cap).

### 5. Fixes from the baseline

Whatever task 2 traced to the loop or the agent (expected suspects: coordinate mapping on scaled
or multi-monitor captures, loop detection stopping legitimate repeated clicks, verify checks).
Each fix gets a unit test that fails first.

- Acceptance: each task-2 failure fixed or reassigned to "model".
- Files: depends on the findings; each fix is its own small PR.

**CHECKPOINT B:** cloud passes every required desktop case 3/3.

### 6. T1 as a case: Wikipedia page → Writer document

`t1_writer_summary`: summarize a fixture article into `~/Documents/<name>.odt` in LibreOffice
Writer (headless Xvfb session), scored from the saved file: it exists, has a title and a summary
whose key terms come from the article, and isn't the article pasted whole.

- Acceptance: the case fails today's way (unsaved or wrong content) if the loop regresses; passes
  3/3 on cloud.
- Files: `tools/ccsuite/cases/t1_writer_summary.json`, a check in `tools/ccsuite/check.go`.

### 7. Stop and take over (MVP B)

`stop_midtask`: the suite stops the task mid-run; no action lands after the stop. A take-over
check: synthetic user input pauses the task within one action, and resume continues it. Fill
what's missing in the indicator (the agent's "controlling" state shown in the island/tray) and the
stop hotkey; most of it exists (durable stop flag, "Paused because you used the computer").

- Acceptance: both checks pass on every run; the indicator shows during a task and clears after
  (desktop screenshot via `scripts/desktop-shot`).
- Files: `tools/ccsuite/cases/stop_midtask.json`, `internal/agent/computer/`, `internal/agent/island/`.

### 8. Live on this box (the user)

Pair an agent built from the branch to the sandbox (:18710), computer control on, and run the
spec's user stories 1–4 on the real Hyprland desktop. Done by the user, with Claude watching the
logs and the audit rows.

**CHECKPOINT C:** the user says whether it feels like the reference.

### 9. Local models

The same matrix on the largest local vision model the dev box has, then the 9B. Prompt fixes per
model family through the prompts-as-text system; no case that passed before may regress.

**CHECKPOINT D:** the user judges the cloud-vs-local table.

## Risks

| Risk | Mitigation |
|---|---|
| Cloud cost of repeated suite runs | Sonnet 5.5, 2 screenshots per call (existing cap), `--repeat 3` only at checkpoints |
| Screenshots of the user's real screen leave the box in task 8 | Only in the user's own live run, on the opt-in cloud slot; the suite uses fixture sessions |
| A vision-first prompt hurts cases that pass on elements today | Separate desktop prompt; browser prompt untouched; task 4's acceptance includes "no worse" |
| Headless sway/Xvfb differ from real Hyprland | Task 8 is on the real desktop; findings feed task 5 |

## Not in this plan

macOS and Windows (own plans after checkpoint D), browser vision-first (settled question 2), the
rich watch-it-work view (V3, later), Teach Jarvis changes.
