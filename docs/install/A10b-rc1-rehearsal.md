# A10b fresh-install rehearsal from the published `v0.1.0-rc1`: this box (CUDA), 2026-10-08

The second A10 rehearsal ([A10-rehearsal.md](A10-rehearsal.md) was the first), this time from the
**real GitHub prerelease** `v0.1.0-rc1` (assets `install.sh`, `install.ps1`, four archives,
`SHA256SUMS`, `SHA256SUMS.minisig` signed with the project key; darwin binary notarized). Wiped home,
the one-liner, the browser wizard with real downloads, chat, a voice turn through the real jarvis-dev
node, doctor, the Update page, the admin's restart button, `upgrade --check`, then a full restore.

**Box:** 10.0.0.122 (Arch/Omarchy, systemd, RTX 3080 Ti 12 GB, ufw already admitting 10.0.0.0/24, no
passwordless sudo), so `--user`. **MBP:** checked, not installed (see "MBP" below).

**What was preserved:** the dev jarvisd (`dev-433f208`, started by hand from `~/.jarvisd/run.sh`, cwd
`~/jarvis/jarvis-server`, stdio `/dev/null` + `~/.jarvisd/jarvisd.log`) was stopped with SIGTERM (it and
its three engines gone in 0.5 s), `~/.jarvisd` renamed to `~/.jarvisd.pre-rc1`, and afterwards renamed
back and restarted with the same command, cwd, stdio and `JARVIS_*`/`ADMIN_API_KEY` environment (byte
compare against `/proc/<pid>/environ`), parent systemd --user as before. Engines healthy 7 s later,
jarvis-dev back on MQTT on the first try. The rehearsal home is kept as
`~/.jarvisd.rehearsal-rc1-2026-10-08` (9.2 GB; the rc1 binary and `jarvisd.prev` from `~/.local` moved
into its `rehearsal-bin/`, since neither existed before). Linger `no` → `yes` (by `service install
--user`) → `no`. jarvis-dev's `config.json` restored byte for byte (sha256 `68e496ae…` before and after).

## Timeline

| Step | Time | Notes |
|---|---|---|
| Stop dev jarvisd, rename home | 0.5 s | SIGTERM; MQTT and mDNS shut down cleanly. |
| The documented one-liner (`releases/latest/download/install.sh`) | — | **404, and `sh` exits 0 having done nothing** (R1). |
| `releases/download/v0.1.0-rc1/install.sh \| sh -s -- --user` | — | **"the release has no jarvisd for linux-amd64"** (R2). |
| `… \| sh -s -- --version v0.1.0-rc1 --user` | **1.8 s** | The mise `minisign` shim (no version set) on PATH: "doesn't run … treating it as not installed", checksum only (F1 working as designed). Unit written, healthy, doctor all OK, setup link printed. |
| Same with a working minisign 0.12 first on PATH, `--force` | 1.6 s | **"SHA256SUMS signature verified."** against the project key. |
| Wizard: Check | 0.3 s | "Everything looks good": 12 ports, ufw, data dir, GPU (573 MB held by others), legacy containers not on our ports. |
| Account | 0.4 s | `admin@rehearsal.example.com`, 201. |
| Hardware | 0.1 s | CUDA0 RTX 3080 Ti, 9.2 of 12 GB free; placement live/background → cuda 0, embeddings → cuda. |
| Models: "Install recommended (5)" | **2 min 3 s** | 9.80 GB, ~80 MB/s, **two lanes** (F7 fix): big lane Qwen3.5-9B + mmproj + llama-server CUDA 7.37 GB done +104 s; small lane all-MiniLM 812 MB +18 s → whisper small.en + whisper-server CUDA 1.24 GB +65 s → Kokoro 350 MB +121 s → ERes2Net 26 MB +122 s. TTS ready at 2:01 (first rehearsal 2:32; R8). |
| Privacy | 1.8 s | Defaults unchanged from the first rehearsal (web search, reader proxy, update checks, push, ambient context, speaker recognition off; memories, learning on). |
| Done → Dashboard | 3.0 s | Six labels ready, prompt provider `Qwen3_5_9B_Compressed` (from the live model), every check passed. **Wizard total 2 min 13 s.** |
| Node re-pair (jarvis-dev) | 44 s to MQTT, 47 s warmup | Same procedure as the first rehearsal; one broker refusal, credentials fetched and persisted, connected. **The node's LLM warmup on Qwen3.5-9B completed** (the F10 "No user query found" error is gone). |
| Voice turn (wake word + question through the Pi's speaker) | wake cycle 8.0 s | STT "is the capital of France." (start cut by the wake window, as before), **Qwen3.5-9B answered "Paris."**: LLM 146 ms (9 195 prompt tokens, cached), first audio byte 290 ms, reply played. |
| Mobile chat (`POST /api/v0/mobile/chat`, the real node) | 3.8 s cold, 2.2 s warm | "Paris is the capital of France."; a joke 2.2 s. **"What's the weather like?" → "Too many tool iterations" (R6, blocker, fixed).** |
| TTS / STT | 0.83 s / 0.27 s | 171 KB WAV; exact transcript. |
| `jarvisd doctor` | instant | All OK, exit 0 (with and without `--home`). |
| `jarvisd upgrade --check` | < 1 s | "jarvisd v0.1.0-rc1 is up to date" (correct: rc1 is the only jarvisd release; a prerelease build considers prereleases). |
| Admin Update page | — | Checks off by default: "Update checks are off". Turned on: amber "Couldn't check for updates / Not checked yet." until Check now (R7); then "jarvisd is up to date · v0.1.0-rc1 is the latest release" (no mention of pre-release; R7). |
| Admin "Restart jarvisd" (Settings) | **6.3 s** | 202, new PID, health ok, toast "jarvisd restarted"; systemd counts it (`NRestarts=1`). |
| Restore | ~4 min | `service uninstall --user --keep-firewall`, homes swapped, dev jarvisd restarted as recorded, node config restored and node restarted, linger off. |

## R6: the recommended model could not use any dated tool (blocker, fixed)

Every weather question on Qwen3.5-9B ran the node's `get_weather_meteo` five times and failed with
"Too many tool iterations". With the live engine on `--verbose` (`llm.live.extra_args`, debug log
level) the rendered prompts showed why:

- The model called `get_weather_meteo` with `resolved_datetimes: ["2026-07-24T00:00:00Z"]` (today is
  2026-10-08), the ISO-date guard nagged once, it tried again with ISO dates, the node answered "No
  forecast data for July 24, 2026", and it kept guessing other July dates.
- The prompt says, emphatically, to write date **keys** (`"today"`), never ISO timestamps. The model
  couldn't: the SDK marks date parameters with `items: {"format": "date-time"}` (CC reads that marker
  to know where to resolve keys), CC forwarded the schema verbatim, and llama.cpp's tool-call grammar
  for Qwen 3.5 (`chat format: peg-native`, XML arguments constrained to their schema) compiled it to
  `date-time-string ::= "\"" date "T" time "\""`. A key was not a legal token sequence.
- A/B straight against the engine, same tool with and without the marker: with it, "What's the
  weather like?" → `["2025-01-15T00:00:00Z"]` and "Will it rain tomorrow?" → `["2025-06-14T…"]`;
  without it, `["today"]` and `["tomorrow"]`. **Qwen3-8B (the dev jarvisd's model) does the same**
  (`["1970-01-01T00:00:00Z"]`, `["2023-10-05T00:00:00Z"]` with the marker), so this was not
  Qwen 3.5-specific; it hits every native-tools catalog model on llama-server.

**Fix `4d90ce3`:** `llmTools` sends the engine a copy of each tool without `"format": "date-time"`
(other formats kept; the cached schemas keep the marker, so date injection and validation are
unchanged; prompts and goldens untouched). Verified live by swapping a build of rc1 + the fix into
the rehearsal install: "What's the weather like?" → "It's 53 degrees and clear sky out there in
Brick, New Jersey…" (6.5 s, cold conversation), "Will it rain tomorrow?" → "No rain expected
tomorrow… overcast with a high of 68" (2.3 s). Not changed: the automation `pickAction` path
(signal reactions), which sends tools the same way but doesn't run date-key injection afterwards, so
stripping the marker there would hand a node raw keys; it needs its own look (R9, since fixed:
`86ce21f`).

## Friction list

Fixed (each with a test; branch `worktree-agent-a60c9edce963673b6`):

| # | Where | Problem | Fix |
|---|---|---|---|
| R1 | releases / install.sh / admin | `releases/latest/download/install.sh` 404'd: GitHub's latest was `engines-whisper-b5454` although whisper-builds.yml created it with `--latest=false` (with no full jarvisd release, GitHub falls back to the newest full release). And `curl -fsSL … \| sh` exited 0 on the 404 (sh ran an empty script). | `2ad2133` engine releases are created as **prereleases** (`--prerelease --latest=false`; an overwrite re-applies it with `gh release edit`), and a workflow step fails if the release is latest anyway; the engine manager fetches by tag and the update check skips non-version tags. The documented command (install.sh header) and the admin's install command now download, then run: `curl -fsSLo install.sh <url> && sh install.sh`, which exits non-zero on a 404. While only rcs exist, `releases/latest` 404s (loudly now): an rc is installed from its versioned URL. **One-time, for the existing release:** `gh release edit engines-whisper-b5454 -R alexberardi/jarvis-server --prerelease --latest=false`. Tests: the header command with a 404ing fake curl exits non-zero (the old pipe form exits 0); every `gh release create/edit` in the workflow carries both flags. |
| R3 | admin Update page | The suggested install command was the bare script; on a `--user` install it would attempt a system install. | `14d5804` jarvisd detects the systemd `--user` unit (its `jarvisd.service` cgroup is under `user@<uid>.service`) and the command passes `--user` (`… && sh install.sh --user` since R1). Tests: cgroup cases, system vs user command. |
| R8 | model installs | The small lane ran one at a time: Kokoro waited behind the 1.24 GB whisper-server CUDA engine. | `312c05e` the small lane runs two installs at once (big lane still one; engine fetches stay serialised per engine). Test: a held whisper engine download, a second small install finishes meanwhile. |
| R9 | cc automation `pickAction` | Sent tools with the `date-time` marker (R6's grammar) and ran no date handling, so a dated client tool got an invented ISO date. | `86ce21f` uses the shared `llmTools` (marker stripped) and runs the voice engine's ISO guard + date injection on the chosen call, in the household's zone, with the keys named in the rule's instruction as the turn's keys (else today). Audit: only the voice/mobile-chat engine and `pickAction` send native tools; the errands planner (menu in the prompt, no schemas), phone, memory, continue and node-plugin calls send none. Test: `"tomorrow"` from the model, an empty date with "tomorrow" in the instruction, and an empty one without (today); the engine copy has no marker. |
| R2 | install.sh / install.ps1 | The script at a release's URL didn't know its release: without `--version` it asked `releases/latest` for `SHA256SUMS` and failed with "the release has no jarvisd for linux-amd64" (latest is the whisper-engines release). A prerelease is never `latest`, so every rc install needed `--version`, and the admin's update command (that URL piped to `sh`, no flags) and install.ps1's elevated re-run (fetches latest) had the same problem. | `7012879` release.yml stamps the tag into the published copies (`RELEASE_VERSION=""` / `$ReleaseVersion = ''`, grep-checked in the step); the scripts default to it unless `--version`/`--base-url` is given; repo copies stay empty (latest). Tests: placeholder present once and stamped by release.yml; install.sh with a fake `curl` asks for the stamped, the `--version`, the `--base-url` and (unstamped) the latest URL. |
| R5 | install.sh | The closing hint said `sh install.sh --uninstall --user`; the one-liner leaves no install.sh on disk. | `a744c2a` piped, it prints `curl -fsSL <this script's URL> \| sh -s -- --uninstall --user`. Test runs the block from a file and piped. |
| R6 | cc → llama-server | **Blocker** (above): the `date-time` marker became a grammar; no dated tool (weather, reminders, calendar) could be called correctly. | `4d90ce3` |
| R7 | admin Update page | Opting in showed an amber "Couldn't check for updates: Not checked yet." until Check now; up to date on rc1 said "the latest release". | `22043d2` opting in runs the check; "the latest pre-release" when it is one. Vitest. |

Logged:

| # | Severity | Problem | Suggestion |
|---|---|---|---|
| R10 | info | `jarvisd service restart` through the admin counts as a systemd restart: `service status` then shows "restarts 1", which reads like a crash count. | Say "restarts (incl. requested)" or subtract requested restarts. |
| R11 | info | `cp` of a new binary over `~/.local/bin/jarvisd` right after `jarvisd service stop --user` failed once with "Text file busy"; a rename-over worked. Only a rehearsal swap does this (install.sh and `upgrade` rename). | — |
| R12 | info | Signing in lands on `/setup`, which forwards to the dashboard (F9 by design). | — |

## MBP (Metal): needs the user's sudo

`install.sh` has no no-sudo mode on macOS: `--user` is refused with "--user is for Linux; on macOS
jarvisd runs as a LaunchDaemon as you" (§2.2's LaunchDaemon decision), and the default path writes
`/usr/local/bin` and `/Library/LaunchDaemons` with sudo. So, as instructed, no workaround: the MBP Metal
rehearsal needs the user at the keyboard for one `sudo`. What was checked without installing (MBP
macOS 26.1, temp dir, removed afterwards; the legacy stack untouched): the curl-downloaded
`jarvisd-v0.1.0-rc1-darwin-arm64.tar.gz` matches `SHA256SUMS`, the binary has **no quarantine xattr**
(curl doesn't set it), runs (`v0.1.0-rc1`), and `spctl -a -t open --context context:primary-signature`
says **accepted, source=Notarized Developer ID**, origin "Developer ID Application: Alexander Berardi
(8H5GA7SX77)", identifier `net.jarvisautomation.jarvisd`, hardened runtime. Nothing was installed.

## Not covered

The MBP Metal install (needs sudo, above) and Windows; the system-unit (`sudo`) Linux path; an upgrade
and rollback from a published release (rc1 is the only one; `upgrade --check` was exercised); the
Android app against the rehearsal install.
