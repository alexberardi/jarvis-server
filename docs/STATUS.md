# Status / handoff log

**Read this first when resuming.** It is the source of truth for *where we are*. The *what and why* lives in [PLAN.md](PLAN.md).

Update it at the end of every working session, and whenever a task finishes or a decision is made.

## Current phase: 5 (command-center) — Phases 1–4 done (whisper CI builds finishing) — Phase 1 control plane done; Phase 0 leftovers: 0.5 G4 ISO guard, 0.6 a few wire rows

### Phase 0 checklist

- [x] Repo created: public, AGPL-3.0, `github.com/alexberardi/jarvis-server`
- [x] **0.1 purego sherpa loader** (`internal/voice/sherpa`). The shared libraries are embedded, extracted to `<base>/sherpa-<hash>/`, and loaded with purego. `CGO_ENABLED=0` everywhere.
  - [x] linux/amd64: `TestStructLayout` matches all 12 mirrored structs against the C header via gcc. EER is **1.25%** (identical to the cgo spike), Kokoro renders, and the smoke test passes (same-voice 0.70 vs cross-voice −0.08).
  - [x] All 4 targets cross-compile with `CGO_ENABLED=0`.
  - [x] **Runtime verified in CI on all 4** (linux amd64/arm64, macos-14, windows-latest). Kokoro and speaker ID work everywhere; the smoke test gives same-voice ≈0.67–0.69 vs cross-voice ≈−0.08 on every OS.
  - Fixes needed: the fetch script avoids bash associative arrays (macOS has bash 3.2), and the tests extract to a persistent dir because Windows locks loaded DLLs.
  - Notes:
    - Windows: no by-value float args, because purego routes through `syscall.SyscallN`. TTS uses `GenerateWithConfig`.
    - The C structs live in `capi.go`. A version bump means replacing the header, updating the fetch script, and running the layout test.
- [~] 0.2 CI: `.github/workflows/ci.yml` has the build job (vet, 4-target cross-compile, race tests) and the native-voice matrix on 4 OS runners. Still to do: release packaging.
- [x] 0.3 `internal/platform` skeleton, all race-tested, cross-compiling for 4 targets, with platform tests in CI on every OS:
  - `config` (data dir, legacy ports + env overrides), `db` (modernc sqlite, one writer + read-only pool, WAL, FKs, per-module goose version tables), `httpx` (FastAPI-shaped errors, JSON decode, recover/log middleware), `module` (Module/Starter, Runner = one server per legacy listener; migrates and starts platform services)
  - `authn` (HS256+RS256 by algorithm family, forged-token test, principals, `Authority` interface for the auth module), `settings` (per-module tables, 5-level cascade, legacy `/settings` router)
  - `queue` (durable jobs), `scheduler` (interval/cron/once triggers on the queue, D26/D27), `blob` (filesystem store), `mqtt` (mochi broker, per-node ACLs D4, Request/response), `mdns` (`_jarvis-config._tcp`; mobile then checks `GET /info` → `service == "jarvis-config-service"`), `engines` (supervised subprocesses), `logging` (stderr + batched sink)
  - Deferred to the first module that needs it: sqlc (queries arrive with Phase 1), broker + mDNS wiring into `serve` (need the auth and config modules), engine downloads (Phase 3).
- [x] 0.4 Contract harness: Go, black-box, build tag `contract` (`contract/`, run `scripts/contract.sh`, env in untracked `.contract.env`). Green against the Python stack on the MBP (72 tests). Fixtures create/cleanup throwaway users, households, nodes, apps via real APIs. Legacy bugs frozen with `LEGACY-BUG` markers (see `docs/contract/README.md`).
- [~] 0.5 Golden fixtures. Exporters live in `tools/golden/` (not beside the Python code: hard stop). **Done:** G1 prompts (88 fixtures: 4 providers × 22 cases incl. a real node's tool set), per-turn blocks + rule constants, G5 parse/sanitize, G2/G3 legacy date context + resolution (reference for the D8 fixes). **To do:** G4 ISO guard, recently-shown block, LLM-proxy/STT/TTS fixtures as their phases need them. See `fixtures/golden/README.md`.
- [~] 0.6 Wire-contract freeze. **Done:** `/services`, `/info`, `/health`, app-ping, validate-node, public-key, token flows, log batch, `/settings`. **To do:** LLM stream frames, PCM stream headers, MQTT topic catalogue (MBP broker on 1884), public plugin endpoints, CC node routes (needs a CC node row via its admin key); fakes: LLM, relay, MQTT node.
- [x] 0.7 Schema baseline: one goose SQLite migration per module under `internal/modules/<module>/migrations/`, from the MBP dev DBs at alembic head (cc 31 tables; auth 9, recipes 16, notifications 3, config 1; ocr/llm/logs/stt/tts have no tables of their own). Notes per module in `docs/schema/`. Import transforms to remember: lowercase legacy enum names, dedupe duplicate system settings rows, copy recipe image blobs, re-embed memories (or NULL + sweep), settings key renames. The MBP has **uncommitted WIP** in jarvis-auth (`signing_keys` table for RS256, kept as `auth_signing_keys`) and jarvis-ocr-service (`003_fix_seed_drift`): the user chose the simplest key setup (see decision log): keep `auth_signing_keys`, one key generated once.
- [-] ~~0.8 SQLite load test~~: dropped. Self-hosted with at most about 10 concurrent clients, so the user confirmed SQLite scale is a non-issue.
- [x] **0.9 command-center deep dive.** All questions answered (D4–D49); decisions folded into every `docs/cc/NN` doc; out-of-repo changes in `docs/EXTERNAL-CHANGES.md`.  Read each subsystem, then ask the user about intent. Write `docs/cc/<subsystem>.md` specs: purpose, behaviour, data, routes, invariants, keep/cut/change. This drives the CC contract tests and the Phase 5 port.

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
- **Hosts** (test targets: this box 10.0.0.122 and the MBP are free to use, including resetting their dev DBs; prod read-only):
  - **dev node:** `pi@jarvis-dev.local` (10.0.0.142). Seeed 2-mic HAT, ALSA `plughw:1,0`. It drops into provision mode when config-service is down.
  - **prod:** `jarvis@10.0.0.107`, under `~/.jarvis/`. **Read-only unless the user says otherwise.**
  - **MacBook Pro:** `alexanderberardi@10.0.0.103`, M2 Max. Full legacy stack (Docker + llm-proxy/whisper/tts native for Metal). Contract-suite target and the Metal test box. No Go toolchain installed (Homebrew present).
  - **this box:** 10.0.0.122, partial stack in Docker (auth, config, settings-server, OCR, infra).
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
| 2026-10-06 | **Hard stop on Python.** Nothing lands in the Python server repos during the migration, so there is no dual maintenance. |
| 2026-10-06 | Prod is the user's home and friends/family instance and tolerates outages. Testing on dev and prod hardware is welcome. SQLite load testing was dropped (at most about 10 clients). |
| 2026-10-06 | purego loader proven on linux/amd64 (same EER as cgo). Windows MT (static CRT) DLLs, so no VC++ redistributable is needed. |
| 2026-10-06 | Engines: a health-failure restart first drains (`HealthCheck.DrainGrace`, called off if the engine recovers). GPUs: auto-detect, always configurable in admin/settings, multi-GPU (per-engine device lists, tensor split). PLAN §3.3. |
| 2026-10-06 | **JWT signing key: one RS256 key, generated once on first run, stored in `auth_signing_keys`** (DB file is 0600 under `~/.jarvis`). No env var to set, no rotation machinery, nothing to keep in sync: jarvisd is the only minter and verifier. Legacy import copies the old `AUTH_PRIVATE_KEY` (and the HS256 `AUTH_SECRET_KEY`, verify-only) so existing sessions survive the cutover. New installs never mint HS256. User: "whatever is simplest". |
| 2026-10-06 | Auth follow-ups settled (user): (1) access tokens stay valid until expiry (30 min) after logout/password change: fine for now. (2) Rotating a revoked app client re-activates it: kept as the one explicit "reissue + reactivate" admin action (there is no separate un-revoke route), documented rather than changed. (3) An admin removing a member from their only household is by design. |
| 2026-10-06 | **Recipes stays out of jarvisd** (user): it is an optional add-on server. It keeps running as jarvis-recipes-server and uses jarvisd over HTTP (auth, registry, OCR). OCR folds into jarvisd. |
| 2026-10-06 | **Recipes ↔ OCR handoff stays queue-based, owned by jarvisd** (user: OCR is long-running, it needs a queue). jarvisd OCR exposes an async HTTP job API (`POST /v1/ocr/jobs` → 202 job_id, `GET /v1/ocr/jobs/{id}`, optional `callback_url` POSTed with app creds on completion, retried) on the durable queue. Recipes changes `from_image.py` to submit over HTTP and adds a callback route that enqueues onto its own RQ. No Redis between them, no pickled jobs. Legacy sync `/v1/ocr/batch` kept. |
| 2026-10-06 | **D7 STT: whisper.cpp `whisper-server` as a supervised engine**, sharing llama-server's engine/GPU/model-manager machinery (one code path for both engine kinds). Priority: neat, maintainable, debuggable. |
| 2026-10-06 | **whisper-server builds come from our own CI** (user: "a for sure"): jarvis-server CI builds whisper.cpp's whisper-server per flavour (Metal on darwin/arm64, CUDA/Vulkan/ROCm/CPU on linux, Windows) and publishes them as release assets with checksums; the engine manager fetches them like llama-server builds, so STT stays one-click everywhere. |
| 2026-10-06 | **Speaker-ID threshold for ERes2Net: 0.43** (D33 calibration). One real speaker (user, 14 clips from the jarvis-dev Seeed mic: 3 enrollment lines, 8 near + 3 far commands) vs 684 impostors (LibriSpeech 40 speakers + 28 Kokoro voices saying commands): genuine min 0.475 (far), median 0.603; impostor max 0.387 (TTS max 0.326). Zero errors anywhere in 0.40–0.45; 0.43 is the midpoint. Caveat: impostors are not same-room household members; the margin gate covers multi-member homes, and the setting stays tunable. Recording kept locally (spikes/, not in the public repo). Re-check once more household voices are enrolled. |
| 2026-10-06 | **D9: jarvis-admin is absorbed into the monorepo** (SPA embedded in jarvisd, backend to Go endpoints; old repo retired). Client-side changes (node-setup, mobile, recipes) go on branches with PRs for user review. Phone: built against a fake Twilio; first real call at prod cutover unless a dev Twilio number appears. |

## Session log

- **2026-10-06:**
  - Surveys of every service.
  - Plan written.
  - Route audit.
  - Prod settings read.
  - Voice spike passed: LibriSpeech eval, plus a listening test and mic test on jarvis-dev.
  - Static-link proof.
  - Repo scaffolded.
  - Task 0.1: the purego loader works on linux; CI added for the other 3 OSes.
  - 0.1 done: the single-binary approach is proven on all 4 OSes.
  - Started 0.9, the CC deep dive. 14 agents are drafting `docs/cc/00..13-*.md`, each ending with questions for the user.
  - All 14 `docs/cc` drafts are done (about 166 questions), reduced into a queue in `docs/cc/QUESTIONS.md`: policies P1–P4, then scope S1–S14, then behaviour, then a minor list.
  - Decisions D1 (Caddy orphaned) and D2 (drop node last-speaker); facts F1 (fastText off in prod) and F2.
  - CC spec questions: P1–P4 → D4–D11; Q-CAT → D12; S1–S14 → D13–D39; B list triaged (4 settled, 56 defaults as D40, 6 asked → D41–D46). Prod facts gathered read-only (errand/phone/attention/memory/routine counts). Notable: phone gateway absorbed into jarvisd (D16), situation matcher cut (D17), learn from voice (D19), account deletion scope (D20), unknown speaker refuses per-user tools (D21), routines run on the node and CC owns definitions (D24, D44), cameras deferred (D29), voiceprints only and recognition off by default (D34, D35).
  - Folded decisions into all 14 docs (D48–D49 loose ends); wrote EXTERNAL-CHANGES.md. Started 0.3: config, db, httpx, module runner, queue (all race-tested, 4-target cross-compile green).
  - 0.3 finished: authn, settings, blob, mdns, mqtt, engines, scheduler (fixed a DST infinite loop in cron), logging; serve wires queue/scheduler/blobs. Platform tests now run on all 4 OSes in CI; Windows caught a blob bug (can't replace an open file), fixed with POSIX-semantics rename (`FileRenameInfoEx`) + FILE_SHARE_DELETE readers. CI all green.
  - Phase 1 started: **config module** done and passing the legacy contract tests unchanged (first parity proof). jarvisd self-registers its listeners in the registry and advertises mDNS.
  - **logs module** written (SQLite store replacing Loki, same API, retention purge, sink for jarvisd's own logs); unit-tested, not yet wired into main (waits for the auth module, which provides `authn.Authority`).
  - **auth module** ported (63 routes) and passing all 62-route auth contract coverage against jarvisd; closes the X-Household-Id join hole (D4/D5). **logs module** wired and passing its contracts. Contract tests now branch on `JARVIS_CONTRACT_IMPL=jarvisd` for decided fixes. Shared `/settings` guards in the platform. jarvisd's own logs land in the log store.
  - Auth follow-ups settled (decision log). Still to build: the D20 per-household leave hook (erase a user's data for a household they leave).
  - **Phase 1 done:** jarvisd serves config, auth, logs, notifications. 45 control-plane contract tests pass against one jarvisd process (only the registry's missing command-center row fails, expected until Phase 5). Notifications: pushes via the relay as durable queue jobs (D31); D20 purge + log de-identification.
  - **Phase 2 done:** OCR module in jarvisd (legacy routes + async job API with callbacks per the recipes handoff decision); 9 OCR contract tests green vs Python and vs the jarvisd binary. jarvisd now serves config, auth, logs, notifications, ocr. Recipes-side change listed in EXTERNAL-CHANGES.md. Note: the MBP's Docker Desktop leaked ~6k CLOSE_WAIT sockets (contract runs likely contributed); user restarted it. If it recurs, make the harness reuse connections.
  - Phase 3 spec written (`docs/llm/`), all LLM questions answered (LD1–LD8): labels with path-keyed engine sharing, OpenAI-compatible remotes only, first-class model manager (install from Hugging Face), images follow labels, prod cutover starts clean (dogfood fresh install), embeddings on llama-server with model-tagged vectors + auto re-embed, vLLM dropped, thinking is the user's choice.
  - **Phase 3 built:** llm module = engine stack (llama-server + whisper-server, pinned builds per OS/GPU flavour, GPU detection, label-keyed shared instances, model manager with Hugging Face installs) + Service/API (frozen stream format, request shaping, regex date keys, queue/callbacks, D8 fixes). End-to-end through jarvisd: here on CUDA (RTX 3080 Ti) and on the MBP on **Metal** (M2 Max, metal build, MTL0 device), one engine serving both labels; stopping jarvisd leaves no llama-server orphan. Fixed from the e2e run: explicit install backend now applies to the assigned labels.
  - whisper-server builds: our CI builds and hosts them (decision log). CUDA 12.x chosen for x64 (widest driver support).
  - Phase 3 verified: all LLM contract tests pass against jarvisd with real engines (CUDA here, MiniLM 384-d embeddings); real Qwen3-4B on the MBP via the model manager on **Metal**: no-think answer in 0.61 s, thinking at ~95 tok/s, `tomorrow_morning` date key extracted, no orphan engine on stop. OCR's LLM vision/validation now call the llm module in memory (`InProcessClient`).
  - **Phase 4 done:** `stt` (whisper-server STT + in-binary ERes2Net speaker ID with the calibrated 0.43 threshold, D33–D38) and `tts` (Kokoro in-binary, sentence streaming, first audio ~200 ms) modules, wired; both get models from the model manager. Verified: Kokoro speech → whisper-server transcription round trip through one jarvisd, all engines in one /v1/hardware view. Open: CC must map a node-reported rejected enrollment take to `low_quality` (stt Q1) and pass validated member ids in-process (Q2); VAD/consistency gate still uncalibrated on real node takes.
- **Next:** whisper-server CI builds (agent finishing; its engine/release.go changes are uncommitted on purpose until reviewed). Then **Phase 5, command-center**, in sub-phases 5a nodes+MQTT data plane → 5b voice pipeline + tool loop + prompts (G1 goldens gate) → 5c memory/errands/phone/signals/routines → 5d mobile surface; then the factory-reset + fresh-provision end-to-end on jarvis-dev with the user.
