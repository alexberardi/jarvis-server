---
name: land-pr
description: Finish a change in jarvis-server and open its PR. Covers the pre-commit gate (scripts/check plus the full race suite), the docs/STATUS.md update (session log, Next line, decision log), the commit and PR conventions, and getting the PR number into STATUS on the same branch before the squash merge. Use it whenever a code or docs change is done and is about to be committed, pushed or turned into a PR, or at the end of a session.
---

# Landing a change

## 1. Branch

Never commit to `main`. Use a short topic branch (`fix/phone-cancel-hangup`, `agent/tray-win`,
`cc/desktop-voice-names`, `feat/…`, `test/…`). In a worktree, `git status` first: the stash is
shared with every other worktree, so use a WIP commit instead of `git stash`.

## 2. Gate (exit codes, not `| tail`)

```bash
scripts/check                       # gofmt on changed files, go vet ×3 OS (contract tag), race tests of
                                    # changed packages, admin SPA tsc+vitest if web/admin changed
CGO_ENABLED=1 go test -race ./...   # CLAUDE.md: required before committing (cgo is test-only)
```

- "Changed" in `scripts/check` means against the merge base with `origin/main`. Use
  `--base REF` for a stacked branch and `--all` to race-test everything.
- If Windows vet fails with `pattern libs/windows-amd64: no matching files found`, run
  `scripts/fetch-sherpa-libs.sh windows-amd64` once. The `cross-os` skill has more.
- `.claude/settings.json` already runs gofmt plus `go vet` on each Go file you edit
  (`scripts/hooks/go-edit.sh`). It does not replace the gate.
- Behaviour visible to a voice user: also run the `pipeline-test` skill. Wire changes: the
  `wire-change` skill.

## 3. STATUS.md (same branch, same PR)

CLAUDE.md: update `docs/STATUS.md` before ending a session. Three places:

- **Session log** (end of `## Session log`): one bullet, bold title, the date, then the PR number
  or branch, then a doc link:
  `- **Cancelled phone calls always hang up (2026-10-13; #115):** found as a CI flake (…). <cause>. <fix>. Unit tests only.`
  Say what was verified and what was **not** (for example "not run on a Windows desktop").
- **Next line** when the plan changed: `- **Next (2026-10-13, after <thing>):** …`.
- **Decision log** table, for any user decision or design choice:
  `| 2026-10-14 | **App names never go into the LLM prompt** (user). <why, where> |`.
- Tick checklist items under "Before the official release" if this closes one.

## 4. Commit

Subject: `<area>: <what changed, lowercase>`. Areas seen: `cc`, `agent`, `voice`, `admin`,
`phone`, `jarvisd`, `harness`, `docs(status)`, `fix(<pkg>)`, `test(<pkg>)`, `feat(<pkg>)`.
The body says why, the user-visible effect and the tests. End it with the attribution trailer
the session specifies (`Co-Authored-By: …`).

## 5. PR

```bash
git push -u origin HEAD
gh pr create --title "<same as the subject>" --body-file /path/to/body.md
```

Body layout (#115): the trigger (the CI run, user report or plan item), then `## Cause` /
`## Fix` / `## Tests`, or a short summary plus `## Tests`. State the gate you ran, for example
"race tests, vet for three OSes, and the agent build". End with the PR attribution line the
session specifies.

**Then put the PR number into STATUS on the branch.** PRs are squash-merged, so main's
subjects end in `(#N)`. Swap the branch name in your session-log entry for `#N`, commit
(`docs(status): #N`), and push. It then lands in the same squash. Without this step, a
separate `docs(status)` commit on main is needed later.

## 6. After pushing

- `gh pr checks --watch`. The CI jobs are build (vet + cross-compile for 4 targets + race),
  admin, native-voice (4 targets), agent (3 OS), service, upgrade and install. A failure in an
  unrelated test is likely a flake: use the `deflake-test` skill, and don't just re-run it.
- Merge only when the user says so.
- Prod (`<prod-host>`) is read-only. Merging does not deploy. Releases follow the `cut-rc`
  skill, and the user applies them.
