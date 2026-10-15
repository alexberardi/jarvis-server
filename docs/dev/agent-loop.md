# The agent loop (jarvis-loop)

Work on this repo is moving to a pipeline of agents. The pipeline itself lives in the private repo
`alexberardi/jarvis-loop` (local checkout `~/jarvis/jarvis-loop`); its design is `docs/DESIGN.md`
there, settled with the user on 2026-10-14.

## How it uses this repo

- **Stages:** intake → triage (reproduce on `main` with the harness) → ready (by priority, P1–P5) →
  build (a worker agent in its own worktree) → review (a different agent) → land (a script) → done.
  The user steps in only for questions and held merges, in Slack.
- **Verification is this repo's harness:** `scripts/sandbox`, `tools/pipeline` scenarios (text and
  voice), `scripts/screenshots`, `scripts/desktop-shot`, `scripts/mobile-shot`, `scripts/check`.
  A PR from the loop carries its evidence: scenario results and before/after screenshots, from
  sandboxes with the seeded demo data only (this repo is public).
- **Merge gates**, for every PR the loop lands: an external agent's review that judges whether the
  tests cover the change meaningfully, harness verification with its evidence in the PR, and CI.
  The hold list in `CLAUDE.md` (auth, update signing and the helper, prod, weakened tests,
  "needs review") still applies on top: those wait for the user's approval, a Slack button or a
  plain reply in the item's thread.

## What an agent working here should know

- Use a unique sandbox name and your own worktree; never touch the user's checkouts, the user's
  emulator (`emulator-5554`) or processes you did not start.
- A fresh worktree needs `scripts/fetch-sherpa-libs.sh` (and `windows-amd64` for the Windows vet in
  `scripts/check`).
- Never `gh pr merge --auto`: this repo has no required checks, so it merges at once.
