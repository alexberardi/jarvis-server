# Status / handoff log

**Read this first when resuming.** It is the source of truth for *where we are*. The *what and why* lives in [PLAN.md](PLAN.md).

Update it at the end of every working session, and whenever a task finishes or a decision is made.

## Current phase: 0 (groundwork)

### Phase 0 checklist

- [x] Repo created: public, AGPL-3.0, `github.com/alexberardi/jarvis-server`
- [ ] **0.1 purego sherpa loader.** Embed the sherpa-onnx and onnxruntime shared libraries, extract them to `~/.jarvis/lib/<hash>/`, and load them with purego.
  - Reproduce the spike's results on linux: ERes2Net EER 1.25% and a Kokoro render.
  - Then darwin/arm64 and windows/amd64 in CI.
  - Fallback: static cgo (proven) plus Windows DLLs.
- [ ] 0.2 CI: `CGO_ENABLED=0` cross-compile matrix for linux/amd64, linux/arm64, darwin/arm64 and windows/amd64, plus `go test -race` on linux.
- [ ] 0.3 `internal/platform` skeleton: httpx, authn, settings, db (modernc sqlite + goose + sqlc), queue, blob, mqtt, logging, mdns, engines. Also the multi-listener runner and the module interface.
- [ ] 0.4 `contract/` black-box suite plus fakes (LLM, relay, MQTT node). It must run green against the **Python** stack first.
- [ ] 0.5 Golden-fixture exporters, which live beside the Python code. Output goes to `fixtures/golden/`. Include byte-exact prompts for the kept Qwen providers.
- [ ] 0.6 Wire-contract freeze tests: `/services`, `/info`, log batch, app-ping, validate-node, LLM stream frames, PCM stream headers, the MQTT topic catalogue, and the public plugin endpoints.
- [ ] 0.7 Schema baseline: goose SQLite migrations per module, from each service's alembic head.
- [ ] 0.8 SQLite load test: voice traffic plus background jobs on Pi-class and mini-PC hardware.

## Environment notes

- **Go:** installed via mise without changing global config. Run `mise exec go@1.25 -- go …`.
- **Spike:** `/home/alex/jarvis/spikes/voice-onnx/` (umbrella repo, untracked). Contents:
  - `main.go` harness with `tts`, `embed` and `eval` subcommands
  - downloaded models under `models/`
  - LibriSpeech clips under `spk/data30` and `spk/data15`
  - write-up in `RESULTS.md`
- **Static-link recipe** (fallback path): link sherpa-onnx v1.13.8 `linux-x64-static-lib` with
  `-Wl,--start-group -lsherpa-onnx-c-api -lsherpa-onnx-core -lkaldi-decoder-core -lsherpa-onnx-kaldifst-core -lsherpa-onnx-fstfar -lsherpa-onnx-fst -lkaldi-native-fbank-core -lkissfft-float -lpiper_phonemize -lespeak-ng -lucd -lssentencepiece_core -lonnxruntime -Wl,--end-group -lstdc++ -lm -ldl -lpthread`.
  This gives a 41 MB binary that needs only libc and libstdc++.
- **Hosts:**
  - **dev node:** `pi@jarvis-dev.local` (10.0.0.142). Seeed 2-mic HAT, ALSA `plughw:1,0`. It drops into provision mode when config-service is down.
  - **prod:** `jarvis@10.0.0.107`, under `~/.jarvis/`. **Read-only unless the user says otherwise.**
- **Prod facts (2026-10-06):**
  - `llm.interface=Qwen3_14B_Compressed`.
  - LLM: Qwen3.8-27B via `llama-server` sidecars, using llm-proxy's REST backend.
  - TTS: Kokoro `bm_george`, speed 1.25.
  - Speaker ID is enabled with a 0.49 threshold (ECAPA).
  - Hardware: Ryzen 9 5950X and 2× RTX 3090, both nearly full.

## Decision log

| Date | Decision |
|---|---|
| 2026-10-06 | Go, a single binary, the `jarvis-server` repo. Reason: install simplicity, not speed. |
| 2026-10-06 | SQLite. Legacy Postgres import is wanted, not required. |
| 2026-10-06 | Embedded durable queue; Redis not required. |
| 2026-10-06 | Voice is greenfield, using sherpa-onnx. Kokoro passed the user's listening test; speaker ID ≥ ECAPA. |
| 2026-10-06 | Cut LoRA/adapters, unused CC routes (PLAN Appendix A), all prompt providers except Qwen 3.x + ChatGPTOpenAI (e2e), settings-server, mcp. |
| 2026-10-06 | Native Windows support → no cgo anywhere: purego plus embedded native libraries, and the pure-Go SQLite driver. |
| 2026-10-06 | Public repo, AGPL-3.0. |

## Session log

- **2026-10-06:**
  - Surveys of every service.
  - Plan written.
  - Route audit.
  - Prod settings read.
  - Voice spike passed: LibriSpeech eval, plus a listening test and mic test on jarvis-dev.
  - Static-link proof.
  - Repo scaffolded.
- **Next:** task 0.1.
