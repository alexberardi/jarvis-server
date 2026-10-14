# Status

**Read this first when resuming.** It is the source of truth for *where we are*. The *what and why* lives in [PLAN.md](PLAN.md).

Edit it only when the state changes: an item is ticked or added, or the Next line moves. Decisions go in [decisions/](decisions/), one file each; history is in `git log` and the PRs.

**Cutover gate (user 2026-10-08) — done: prod cut over to jarvisd rc12 on 2026-10-09 ([archive](status/archive-2026-10.md)). Historical status 2026-10-08: jarvisd `v0.1.0-rc3` published (recipes inside, R0–R11 done); node-setup v0.3.2 released; mobile Twilio (#83) and recipes-app image fix (#20) merged; all cutover questions Q1–Q11 answered (runbook §Answers). Remaining: Mac rehearsal on rc3 (user sudo on the MBP), kitchen node → 0.3.2 via legacy admin (user go), then schedule the cutover. Original gate: one coordinated release — jarvisd with recipes ported in (2026-10-08 decision) + node-setup v0.3.2 (#134/#135) + mobile Twilio section; recipes remapped by email.**

## Current phase: 6 (packaging) — pre-releases through `v0.1.0-rc14`, no full release yet; prod cut over to jarvisd (rc12, 2026-10-09). Phases 0–5 done. In parallel: the Go node, `jarvis-agent` (desktop voice, browser control, island, read the screen, dictation; Pi phase 4.1 HAT done, 4.2 next)

### Before the official release (user 2026-10-12): next step

**Installer plan:** [install/01-no-cli-plan.md](install/01-no-cli-plan.md) ties the three "without a terminal" items below into one build order (artifacts per OS, CI jobs, sign-off matrix, open questions Q1–Q8).

Done together with the `v0.1.0` release, not before, because the legacy Python stack stays the supported install until then:

- [ ] **jarvis-installer:** merge the jarvisd landing page (branch `feat/jarvisd-landing`, draft PR). It replaces the compose wizard at installer.jarvisautomation.io with the `install.sh`/`install.ps1` one-liners and a legacy-import pointer (`docs/install/00-installers.md` §1.1). Its one-liners use the stable release URLs, which 404 until a non-prerelease exists.
- [ ] **Server install without a terminal** (user 2026-10-12: "absolutely 0 cli involvement"):
  - Per-OS download installers: macOS `.dmg`, Windows `.exe`, Linux `.deb`.
  - The OS permission prompt, then the browser opens the setup wizard.
  - An "Also use Jarvis on this computer" option adds the agent's steps to the wizard.
  - The one-liners stay as "Advanced".
  - Plan: `docs/install/00-installers.md` §9.
- [ ] **Desktop agent without a terminal:** device-code pairing, a first-run window (pairing, browser
  control, macOS permissions), per-OS installers, and "Add a computer" in admin and app.
  An admin setup checklist ("install the agent" → "add the browser extension", ticks turning
  green live) and public extension listings on AMO and the Chrome Web Store (submit early: review
  takes days). Plan, decisions and open questions are in `docs/agent/DESIGN.md` §13.
  Store submissions: follow `docs/agent/store-listing.md` (kit in `internal/agent/browser/store/`);
  the Chrome store id (`abkbjmelelgelkhneobebgoilejlgbob`) is in `ChromeExtensionIDs`; release the
  agent and re-run `jarvis-agent browser install` so existing hosts admit it.
  **The extension is now 0.2.0** (dictation's `fill` and `click`, 2026-10-13): upload 0.2.0, not
  0.1.x, to both stores, and re-sign the embedded `.xpi` (it is 0.1.0, which answers fill/click
  with `unknown_command`).
- [ ] **Try dictation on a GNOME or KDE Wayland desktop (e.g. Bazzite)**: `jarvis-agent dictation allow`, then type text with accents/CJK and press keys; report whether the dialog remembered the grant and which characters arrived ([agent/DESIGN.md §14](agent/DESIGN.md), the RemoteDesktop portal). Also Hyprland (built-in virtual keyboard) and any X11 desktop (XTEST/EWMH).
- [ ] **Review dictation's defaults** (`docs/agent/DESIGN.md` §14, open questions 1–9; built
  2026-10-13 with confirmation off, 2000-character cap, no clipboard, password fields only when
  named, Enter/Escape for native dialogs).
- [ ] **Clean-install sign-off by the user** (user 2026-10-12: "i'm going to need to do like a clean
  install for what users would see in order to okay everything"). Once pairing, the agent installers
  and the store listings exist: a fresh machine or user account per OS, starting from the landing
  page with no dev tools. The flow is install jarvisd → setup wizard → add a computer (download,
  install, shortcut, pair) → add the browser extension from the store → first voice command. The
  user approves each step or files what to change.
- [ ] **Admin UI usable on phones and tablets** (user 2026-10-12: "on smaller screens it's basically
  unusable"). In progress.
- [ ] **Marketing material for the landing page:** reset the dev server to demo data
  (`docs/dev/demo-reset.md`), then take the admin screenshots (scripted, light and dark), mobile
  captures, the hero clip of the desktop agent (user), and a photo of the Pi node with the HAT.
- [ ] **Docs:** update jarvis-docs, which still describes the multi-service Python stack, for jarvisd: install, upgrade, `import-legacy`, and the Go node. Also re-check this repo's README and `docs/install/` against the released version.

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
- [x] 0.2 CI: `.github/workflows/ci.yml` has the build job (vet, 4-target cross-compile, race tests) and the native-voice matrix on 4 OS runners. **Release packaging done 2026-10-07:** `.github/workflows/release.yml` (tag `v*` publishes; manual run is a dry run) builds the 4 one-file archives + SHA256SUMS and starts each on its own OS; dry run `v0.0.0-dryrun` green on all 4. The embedded admin SPA build step landed in Phase 6; **macOS Developer ID signing + notarization done 2026-10-08** (job `sign-macos`, `docs/install/00-installers.md` §8.4); Windows ships unsigned (ID9).
- [x] 0.3 `internal/platform` skeleton, all race-tested, cross-compiling for 4 targets, with platform tests in CI on every OS:
  - `config` (data dir, legacy ports + env overrides), `db` (modernc sqlite, one writer + read-only pool, WAL, FKs, per-module goose version tables), `httpx` (FastAPI-shaped errors, JSON decode, recover/log middleware), `module` (Module/Starter, Runner = one server per legacy listener; migrates and starts platform services)
  - `authn` (HS256+RS256 by algorithm family, forged-token test, principals, `Authority` interface for the auth module), `settings` (per-module tables, 5-level cascade, legacy `/settings` router)
  - `queue` (durable jobs), `scheduler` (interval/cron/once triggers on the queue, D26/D27), `blob` (filesystem store), `mqtt` (mochi broker, per-node ACLs D4, Request/response), `mdns` (`_jarvis-config._tcp`; mobile then checks `GET /info` → `service == "jarvis-config-service"`), `engines` (supervised subprocesses), `logging` (stderr + batched sink)
  - Deferred to the first module that needs it: sqlc (queries arrive with Phase 1), broker + mDNS wiring into `serve` (need the auth and config modules), engine downloads (Phase 3).
- [x] 0.4 Contract harness: Go, black-box, build tag `contract` (`contract/`, run `scripts/contract.sh`, env in untracked `.contract.env`). Green against the Python stack on the MBP (72 tests). Fixtures create/cleanup throwaway users, households, nodes, apps via real APIs. Legacy bugs frozen with `LEGACY-BUG` markers (see `docs/contract/README.md`).
- [x] 0.5 Golden fixtures (G4 ISO guard and recently-shown were done in 5b: `TestISOGuardGolden`, `TestRecentlyShownGolden`). Exporters live in `tools/golden/` (not beside the Python code: hard stop). **Done:** G1 prompts (88 fixtures: 4 providers × 22 cases incl. a real node's tool set), per-turn blocks + rule constants, G5 parse/sanitize, G2/G3 legacy date context + resolution (reference for the D8 fixes). **To do:** G4 ISO guard, recently-shown block, LLM-proxy/STT/TTS fixtures as their phases need them. See `fixtures/golden/README.md`.
- [x] 0.6 Wire-contract freeze (closed 2026-10-07). Green against the MBP Python stack and against jarvisd (detail and per-module parity in `docs/contract/README.md`): `/services`, `/info`, `/health`, all 62 auth routes, log batch, `/settings`, notifications + the fake push relay, OCR, LLM (stream frames, non-stream, embeddings, enqueue), TTS/STT + voice profiles, CC node auth/registry/provisioning/update tasks/settings requests/K2, the CC media proxy and mobile audio, node-mic enrollment, the voice hot path (shape only) and admin traces, the public plugin endpoints, signals, the MQTT catalogue (every row but 21 context/query, which has no LLM-free trigger; 10 deferred D29, 19 cut D5) with the fake MQTT node; admin §9 (jarvisd-only). Fake LLM: not needed (CC→LLM is in process in jarvisd).
- [x] 0.7 Schema baseline: one goose SQLite migration per module under `internal/modules/<module>/migrations/`, from the MBP dev DBs at alembic head (cc 31 tables; auth 9, recipes 16, notifications 3, config 1; ocr/llm/logs/stt/tts have no tables of their own). Notes per module in `docs/schema/`. Import transforms to remember: lowercase legacy enum names, dedupe duplicate system settings rows, copy recipe image blobs, re-embed memories (or NULL + sweep), settings key renames. The MBP has **uncommitted WIP** in jarvis-auth (`signing_keys` table for RS256, kept as `auth_signing_keys`) and jarvis-ocr-service (`003_fix_seed_drift`): the user chose the simplest key setup (see decision log): keep `auth_signing_keys`, one key generated once.
- [-] ~~0.8 SQLite load test~~: dropped. Self-hosted with at most about 10 concurrent clients, so the user confirmed SQLite scale is a non-issue.
- [x] **0.9 command-center deep dive.** All questions answered (D4–D49); decisions folded into every `docs/cc/NN` doc; out-of-repo changes in `docs/EXTERNAL-CHANGES.md`.  Read each subsystem, then ask the user about intent. Write `docs/cc/<subsystem>.md` specs: purpose, behaviour, data, routes, invariants, keep/cut/change. This drives the CC contract tests and the Phase 5 port.

## Environment notes

- **Go:** installed via mise without changing global config. Run `mise exec go@1.27 -- go …`.
- **Spike:** `~/jarvis/spikes/voice-onnx/` (umbrella repo, untracked). Contents:
  - `main.go` harness with `tts`, `embed` and `eval` subcommands
  - downloaded models under `models/`
  - LibriSpeech clips under `spk/data30` and `spk/data15`
  - write-up in `RESULTS.md`
- **Static-link recipe** (fallback path): link sherpa-onnx v1.13.8 `linux-x64-static-lib` with
  `-Wl,--start-group -lsherpa-onnx-c-api -lsherpa-onnx-core -lkaldi-decoder-core -lsherpa-onnx-kaldifst-core -lsherpa-onnx-fstfar -lsherpa-onnx-fst -lkaldi-native-fbank-core -lkissfft-float -lpiper_phonemize -lespeak-ng -lucd -lssentencepiece_core -lonnxruntime -Wl,--end-group -lstdc++ -lm -ldl -lpthread`.
  This gives a 41 MB binary that needs only libc and libstdc++.
- **Hosts** (test targets: this box <dev-box> and the MBP are free to use, including resetting their dev DBs; prod read-only; real addresses are in HOSTS.local.md, gitignored, local only):
  - **dev node:** `pi@<dev-node>.local` (<dev-node-ip>). Seeed 2-mic HAT, ALSA `plughw:1,0`. It drops into provision mode when config-service is down.
  - **prod:** `<user>@<prod-host>`, under `~/.jarvis/`. **Read-only unless the user says otherwise.**
  - **MacBook Pro:** `<user>@<mbp-host>`, M2 Max. Full legacy stack (Docker + llm-proxy/whisper/tts native for Metal). Contract-suite target and the Metal test box. No Go toolchain installed (Homebrew present).
  - **this box:** <dev-box>, partial stack in Docker (auth, config, settings-server, OCR, infra).
- **Prod facts (2026-10-06):**
  - `llm.interface=Qwen3_14B_Compressed`.
  - LLM: Qwen3.8-27B via `llama-server` sidecars, using llm-proxy's REST backend.
  - TTS: Kokoro `bm_george`, speed 1.25.
  - Speaker ID is enabled with a 0.49 threshold (ECAPA).
  - Hardware: Ryzen 9 5950X and 2× RTX 3090, both nearly full.

## Decisions

One file per decision in [decisions/](decisions/), named `YYYY-MM-DD-<slug>.md` (newest last in
`ls`). A new decision is a new file, so parallel branches never conflict over it; see
[decisions/README.md](decisions/README.md).

## History

There is no session log any more: what changed and why lives in the squash commits and PR
descriptions (`git log`, `gh pr list --state merged`). The old log, to 2026-10-14, is in
[status/archive-2026-10.md](status/archive-2026-10.md).
