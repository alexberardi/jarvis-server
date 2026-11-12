# Spec: Computer use, vision-first (like Claude's and ChatGPT's)

Status: **draft, awaiting approval** (2026-11-11). Builds on
[COMPUTER-CONTROL.md](COMPUTER-CONTROL.md) (decisions C1–C11 stand) and the element layer
([ELEMENT-LAYER-WORK.md](ELEMENT-LAYER-WORK.md)); it replaces neither. Where this spec and
COMPUTER-CONTROL.md disagree about the loop's observation order, this spec wins.

## Objective

Give Jarvis what Claude's and ChatGPT's computer use feel like: say a goal, Jarvis takes the
screen, looks at it, clicks and types until the goal is met, and the result is checked. It must
work in **any app**, including ones whose accessibility tree is empty or useless, because pixels
are always there.

Today's loop (`internal/modules/cc/computer/loop.go`) is **element-first**: every round reads
the element snapshot, and a screenshot is added only on the desktop or when the snapshot has
fewer than 5 elements. Its prompt is written around element refs. Much of the vision plumbing
already exists and is reused: the `computeruse` action schema (click, double/triple click, drag,
scroll, move, keypress, type, zoom, wait), coordinate adapters (`computeruse.AdapterFor`:
pixels, 0–999, 0–1), capture geometry, and pointer/keyboard input on wlroots and X11
(`internal/agent/input`).

### Decisions (user, 2026-11-11)

| # | Decision |
|---|---|
| V1 | **Vision-first (A) is the build.** The loop works from a screenshot every round and acts by coordinates. Elements, where they exist, are a hint the model may use, never a requirement. |
| V2 | **Judged by results, always (C).** Every slice is accepted by what the task leaves behind (a saved file, a note, an order in the fixture store), checked by `tools/ccsuite` or a live check, never by the model saying "done". Today's failures (T1: reported success, wrong unsaved document) become acceptance cases. |
| V3 | **A minimal watch-it-work layer (B) now, the rich one later.** MVP: an on-screen "Jarvis is using your computer" indicator, live step lines, Stop (hotkey, voice, card), and take over by touching the mouse or keyboard (pause), with Resume. Later: a live screenshot view and step timeline in chat and the app. |
| V4 | **Model: local by default, cloud opt-in through the existing `computer.model` slot** ([feature models](../llm/SPEC-feature-models.md), [cloud slot](../decisions/2026-10-22-cloud-model-slot.md)). **Development order:** prove the loop and tool calls against the cloud model first, so loop bugs aren't confused with model weakness; then bring the local models up with prompt and adapter fixes, measured on the same cases. |
| V5 | **One action schema for every model** (proposed, not objected to): the cloud model calls our `computeruse` actions as ordinary function tools through the existing Anthropic/OpenAI clients, not Anthropic's native `computer` tool or OpenAI's computer-use tool. Testing cloud on our schema tests the calls the local models must make. A native-tool adapter is a possible later quality option. |

### User stories

1. "Open the notes app and write down that the plumber comes Thursday at 10" → a note with that
   text exists in the app, saved.
2. "Summarize this Wikipedia page into a LibreOffice document called Rome" → `~/Documents/Rome.odt`
   exists and holds a summary of that page (T1).
3. A task in an app with **no accessibility tree** (the fixture canvas app, below) is completed from
   pixels alone.
4. While Jarvis works, the user sees that it is working and what it's doing, can say "stop" or press
   the stop hotkey and it stops within one action, and can grab the mouse to take over; Jarvis
   pauses and offers to resume.
5. The household's rules still hold: a blocked app is refused at the agent, risky moments (send,
   buy, delete) still ask (C2, C8), whatever the model outputs.

## What changes

### Loop (cc, `internal/modules/cc/computer/`)

- **Observe:** every round sends a fresh screenshot of the target (window, or screen when the
  target is the desktop), with its geometry. The element snapshot is still taken when the target has
  one and sent as a short hint list (capped), not as the primary view. A target with no elements is
  normal, not a fallback.
- **Prompt:** rewritten for screenshots: the task, the coordinate convention of this model
  (`coordinateHint`), recent steps, the newest screenshot(s) (keep the existing cap of 2 images per
  call), optional element hints with their screen boxes. Kept per model family through the
  prompts-as-text system where a model needs different wording.
- **Act:** the model returns a batch of `computeruse` actions in coordinates (or an element ref when
  it used a hint). The agent maps coordinates through the capture's geometry, enforces rules, acts,
  and returns a new screenshot.
- **Zoom:** `zoom` (exists) lets the model ask for a closer crop when small text matters, without
  sending full-resolution frames every round.
- **Done:** the model's "done" triggers the C4 check: a new screenshot plus the task's success
  check (file exists and contains X, note saved, page shows Y). A failed check is fed back to the
  model as a new round, within the step budget.
- **Budgets:** the existing rounds/minutes budgets and durable stop flag stay. Loop detection
  (re-clicking the same spot, `clickTolerance`) stays.

### Agent (`internal/agent/computer/`, `internal/agent/input/`)

- Capture of the target window or screen with geometry, on every observe, for the first platform
  (Open question 1).
- Pointer and keyboard input through `internal/agent/input`, behind the existing guard
  (`guard.go`): deny list, blocked apps, never Jarvis's own windows or OS auth prompts.
- MVP B on the agent: the indicator while a task holds the screen (island/tray; the existing
  "controlling" indicator is extended, not replaced), the stop hotkey, and pause on user input
  (exists for errands, "Paused because you used the computer").

### Browser

Browser tasks keep going through the extension (C3). See Open question 2 for whether they become
vision-first too.

### Not changing

Errand runtime ownership (C6), per-computer opt-in (C11), rules store and `policy.Decide` (C2),
approvals minted by cc (C8), attended-only desktop control (C9), Teach Jarvis (C10).

## Tech stack

Go (version in `go.mod`, run as `mise exec go@1.27`), `CGO_ENABLED=0` builds. No new
dependencies planned; any new one is an ask-first.

## Commands

```
Build:      CGO_ENABLED=0 mise exec go@1.27 -- go build ./cmd/jarvisd ./cmd/jarvis-agent
Gate:       scripts/check            (scripts/check --all before a PR that touches the loop)
Focused:    mise exec go@1.27 -- go test ./internal/computeruse/... ./internal/modules/cc/computer/... ./internal/agent/computer/... ./internal/agent/input/...
Suite:      go run ./tools/ccsuite run --llm shared --label background --filter 'notes_|canvas_' --repeat 3
Suite cloud: the same with the computer.model slot set to cloud on the instance under test
Sandbox:    scripts/sandbox up --name computer-use --llm scripted --ui
Live:       the dev box jarvisd on :7710 (cloud slot set by the user), agent built from this branch
```

## Project structure

```
internal/computeruse/            action schema, coordinate adapters, geometry, risk, deny (shared)
internal/modules/cc/computer/    the loop, prompt, parse, verify (server)
internal/modules/cc/errands/     computer_task step, cards, stop/approve (server)
internal/agent/computer/         observe/act tools, guard, clipboard, documents (agent)
internal/agent/input/            pointer and keyboard per platform (agent)
tools/ccsuite/                   the computer-task suite; cases/*.json
tools/ccsites/                   fixture apps and sites (notes app; new: canvas app)
docs/agent/SPEC-computer-use.md  this spec
```

## Code style

The repo's existing style: small packages, interfaces at the boundary, comments that say why,
table tests. The loop's observation keeps one shape for every target:

```go
// observe takes a screenshot every round; elements, when the target has them, ride along as
// hints. A target without elements is normal, not a fallback.
o := Observe{RunID: r.run.ID, Target: t.Target, Screenshot: true, Snapshot: t.HasElements}
```

## Testing strategy

- **Unit:** `computeruse` adapters and geometry (exist; extend for each new platform's scaling),
  prompt rendering (golden), parse of coordinate actions per model family, verify checks.
- **Loop with a scripted model:** `loop_test.go` drives coordinate actions against a fake executor:
  stop within one action, pause on input, a failed done-check continues, budget exhaustion ends.
- **Suite (acceptance, V2):** `tools/ccsuite` cases scored from the fixtures' state:
  - existing `notes_wlroots`, `notes_x11` (desktop);
  - new `canvas_x11` / `canvas_wlroots`: a fixture app that draws its UI on a canvas with **no
    accessibility tree**, task completed from pixels;
  - new `t1_writer_summary`: T1 as a case, scored from the saved `.odt`;
  - new `stop_midtask`: the suite presses Stop mid-task; no further action lands.
- **Model matrix:** each suite run records model, slot and pass rate; cloud first, then local
  (V4). Results go in the PR and in a short table in this spec.
- **Live:** the user runs the user stories on this box against :7710 (cloud), then local.
- No wall-clock waits in tests; flakes are bugs.

## Boundaries

- **Always:** gate on `scripts/check` exit codes; keep local operation working (cloud never on
  silently); judge acceptance from state, not model text; keep the guard on the agent side (C8).
- **Ask first:** changing a C1–C11 decision; a new dependency; changing the default
  `computer.model`; anything that sends screenshots off the box by default; changing the deny list
  defaults; merging (loop/safety PRs wait for the user's review per the merge policy).
- **Never:** use prod jarvisd or the prod LLM for development; act on another task's windows or the
  user's real accounts in tests; weaken or skip a suite case to get green; let model text grant an
  approval.

## Success criteria

1. On the first platform, with the cloud model: `notes_*`, `canvas_*`, `t1_writer_summary` and
   `stop_midtask` pass at least 3 of 3 repeats each.
2. The same cases on the largest local vision model: numbers recorded, and each failure traced to
   loop, prompt or model; target agreed after the first measured run (Open question 3).
3. Stop lands within one action, and taking the mouse pauses the task within one action, in both
   the suite and the user's live test.
4. A blocked app is refused by the agent even when the model asks for it (a suite or unit case).
5. The user completes user stories 1–4 live on this box and agrees it "feels like" the reference.

## Open questions (each with a recommendation)

| # | Question | Recommendation |
|---|---|---|
| 1 | Which platform first? | **This box first** (wlroots/Hyprland live, X11 under Xvfb for the suite): fastest build-and-test loop with the user, desktop cases exist. macOS next on the MBP with the user (permissions need clicks), then Windows in the Win11 VM. |
| 2 | Do browser tasks become vision-first too? | **No, not in this spec.** Keep them element-first through the extension (the DOM is rich, and C3's per-site rules depend on knowing the page), but send a tab screenshot every round so the model sees what it acts on. Revisit with suite numbers. |
| 3 | Local-model pass target? | Measure first: set it after the first local run, from the cloud-vs-local gap. |
| 4 | Element hints: always sent, or only on request? | Always, capped (e.g. 40 items with boxes), dropped for a model whose numbers improve without them. |
