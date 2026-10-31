# jarvis-server

This repo builds **jarvisd**, the self-hosted Jarvis server, and **jarvis-agent**,
the desktop and Raspberry Pi node. The server is a modular Go program with
embedded storage and a React web app. The Python microservice port is complete;
new work extends this implementation.

This file is the shared guide for coding agents. `CLAUDE.md` imports it. Paths
and commands below are relative to this checkout, including in a worktree.

## Start a task

1. Check `git status --short`, the branch and `git rev-parse --show-toplevel`.
   Work on a topic branch; use an isolated worktree when the existing checkout
   belongs to another task. Never commit to `main`. Harness branches use
   `harness/*`; agent worktrees conventionally live in `.claude/worktrees/`.
   The stash is shared across worktrees: preserve unrelated work without stashing it.
2. Read [docs/STATUS.md](docs/STATUS.md) for current work and the relevant plan
   and decision records. Treat old phase checklists and porting notes as context;
   verify implementation and release claims against code and recent history.
3. Read the applicable scoped guide before editing:
   [internal/agent/CLAUDE.md](internal/agent/CLAUDE.md) for agent work,
   [web/admin/CLAUDE.md](web/admin/CLAUDE.md) for UI work. The latter includes
   the design skills, audit and screenshot requirements.
4. Read the implementation, nearby tests and one existing example of the pattern
   you need. Keep the change focused. Resolve routine choices from the repo;
   surface material uncertainty and ask a specific question when needed.

## Code and documentation map

| Area | Start here |
| --- | --- |
| Server startup, module wiring, CLI and legacy import | `cmd/jarvisd/` |
| Module lifecycle and shared dependencies | `internal/platform/module/` |
| Database, auth helpers, HTTP, settings, queue, blobs, MQTT, logging, engine supervision | `internal/platform/` |
| Server features | `internal/modules/{config,auth,logs,notifications,ocr,llm,stt,tts,recipes,admin,cc}/` |
| Conversation, tool routing, memory, devices, routines, errands, phone | `internal/modules/cc/`; [subsystem index](docs/cc/README.md) |
| Model selection, engines, prompts and remote providers | `internal/modules/llm/`; [LLM overview](docs/llm/00-overview.md) |
| Desktop/Pi node and browser extension | `cmd/jarvis-agent/`, `internal/agent/`; [agent design](docs/agent/DESIGN.md) |
| Audio, wake word, speaker recognition, native speech bindings | `internal/audio/`, `internal/voice/` |
| Admin/member SPA and its Go API | `web/admin/`, `internal/modules/admin/` |
| Updates, installers and release packaging | `internal/update/`, `scripts/packaging/`, `.github/workflows/release.yml` |
| Unit, compatibility and end-to-end evidence | Colocated `*_test.go`, `contract/`, `fixtures/golden/`, `e2e/`, `tools/pipeline/` |

The mobile app, Python Pi node, SDK, Pantry and development agent loop are separate
repos. Make changes in their owning checkout. This guide works for a standalone
server clone; an umbrella checkout also supplies workspace-wide guidance.

## Implementation rules

- Extend existing modules and Go interfaces. Shared dependencies come through
  `module.Deps`; use the platform helpers instead of rebuilding auth, settings,
  HTTP error handling, job queues or logging. Module wiring lives in `cmd/jarvisd`.
- SQLite uses one writer pool and a separate read pool. Use `DB.Write`/`DB.Tx`
  for writes and `DB.Read` for reads. Migrations are embedded goose SQL under
  `internal/modules/<module>/migrations/`; follow the module's naming and scope.
- Preserve household/user isolation at the server boundary. UI visibility does
  not authorize an operation. Route tiers and auth changes follow the merge
  holds below.
- Preserve deployed HTTP, JSON, SSE, MQTT and SDK contracts. For wire changes,
  read the [wire-change skill](.claude/skills/wire-change/SKILL.md) and
  [contract guide](docs/contract/README.md). Legacy parity needs evidence against
  Python; jarvisd-only behavior uses the documented jarvisd-only contract cases.
  Do not silently regenerate goldens or relax assertions.
- The old cut lists in [docs/PLAN.md](docs/PLAN.md) explain deliberate removals.
  Check later decisions before porting something back; do not treat historical
  Python code as the desired behavior for every new feature.
- Keep Go builds `CGO_ENABLED=0`. Native libraries load through purego; cgo is
  enabled only for tests that need it, including the race detector.
- Use `gofmt`, explicit errors and contexts, and the existing `slog` logger.
  Preserve cancellation and shutdown behavior when adding background work.

## Toolchain and setup

Use the Go version in `go.mod` and CI; the current invocation is
`mise exec go@1.27 -- go …`. Dev scripts use Go on PATH, with a mise fallback
in `scripts/lib/go.sh`. A wrong Go on PATH still needs fixing.

A fresh checkout/worktree needs native libraries before builds and tests:

```bash
scripts/fetch-sherpa-libs.sh                 # this host
scripts/fetch-sherpa-libs.sh windows-amd64    # also needed by cross-OS vet
CGO_ENABLED=0 mise exec go@1.27 -- go build ./cmd/jarvisd ./cmd/jarvis-agent
```

For release-target checks, fetch all four targets as CI does:
`linux-amd64 linux-arm64 darwin-arm64 windows-amd64`. Voice integration tests
also need `scripts/fetch-test-models.sh` and
`JARVIS_SHERPA_MODELS="$PWD/.models"`. Missing models can skip tests; a passing
unit suite does not prove native voice behavior.

The SPA uses the Node version in `web/admin/.nvmrc` and its committed npm lockfile.
Run `npm ci` in `web/admin/` when needed. Build it with `npm run build` or use
the sandbox's `--ui` flag; otherwise the server may serve the embedded placeholder.

## Verification

Use TDD for new behavior and bug fixes: reproduce the failure, implement the
smallest correct change, then refactor. Start with focused tests while iterating.

**Before committing, run `scripts/check --all`.** It combines the existing
`scripts/check` gate with the full Go race suite:

- Formatting on changed Go files; cross-OS vet for Linux, macOS and Windows,
  including files behind the `contract` build tag.
- `CGO_ENABLED=1 go test -race ./...`.
- SPA lint/typecheck/Vitest when `web/admin/` changes; browser extension tests
  when its extension or test files change.

Plain `scripts/check` race-tests only changed packages. Its baseline is the
merge base with `origin/main`; use `--base REF` for a stacked branch. Neither
command runs the live contract suite or every E2E/hardware lane.

Add evidence appropriate to the behavior:

| Change | Verification |
| --- | --- |
| Prompts, tool definitions/routing, follow-ups or voice-turn behavior | [pipeline-test skill](.claude/skills/pipeline-test/SKILL.md), with relevant scenarios |
| HTTP/MQTT wire shapes | Module tests plus the relevant `scripts/contract.sh` filter against a dev/sandbox target |
| Web UI | Scoped UI guide, `scripts/e2e web`, screenshots and design audit |
| Desktop UI or computer control | `scripts/desktop-shot`; [computer suite](docs/dev/computer-suite.md) where applicable |
| Voice, platform or hardware integration | Relevant native/platform lane; state which OS/device was actually exercised |
| Documentation | Verify paths, links, commands, imports and consistency against source |

New admin pages or mobile screens need entries in `e2e/coverage.json`: specs/flows
or a reason there are none. Check real exit codes. Report skips, missing
dependencies and pre-existing failures; never weaken tests or checks to get green.

## Runtime harness

Use a unique sandbox name for your task. For UI and deterministic integration work:

```bash
scripts/sandbox up --name <task> --llm scripted --ui
source .sandbox/<task>/sandbox.env
scripts/screenshots --sandbox <task>
scripts/sandbox down --name <task> --purge
```

The sandbox owns its ports/data and uses a fake push relay. The scripted model
requires no installed jarvisd. `--profile empty` tests first-run setup;
`scripts/sandbox node-sim --name <task>` pairs a simulated node.

For real-model checks, `--llm shared` (the default) uses an existing model through
its API. `~/.config/jarvis-harness/installed.env` supplies the installed app
credentials; `JARVIS_HARNESS_ENV` selects another file. `INSTALLED_LLM_URL` and
`INSTALLED_LLM_LABEL` select another server/label. Check `scripts/installed status`
first, keep household traffic ahead of test work, and do not start a competing
model to make a check pass. Follow the pipeline skill for text/voice scenarios;
the sandbox's `llm.jsonl` records model calls.

Detailed lanes and artifacts:

| Harness | Guide |
| --- | --- |
| `scripts/e2e web\|mobile\|all`; reports in `.e2e/<run>/` | [E2E](docs/dev/e2e.md); `all` excludes voice, computer and iOS |
| `scripts/screenshots --sandbox <task>` | [Web screenshots](docs/dev/screenshots.md) |
| `scripts/desktop-shot --sandbox <task>` or `--offline` | [Desktop screenshots](docs/dev/desktop-shots.md) |
| `scripts/mobile-shot --sandbox <task>` | [Mobile screenshots](docs/dev/mobile-shots.md) |
| `scripts/e2e voice`, `computer`, `ios` | [E2E](docs/dev/e2e.md), [computer suite](docs/dev/computer-suite.md) |
| `scripts/sandbox up --voice --sip`, `tools/siplane`, `scripts/freepbx-lane` | [SIP testing](docs/cc/phone-sip.md) |
| Fake or local remote-speech servers | [Remote speech](docs/llm/07-remote-speech.md) |

Voice/computer lanes use real models and can take minutes; follow their explicit
invocation and scheduling rules. Real-Pi audio is overnight only and must leave
the Pi as found. FreePBX L2 runs by hand on Thursdays on the dev box, never a timer.
Use fake/local providers and a test PBX, never paid speech providers or a real carrier.

## Temporary files and cleanup

Cleanup is part of task completion, including failed or interrupted runs.
Keep track of the exact scratch directories, processes and sandbox names you
create; after an interruption, check for leftovers before starting another run.

- Reuse the normal disk-backed Go build/module caches across worktrees. Check
  `go env GOCACHE GOTMPDIR GOMODCACHE` before a large build; do not assign a new
  `GOCACHE` under `/tmp` for each task. An isolated cache is disposable and must
  be removed when its run finishes. Do not routinely run `go clean -cache` or
  `go clean -modcache` against shared caches.
- Use `mktemp -d` and an exit trap for disposable files. When `/tmp` is constrained,
  use a short, disk-backed `TMPDIR`/`GOTMPDIR`, scoped to the command or subshell.
  Keep paths short for Unix-socket tests. Scratch under `~/.cache` still needs
  removal; do not accumulate a new fallback directory on every retry.
- Stop your sandbox with `scripts/sandbox down --name <task> --purge` after
  saving needed evidence. Without `--purge`, its data and binaries remain.
  Stop other processes you launched before removing their files. Remove your
  scratch builds, downloads and redundant `.e2e/<run>/` reports; retain only
  evidence needed for review/debugging and report its path and reason.
- Remove your finished worktree with `git worktree remove` from another checkout
  once commits are pushed and no work or review needs it. Check untracked and
  ignored files first; remove only disposable generated files you own, preserving
  credentials and unfinished work. Do not force removal to bypass that check.
- On a space/quota error, inspect `df -h`, `df -i` and relevant directory sizes
  before retrying. Delete only artifacts you know belong to your finished task.
  Never sweep `/tmp`, purge shared model/package caches or clean another task's
  sandbox/worktree. Age or a familiar filename alone does not establish ownership.

For a foreground check needing disk-backed scratch (Bash):

```bash
(
  set -e
  task_tmp_base="${XDG_CACHE_HOME:-$HOME/.cache}"
  mkdir -p -- "$task_tmp_base"
  task_tmp=$(mktemp -d "$task_tmp_base/jtmp.XXXXXX")
  trap 'rm -rf -- "$task_tmp"' EXIT
  export TMPDIR="$task_tmp" GOTMPDIR="$task_tmp"
  scripts/check --all
)
```

The existing `GOCACHE` is reused. Keep any needed failure evidence outside the
disposable directory before it is removed. A killed shell cannot always run its
trap, so verify cleanup after cancellation as well as normal completion.

## Graphify and documentation

If an existing `graphify-out/graph.json` is available, use Graphify to find
relationships across unfamiliar code, then inspect the current source with
`rg`. From a worktree, pass `--graph /absolute/path/to/graphify-out/graph.json`.
The umbrella's graph includes multiple repos and is a dated snapshot. Do not
rebuild it for routine navigation or treat inferred edges as verified behavior.

Keep `docs/STATUS.md` to current state: open checklists and the next step.
Update it only when that state changes, not for every PR. Record durable user
answers and design choices in individual `docs/decisions/YYYY-MM-DD-<slug>.md`
files using [the decision format](docs/decisions/README.md). Commit messages and
PRs carry change history; there is no session log.

For committing and PRs, use the [land-pr skill](.claude/skills/land-pr/SKILL.md).
The private `jarvis-loop` repo owns the development pipeline; loop-managed work
also follows its review/evidence/landing gates ([integration](docs/dev/agent-loop.md)).

## Merge policy

The user's 2026-10-14 policy permits an agent to squash-merge **its own** PR once
the required CI checks have passed, then check main's CI after the merge.
Wait explicitly for checks on the PR's current head. Never use
`gh pr merge --auto`: it can merge immediately when branch protection has no
required checks.

**Hold for the user's review**, explaining why, when a PR:

- Changes access rules: admin tier tables or `m.handle` registrations, expectations
  in `tiers_test.go`/`tiers_e2e_test.go`, auth/authz code, membership/role checks,
  node/app credentials or pairing approval.
- Changes release signing, update verification or the root update helper
  (minisign, `internal/update`, prestart helper, `.github/workflows/release.yml`).
- Touches prod or anything running there outside a release.
- Weakens or removes a test/CI check to get green, or is marked `needs review`.

Access gates are `TestEveryAPIRouteHasATier`,
`TestNoMemberReachesInstallWideRoutes` and `TestMemberTiersEndToEnd`. They cover
anonymous, member, power user, household admin, superuser and cross-household
access. A new `/api` route needs expectations in the same PR and **always gets
review**. Loosening an expectation or adding a route below `superuser` changes
access rules. UI/client changes that only use existing rules may merge on green
if they do not change those expectations.

Never merge another author's PR (including another agent's; Dependabot follows
its separate dependency rules), and never force-push `main`. Documentation and
test-only PRs follow the same policy. When uncertain, hold for review.

## Operational boundaries

Production is read-only unless the user explicitly authorizes writes. Use the
isolated harness; contract fixtures must never target prod.

Keep secrets, real host details, user data and model transcripts out of commits
and public PRs. `HOSTS.local.md`, environment files and harness credentials are
local and gitignored. Use seeded demo data for published evidence.

Do not stop processes you did not start, take over the user's desktop/audio,
or use their emulator/simulators. The desktop harness uses disposable sessions;
mobile uses its own AVD and must not touch `emulator-5554`; iOS uses
`jarvis-harness-e2e`. Clean up only your task's sandboxes.
