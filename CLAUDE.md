# jarvis-server

Single-binary Go rewrite of the Jarvis server stack.

**Start every session by reading `docs/STATUS.md`**, which says where we are and what's next. Then read the relevant sections of `docs/PLAN.md`.

## Rules

- **Update `docs/STATUS.md` before ending a session or a context.** Tick tasks, add to the decision log, and write the session-log entry plus the "Next" line. Commit it.
- **Go toolchain:** `mise exec go@1.27 -- go …`.
- **`CGO_ENABLED=0` always.** Native libraries (sherpa-onnx, onnxruntime) are loaded via purego (PLAN §3.4).
- **TDD** per the umbrella repo's RULES.md. Before committing, run `CGO_ENABLED=1 go test -race ./...`. The race detector needs cgo; that is test-only. Builds stay `CGO_ENABLED=0`.
- **Native libs:** run `scripts/fetch-sherpa-libs.sh` once, and `scripts/fetch-test-models.sh` for the voice integration tests (`JARVIS_SHERPA_MODELS=$PWD/.models`).
- **Behaviour parity is proven by the contract suite (`contract/`) and golden fixtures (`fixtures/golden/`), not by reading code.** A contract test is only valid once it passes against the legacy Python stack.
- **Don't port what's on the cut lists** (PLAN §7, Appendices A and B).
- **Prod** (`<user>@<prod-host>`) is **read-only** unless the user explicitly says otherwise.
- **Host placeholders** (`<prod-host>`, `<mbp-host>`, `<dev-box>`, `<dev-node>.local`, `<pi4-node>`, `<pi5-node>.local`, `<lan-subnet>`, `<user>@…`) appear throughout CLAUDE.md and `docs/`. Real values are in HOSTS.local.md (gitignored, local only).
- **Merging (user, 2026-10-14).** An agent may squash-merge **its own** PR into `main` once every required CI check has passed, then must check main's CI after the merge. **Hold for the user's review** instead, with a note saying why, when the PR:
  - changes authentication, authorization or access tiers (`internal/modules/auth`, `internal/platform/authn`, the admin gateway tiers, node/app credentials, pairing), or anything else that decides who may do what;
  - changes release signing, update verification or the root update helper (minisign, `internal/update`, the prestart helper, `.github/workflows/release.yml`);
  - touches prod or anything that runs there outside a release;
  - weakens or removes a test or CI check to get green;
  - is marked "needs review" by its author.

  Never merge someone else's PR (contributors, Dependabot outside the dependency rules, other agents' open PRs), and never force-push to `main`. Doc-only and test-only PRs follow the normal rule. When unsure which side a PR falls on, hold it.
