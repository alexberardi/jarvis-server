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
- **Prod** (`jarvis@10.0.0.107`) is **read-only** unless the user explicitly says otherwise.
