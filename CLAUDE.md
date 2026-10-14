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

## Harness (verify without the user)

- `scripts/sandbox up [--name N]`: a seeded jarvisd for this checkout on its own ports and data. Its model is the installed jarvisd's live one, through that instance's LLM API (no extra VRAM); that needs an app client in `~/.config/jarvis-harness/installed.env` (`INSTALLED_APP_ID`, `INSTALLED_APP_KEY`, created once by the user in the installed admin). `source .sandbox/<name>/sandbox.env` for URLs and credentials. Use a unique `--name` per agent; `down --purge` when done. A fresh worktree needs `scripts/fetch-sherpa-libs.sh` first.
- `go run ./tools/pipeline say -v "…" "…"`: a text conversation as the seeded node (speaker = the seeded superuser, devices from `tools/pipeline/homes/avery.json`). `… run [--filter RE] [--repeat N]` plays `tools/pipeline/scenarios/`. Every model call (prompt, tools, reply) is in `.sandbox/<name>/llm.jsonl`.
- `scripts/screenshots --sandbox N [--tiles 1600]`: admin SPA at phone/tablet/desktop × light/dark, with overflow and console reports (`docs/dev/screenshots.md`).
- `scripts/check` before committing; `scripts/installed status` shows who holds the GPU. The installed model has one slot: sandbox calls wait behind the house's.
- Branches: harness work uses `harness/*`; subagent worktrees live in `.claude/worktrees/`.
