# A10 fresh-install rehearsal: this box (CUDA), 2026-10-07

Admin inventory row A10 and installer row I9 (LD5): a wiped home, `install.sh`, the browser wizard
with real downloads, a chat turn, a voice turn, an upgrade and a rollback, done as a user would. The
MBP (Metal) and Windows rows are still to do.

**Box:** 10.0.0.122, Linux (Omarchy/Arch, systemd), 24 CPU threads, 31 GB, RTX 3080 Ti 12 GB,
ufw active (rules for 10.0.0.0/24 already there from earlier dev work). No passwordless sudo, so
`install.sh --user` (systemd --user unit, `~/.local/bin`, `~/.jarvisd`).

**Builds:** `v0.0.0-rehearsal` = `main` at `b7279a6` with the admin UI embedded (`npm run build`,
`TestEmbeddedUI -tags release_ui` passing). `v0.0.1-rehearsal` and `v0.0.2-rehearsal` = the same plus
the fixes below. Each is a release-shaped directory like the CI `install` job makes (archive, `SHA256SUMS`,
`install.sh`) plus `SHA256SUMS.minisig` signed by a throwaway minisign key that the binaries trust through
the test-only `-X …/internal/update.extraTrustedKey=` ldflag (as `scripts/upgrade-e2e.sh` does), served
with `python3 -m http.server` on 127.0.0.1. `install.sh` got `JARVISD_MINISIGN_PUBKEY=<throwaway key>`.

**What was preserved:** the dev jarvisd (`~/.jarvisd`, started by hand from `~/.jarvisd/run.sh`) was
stopped with SIGTERM (it and its three engine children exited in 1 s), the home renamed to
`~/.jarvisd.pre-rehearsal`, and afterwards renamed back and restarted with the same command, working
directory, stdio and `JARVIS_*`/`ADMIN_API_KEY` environment (checked against `/proc/<pid>/environ`).
The rehearsal home is kept as `~/.jarvisd.rehearsal-2026-10-07` (15 GB; the rehearsal binaries are in its
`rehearsal-bin/`). Linger was `no` before, `yes` after `service install --user`, set back to `no`.

Screenshots (not committed) are named `a10-*.png` below.

## Timeline

| Step | Time | Notes |
|---|---|---|
| Stop dev jarvisd, rename home | 8 s | SIGTERM, engines gone in < 1 s, MQTT/mDNS shut down cleanly. |
| `install.sh --user`, attempt 1 | 0 s | **Refused**: "SHA256SUMS signature is INVALID" (F1). |
| `install.sh --user`, attempt 2 | **4 s** | Download 19.8 MB, signature + checksum, unit written, healthy, setup link printed. No prompts (no TTY; F2). |
| Wizard: Check → Account → Hardware | < 2 s | Doctor all OK (ports, ufw, data dir, legacy containers not on our ports). Hardware: CUDA0 RTX 3080 Ti, 9.6 of 12 GB free; "Looks good". |
| Account | — | First try with `admin@rehearsal.local`: 422 and the form **blanked** with no message (F6). |
| Models: "Install recommended (5)" | **2 min 33 s** | 9.79 GB at ~64 MB/s, strictly one at a time (F7): Qwen3.5-9B + mmproj + llama-server CUDA build 7.36 GB (done +78 s), all-MiniLM + 812 MB, whisper small.en + whisper-server CUDA 1.24 GB (+105 s), Kokoro 350 MB (+152 s), ERes2Net 26 MB (+153 s). Every label ready. |
| Privacy → Done → Dashboard | 12 s | Defaults kept: web search, reader proxy, update checks, push, ambient context, speaker recognition off; memories + learning on. Done step: all six labels ready, prompt provider `Qwen3_5_9B_Compressed`, every check passed. |
| First chat turn (`POST /api/v0/mobile/chat`) | 10 s | **Failed**: llama-server 500, Qwen3.5 chat template (F10). TTS and STT fine. |
| Workaround: install Qwen3-8B, assign live+background | < 2 min | 5.03 GB. |
| Chat turn | **1.06 s** | "The capital of France is Paris." |
| Voice turn from the real jarvis-dev node | 5.7 s wake → reply played | CC turn 462 ms; see "Node" below. |
| `jarvisd upgrade` v0.0.0 → v0.0.1 | 2 s (+6 s models) | Via `JARVIS_UPDATE_API` (F5 blocks the install.sh path from v0.0.0). Snapshot taken, `jarvisd.prev` kept, gate passed. |
| `install.sh` re-run v0.0.1 → v0.0.2 (hand-off) | **2 s** | The fixed hand-off: `jarvisd upgrade` from the flat `--base-url` directory. |
| `install.sh` re-run, same version | 1 s | "already installed and running" (still downloads the 20 MB archive first; F21). |
| `jarvisd upgrade --rollback --user` | instant, live model back in 11 s | v0.0.1 running; one chat sent during the reload got 503 `model_not_loaded` (F22). |
| Restore | 2 min | `service uninstall --user --keep-firewall`, homes swapped back, dev jarvisd restarted (engines healthy in 5 s), node config restored, node back on MQTT. |

## Exercise results

- **Chat** (the app's API, superuser JWT, a node registered with a provisioning token): 1.0–2.7 s per
  turn on Qwen3-8B. Through the real jarvis-dev node (21 client tools reported over MQTT): weather ran the
  node's tool headlessly and answered from it (2.74 s). "What time is it?" answered a made-up "3:15 PM"
  without a tool, and asked for date and time the node's `get_current_time` returned `success: False`
  (F20, node side).
- **TTS** `POST :7707/speak` (app client made on the Connections page API): 171 KB WAV in 1.5 s (first
  call loads Kokoro, 0.7 s). **STT** of that WAV: 0.23 s, exact text.
- **Fake node voice turn** (`fake_node.py` against the media proxy and `/voice/command/stream`): STT fine;
  on Qwen3.5 every turn 202 `stop_reason: "error"`, which the trace recorded as **ok** (F11).
- **`jarvisd doctor`**: all OK, exit 0, before and after the upgrades.
- **Logs / Traces pages** (`a10-qwen35-logs.png`, `a10-qwen35-traces.png`, `a10-v001-*.png`): both
  conversations listed with source, node, what was said and duration; the failing mobile turn red. The Logs
  page showed the turn's lines plus noise fixed below (F12, F14).

### Node (jarvis-dev) without physical access

Done headlessly and reverted; Wi-Fi untouched. The node only enters provisioning (AP) mode at boot with no
`.provisioned` marker or no LAN (`jarvis-node-setup/provisioning/startup.py`), never on server errors, so
the outage and the foreign server were safe: it kept retrying MQTT (refused, rc 5) and HTTP (401).

1. A provisioning token for the node's **existing** `node_id` (`POST :7703/api/v0/provisioning/token`
   with the superuser JWT and `node_id`; allowed because that id isn't registered in the new DB), then
   `POST /api/v0/nodes/register` → a new `node_key`.
2. Over SSH as `pi`: back up `/opt/jarvis-node/config.json`, set `api_key` to the new key, `kill` the
   service's main PID (no sudo needed: the unit has `Restart=always`).
3. The node came up, got refused by the broker once, fetched and **persisted** new MQTT credentials from
   CC, connected (55 s after the kill; Pi Zero start-up).
4. Voice turn: `aplay` on the Pi of a Kokoro rendering of "Hey Jarvis... What is the capital of France?"
   through its own speaker. Wake fired, STT heard "capital of France." (the start is cut by the wake
   window), CC answered in 462 ms, 2.2 s of reply audio streamed and played; wake cycle 8.8 s.
5. Revert: restore the backed-up `config.json` (the node had rewritten its MQTT password into it), kill
   again; it reconnected to the dev jarvisd with its original credentials. Backup and temp files removed;
   the file hash matches the original.

**Cutover note:** this is the headless re-pair for the prod cutover too (ID6 start-clean): register the
same `node_id` on the new install, swap `api_key`, restart. A small node-setup CLI (or `authorize_node.py`
with a JWT instead of the CC admin key, which a fresh jarvisd doesn't have, ID4) would make it one command.

## Friction list

Fixed in this rehearsal (each with a test; on branch `worktree-agent-affdc20cc93bf81d3`):

| # | Where | Problem | Fix |
|---|---|---|---|
| F1 | install.sh | A mise/asdf `minisign` shim with no version selected is on PATH but fails every call; the script said "signature is INVALID" and refused. | `badbd0c` probe `minisign -v`; a non-working minisign counts as absent (checksum + warning). |
| F2 | install.sh `--user` | Ran `sudo jarvisd doctor` twice for a read-only check of the user's own home: no TTY → sudo errors and no doctor output; with a TTY, a password prompt for nothing. | `10d38aa` doctor runs as the user; only `--fix` uses sudo. |
| F3 | install.sh `--user` | Closing "Manage:" line said `sudo jarvisd service restart` and `sh install.sh --uninstall` (which looks in /usr/local/bin). | `10d38aa` prints the `--user` forms. |
| F4 | install.sh hand-off | `jarvisd upgrade` was called without `--user`/`--home`. Harmless in practice (service detection finds the user unit), made explicit. The `badbd0c` message overstates it ("refused"): the observed failure was F5. | `badbd0c` |
| F5 | `jarvisd upgrade` | Ignored `JARVISD_RELEASE_BASE`, which install.sh passes: any `--base-url` install (mirror, offline copy, CI, this rehearsal) could not upgrade: "release v0.0.1-rehearsal: no such release" from the GitHub API. | `e9b9a2c` `update.Source.ReleaseBase`: the one release a flat directory's `SHA256SUMS` names; signature rules unchanged. Verified live v0.0.1 → v0.0.2. |
| F6 | wizard Account | A refused setup (422) blanked the form with no message: `SetupWizard` returned null while `auth.isLoading`, unmounting the step mid-request. | `03c3fb1` gate removed; Vitest for keep-form-and-show-reason. `.local` addresses being refused is legacy email-validator parity, left as is (`a10-2b-account-refused-blank.png`). |
| F11 | traces | A voice turn whose LLM call failed (202 `stop_reason: "error"`) was stored as an **ok** trace; dashboard and Traces showed failures green. | `20d5fee` loop span carries the error; trace status error with the LLM message. |
| F12 | Logs | Leaving the Settings page logged "admin: listing settings failed … context canceled" at ERROR, once per module (8). | `520f28b` stop quietly when the client is gone. |
| F13 | sidebar | "jarvisd vv0.0.0-rehearsal". | `af6bbce` |
| F14 | Logs | mochi logs `Warn("")` for every connection that ends with an error: rows of blank WARNINGs, mostly ordinary hang-ups (EOF). | `b7a6a3e` wrapped handler: a message, and hang-ups at Debug. |
| F15 | login | Signing in landed on Settings (the old admin's home). | `15b4928` → Dashboard. |
| F23 | install.sh `--uninstall` | Left `<bin>/jarvisd.prev` (55 MB) after any self-update. | `f9904f6` |

Logged in the rehearsal; most fixed since (2026-10-07, F7–F24 batch), F17 open:

| # | Severity | Problem | Suggestion |
|---|---|---|---|
| **F10** | ~~blocker~~ **fixed** (ID12, `335ab92`) | The catalog's **recommended** live model on this box, Qwen3.5-9B (`unsloth/Qwen3.5-9B-GGUF` at the pinned revision) with `Qwen3_5_9B_Compressed`, fails **every** chat and voice turn: its chat template raises "System message must be at the beginning" (jarvisd's turn layout has the speaker/ambient/recently-shown blocks, and the engine's retry nags, as extra `system` messages, `turn.go`, `engine.go`, `continue.go`) and "No user query found in messages" for the warmup (system only, `warmup.go`). Qwen3-8B's template accepts the same layout. Legacy never hit it only because its older GGUF upload had a looser template that **silently dropped** every system message after the first, so on legacy the model never saw those blocks either. Nothing renders our message lists through a real template, so goldens (which match Python) passed. | **Fixed: fold + pin (ID12).** Requests to an endpoint flagged `fold_system_messages` (catalog: Qwen 3.5 9B and Qwen 3.8 27B, whose template is strict too; per-label override) get later system messages folded into the adjacent user turn as a `<system>` block and a minimal user turn when there is none, in `llm.Service` (history, prompts and goldens unchanged). Every catalog LLM pins its chat template (`--chat-template-file`, embedded, sha256-checked). Verified on this box with a throwaway jarvisd and the rehearsal's Qwen3.5-9B GGUF (registered with `catalog_id`, `-ngl 2`: the dev jarvisd held ~10 GB): engine `/props` template = the pinned file; `/conversation/start` warmup, a voice turn, mobile warmup + chat turn, and `/node/llm/chat` with a mid-conversation system message ("answer only in French" → "L'herbe est verte.") all succeed; with `fold_system_messages=off` the same calls fail with exactly the two template errors. |
| F7 | ~~medium~~ **fixed** (`f677288`) | "Install recommended" runs the five installs strictly in order, so 400 MB of voice models wait behind the 7.4 GB LLM (TTS ready 2.5 min after the click here; at 10 MB/s it would be ~13 min). | **Fixed:** a second install lane: installs up to 2 GiB (voice, embeddings, small engines) run in `llm.models.install.small`, one at a time, beside the big lane, so at most two downloads share the link. Same commit: `Install` no longer rewrote the whole row after enqueueing (a worker could be put back to "queued"); the flaky `TestInstallCatalogWithProjectorEngineAndAssign` checks the queued install before the queue starts. |
| F8 | ~~low~~ **fixed** (`214d2aa`) | Catalog fit verdicts after install count the installed live model as co-resident even for a model that would replace it ("Qwen 3 4B: Too big"), and call the 44 MB embeddings model "Too big" (143 MB needed). | **Fixed:** catalog and HF verdicts are judged for the labels of the model's kind, without them (an LLM is not counted next to the live/background model it would replace; whisper not next to the stt engine). A kind whose labels all run on the CPU (embeddings, `gpu_layers` 0 by default) is "cpu". And "other programs" double-counted an engine that was still loading at detection (it went by the state's `Since`): engines now report `Started` (process launch). |
| F9 | ~~low~~ **fixed** (`8e54dd3`) | Wizard resume is sessionStorage only: closing the tab mid-download and signing in again lands on the dashboard (Privacy and Done never shown). The dashboard does nudge about models. | **Fixed:** `/api/setup/state` gains `setup_completed` (admin setting `setup.completed`, written when the wizard reaches Done) and `setup_step` (Models once a live model is assigned, else Hardware). Sign-in goes through `/setup`, which resumes there in any tab or browser or forwards to the dashboard; the dashboard shows "Finish setup" meanwhile. An install set up before this has `setup.completed` off, so its next sign-in walks the wizard from Models once. |
| F16 | ~~medium~~ **decided: node required, as legacy** (`74c6588`) | A fresh install has no node, and mobile chat needs a `node_id` in the household, so the app can't chat until a node is provisioned. | **Kept, with a clear error:** legacy requires a node too (`node_id` required, 404 when not in the household; the app disables chat until a node is picked), so no virtual node here. For a household with no nodes the 404 now says chat runs through a node and to add one first (docs/cc/13 §3.1). A server-only chat would be a product change. |
| F17 | info | A node with the old install's credentials retries MQTT and `/node/mqtt-credentials` every few seconds against the new install (WARN lines). Expected at cutover; the re-pair above ends it. | — |
| F18 | ~~low~~ **fixed** (`010f6eb`) | Models step, before anything is installed: a red "No prompt provider … voice turns will fail until you pick one" with a pick-list (`a10-4-models.png`). Alarming on a box where the next click installs a model that supplies one. | **Fixed:** before a live model is assigned the card says the provider comes from the model; the red warning and pick-list appear only when a live model is assigned and nothing resolves (or an override is unknown). |
| F19 | ~~low~~ **fixed** (`084ba62`) | The first chat to a just-registered node that never connected waited 10 s for a `report_tools` answer ("node did not respond in time"). | **Fixed:** `cc_nodes.contacted` (migration 00150, existing rows 1) is 0 from registration until the node's first authenticated request or MQTT reply; report_tools (every caller), chat tool calls, callbacks, media relay and routine run-now treat such a node as offline and fail fast. The nodes list's `online` stays legacy's. |
| F20 | node, **documented** (`e7e28f0`) | Node `get_current_time` failed headlessly (`success: False`); Qwen3-8B answered "what time is it" with an invented time without calling a tool. | **Node-side; documented in EXTERNAL-CHANGES:** jarvisd's dispatch matches legacy's and the command needs no server context; `timezone_command.py` rejects a `location` the model fills with a non-place ("today", "local time", an IANA name) with `Unknown location`. The invented time is the model (no clock in the prompt unless ambient context is on, as in legacy). |
| F21 | ~~low~~ **fixed** (`1c27dcb`) | install.sh re-run with the installed version downloads the whole archive before saying "already installed". | **Already fixed on main** (`0af2884`, after the rehearsal's build): the version is checked after `SHA256SUMS`, before the archive. Remaining case fixed: installed but stopped now reinstalls the service on the binary already there (install.sh and install.ps1); CI runs both against a base holding only `SHA256SUMS`. |
| F22 | ~~low~~ **fixed** (`dd0c513`) | The upgrade health gate passes before engines are loaded; turns in the next ~5–10 s get 503 `model_not_loaded`. | **Fixed:** cc's in-process LLM waits up to 30 s (`cc.ReadyWait`) for a label that is loading (`llm.WithReadyWait`), then 503s saying it was still loading; failed/unconfigured fail at once. The llm HTTP API keeps legacy's immediate 503. |
| F24 | ~~info~~ **fixed** (`fce034b`) | `jarvisd upgrade` from a flat directory prints "downloading" without a percentage (no size in `SHA256SUMS`). | **Fixed:** the flat base already HEADs the archive for its size (`0af2884`); the CLI now prints every 10% (every 10 MB with no size) instead of only 0% and 100%. |

## Not covered

MBP (Metal) and Windows rehearsals; the system-unit (`sudo`) path and its firewall prompt (this box has
no passwordless sudo, and ufw already admitted the LAN); the Android app against the rehearsal install
(the emulator stayed pointed at the dev setup); the admin's Update button (the CLI paths were used).
