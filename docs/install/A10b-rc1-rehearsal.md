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
stripping the marker there would hand a node raw keys; it needs its own look (logged below).

## Friction list

Fixed (each with a test; branch `worktree-agent-a60c9edce963673b6`):

| # | Where | Problem | Fix |
|---|---|---|---|
| R2 | install.sh / install.ps1 | The script at a release's URL didn't know its release: without `--version` it asked `releases/latest` for `SHA256SUMS` and failed with "the release has no jarvisd for linux-amd64" (latest is the whisper-engines release). A prerelease is never `latest`, so every rc install needed `--version`, and the admin's update command (that URL piped to `sh`, no flags) and install.ps1's elevated re-run (fetches latest) had the same problem. | `7012879` release.yml stamps the tag into the published copies (`RELEASE_VERSION=""` / `$ReleaseVersion = ''`, grep-checked in the step); the scripts default to it unless `--version`/`--base-url` is given; repo copies stay empty (latest). Tests: placeholder present once and stamped by release.yml; install.sh with a fake `curl` asks for the stamped, the `--version`, the `--base-url` and (unstamped) the latest URL. |
| R5 | install.sh | The closing hint said `sh install.sh --uninstall --user`; the one-liner leaves no install.sh on disk. | `a744c2a` piped, it prints `curl -fsSL <this script's URL> \| sh -s -- --uninstall --user`. Test runs the block from a file and piped. |
| R6 | cc → llama-server | **Blocker** (above): the `date-time` marker became a grammar; no dated tool (weather, reminders, calendar) could be called correctly. | `4d90ce3` |
| R7 | admin Update page | Opting in showed an amber "Couldn't check for updates: Not checked yet." until Check now; up to date on rc1 said "the latest release". | `22043d2` opting in runs the check; "the latest pre-release" when it is one. Vitest. |

Logged:

| # | Severity | Problem | Suggestion |
|---|---|---|---|
| R1 | medium (until a stable release) | The documented one-liner `releases/latest/download/install.sh` 404s: GitHub's latest is `engines-whisper-b5454` (the only full, non-pre release), and prereleases are never latest. Worse, `curl -fsSL … \| sh` prints the curl error and **exits 0** (sh ran an empty script); nothing in the script can catch a 404 it never received. | Resolves itself when `v0.1.0` (stable) is published: it becomes latest, and whisper-builds.yml creates later engine releases with `--latest=false`. Until then an rc announcement must use the versioned URL (flag-free since R2). |
| R3 | medium | The admin's suggested install command is the bare script (`curl … \| sh`). On a `--user` install it would try a **system** install (no `/usr/local/bin/jarvisd` → fresh install with sudo) and stop at "another program holds jarvisd's ports". The version half is fixed by R2. | Append `-s -- --user` when the running service is a systemd user unit (admin knows the supervisor via the restart route), or show `jarvisd upgrade` instead; the in-place Update button is the primary path anyway. |
| R8 | low | The small install lane is still serial: Kokoro (TTS) waited behind the 1.24 GB whisper-server CUDA build; TTS ready at 2:01 of a 2:03 install. | Order the small lane voice-first (speaker, TTS, STT model, then engines), or let the smallest go first. |
| R9 | low | Automation `pickAction` (signal reactions) sends tools with the `date-time` marker too (R6's grammar) but runs no date-key injection, so a dated client tool there gets an invented ISO date. | Strip the marker and resolve keys there as the voice/chat engine does, or keep dated tools out of reactions. |
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
