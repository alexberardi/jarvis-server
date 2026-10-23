# Go Pi voice node: research and spikes

Status: research, 2026-10-09 (overnight); since then a build log. Phases 2, 3 and 4.1 of §7 have
landed (§3.7–§3.12); the Python node is still the supported Pi node. Copied here from `jarvis-host-agent/docs/node/` on
2026-10-10, when the agent moved into jarvis-server (phase 2 of §7). The spike code stays in that
repo under `spikes/pinode/` (a separate Go module); `ort/`, `oww/` and `dsp/` were ported here as
`internal/voice/ort`, `internal/voice/oww` and `internal/audio` (the decimator).

**Goal.** Replace the Python `jarvis-node-setup` node with the Go `jarvis-agent` core plus a voice
pipeline. Pantry packages and the built-in commands run in the Python sidecar (DESIGN.md §6).

**Path shorthand.** `N:` is `jarvis-node-setup` at 0.3.4 (`7b762d3`). `S:` is
`jarvis-server/internal/modules/cc/` at `67c3a13`, unless shown otherwise.

## TL;DR

- **Wake word in Go is solved.** openWakeWord's three ONNX models run on onnxruntime's C API via
  purego, with no cgo, using the same `libonnxruntime.so` 1.28.2 that jarvisd already embeds.
  - Scores match Python oww 0.6.0 to within **3e-8**, frame for frame, on all 6 test clips. The
    peak frames are identical.
  - Live through ALSA loopback: **3/3 wakes and 0 false fires** over 67 s.
- **Go is cheaper than Python on the same Pi 4** (same governor, real mic):
  - CPU: **20.5% of one core vs 29.0%**.
  - Memory: **40 MB RSS vs 217 MB** for the same work (capture, resample, oww).
  - With the clock capped at 600 MHz (a Zero 2 W proxy), the oww pipeline alone costs **49% vs
    66%** of a core.
- **Audio I/O: ALSA via purego (dlopen libasound) works, with no cgo.**
  - Capture from the HAT's `dsnoopmic` at 48 kHz, decimated to 16 kHz in Go: **3.6%** of a core,
    **0 xruns**.
  - Playback: 1.5–2.6%.
  - Shelling out to arecord/aplay costs about the same (3.0%), so it is a valid fallback.
  - Direct ALSA to the HAT works with PulseAudio stopped, while dsnoop capture runs.
- **HAT peripherals work from a fully static binary.** The APA102 LEDs (raw spidev) and the GPIO17
  button (gpiochip cdev) are pure Go.
- **The Python quick win is real, and bigger than estimated: −84 MB RSS.**
  - It needs **both** changes: stub oww's training-only import, and replace `scipy.signal.resample_poly`.
  - The stub alone saves only 25 MB in the wake-loop process, because `resample_poly` still pulls
    in scipy.
- **Estimated footprint on a Zero 2 W:**

  | Configuration | RSS |
  |---|---|
  | All-Go node | ~55 MB |
  | Go node + sidecar | ~125 MB |
  | Go node + sidecar + PulseAudio | ~160 MB |
  | Python node + PulseAudio today | ~340–420 MB |

- **Recommendation: build it.** Order the work as:
  1. The Python quick-win PR.
  2. Move the agent into jarvis-server (`cmd/jarvis-agent`), so it can reuse `internal/update` and
     the embedded ORT.
  3. Desktop voice, Linux first.
  4. The Pi: HAT, install, provisioning.
  5. The long tail of MQTT verbs.

---

## 1. Voice protocol checklist

Tags:
- **MVS**: needed for one end-to-end voice turn.
- **later**: port, but not for the first turn.
- **drop**: do not port.

**The MVS needs no MQTT.** Every authenticated request refreshes `last_seen` (debounced 60 s,
`S:authz.go:86-98`), so voice turns alone keep a node "online".

### 1.1 One voice turn, in wire order

| # | Step | Python node | jarvisd | Tag |
|---|---|---|---|---|
| 1 | Auth header `X-API-Key: {node_id}:{node_key}` on every call. A missing header gets **400**; a bad key gets 401. | `N:clients/rest_client.py:51-58` | `S:authz.go:40-75` | MVS (in Go core) |
| 2 | Discovery: the `jarvis-command-center` URL from config-service | `N:utils/service_discovery.py:54-182` | `internal/modules/config/config.go:125-128` | MVS (in Go core) |
| 3 | Capture 48 kHz S16_LE mono, 3840-sample (80 ms) chunks from `dsnoopmic`; 4 s ring buffer | `N:core/audio_bus.py:37-265`, `N:scripts/voice_listener.py:82-83,281` | – | MVS |
| 4 | Wake: resample 48k→16k (`resample_poly(1,3)`), oww `predict` on 1280-sample chunks; fire at score > `wake_word_threshold` (0.4); 8 s debounce; reset oww after a fire | `N:core/wake_loop.py:107-108,379-385`, `N:core/wake_detector.py:50-190` | – | MVS |
| 5 | Wake clip: the last ~2 s of consumed chunks (25 × 80 ms), 48 kHz WAV | `N:core/wake_loop.py:253-255,602-608` | – | MVS |
| 6 | Wake ack: cached greeting WAV, else a bundled `sounds/wake/*.wav`, else TTS "Yes?"; wake paused while it plays | `N:core/wake_response.py:149-248` | – | MVS (a bundled WAV is enough); the Pi plays the prefetched greeting, else the chime (#427) |
| 7 | `POST /api/v0/conversation/start` on a goroutine: `{conversation_id: uuid4, node_context:{timezone,…}, available_commands:[], client_tools:[]}`. The response only arrives after warmup (≤120 s server side); body ≤1 MiB. | `N:clients/jarvis_command_center_client.py:575-714`, `N:core/wake_loop.py:721-728` | `S:warmup.go:97-174` | MVS |
| 8 | Record: RMS endpointing. Threshold = clamp(median pre-wake RMS × 2, 200, 1000), else 300. Stop after 0.8 s below threshold; min 2 s, max 7 s. Write a 48 kHz WAV. | `N:scripts/speech_to_text.py:260-404`, `N:core/vad_thresholds.py:99-134`, `N:core/wake_loop.py:761-776` | – | MVS |
| 9 | STT: `POST /api/v0/media/whisper/transcribe`, multipart: `file` (command WAV), `speaker_audio` (wake clip + command, **must start with the wake audio**), `conversation_id`. Response `{text, segments, speaker{user_id,confidence}, affect}`. | `N:stt_providers/jarvis_whisper_client.py:106-144` | `S:media.go:193-281`, `internal/modules/stt/transcribe.go:22-34` | MVS |
| 10 | Drop non-speech results (empty, `[BLANK_AUDIO]`, `*…*`, `(…)`) | `N:core/voice_filters.py:36-68` | – | MVS |
| 11 | Join the warmup (≤10 s), then `POST /api/v0/voice/command/stream {voice_command, conversation_id, turn_source:"wake", wake_confidence, pre_wake_speech_seconds?}` | `N:...center_client.py:138-255` | `S:voice_routes.go:32-65,224-256` | MVS |
| 12 | **200**: `audio/raw` s16le, headers `X-Audio-Sample-Rate` (24000 from Kokoro), `-Channels`, `-Sample-Width`, and `X-Assistant-Message` (URL-quoted). Stream it to playback; 8 s inter-chunk idle timeout. | `N:utils/command_execution_service.py:398-541`, `N:core/platform_abstraction.py:190-350` | `S:voice_routes.go:407-474` | MVS |
| 13 | **202**: JSON. `not_for_me` → stay silent. `complete` → speak `assistant_message` via `POST /media/tts/speak/stream` (over 200 chars) or `/media/tts/speak` (WAV). `tool_calls` / `validation_required` → later. | `N:utils/command_execution_service.py:733-975,1486-1525` | `S:voice_routes.go:150-158`, `S:media.go:53-139` | MVS (complete, not_for_me) |
| 14 | `POST /api/v0/conversation/end {conversation_id}`. Python skips this after barge-in and boot warmups; Go should always send it. | `N:core/follow_up_loop.py:424-428` | `S:warmup.go:310-323` | MVS |
| 15 | Error phrases ("I can't reach my server right now." and so on), with a fallback chime | `N:core/wake_transcription.py:185-196` | – | MVS (chime only) |
| 16 | Follow-up window after **every** reply not marked terminal (`end_of_exchange`) or `not_for_me`: 4 s, −2 s per iteration (floor 3 s), ≤5 iterations, quiet-wait, onset detection, noise and self-echo filters, blocking `POST /voice/command` with `turn_source:"follow_up"`, `follow_up_iteration` | `N:core/follow_up_loop.py:97-428`, `N:scripts/speech_to_text.py:455-691`, `N:core/conversation_filters.py` | `S:voice_routes.go:171-220` | **done for the desktop agent (§3.9); widened by the follow-up gate (§3.10)** |
| 17 | Barge-in: local only. oww score > 0.04 plus an RMS gate, 2 consecutive chunks → cancel playback and close the stream. Nothing is sent to the server. | `N:core/barge_in.py:100-359` | – | **done in the Go agent (§3.13)** |
| 18 | Client tools: `tool_calls` → run → `POST /voice/command/continue[/stream]` | `N:utils/command_execution_service.py:977-1089` | `S:voice_routes.go:260-339` | **done for the desktop agent (§3.8)**, blocking continue only |
| 19 | Validation: speak the question (+ "Your options are: …"), re-listen with `listen()`, `POST /voice/command/continue` with `{tool_call_id: "validation-response", output: {answer, parameter_name, question, options}}` | `N:core/wake_transcription.py:204-231`, `N:clients/jarvis_command_center_client.py:362-412` | `S:engine.go:339-366`, `S:continue.go` | **done for the desktop agent (§3.9)** |
| 20 | Processing ack ("One moment."), 3 s delayed ack `/voice/acknowledge`, next-greeting prefetch `/wake-response` | `N:core/wake_response.py:73-80,294-380`, `N:utils/command_execution_service.py:610-620` | `S:voice_routes.go:489-541` | prefetch **done on the Pi** (#427); the acks later |
| 21 | Pre-wake speech seconds hint (RMS over a 5 s ring) | `N:core/wake_loop.py:113-116,361-367,523-529` | `S:hints/` | **done in the Go agent (§3.16, 17c)** |
| 22 | Tentative wake (duck music in [0.2, 0.4)), not_for_me cool-down, auto threshold | `N:core/wake_loop.py:224-456`, `N:core/wake_calibration.py` | – | tentative wake **done in the Go agent (§3.16, 17c)**; cool-down and auto threshold **built (§3.20, 17d)** |
| 23 | Boot warmup that sends a real "hello" LLM turn | `N:scripts/main.py:390-424` | – | drop |

**Wake verification is server-side only.** There is no `wake_unverified` logic on the node.
- The server slices the **leading 2.2 s** of `speaker_audio`, re-transcribes it, and fuzzy-matches
  it against `voice.wake_verification_phrase` (`S:media.go:32,236-281`). The turn waits up to
  1.2 s for the verdict (`S:turn.go:24,296-335`).
- The mode defaults to `off` in jarvisd (`S:voice_deps.go:194`); node comments say prod used `bias`.
- What this means for Go: always send `speaker_audio` starting with ~2 s of wake audio, plus
  `conversation_id`. The Python follow-up path omits `conversation_id`
  (`N:core/follow_up_loop.py:260-262`), which loses speaker identity; don't copy that.

### 1.2 MQTT

**Connection:**
- Client id `jarvis-node-{id}`, `clean_session=false`, credentials from `GET /node/mqtt-credentials`.
- Subscribes to `jarvis/nodes/{id}/#` and `jarvis/auth/+/ready`, QoS 1
  (`N:scripts/mqtt_tts_listener.py:68-105,2677-2850`).
- The Go core already does this (narrower: `/commands` only).

**Broker ACL:** the node may publish only `.../command-data/{op}/response/{cid}` and
`.../context/query/response/{cid}` (`internal/platform/mqtt/auth.go:48-75`). Not
`.../context/operations/response/{cid}`: the Go node answers `context/operations` (as N: does), but the
broker drops that answer until the ACL allows it (an access change, held for the user; §7 5.7).

**Verify before acting.** jarvisd never sends `trusted`, so the node verifies `action`,
`measure_ambient_noise` and `tool_call` with `POST /commands/{rid}/verify` (`S:bus.go:96-150`).

| Verb / topic | Python | jarvisd publisher | Reply sink | Tag |
|---|---|---|---|---|
| `report_tools` | `:454-551` | `S:nodetools.go:100` | `POST /mobile/node-tool-reports/{rid}` | done in Go core |
| `tool_call` | `:830-923` | `S:mobile_chat.go:598`, `S:errands.go:208`, `S:signals_reactions.go:348` | `POST /device-control-results/{rid} {"output":…}` | done in Go core |
| `action` (mobile action / device control) | `:192-298` | `S:commands.go:44`, `S:smarthome_control.go:116` | `/device-control-results/{rid}` (top-level `{success, error, input_required}`) | done in Go core, every profile (§3.18) |
| `callback` | `:301-422` | `S:callbacks.go:460` | `GET /callbacks/{job}`, `POST …/result` | done in Go core, every profile (§3.18) |
| `routine` (carries the full definition) | `:926-982` | `S:routines_run.go:104-110` | `/device-control-results/{rid}` | later |
| `enroll_voice` / `verify_voice` | `:554-803` | `S:media.go:495` | `/media/whisper/voice-profiles/*`, `/mobile/voice-profile-results/{rid}` | done in Go core (Pi profile only; a desktop's speaker is the signed-in user) |
| `update_node_config` (settings push; restart only for `wake_word_model`) | `:1092-1177` | `S:commands.go:91` | – | done in the Pi profile (§3.14) |
| `preview_led_pattern` | `:1180-1221` | `S:commands.go:112` | – | done in the Pi profile (§3.14) |
| `measure_ambient_noise` | `:1257-1383` | `S:ambient.go:64` | `POST /nodes/{id}/ambient-noise-measurements/{rid}/result` | done in the Pi profile (§3.14) |
| `toggle_command`, `invalidate_device_cache`, `device_removed` | `:1005-1254` | `S:smarthome*.go` | – | `invalidate_device_cache`, `device_removed` done in the Pi profile (§3.17); `toggle_command` done in Go core, every profile (§3.18) |
| `/k2/provision` (K2 AES key) | `:1622-1669` | `S:settingsreq.go:316` | `GET …/k2/provision/{rid}`, `POST …/k2/ack/{rid}` | built for the desktop agent (`internal/agent/nodesettings`, [DESIGN §16](DESIGN.md)); the Go Pi node reuses it |
| `/settings/request` (AES-256-GCM snapshot with K2, AAD `{node}:settings:snapshot`), plus a backstop poll on connect | `:1837-1876`, `N:services/settings_snapshot_service.py:749-857` | `S:settingsreq.go:92` | `PUT …/settings/requests/{rid}/snapshot` | built for the desktop agent (`internal/agent/nodesettings`), including the app's secret Sync as source (`include_values` + user) and target (`settings:secrets` push), pinned by `sync_golden.json`; the Pi adds its node_config and hardware |
| `/config/push`, `/routines/sync` | `:1769-1812` | `S:settingsreq.go:424`, `S:routines.go:867` | GET pending / ack; routines: `GET /nodes/{id}/routines` | config push built for the desktop agent (`internal/agent/nodesettings`: secrets, command_registry); routines sync done in the Pi profile (§7 5.7) |
| `/package-install`, `-uninstall`, `-revert`, `/test-install` | `:1940-2020,2207-2224` | `S:packages.go:96-451`, `S:testinstall.go` | verify + results | built (§7 parity 5.6; apt/post_install and test-install on the Pi profile only) |
| `/factory-reset` (tracked with `task_id` + `X-Reset-Token`, or legacy) | `:2023-2204` | `S:reset.go:84,189` | `POST /nodes/factory-reset/{task}/status` | done in the Pi profile (§7 5.7) |
| `/device-list`, `/device-scan`, `/device-state` | `:1879-1917,2357-2374` | `S:smarthome*.go` | results POSTs | done in the Pi profile (§3.17) |
| `/bluetooth-*` (scan, pair, disconnect, discoverable, release, auto-connect) | `:2227-2354` | `S:smarthome.go:102-111` | results POSTs | done in the Pi profile (§3.15) |
| `jarvis/auth/{provider}/ready` | `:1505-1596` | `S:smarthome_oauth.go:602` | `GET /oauth/provider/{p}/credentials` | done in the Pi profile (§3.17) |
| `/command-data/{op}`, `/context/{op}` (the node **publishes** responses) | `:2484-2501` | `S:commanddata.go:119` | MQTT publish | command-data built for the desktop agent (`nodesettings.CommandData` over the sidecar's packages, pinned by the Python node's own handler, [DESIGN §16](DESIGN.md)); context done in the Pi profile (§7 5.7) |
| `tts`, `train_adapter`, `/camera-credentials`; the button publishes (denied by the ACL) | various | none or no route | – | drop |

### 1.3 Heartbeat

- Python sends `POST /api/v0/admin/nodes/heartbeat` 10 s after start, then every 300 s.
  Body: `{version_info{version, install_mode, git_sha, install_dir, release_track}, is_busy,
  protocols[], needs_k2, memory{…}, thread_status{…}}` (`N:scripts/mqtt_tts_listener.py:2590-2648`).
- The response may carry `pending_update{task_id, target_version}`, which is the update trigger
  (`S:nodes.go:174-255`, `S:updates.go:167-170`; tarball installs only).
- `is_busy` is always false: `mark_active()` has no callers.
- The Go core already heartbeats every 60 s. It still needs `pending_update` handling and the
  `install_mode` question (open question 6).

### 1.4 Doc/code discrepancies found

These are worth fixing upstream regardless of Go:
- Follow-up STT sends no `conversation_id`.
- Button MQTT publishes are denied by the ACL.
- `bluetooth-release` and `bluetooth-auto-connect` are documented as dead but are published.
- `camera-credentials` has no route.
- `N:core/echo_cancel.py:22-24` claims capture goes through Pulse; it goes through dsnoop (so
  N:'s echo cancel never engages on a Pi; the Go node's 17b gives Pulse a source on the same
  dsnoop instead, §3.16).
- Doc 01 §7.12 gives the TTS fallback rate as 16000/22050; jarvisd always sends 24000.
- `is_busy` is dead.
- A 202 `complete` with an empty message makes the node say "Task completed."
- Follow-up (found 2026-10-10, §3.9): the window opens after **every** reply, not only when the
  server expects an answer; `wait_for_input` only changes the spoken message and stops the tool
  loop. `on_response_complete` runs only for the *wake* exchange's result (a follow-up's
  "play X" never starts), and the MQTT `tool_call` path drops it. Lone-word follow-ups outside a
  small allowlist ("Kitchen.") are discarded as noise (the Go agent sends them: user decision
  2026-10-10). Validation answers are transcribed with no
  `conversation_id` and no speaker audio.
- jarvisd's streamed 200 reply carries no `end_of_exchange` (Python's neither), so a terminal
  reply spoken on the stream route can't close the follow-up window; only 202/blocking replies can.

---

## 2. Decisions

| # | Decision | Recommendation | Why (evidence) |
|---|---|---|---|
| D1 | Audio I/O on Linux | **ALSA via purego** (`spikes/pinode/alsa`, ~120 lines). Open asound.conf names (`dsnoopmic`, `output`, `default`). Keep **arecord/aplay exec** as the fallback. | §3.2: CGO_ENABLED=0, 0 xruns, 3.6% CPU for capture with Go decimation, 1.5–2.6% for playback. Exec costs the same (3.0%) and is a fine fallback. |
| | | Pure-Go PulseAudio/PipeWire protocol: **no** for the Pi. | On the Pi, capture deliberately bypasses Pulse (dsnoop, with udev `PULSE_IGNORE`). On a desktop, ALSA `default` already routes through pipewire-alsa or the pulse plugin. |
| | | **malgo: no.** | cgo breaks the one-command cross-compile that the agent's CI relies on (`linux/arm64` from amd64). |
| D2 | Capture rate and resampling | **Capture 48 kHz** (the codec clock is shared with the 48 kHz Pulse sink) and decimate in Go with a **streaming** 61-tap Kaiser FIR (scipy's `resample_poly` filter). | ALSA plug's 16 kHz conversion cost more (5.2% vs 3.6%) and uses a linear interpolator. The Python node resamples each 80 ms chunk independently, with zero-padded edges; the Go version has none of those per-chunk edge artifacts (`dsp/decimate_test.go`). |
| D3 | Wake word | **openWakeWord ONNX on onnxruntime's C API via purego.** Use jarvisd's embedded ORT 1.28.2 and the same models. | §3.1: matches Python to 3e-8. jarvisd already ships this exact loading pattern (`internal/voice/sherpa/open_unix.go`). |
| | | sherpa-onnx KWS: **no**. | It needs a transducer KWS model and would change wake behaviour and thresholds that beta tuned. |
| | | Pure-Go inference: **no**. | The embedding CNN is 87% of the cost and is exactly what MLAS optimises. |
| | | int8: **no**. | Dynamic quantisation gave no speedup on A72-class cores (18.7% vs 17.6%), because they lack sdot. |
| D4 | VAD and endpointing | **Port the RMS endpointing as-is.** Optionally add silero later through the same ORT path. | The node uses adaptive RMS today, not silero or webrtcvad (`N:core/vad_thresholds.py`). Silero via ORT costs 3.3% of a core and only runs while recording. |
| D5 | HAT LEDs and button | **Raw spidev write** for the APA102s (one ioctl for speed). **go-gpiocdev** for GPIO17. Not periph.io. | §3.4: works from a static binary. sysfs GPIO is deprecated. The APA102 frame is 20 lines. |
| D6 | Provisioning AP | **Keep the hostapd + dnsmasq exec design** (proven on the Zero 2 W), with `nmcli` for station profiles. Port the HTTP API (`:8080`, the captive-check URLs, `/api/v1/provision[/k2]`) to net/http. NetworkManager D-Bus (godbus) can come later. | The Python node auto-selects hostapd on a Pi (`N:scripts/run_provisioning.py:84-88`). The recovery watcher and rollback logic are the valuable parts; port them 1:1. |
| D7 | Updates and install | **Move the agent into jarvis-server first, then reuse `internal/update`.** That package verifies the minisign-signed SHA256SUMS with a built-in key, stages the binary, swaps it with a rollback copy, and runs a post-restart health gate. | It is the same pattern install.sh hand-rolls in bash (`N:install.sh:553-628,1967-2046`). Keep heartbeat `pending_update` → sudo wrapper → `systemd-run` → health check. |
| D8 | Sidecar and node internals | **Expand SDK backend registries** (`set_*_backend`, like storage/inbox/signals) instead of a new `jarvis-node-runtime` package. | §5: 7 commands only fail on 3-line re-export shims. The real host services are Timer, AudioControl/Speak, AlertQueue, LLM chat, CC POST, CommandInvoker and Devices. |
| D9 | PulseAudio on the Pi | **Keep it for v1.** The Go node writes TTS to ALSA `output`, which is Pulse. Make "ALSA direct + dmix, no Pulse" an install option for HAT-only nodes. | Pulse carries Bluetooth audio, `module-echo-cancel`, and the music packages' output. It costs 31 MB RSS / 13 USS, plus ~4.4% of a core for the `paplay /dev/zero` keepalive. The Go node could hold the codec awake itself with a silent ALSA stream instead. |

---

## 3. Spike results

All measurements are on the Pi 4B (Rev 1.5, 4× A72 at 1.8 GHz, Debian 13 trixie, ondemand
governor) with the ReSpeaker 2-mic v2.0 HAT. The Python node service was stopped during
measurements unless stated.

**Zero 2 W proxy:** `scaling_max_freq=600000` on all cores. An A72 at 600 MHz is roughly an A53 at
1–1.2 GHz; this is an estimate, not a Zero 2 W measurement.

**Units:**
- CPU is "% of one core": process user+sys time ÷ wall time.
- Memory is from `/proc/self/smaps_rollup`; USS = private clean + private dirty.

**Builds:** every Go spike is `CGO_ENABLED=0 GOOS=linux GOARCH=arm64`, built with Go 1.27.1 on
amd64. Binaries using purego are dynamically linked against glibc's loader; `hat` is fully static.

### 3.1 Wake word: Go (purego ORT) vs Python (oww 0.6.0, ORT 1.30.0)

**Test set:** `spikes/pinode/py/prep.py`. Every clip is padded with 2 s of seeded low noise before
and 1 s after, and resampled to 16 kHz with scipy. Both harnesses read the same files in 1280-sample
chunks.

**Clips:**
- **Positives:** a synthetic "Hey Jarvis" (TTS), and two real mic captures from the ReSpeaker (the
  node's cached `wake.wav` and `speaker.wav`).
- **Negatives:** "Tell me a joke" (TTS), and two 9 s room recordings.

| Clip | Python max | Go max | Max \|diff\|, frames ≥ 21 |
|---|---|---|---|
| pos_tts | 0.9973 @39 | 0.9973 @39 | 1.6e-8 |
| pos_mic (real mic) | 0.9977 @54 | 0.9977 @54 | 1.5e-8 |
| pos_mic_cmd (real mic) | 0.9961 @55 | 0.9961 @55 | 2.9e-8 |
| neg_tts | 0.0051 | 0.0051 | 1.6e-10 |
| neg_room2 | 0.0026 | 0.0026 | 3.4e-11 |
| neg_room3 | 0.0564 | 0.0564 | 4.6e-10 |

Frames before 21 are excluded because Python oww seeds its feature buffer with embeddings of
*random* noise (`openwakeword/utils.py:170`). Its early scores (up to 0.089 here) are that noise.
Go starts empty and scores 0 until 16 embeddings plus oww's 5-frame warmup.

**Cost, file-fed (soak60.wav, 60 s):**

| | CPU @1.8 GHz | CPU @600 MHz | RSS / USS after |
|---|---|---|---|
| Go (ORT 1.28.2, arena on) | **17.6%** | **48.9%** | 37.8 / 36.5 MB |
| Go, CPU arena off | 17.8% | 49.0% | 39.8 / 38.4 MB (no gain) |
| Python oww (lean import) | 28.4% | 66.0% | 119 / 111 MB |
| Python oww (stock import) | 27.6% | – | 199 / 191 MB |

**Stage breakdown (Go, 1.8 GHz), per 80 ms chunk:**
- melspectrogram: 1.07 ms
- **embedding: 12.1 ms (87%)**
- hey_jarvis: 0.53 ms
- Total: 13.9 ms average, 24 ms worst.

At 600 MHz the total is about 39 ms per 80 ms chunk, so there is headroom, but not a lot.

**Memory growth (Go):** 4.2 MB at start → 16.4 MB after loading ORT → 29.6 MB with the three
sessions → 37.8 MB steady.

**Live (`cmd/wakelive`: ALSA capture 48 kHz → Go decimator → oww → 0.4 threshold, 8 s debounce,
reset after fire):**
- **Through snd-aloop**, playing `loop48.wav` (all six clips plus gaps, 67 s), on Loopback subdevice
  3 because PulseAudio grabs subdevice 0:
  - **Fires at 7.31 s, 33.39 s and 63.31 s, which are the three positives, and nothing else.**
  - Scores at the fire frame: 0.91, 0.93, 0.83.
  - 0 xruns, 0 chunks over 80 ms. 32.5% CPU (via plughw on the loopback); 39.8 MB RSS.
- **Real mic `dsnoopmic`, quiet room, 60 s, same governor:**

  | Process | CPU | RSS / USS |
  |---|---|---|
  | **Go** | **20.5%** | **40.4 / 36.4 MB** |
  | Python harness copying `wake_loop`'s per-chunk work (`py/pywakelive.py`: PyAudio 3840 → resample_poly → oww.predict) | 29.0% | 216.6 / 196.6 MB |
  | Python, stub only (still imports scipy for resample_poly) | 28.9% | 192.0 / 172.1 MB |
  | Python, stub + numpy decimator | 28.9% | **133.0 / 113.0 MB** |

- **Note:** a measurement of the running Python node itself was invalid. Its config points at last
  night's throwaway jarvisd (`localhost:27700`), so the voice listener was parked waiting for
  discovery. The harness numbers above are the fair comparison.

### 3.2 Audio I/O

| Test | CPU (self + child) | RSS | xruns |
|---|---|---|---|
| Capture `dsnoopmic` 48k mono, purego ALSA, Go decimate to 16k, 30 s | 3.6% | 13.3 MB (includes the 1 MB kept WAV) | 0 |
| Same via `arecord` child + Go decimate | 1.5% + 1.5% | 9.9 MB + arecord | – |
| Capture `dsnoopmic` at 16k (ALSA plug linear resampler) | 5.2% | 8.5 MB | 0 |
| Capture `hw:` stereo 48k, no conversion, 10 s | 2.0% | 9.2 MB | 0 |
| Play 24 kHz TTS PCM → loopback, purego, 20 ms writes | 0.7% | 8.5 MB | 0 |
| Same via `aplay` | 0.1% + 0.6% | – | – |
| Play 20 s (silence) → HAT via Pulse (`output`), purego / aplay | 2.6% / 0.5% (Pulse's own CPU not counted) | 10.3 MB | 0 |
| Play 20 s → HAT **direct** (`plughw:seeed2micvoicec`, Pulse stopped), purego / aplay | 1.6% / 0.7% | 10.4 MB | 0 |
| Direct playback **while** dsnoop capture runs | 2.0% play, 3.2% capture | – | 0 / 0 |

Playback after 23:00 was kept inaudible: real PCM went to the loopback and digital silence to the
HAT.

**Not spiked:**
- A pure-Go Pulse client (D1 explains why).
- malgo (cgo).
- The desktop audio paths: macOS AudioQueue via purego, as macax does; Windows WASAPI capture over
  the raw COM vtables the agent already uses for volume.

### 3.3 VAD

Silero (the v4-style `silero_vad.onnx` bundled with oww: inputs input/sr/h/c), 30 ms frames,
through the same binding (`cmd/vadbench`):
- **3.3%** of a core at 1.8 GHz.
- ~14 MB on top of ORT if loaded alone.
- Speech-frame counts look sane: 2/400 on room noise, 49/212 on a real command clip.

This is not needed for MVS.

### 3.4 HAT

`cmd/hat`, static binary (no purego):
- **LEDs:** wrote APA102 frames (dim blue, then off) to `/dev/spidev0.0` at 8 MHz; no errors. The
  light itself was not observed; nobody was there at night.
- **Button:** read GPIO17 through `/dev/gpiochip0` with pull-up and edge events → level 1 (released).
- The `pi` user is already in the `spi` and `gpio` groups (`N:install.sh:1174-1178`).

### 3.5 What the spikes prove about purego + ORT without cgo

- **Function table.** `OrtGetApiBase → GetApi(28)`. Slots are indexed from the 1.28.2 header
  (`tools/ortidx.py`: 424 slots). The table is append-only, so newer runtimes keep working.
- **Threads.** ORT's threads (intra = inter = 1, as oww uses) work under `CGO_ENABLED=0`, as they
  already do in jarvisd.
- **Pointer rules.** Input tensors wrap Go memory, and the name and value arrays are Go slices
  passed to C. That is legal only because Go's GC does not move heap objects and the slices are
  kept alive across the call. Production code should keep these in C-allocated memory or
  long-lived fields (as `sherpa/capi.go` does), and add a layout and lifetime test.
- **`go vet`** flags the C-pointer `unsafe.Pointer` conversions, as it would for sherpa.

---

### 3.6 Measured on a real Pi Zero 2 W (<dev-node>, 2026-10-10)

Trixie arm64, Seeed 2-mic HAT, `dsnoopmic` at 48 kHz, 1 GHz, no throttling (49–58 °C). 15-minute windows; the HAT speaker played the TTS "hey jarvis" clip every 60 s (15 plays) into the HAT mic.

| Run | Wakes | CPU (one core) | Memory |
|---|---|---|---|
| Python node 0.3.4, whole service, idle listening (mpd crash loop fixed first) | n/a | 92.6% | 123 MB RSS + 96 MB swapped (~219 MB) |
| Python node 0.3.4, whole service, 15 plays at 70 s spacing | **15/15** (1 backlog drop) | | |
| Go `wakelive` (purego ALSA + ORT oww) | **15/15**, 0 false | 54.5% | **39.5 MB RSS** |
| `pywakelive.py` (single-threaded harness, stock) | 2/15 | 41% | 183 MB |
| `pywakelive.py --lean --noscipy` | 6/15 | 39% | 131 MB |

- The `pywakelive.py` miss counts are a **harness artifact**: it reads and predicts in one loop with `exception_on_overflow=False`, so when predict falls behind, PyAudio overflows and the audio (and the wake word) is silently dropped. That is also why its CPU looks lower than Go's. The real node reads on a separate thread and caught every wake. Use the harness for memory only.
- Go had 140 ALSA xruns in 15 min (0 chunks late over 80 ms) and still fired on every play; worth a larger period/buffer on the Zero.
- The whole Python service idles at ~93% of a core on the Zero (wake loop plus everything else), against 54.5% for the Go wake loop alone.
- Before this run <dev-node> had a leaked audacy mpd drop-in crash-looping mpd (5,169 restarts, ~4 s CPU each); fixed by jarvis-node-setup #143 (self-heal on boot).
- Memory quick win (jarvis-node-setup #144, merged): −77 MB on the Pi 4, wake scores bit-identical.

### 3.7 Desktop voice landed (phase 3, 2026-10-10)

`jarvis-agent run --voice` (and `jarvis-agent listen`, the pipeline alone) runs the §1.1 MVS turn
on Linux and macOS. Code: `internal/audio/device` (ALSA over purego on Linux, AudioQueue over
purego on macOS, `wav:FILE` on every OS) and `internal/agent/voice` (pipeline, endpointing,
command-center client, wake-model fetch). Windows reports "not supported yet" and links no
onnxruntime.

**What it does, in wire order** (rows refer to §1.1):

| Row | Go behaviour | Same as Python? |
|---|---|---|
| 3 | Capture 48 kHz S16 mono, 80 ms chunks, from `--voice-input` (`default`) | yes. ALSA/AudioQueue convert from the hardware rate. |
| 4 | Streaming decimator → oww `hey_jarvis` 0.4, 8 s debounce, reset after a fire | yes (D2: no per-chunk edge artifacts) |
| 5 | Wake clip: the last 25 raw chunks (2 s), 48 kHz | yes |
| 6 | Synthesised two-tone chime, then the queue is flushed (`history_secs=0`) | chime instead of a greeting |
| 7 | `/conversation/start` on a goroutine at wake: `node_context.timezone` (from `$TZ` or `/etc/localtime`), plus the node's client tools (§3.8) | yes |
| 8 | RMS endpointing: clamp(median pre-wake RMS × 2, 200, 1000) else 300; 0.8 s silence, 2–7 s; then DC removal + gain to −18 dBFS (≤ +18 dB). Golden-tested against `speech_to_text.py`. | yes, including the normaliser |
| 9 | STT multipart: `file` (48 kHz), `speaker_audio` (wake clip + command), `conversation_id` | yes (48 kHz per §8 Q1) |
| 10 | `is_non_speech` port, golden-tested against `voice_filters.py` | yes |
| 11 | Join the warmup ≤ 10 s, then `/voice/command/stream` `{voice_command, conversation_id, turn_source:"wake", wake_confidence, affect}` | yes (with `pre_wake_speech_seconds` and `self_playback` since 17c, §3.16) |
| 12 | 200: PCM streamed to the speaker at `X-Audio-*`, 8 s inter-chunk idle timeout | yes |
| 13 | 202 `not_for_me` silent; `complete` spoken via `/media/tts/speak` (≤ 200 chars) or `/speak/stream`; empty `complete` stays **silent** (§1.4: Python says "Task completed."); `tool_calls` run locally (§3.8); `validation_required` asked aloud (§3.9); `error` speaks its message | as §1.1, minus the §1.4 quirk |
| 14 | `/conversation/end` always, after both the turn and start have finished | Go always sends it |
| 15 | Error chime | chime only |

**Wake models at runtime:** `<user cache>/jarvis-agent/openwakeword/` (`~/.cache` on Linux,
`~/Library/Caches` on macOS), downloaded once from openWakeWord's v0.5.1 release (the files
`fetch-test-models.sh` uses) and **pinned by SHA-256**; `--wake-models DIR` uses a directory
as-is (verified, never downloaded into). onnxruntime is the embedded copy, extracted under
`<user cache>/jarvis-agent/native/`. The agent binary grows from ~12 MB to ~44 MB on
linux/amd64 and darwin/arm64 (Windows unchanged).

**Bug found on the way (fixed): `internal/voice/ort` could hand onnxruntime a dangling
pointer.** `Runtime.call` took the out-parameter addresses (`&vals[i]`, `&outs[0]`, `&info`) as
`...uintptr` and passed them on to `purego.SyscallN`; `//go:uintptrescapes` only protects the
direct callee, so those variables could stay on the goroutine stack, and a stack growth inside
the call moved them. Live runs failed after 0.2–44 s with "NULL input supplied for input
input"; the golden tests never hit it because their stacks were already grown.
`TestProcessAtAnyStackDepth` reproduces it at once; `call` (and the Windows COM helper
`comCall`, same pattern) are now `//go:uintptrescapes`.

**Measured** (listening, no turn; CPU = process user+sys ÷ wall):

| Host | Path | CPU (one core) | Memory |
|---|---|---|---|
| i7-13700KF, Arch/PipeWire | ALSA `pulse` plugin → null-sink monitor, 120 s | 1.7% | RSS 88 MB: anon 29, file 16, and 38 MB of the pulse plugin's shared-memory pool |
| same | ALSA `default` (pipewire-alsa, real headset mic, threshold 1.0 so nothing can fire), 60 s, while the repo's race suite ran | 2.2% | RSS 90 MB, same split |
| M2 Max, macOS 26.1 | AudioQueue default mic (TCC-silenced over SSH), 60 s | 12.0% | 56 MB physical footprint (RSS 135 MB counts shared frameworks) |

**Verified live:**
- Linux, this box: a WAV of "hey jarvis" + Kokoro "What time is it?" through the real ALSA
  path (played into a PipeWire null sink; the agent captured its monitor through ALSA and
  played the reply to a second null sink, recorded with `parecord`) and through `wav:` in/out:
  wake 0.82–0.90, transcript "What time is it?", reply "It's 5:00 PM on Saturday." streamed and
  played (2.7 s), against the dev jarvisd.
- macOS, the MBP: the same WAV through `listen --voice-input wav:… --voice-output default`:
  wake 0.815, reply streamed and played through AudioQueue in real time (4.05 s). AudioQueue
  playback drains in real time; capture delivers buffers but **all zeros over SSH**: TCC gives
  sshd-launched processes silence for the microphone. A live mic test needs the bundle run
  under launchd (`scripts/agent-dev-run.sh`) and the user to click "Allow" on the
  microphone prompt (`NSMicrophoneUsageDescription` is now in Info.plist; without it macOS kills
  the app on first capture).

**Not yet (phase 5 parity list):** ~~follow-up~~ (§3.9), ~~barge-in~~ (§3.13), ~~client tools/continue~~ (§3.8),
~~validation~~ (§3.9), processing ack / wake greeting, `pre_wake_speech_seconds`, AEC (on a desktop with
speakers the reply can reach the mic; wake is paused while it plays, so it can't wake itself;
barge-in's energy gate keeps the reply from interrupting itself), device selection on macOS
(default devices only), Windows WASAPI.

### 3.8 Client tools in voice turns (2026-10-10)

"Hey jarvis, set the volume to 30 percent" now runs the desktop tool. Code:
`internal/agent/voice/tools.go` (the loop), `client.go` (`StartConversation` with tools,
`ContinueTools`), `cmd/jarvis-agent` (one `toolFlags.build` for `run` and `listen`).

- **One source of truth.** `run` builds one `node.Multi` (desktop tools, then the sidecar's
  commands) and hands the *same* value to the MQTT dispatcher and to `voice.Serve`. A voice
  turn offers `Available()` at wake (capabilities computed then, as for `report_tools`) as
  `client_tools` + `available_commands` on `/conversation/start`, and runs a `tool_calls`
  reply through `Toolset.Call`. `listen` builds the same set (`--no-tools` for none).
- **The loop** (ported from `N:_run_conversation_loop` / `_execute_tools`): the calls of a
  round run in order; their `{tool_call_id, output}` go back in one `POST
  /voice/command/continue`; a `tool_calls` answer starts another round, anything else is
  handled like the first 202 (`complete` spoken via TTS, empty stays silent, `not_for_me`
  silent). At most 10 rounds; a round identical (names + arguments) to the previous one ends
  the turn with "Sorry, that didn't work: …" (or "Sorry, I wasn't able to complete that
  request."); when every call of the last round failed, the tool's error (the call's
  `failure_message` first) is spoken instead of the model's reply, as Python does, because
  small models report success anyway. No `/commands/{rid}/verify` here: the calls arrive on
  the node's own authenticated HTTP response, not over MQTT.
- **Failures are results, never hangs.** Unknown tool → `{"success": false, "error":
  "Unknown tool: X"}` (Python's text); a call that exceeds `Config.ToolTimeout` (25 s, the
  dispatcher's) or panics → a failure result, and the turn goes on; the call runs on its own
  goroutine because the sidecar ignores deadlines today. A continue that fails (HTTP, or the
  200-with-`stop_reason: null` processing error) → error chime.
- **Deliberate difference: blocking continue only.** The Python node tries
  `/voice/command/continue/stream` first and falls back to the blocking route on 202.
  jarvisd's stream route only *speaks* the post-results answer (no tools in that LLM call), so
  a chain can never happen there: "minimise Firefox" needs `desktop_list_windows` then
  `desktop_minimize_window`, and "turn it down 5%" needs get then set. The blocking route
  re-enters the engine's tool loop on native-tools models; the cost is that the final answer
  is rendered by `/media/tts/speak` after the LLM finishes instead of streamed sentence by
  sentence. Forced retries (cc engine, PR #29) don't fire on these continues: the tool
  results in the history count as "a tool ran this turn".
- **Confirmation:** none, same as the MQTT path and the Python node.
  `desktop_close_window` is marked `is_risky` (only errands' planner reads that) and is a
  graceful close (the app may prompt to save); nothing else the desktop offers is
  destructive. See DESIGN.md §9 Q2.
- **Not ported:** `act_on_items`, `post_process_tool_call`. (`wait_for_input`,
  `clear_history` and `on_response_complete` landed with follow-up, §3.9; music takeover with
  §3.16.)

**Verified live** (2026-10-10, `listen --voice-input wav:… --no-chimes`, Kokoro-rendered
"Hey Jarvis." + command, against this box's dev jarvisd, as the MBP's node):

| Box | Said | Tools run | Reply | Turn |
|---|---|---|---|---|
| this box (Linux/PipeWire) | "Set the volume to 25 percent." | `desktop_set_volume{"volume":25}`; `wpctl` read 0.25, then restored to 0.30 | "Got it, volume's set to 25%." | 5.3 s |
| same | "What is the volume on my computer?" | `desktop_get_volume` | "Your computer's volume is at 30%." | 4.7 s |
| same | "What apps can you open?" | `desktop_list_apps` | "…You have 62 apps installed, including…" (16 s of audio) | 7.0 s |
| same | "Turn the volume on my computer down by 5 percent." | `desktop_get_volume`, then `desktop_set_volume{"volume":25}` (**two rounds**); restored | "Got it — I've turned your computer volume down by 5%." | 7.1 s |
| MBP (macOS 26) | "Open the Chess app on my computer." | `desktop_list_apps{"query":"chess"}`, then `desktop_open{"target":"Chess"}` (**two rounds**); Chess came up, quit after | "Got it, the Chess app is opening up now." | 5.7 s |

Not live-tested: window tools by voice (this box is Wayland, so none are offered; the MBP's
agent binary over SSH has no Accessibility grant), sidecar commands by voice (covered by the
same `Toolset` in tests), a live mic.

### 3.9 Follow-up and validation questions (2026-10-10)

After a reply the agent keeps listening without the wake word, and a `validation_required`
reply is asked aloud and answered by voice. Code: `internal/agent/voice/followup.go` (the window
and its filters), `converse.go` (the conversation loop: tool rounds + validation questions, the
final message), `client.go` (`VoiceCommand`, `Control.Validation`/`EndOfExchange`),
`internal/agent/node` (`Signals`, `TakeSignals`) and the sidecar (`signals`, `complete`).

**What it does** (ported from the Python node, rows 16 and 19 of §1.1, same defaults):

| Step | Go behaviour | Python |
|---|---|---|
| When | After every exchange that ended spoken, silent or non-speech; not after `not_for_me`, an error, or a reply with `end_of_exchange` | same (Python also opens it after an error, with a fresh conversation) |
| Echo | The queue captured while the reply played is flushed, then a quiet-wait drains ≤ 0.5 s until 2 chunks are below threshold × 1.5 | quiet-wait the same; history replay up to 2 s since TTS end |
| Window | 4 s, then 3 s (4 − 2 per iteration, floor 3); counted in captured 80 ms chunks; ≤ 5 iterations | wall-clock, same numbers |
| Onset | 3 loud chunks in a row (+1 per iteration, ≤ 8) at the wake's silence threshold; keep ≤ 1.5 s before onset | same |
| Record | until 0.5 s of silence, at least 0.7 s after onset, ≤ 7 s; < 0.3 s is noise; normalised | same |
| STT | `file` + `speaker_audio` (the wake clip + answer) + **`conversation_id`** | no `conversation_id` (§1.4) |
| Filters | non-speech → close; noise (an exact repeat; lone words are sent, unlike Python) → listen again, twice in a row → close; self-echo (only right after our reply: ≥ 90 % of the answer's words are the reply's, in its order, covering ≥ half the reply; stricter than Python since 2026-10-14) → close. Golden-tested against `conversation_filters.py`. | same |
| Send | blocking `POST /voice/command {voice_command, conversation_id, turn_source: "follow_up", follow_up_iteration}`; the reply goes through the same loop as a wake reply (tools, validation), spoken via TTS | same |
| Close | `end_of_exchange`, `clear_history` (a command's signal), `not_for_me`, an error, nobody speaking, 5 iterations; then `/conversation/end` | same |
| Cue | none (Python shows a "listening" LED; a desktop has no LED) | LED |
| Validation | speak the question + "Your options are: a, b"; flush; quiet-wait ≤ 0.8 s; `record()` with the wake's endpointing; answer (non-speech → "I didn't catch that, sorry.") posted as `{tool_call_id: "validation-response", output: {answer, parameter_name, question, options}}` on the blocking continue; up to 10 questions + tool rounds per reply | same, except Python records the answer with its static 1.5 s silence / threshold 300 and only replaces an *empty* transcript |
| Command signals | sidecar results carry `wait_for_input`, `clear_history`, `on_response_complete` beside the output (never posted). `wait_for_input`: after sending the results, speak the next reply without another tool round, preferring the command's own `message`, else "Go ahead, I'm listening.". `on_response_complete`: run once the conversation (follow-ups included) is over; on MQTT `tool_call`, right after the result is posted | same; Python runs only the wake exchange's callback and none on MQTT (§1.4) |

**Server side checked.** jarvisd's request_validation stop leaves the question's tool result in
history; the answer arrives as an extra `tool` message with id `validation-response`, and a
native-tools model re-enters the loop with it (`TestVoiceValidationRequired` now covers the
continue). Follow-up turns go through `processTurn` with `turn_source: "follow_up"` (strict
`not_for_me` posture, follow-up doubt on an unverified wake); they are user turns, so the
must-call retry is armed per turn as usual (PR #29) and the conversation's history and tools
carry over.

**Verified live** (2026-10-10, this box, dev jarvisd, as the MBP's node; PipeWire null sinks:
Kokoro clips played into `agent_mic`, the agent captured its monitor through ALSA `pulse` and
spoke into `agent_spk`; each answer was played when the agent logged that it was listening):

| Said after the wake | Follow-ups | Window |
|---|---|---|
| "What time is it?" → "It's 8:15 PM on Saturday…" | "And what is the date today?" → "It's Saturday, October 10…"; "Thank you, that is all for now." → "You're welcome! Have a great evening." | closed after 3 s of silence |
| "Set the volume on my computer." → set to 50 % | "Thirty percent, please." → `desktop_set_volume{30}` → "Done — volume's down to 30." (follow-up running a tool) | timeout |
| "Remember something for me." → "…Could you tell me what you'd like me to remember?" (asked in prose) | "That my favorite color is teal." → "Got it — I'll remember that…" | timeout |
| "What time is it?", reply played **into the agent's own microphone** | none: it did not hear itself | timeout |
| "Cancel my timer." (a scratch sidecar command with two timers) → cancelled both | "The oven one." → answered in context (sidecar results with signals) | timeout |

**Not verified live:** a `validation_required` question. The dev model (Qwen3.5-9B) never called
`request_validation` across four ambiguous prompts: it asks in prose (which the follow-up window
handles) or guesses. Covered by the pipeline tests and the server test above. Also tests only:
`end_of_exchange` (the model didn't emit `<exchange_complete/>`), `clear_history`,
`on_response_complete`.

**Deferred:** `act_on_items` (a node-side dispatcher over items earlier commands returned, a
separate mechanism: it needs the sidecar to keep `ref_id → command` and call commands'
callbacks), a listening cue on desktops (**done 2026-10-11: the island, DESIGN.md §11**), barge-in during follow-up replies (**done 2026-10-17, §3.13**), the not_for_me
wake cool-down (**done 2026-10-21, §3.20**), an `end_of_exchange` header on the stream route (server).

### 3.10 The follow-up gate: "is this speech for me?" (2026-10-10)

With a 4 s window, longer follow-ups were cut off and a longer window meant the room had to stay
silent: anything said in it (another person, the TV) went to the model, and a misfire ended the
window. Now command-center decides, cheaply, whether a follow-up is for Jarvis; a rejected one
costs nothing, so the window is longer and answers may pause.

**What runs where.**

| Signal | Where | How |
|---|---|---|
| Voice | jarvisd, beside whisper in the media proxy (`cc/followup_gate.go`, `stt/voicematch.go`) | The conversation's first transcription (speaker_audio = wake clip + command) is the reference; every later one's `file` is embedded (ERes2Net, the speaker-ID model; VAD speech frames only, ≥ 0.8 s) and its cosine to the reference kept for the next turn. Identifies nobody; no voiceprint read or written; runs whether or not speaker recognition is on. In memory only: dropped at `/conversation/end`, or swept once idle 15 min if the end never comes. |
| Level | node (`internal/agent/voice/level.go`) | Uploads are normalised, so the node reports `level_vs_wake_db`: the follow-up's speech level (75th-percentile RMS of chunks above the silence threshold) minus the woken command's. |
| Semantics | jarvisd, the turn's own model call | The existing follow-up `<not_for_me/>` posture. No extra call: the acoustics reach the model as one `[voice check: …]` hint line when they doubt the follow-up. |

**Combining (conservative).** Different voice (< 0.25) **and** quiet (≤ −10 dB) → dropped before
the model. Different voice alone, or quiet with a voice that isn't the wake's → a hint, and the
model's `<not_for_me/>` is not double-checked (the /think re-check costs a long pass and exists to
protect genuine follow-ups). Same voice, or no evidence → unchanged. A `not_for_me` follow-up (with
`voice.follow_up_gate` on) is **not committed** to history, not counted as an answered round, not
transcribed, and the response carries `keep_listening: true` until `voice.follow_up_max_ignored`
(3) rejections in a row. The node then listens again without speaking, flushing or using up an
iteration (`MaxIgnored` 5 bounds it locally). Older nodes ignore `keep_listening` and close the
window as before; older servers never send it.

**Node defaults now** (Python's in brackets): window 3 s on every iteration, and at most 3 s
however it is set (4, then 3; it was 8, then 6, then 5 until #296, see
[decisions/2026-10-19-follow-up-window-3s.md](../decisions/2026-10-19-follow-up-window-3s.md)); an answer ends
after 1.2 s of silence (0.5); answers may run 10 s (7). The wake turn's endpointing is unchanged.

**Evaluation** (`internal/agent/voice/gate_eval_test.go` → `internal/modules/cc/followup_gate_eval_test.go`;
both env-gated, commands in the files). Kokoro voices and LibriSpeech speakers in simulated rooms
(near ±3 dB, turned away −6 dB + reverb, another person near/mid/far, a TV: band-limited,
compressed, music bed, −10 dB), plus real node-mic takes of a household member (`spk/calib`), run
through the pipeline's own follow-up listening, then `stt.EmbedVoice` as jarvisd computes it.

*Endpointing.* Pauses inside 192 real LibriSpeech utterances (575 gaps): p50 0.16 s, p90 0.56,
p99 0.96, max 1.36. Utterances whose longest pause would end the recording: **0.5 s: 71 (37%)**,
0.8 s: 17, 1.0 s: 8, **1.2 s: 3 (1.6%)**. Five Kokoro answers with 0.5–1.1 s pauses: 5/5 cut at
0.5 s, 0/5 at 1.2 s. (The wake's 0.8 s would cut 9% of these read utterances; wake commands are
short, so it stays, see decisions.)

*Per signal* (counts of follow-ups flagged):

| Set | n | voice < 0.25 (different) | voice ≥ 0.40 (same) | level ≤ −10 dB | both (dropped) |
|---|---|---|---|---|---|
| same speaker, near (LibriSpeech) | 72 | **0** | 72 | 0 | **0** |
| same speaker, turned away | 72 | **0** | 71 | 0 | **0** |
| household member, real node mic, near / far | 8 / 3 | **0** / **0** | 8 / 2 | 0 / 0 | 0 / 0 |
| Kokoro: genuine, turned away, paused answers | 23 | 0 | 23 | 0 | 0 |
| Kokoro: same voice, unrelated speech | 5 | 0 | 5 | 0 | 0 |
| other person near / mid / far (LibriSpeech) | 72 each | 64 / 64 / 71 | 1 / 1 / 0 | 0 / 0 / 22 | 0 / 0 / 22 |
| TV (LibriSpeech voices) | 72 | 69 | 0 | 16 | 16 |
| real member vs LibriSpeech / Kokoro impostors | 10 / 10 | 10 / 10 | 0 | 0 | 0 |
| Kokoro other person / TV | 6 / 6 | 6 / 6 | 0 | 1 / 2 | 1 / 2 |

Similarity ranges: same speaker 0.35–0.90 (real far takes 0.35–0.50), other voices −0.18–0.48
(p90 ≈ 0.25). Level is weak on its own: the real far takes were −4 to +4 dB of the near wake, and
simulated other people overlap the same speaker; it only ever acts together with the voice.
ERes2Net on one answer: p50 146 ms, max 272 ms (2 threads), in parallel with whisper.

*Combined, against the real model* (Qwen3.5-9B Q4_K_M, jarvisd's pinned template, CPU llama-server;
the full turn pipeline with a node's tools; 35 Kokoro follow-ups, each run with the gate off = today
and on with its measured voice/level):

| Category | n | want | rejected today | rejected with the gate (of which before the model) |
|---|---|---|---|---|
| genuine, same voice | 10 | accept | 0 | **0** |
| genuine, turned away | 3 | accept | 0 | **0** |
| genuine, long with pauses | 10 | accept | 0 | **0** |
| another person | 6 | reject | 3 | **6** (0) |
| TV | 6 | reject | 3 | **6** (3) |
| same voice, not for Jarvis | 5 | reject | 1 | 1 |

False rejects stayed at zero; room speech went from 6/12 to 12/12 rejected. The miss is the
same person talking to someone else ("Honey, can you grab my keys", "Sorry, I'm on the phone"):
only the model can tell, and it answered 4/5 with or without the gate. Latency: genuine follow-ups
make the same model calls as before and get no hint (no prompt change); the voice check runs beside
whisper. A rejection took one model call (≈3.7 s on this CPU) instead of today's two (≈56 s on
CPU: the `<not_for_me/>` double-check thinks); an acoustic drop takes none.

**Verified live** (this box, the agent through PipeWire null sinks, against the dev jarvisd,
which runs *main*, i.e. without the server half): an answer started 5–6.5 s into the window was
heard (the 4 s window would have closed); a 6.7 s answer with 1.0 s and 0.9 s pauses arrived whole
("Can you also tell me what day of the week it will be next Friday?"); `level_vs_wake_db` was
sent (−1.3/−1.4 dB for near/mid voices); an old server's `not_for_me` (TV, another person) still
closed the window. **Not verified live:** the server half (voice comparison inside jarvisd,
`keep_listening`, the acoustic drop): the dev jarvisd needs a restart on this branch; a real room
with real people and a real TV.

**Ambient room speech (2026-10-11).** The gate above catches *other* voices; the user's own voice
talking to someone else in the room was answered almost every time, and every rejected answer
restarted the 8 s window. `internal/modules/cc/testdata/ambient_followups.json` is a 99-line corpus
of follow-ups over 10 woken exchanges (two of them end in a question): genuine follow-ups and
short answers ("Kitchen.", "Thanks."), the user speaking to someone else (often on Jarvis's topic:
"Babe, grab the umbrella, it's going to rain"), phone calls, remarks, kids and pets, other people,
TV. `TestFollowUpAmbientEvalLive` plays each through the real turn pipeline with the gate on and
the case's voice evidence; it takes any OpenAI-compatible endpoint, so it is the check to run for
each model a household may use (wording that works for Qwen3.5-9B may not for another model).

| Qwen3.5-9B Q4_K_M, pinned template, GPU | n | main | now (3 runs) |
|---|---|---|---|
| genuine follow-ups and answers, must be answered | 32 | 32 | **32, 32, 32** |
| the user's voice not talking to Jarvis (to someone else, phone, remarks, kids/pets) | 39 | 6 | **32, 33, 34** |
| other voices and TV | 23 | 21 | **23, 23, 23** |

What changed, and why (each measured on the corpus):
- **Framing (`prompts.OverheardFollowUp`).** A follow-up nobody asked for reaches the model as what
  the open mic heard: `(Heard while the mic stayed open after your reply; no wake word. If it
  answers your question, follows up on what you just said, or asks you to do something,
  respond. If it needs no action or response from you, emit <not_for_me/>.)` then the quoted
  transcript. Presented bare, "Are you going to bed now too?" reads as said to Jarvis. When our
  last reply asked a question the transcript stays bare: framed, the model rejected "Kitchen." and
  "Both." after "Which room?". Gate off = bare, as before.
- **The follow-up turn hint** names concrete cues: a name or pet name, a question about the
  listener's own day or plans, a request only a person could do (grab, pass, stir), one side of a
  phone call, a remark to the room, and says short replies to Jarvis count.
- **An honest double-check.** The not_for_me double-check told the model "the wake word fired;
  the room was quiet", false in the follow-up window; it talked the model out of correct
  verdicts ("Honey, can you grab my keys?"). Follow-ups get their own message. It stays: on the
  corpus it rescued a genuine "Who pitched?" and flipped 3 correct rejections; a double-checked
  rejection takes ~2.8 s on the GPU (one answer ~0.6 s).
- **A bare "Not for me." / "Not for you." reply is the sentinel**: the model wrote the verdict as
  words twice in the runs, which would have been spoken.
- **Ignored answers don't restart the window** (node, `IgnoredGraceSecs` 2): the window keeps what
  is left of it, at least 2 s. Before, every rejected answer bought the room a fresh 8 s.

Still answered: questions about the listener's own plans that Jarvis could answer about itself
("Are you going to bed now too?", "How do you like yours?"), "Ugh, I totally forgot about that"
after a calendar answer, the near side of a phone call ("Yeah, I'll be there in ten minutes"),
and anything after Jarvis asked a question. A harness note: until this change the semantic evals
scripted the woken reply once, and an action-shaped command ("Turn off the lights", "Set a timer")
drew the must-call retry, which got the fake engine's default "ok"; `scriptWake` now scripts both
and fails if the history doesn't hold the intended reply. The 2026-10-10 table above was measured
with that flaw for its action-shaped contexts.

**Python node (jarvis-node-setup): plan, not done.** It already benefits from the server half: a
rejected follow-up no longer enters history and skips the double-check when a level or voice
doubts it. To get the rest, one PR:
1. `core/follow_up_loop.py:164`: pass `conversation_id=conversation_id` to
   `transcribe_with_speaker` (the client takes it; without it there is no voice comparison: §1.4).
2. Level: compute the speech level (75th-percentile 80 ms RMS of chunks ≥ the silence threshold,
   dBFS) on the *raw* frames before `_normalize_frames_to_target_rms`: in `listen()` for the wake
   command (`scripts/speech_to_text.py`) and in `listen_for_follow_up`; return it beside the path;
   send `level_vs_wake_db` (follow − wake, 0.1 dB) from `continue_conversation`/
   `process_voice_command` follow-up calls (`clients/jarvis_command_center_client.py`).
3. `keep_listening`: add it to the response model
   (`clients/responses/jarvis_command_center/tool_calling_response.py`) and to
   `_not_for_me_result` (`utils/command_execution_service.py:733`, skip the LED flash when set); in
   both `not_for_me` branches of `follow_up_loop` (`:353`, `:381`), when `keep_listening` is set:
   don't break, don't arm the not_for_me cool-down, don't increment `iteration`, keep
   `tts_end_ts`/history so nothing is replayed; cap at 5 per window.
4. Defaults (`Config` and `scripts/speech_to_text.py:637-642`, the settings snapshot):
   `follow_up_listen_seconds` 8, `follow_up_min_timeout_secs` 5, `follow_up_silence_duration`
   1.2, follow-up max record 10 s.
5. Tests in `tests/test_follow_up_loop.py`: keep_listening keeps the window open without speaking
   or counting an iteration; a plain not_for_me still closes it; the level is sent.

### 3.11 Phase 4.1: the HAT on the Go node (2026-10-12)

The Go agent now drives the ReSpeaker 2-mics HAT: capture from `dsnoopmic`, playback to `output`
(the device names the Python node's `/etc/asound.conf` defines; the existing purego ALSA backend
needed no change), the three APA102 LEDs and the GPIO17 button. No Pi-specific binary: the HAT is
used when `hat.Detected()` (a Raspberry Pi, `/dev/spidev0.0`, and the `seeed2micvoicec` card), or
with `--hat on`; `--hat off` disables it.

```bash
jarvis-agent run --voice --voice-input dsnoopmic --voice-output output     # the Pi node
jarvis-agent listen --voice-input dsnoopmic --voice-output output --once   # one scripted turn
jarvis-agent hat cycle | hat pattern thinking --for 5s | hat button --for 30s   # by hand
JARVIS_AGENT_LOG=debug ...                                                 # logs each LED state
```

**What was ported** (`internal/agent/hat`, `cmd/jarvis-agent/hat.go`):
- **LEDs** = `respeaker_led_service.py`: the 11 patterns with Python's colours and brightness, the
  two layers (a stable idle/alert pattern under a transient command-flow overlay), the preview
  auto-clear (with Python's fix: a real state set during a preview survives its timer),
  `led_enabled` and `led_brightness_percent` (as `--led-brightness`; the settings snapshot comes
  with parity item 5.5). Frames are apa102_pi's byte for byte (start frame, `0xE0|ceil(pct*31/100)`,
  B, G, R, the SK9822 reset frame, the trailing zeros). **Golden-tested against the Python code
  itself:** `tools/golden/export_hat.py` runs the node's `_PATTERNS` and `apa102_pi`'s own
  `set_pixel`/`show` on the Pi (528 pattern samples, 6 frames → `internal/agent/hat/testdata`).
  Deliberate difference: Python redraws at 20 Hz always; Go draws a static pattern once and
  ticks only for the animated ones (thinking, shutdown_warning). The 20 Hz loop cost ~3% of a
  Pi 4 core; the Go renderer is now within measurement noise of `--hat off`.
- **Voice states** come from the island's `voice.Indicator` (one fan-out feeds both): wake →
  `wake_detected` (purple) for 300 ms, as Python shows it during the wake ack, then `listening`;
  `thinking`; `speaking`; the follow-up window → `listening` (as Python); idle → the stable pattern;
  a failed turn → `error` for 1.5 s (Python clears red once the spoken error ends).
- **Button** = `button_service.py`: active low with pull-up via go-gpiocdev (gpiochip0 line 17),
  50 ms debounce, `shutdown_warning` after 1 s held, power off after 3 s
  (`sudo -n systemctl poweroff`; `--button-poweroff=false` turns it off), the overlay cleared on
  every release. A short press reads every pending alert, most urgent first, then removes them,
  or says "No new notifications." (N:`announce_pending_and_flush`); with nothing heard (no
  pipeline listening) they stay queued and the LEDs flash `error` for 2 s (loop #175). The MQTT
  `button/*` publishes are not sent (the ACL denies them, §1.4).
- **Alert pattern** (loop #175): the stable layer is `alert` (middle LED purple) while the queue
  holds an alert at priority 3 or above, `normal` otherwise (N:main.py's `on_change`, re-synced
  by the queue's 10 s sweep).
- **ACT-LED fallback** (loop #441): with no HAT chain (`--hat auto` on a Pi without the HAT, or
  the HAT's LEDs unusable) and `--hat` not `off`, `openHat` drives the Pi's green activity LED
  instead (`hat.NewACTLEDs`, N:`led_service.py`): normal is lit, alert, error and
  shutdown_warning blink, the command-flow overlays (listening, thinking, …) show the stable
  pattern, as Python's. No button; `hardware.hat_detected` and `led_chain_available` stay false.
  (`preview_led_pattern` over MQTT came with parity item 5.4, §3.14.)

**Reminders and alerts on the lab Pi 4** (loop #175; not run yet: no runner reaches the Pi). Lab
Pi 4 only, never the kitchen node:

```bash
sudo systemctl stop jarvis-node
JARVIS_AGENT_LOG=debug jarvis-agent run --profile pi --voice --voice-input dsnoopmic --voice-output output
# "hey Jarvis, remind me in 2 minutes to check the oven" → "Reminder set: …"
#   by ~5 min after setting it (the poll is 300 s while none is pending, then 60 s):
#   log "reminders: due", the middle LED purple,
#   then "Reminder: check the oven" said between turns, log "alerts: announced"; LED back to white
# set another, and keep a turn going when it falls due: it is said once the turn ends
# "hey Jarvis, what's up?" with one queued: whats_up lists it, the LED goes white
# a short button press with one queued: it is read out; with none: "No new notifications."
# the push (reminder settings: push notifications on): the owner's phone gets "Reminder"
sudo systemctl start jarvis-node
```

**Verified on hardware** (Python node stopped for each test and restarted after; both Pis are back
as they were):

| | Pi 4 (<pi4-node>) | Zero 2 W (<dev-node>) |
|---|---|---|
| All 11 LED patterns written over SPI (`hat cycle`) | no errors | no errors |
| Button line | `input bias=pull-up edges=both consumer="jarvis-agent"` | same |
| Voice turn, `wav:` input of "hey jarvis, what time is it?", reply to a WAV | wake 0.815, "What time is it?" → "It's just past midnight." | wake 0.815, same transcript, reply spoken |
| LED states logged in that turn | listening → thinking → speaking → follow_up → idle | same |
| Idle listening on the HAT mic (`dsnoopmic`), 5 min, CPU of one core | 29.1% (26–28% in later 2-min runs) | **55.0%** |
| Memory: anonymous (private dirty) / RSS | 22.5 MB / 85 MB | **23.7 MB** / 87 MB |
| Overruns / pipeline restarts in that window | 0 / 0 | 0 / 0 (54.8 °C, not throttled) |
| Python node, whole service, idle listening, 5 min | invalid (its config points at a throwaway jarvisd, the listener is parked: §3.1's note) | **87.1%, 129 MB anonymous** (106 MB of swap in use) |

RSS is mostly clean file-backed pages (the 51 MB binary with the embedded onnxruntime, and the
extracted library), which the kernel can drop under pressure; the private footprint is ~23 MB,
below the spike's `wakelive` USS (36 MB). The Zero's 55% matches the bare wake loop (54.5%, §3.6):
the agent around it costs nothing measurable.

**Not verified:** the HAT *speaker* and the mic *acoustically* (it was after midnight: earlier
spikes kept playback inaudible after 23:00 too; on the Pi 4 the mic doesn't hear its speaker at
the current mixer levels, RMS unchanged). The capture path itself is verified (`dsnoopmic`, 0
overruns in 5 min on both), playback via `output` was measured in §3.2. Pressing the button and
seeing the colours need a person. snd-aloop capture failed with EIO right after start on the Pi 4
(the `plughw` capture side of the loopback; the spike's `wakelive` worked on subdevice 3), so the
end-to-end turn used `wav:` input; not chased: it is a test harness, not a node path.

### 3.12 Date answers, listen onset wait, wake debounce (2026-10-12)

Three small ports of ideas from the Python node's jarvis-node-setup #148/#149 (reimplemented,
not copied).

**`get_current_time` answers date questions** (`internal/agent/clock`). jarvisd speaks a tool's
`message` as is (FastPathMessage), so "what day is it?" used to answer the time. New optional
parameters, all backward compatible (no parameters is still "It's 12:55 AM."):

| Param | Values | Message |
|---|---|---|
| `query` | `time` (default), `date`, `weekday`, `year`, `month`, `weekend` | "It's Monday, October 12.", "It's Monday.", "It's 2026.", "It's October.", "Yes, it's the weekend." / "No, it's Monday." |
| `days_offset` | int, tomorrow 1, yesterday −1; clamped to ±366 | "Tomorrow is Tuesday, October 13.", "Yesterday was Sunday.", "In 3 days it'll be Thursday."; the year is added when it differs ("Tomorrow is Friday, January 1, 2027."); with the default `time` query it answers the date |
| `until_date` | `MM-DD` (a leading year is ignored) | "There are 74 days until December 25." (next occurrence, today = 0; February 29 counts to the next leap year); an unreadable value adds a `note` and answers as without it |

The result always carries `day_of_week`, `year` and `is_weekend` of the asked day (today unless
`days_offset`), plus `days_offset`/`target_date` and `days_until`/`until_date` when used. A
`location` applies to all of them (Tokyo's tomorrow, not New York's).

**`--listen-onset-wait DURATION`** (default 0; `Endpointing.OnsetWaitSecs`). The recording after
the wake word counts trailing silence from its first chunk, so a pause longer than the 0.8 s
silence window (after the 2 s minimum) cut the speaker off. With a wait set, silence can end the
recording only once speech has been heard (a chunk at or above the threshold) or the wait is
over; the 7 s cap still applies. 0 keeps the Python node's behaviour.

**`--listen-preroll DURATION`** (default 1s; `Endpointing.PreRollSecs`; loop #229). The wake
chime plays synchronously after the fire, and everything the microphone captured meanwhile used
to be flushed (the Python node's `history_secs=0`). A command said right after "hey Jarvis"
starts under the chime, so on the Pi 4 "Set a timer" was heard as "It's at a timer". Now up to
this much of the audio queued since the fire (at most the 1.28 s queue) goes in front of the
recording. Endpointing still starts after the chime, and the follow-up gate's level reference is
measured without the pre-roll (it may hold the chime's echo). The wake word is not in it: it
ended before the fire. The turn line reports it (`recorded 2.40 s (pre-roll 0.32 s, …)`, log
`pre_roll_s`), which is how the Pi lane measures the chime gap. 0 is the old behaviour.

**`--wake-debounce DURATION`** (default 8s, the Python node's; at least 500ms): no second wake
this soon after one.

### 3.13 Barge-in (2026-10-17, loop #172)

Saying "hey Jarvis" over a reply stops it (`internal/agent/voice/bargein.go`, a port of
`N:core/barge_in.py`). While a reply plays, a goroutine reads the microphone (otherwise idle
during a turn), decimates and scores it with the same `Detector` as the wake loop, and feeds a
pure state machine (`bargeIn`) with each 80 ms chunk's RMS and score:

- the first 0.5 s is skipped (not scored); the next 1 s scores the detector (to prime it) and
  learns the reply's level through the mic: effective energy gate = max(500, p75 RMS × 2);
- a chunk qualifies when score > 0.04 **and** the loudest chunk of the trailing 1 s beats the
  gate (oww peaks 0.5-1 s after the voice's energy); 2 in a row fire. Python's "tier 1"
  (score > 0.5 with the same gate) is implied.

On a fire the reply's request is cancelled, the speaker is closed without draining (ALSA
`snd_pcm_close` drops what it holds; AudioQueue stops immediately), and the exchange ends with
outcome `interrupted` and follow-up end `barge_in`: no error chime or LED, no follow-up
window, nothing sent but the usual `/conversation/end`; the pipeline goes back to the wake word
(Python does the same). The detector is reset after every watched reply. Worst-case reaction
is one speaker write (≤ ~170 ms at 24 kHz) after the second qualifying chunk, plus the device
buffer dropped on close; a fire during the final `Drain` (≤ 200 ms of buffer) is not cut.

Watched: the wake reply (streamed) and every reply `say`s (follow-up replies and wake-turn
replies that come back as control responses). **Not watched**, as in Python: validation
questions (the user must answer them anyway) and command-center error messages.

Config: `Config.BargeIn` (`DefaultBargeIn()`: Python's `barge_in_enabled`, `barge_in_threshold`
0.04, `barge_in_energy_threshold` 500, 2 confirm chunks, 0.5 s skip, 1 s baseline);
`--barge-in=false` turns it off; the Pi's settings snapshot reports `barge_in_enabled`. With
`JARVIS_AGENT_LOG=debug` it logs the baseline, every score > 0.05 and a summary every 25 chunks
(as Python does), and the fire at info.

**Verified:** unit tests of the state machine (one chunk doesn't fire, two do, a broken run
doesn't, the lagging score, skip/baseline never fire, the p75 baseline blocks the reply's own
wake word) and lockstep rig tests (`bargein_test.go`): the wake reply and a follow-up reply are
cut short, undrained, with no follow-up; one chunk, or a wake word at the reply's own level,
lets the reply play out. **Not verified:** acoustically, on a desktop or the lab Pi 4 (no
runner reaches the Pi, #183). To check on the Pi 4 (never the kitchen node):

```bash
sudo systemctl stop jarvis-node
JARVIS_AGENT_LOG=debug ./jarvis-agent listen --profile pi --voice-input dsnoopmic --voice-output output
# "hey Jarvis, tell me a long story" → say "hey Jarvis" over it → playback stops within ~0.3 s,
# LEDs leave cyan, back to idle; the turn prints "follow-up window: barge_in".
# Also: a reply that itself says "Jarvis" must play out (log "barge-in: score" without a fire).
sudo systemctl start jarvis-node
```

On a desktop: `JARVIS_AGENT_LOG=debug jarvis-agent listen --once`, ask for something long and
say "hey Jarvis" over it (`--once` exits 1: the turn ended `interrupted`).
### 3.14 Parity 5.4: node settings, LED preview, ambient noise (2026-10-17)

The Pi profile answers three more `/commands` verbs (`internal/agent/pinode`, handed to
`node.Runner.Verbs` beside the voice-profile verbs of parity 10; the desktop profile still
ignores them). Decisions:
[decisions/2026-10-17-pi-node-settings-verbs.md](../decisions/2026-10-17-pi-node-settings-verbs.md).

**`update_node_config`** (`N:handle_update_node_config`). First, exactly as Python's
`_reject_trust_anchor_keys`: `node_id`, `api_key`, every `*_url`, `mqtt_*` and `_*` key and the
update/egress consent keys (`allow_updates`, `wake_word_model_autodownload_enabled`,
`release_track`) are dropped and logged. Then each key the Go node has is applied live and saved
to agent.json in one serialized write (`settings.Update`, shared with the voice toggle):

| Python key | agent.json key / flag | Live |
|---|---|---|
| `led_enabled` | `leds` / `--no-leds` | `LEDs.SetEnabled` |
| `led_idle_enabled` | `led_idle` / `--no-led-idle` | `LEDs.SetIdleDark`: false keeps the dim idle glow off, so the LEDs light only while the node responds (wake, listening, thinking, speaking, errors, alerts); the admin's Nodes page shows `led_enabled` and this as one LED mode (always on, only when responding, off) per node, from the `led_mode` capability the node reports |
| `led_brightness_percent` | `led_brightness` / `--led-brightness` | `LEDs.SetBrightness` |
| `wake_word_threshold` | `wake_threshold` / `--wake-threshold` | the running pipeline, from its next chunk (`voice.Live.Tune`) |
| `wake_ack_audio_enabled` | `chimes` / `--no-chimes` | same |
| `follow_up_listen_seconds` | `follow_up` / `--follow-up` | same (from the next turn); clamped to 3 s, logged |
| `follow_up_silence_duration` | `follow_up_silence` / `--follow-up-silence` | same |
| `silence_threshold` | `silence_threshold` / `--silence-threshold` | same (`Endpointing.SilenceThreshold`: the static one, used when the pre-wake level isn't known, as Python's) |
| `silence_duration` | `listen_silence` / `--listen-silence` | same (`Endpointing.SilenceSecs`) |
| `follow_up_min_record_after_onset_secs` | `follow_up_min_after_onset` / `--follow-up-min-after-onset` | same (`FollowUp.MinAfterOnsetSecs`) |
| `follow_up_min_speech_secs` | `follow_up_min_speech` / `--follow-up-min-speech` | same (`FollowUp.MinSpeechSecs`) |
| `volume_percent`, `is_muted` | not in agent.json: `sdkhost.Audio` persists them in `volume.json` and applies them again at start | `sdkhost.Audio.SetVolume` / `SetMuted` (pactl on the default sink or `jarvis_output`, else amixer; plus Bluetooth sinks), as `N:set_volume_percent` |

A bad value skips that key with a log line (#295 added the endpointing keys and the volume;
before it they were logged and ignored). `wake_word_model` is
ignored (the Go node has one wake word), so `restart` never restarts anything; every key it takes
applies live. Any other key is logged and ignored. The settings snapshot's `node_config` reports
the values in effect, and `volume_percent` (the persisted level, else the mixer's) and `is_muted`.

**`preview_led_pattern`** (`N:handle_preview_led_pattern`): `hat.LEDs.Preview`, then back to
what was showing (a real voice state set meanwhile survives the timer). `duration_seconds`
defaults to 3 (also for null or a non-number) and is clamped to [0.5, 30]; an unknown pattern
renders off, as Python's. No reply. Without the HAT it logs and does nothing.

**`measure_ambient_noise`** (`N:handle_measure_ambient_noise`): verified with
`POST /commands/{rid}/verify` first. `duration_seconds` defaults to 3, clamped to [1, 10]. The
running pipeline takes `round(d / 80 ms)` chunks from its own capture with wake detection paused
(`voice.Live.Measure`; a turn in progress finishes first, up to 30 s), then starts listening
afresh. Stats as Python's: numpy-linear p50/p75/p95 over float32 and the max, suggested
`clamp(round(p75 × 1.3 / 100) × 100, 1000, 10000)` with Python's half-to-even `round`
(golden: `tools/golden/export_ambient.py` → `internal/agent/pinode/testdata`). Failures post
`{"success": false, "error": …}`: `audio_bus_unavailable` (voice off), `no_audio_captured`,
`capture_failed: …` (including a pipeline that stayed busy).

**Verified** with unit tests and end to end against a sandbox jarvisd: the agent as `--profile pi`
with the three verbs sent through jarvisd's own HTTP API (node-config, led/preview, ambient
measurement and poll), and a `wav:` microphone (a square wave of RMS 1200 → 25 chunks in 2 s,
suggested 1600). **Not yet on a Pi.** On the lab Pi 4 (never the prod kitchen node):

```bash
sudo systemctl stop jarvis-node
jarvis-agent run --profile pi --voice --voice-input dsnoopmic --voice-output output
# In the app's node settings for this node:
#   Test LEDs: each pattern shows ~3 s, then the previous state returns
#   LED brightness / LEDs off: applies at once; restart the agent: still applied (agent.json)
#   Silence threshold → Set Automatically: a value 1000..10000 comes back; the log has p50/p75/p95
#   Wake sensitivity: the log's next wake uses it; agent.json has wake_threshold
sudo systemctl start jarvis-node
```

### 3.15 Parity 5.8: Bluetooth (2026-10-18, loop #178)

The Pi profile subscribes to the six `bluetooth-*` topics jarvisd publishes
(`internal/agent/bluetooth`, wired by `cmd/jarvis-agent/bluetooth.go`; the desktop profile
doesn't). BlueZ is reached over D-Bus (`bluez_linux.go`: `Adapter1`, `Device1`,
`AgentManager1`, and the node's own `Agent1`), PulseAudio through `pactl`. Without BlueZ (no
system bus, no adapter) the node runs without Bluetooth. Decisions:
[decisions/2026-10-18-pi-node-bluetooth.md](../decisions/2026-10-18-pi-node-bluetooth.md), and
#433's fixes after the first run on jarvis-pi4:
[decisions/2026-10-21-pi-node-bluetooth-output-and-progress.md](../decisions/2026-10-21-pi-node-bluetooth-output-and-progress.md).

| Topic (payload) | Does | Reply |
|---|---|---|
| `bluetooth-scan` (`request_id`, `role`) | drop unpaired cache entries, one BR/EDR discovery of at most 10 s that ends 4 s after the last new device (#469), list the named devices and every paired one with `device_type` (audio icon or Audio Sink → `audio_sink`, phone icon → `phone`, Audio Source → `audio_source`) and their real `paired`/`connected`; the list so far is posted each second it grew | `POST …/bluetooth-scan/{rid}/progress` `{"progress": "scanning", "devices"}`, then `POST …/bluetooth-scan/{rid}/results` `{"devices": […]}` or `{"devices": [], "error"}` |
| `bluetooth-pair` (`request_id`, `mac_address`, `role`) | a bonded device is connected with its bond first; only if that connect fails and a discovery sees it in pairing mode is the bond removed (#469); rescan ≤12 s if BlueZ doesn't know it, pair (30 s; a Page Timeout back within 4 s is retried once after 2 s), trust, connect; an audio device must show a bluez sink/source for 2 s within 20 s, or have BlueZ's media transport up (a warning), or it is disconnected; `role: source` makes its bluez sink the default sink, `role: sink` loads the phone's loopback; saved for the reconnect loop | `POST …/bluetooth/pair/{rid}/progress` `{"progress": "finding"/"pairing"/"connecting"/"audio"}` as each step starts, then `POST …/bluetooth/pair/{rid}/results` `{"success", "device_name"}` or `{"success": false, "error"}` (N:'s messages; `"paired": true` when the device stayed bonded) |
| `bluetooth-disconnect` (`mac_address`) | give the output back to the sink it replaced, unload its loopback, disconnect | – |
| `bluetooth-discoverable` (`timeout`, default 120) | Powered, Pairable, DiscoverableTimeout, Discoverable, in N:'s order | – |
| `bluetooth-release` (`mac_address`, `forget`) | as disconnect, and turn its auto-connect off; `forget`: remove the pairing (one BlueZ no longer knows is fine) and the record | – |
| `bluetooth-auto-connect` (`mac_address`, `enabled`) | set the saved device's auto-connect (a device never paired here is ignored) | – |

At start the agent is registered as BlueZ's default and the adapter made pairable; a pairing a
phone starts (the node discoverable) is trusted and connected, as N:'s agent does on
`Paired: yes`. The reconnect loop runs 30 s after start, then every 10 minutes. A connected
speaker or headphones (role `source`) are Pulse's default sink, so replies and music play on them;
`jarvis_output` is the default again when they disconnect (#433). A saved device that connects by
itself (headphones turned back on) gets the output again at once (#469). Each time the output
moves to or from a speaker, the node restarts the music players that are running (shairport-sync,
spotifyd, go-librespot; `packages.System.RestartUnits`): a sink switch can kill a player's Pulse
stream for good (#478). A package's system unit goes through `jarvis-post-install restart-service`,
a user unit through `systemctl --user restart`; a failure is logged, never fatal
([decision](../decisions/2026-10-22-pi-node-bluetooth-player-restart.md)).
`install-pi.sh` installs `bluez`, `pulseaudio-module-bluetooth` and `rfkill`, unblocks the radio
and sets BlueZ's Class of Device to audio (0x240404) in `/etc/bluetooth/main.conf`.

**Verified** with unit tests over an in-memory bus and Pulse, the D-Bus client and agent against a
fake `org.bluez` on a private dbus-daemon (names, signatures, BlueZ's errors), the handler
against jarvisd's own routes through the node's HTTP client (`internal/modules/cc`
`TestGoNodeBluetoothAgainstTheseRoutes`), and `install-pi-check`. **Not yet on a Pi.** On the
lab Pi 4 (never the prod kitchen node), with a Bluetooth speaker in reach:

```bash
sudo systemctl stop jarvis-node
groups | grep -qw bluetooth && rfkill list bluetooth   # in group bluetooth; not soft-blocked
jarvis-agent run --profile pi --voice --voice-input dsnoopmic --voice-output output
# The app, this node's Hardware tab:
#   Scan: the speaker (in pairing mode) is listed as audio_sink; the log has "scan complete"
#   Pair it: "Paired"; bluetoothctl info <MAC> shows Paired/Trusted/Connected: yes
pactl list sinks short            # a bluez_sink.<MAC>.a2dp_sink, and jarvis_output
pactl get-default-sink            # the bluez sink: "What time is it?" is spoken on the speaker
paplay -d bluez_sink.<MAC_>.a2dp_sink /usr/share/sounds/alsa/Front_Center.wav   # plays on the speaker
#   Status (no new scan needed): the speaker under Connected
#   Disconnect: bluetoothctl info shows Connected: no; the bluez sink is gone; pactl get-default-sink is jarvis_output
#   Restart the agent (Ctrl-C, run again): within ~30 s the speaker reconnects ("reconnected" in the log)
#   Auto-reconnect off, restart: it stays disconnected
#   Make discoverable: a phone sees the node and pairs; the log has "trust and connect after an incoming pair"
#   Forget: bluetoothctl devices no longer lists it; ~/.config/jarvis-agent/bluetooth.json drops it; the app's status doesn't list it
#   Forget again: no "remove failed" in the log; pair again from a new scan: it pairs on the first try
sudo systemctl start jarvis-node
```

### 3.16 Parity 5.11: music ducking and takeover (2026-10-19, loop #180 17a)

`internal/agent/music` ports `N:core/music_control.py`; the Pi profile wires it
([decision](../decisions/2026-10-19-pi-node-music-ducking.md), which also lists #180's other
four slices, 17b–17e, still to build).

| When | Does |
|---|---|
| Wake word fires; before each follow-up listen | `Duck`, in the background: SIGSTOP class (mpv, ffplay, cvlc, vlc, spotifyd, librespot) moved to the `jarvis_duck_null` sink and SIGSTOPped; mute-only class (shairport-sync, go-librespot, mpd) set to 20% of the stream (its volume saved once per turn), muted if the volume can't be read |
| The turn is over (follow-ups included) | `Restore` (SIGCONT, moves back to `@DEFAULT_SINK@`, or to `jarvis_ec_sink` while echo cancel is engaged (17b), unmute, saved volumes back), then each `on_response_complete`; a deferred play that ran marks the node's own music as playing |
| The node's own music starts / stops | The HAT's mic PGA drops to 49% / goes back to what it was (a no-op on the installer's 49% baseline); at start, a PGA left at the music level is put back |
| A music command's call with `action: "play"` | `sdkhost.Host.BeforeCall` stops every other player that has an uncorked stream: mpd by `stop`+`clear` on :6600, go-librespot by `POST :3678/player/pause`, the rest by SIGINT; the command's own player (`takes_over_playback_binary`, else N:'s legacy map) is left alone |

The voice pipeline runs the duck and restore steps in order on one background queue
(`voice/music.go`), so a restore never overtakes a slow duck and listening never waits for
pactl; `Run` waits for the queue, so a stopping agent still restores the music. It runs on a Pi
install only (the HAT or `/etc/jarvis-node`): `--profile pi` on a dev box (node-sim, the voice
lane) leaves the user's own players alone.

**Verified** with unit tests: N:'s `tests/test_music_control.py` cases over a fake pactl/pkill
(`music_test.go`, including the control-API invariant that mpd, go-librespot and shairport-sync
are never SIGSTOPped), a fake mpd port and go-librespot API, the PGA read/write against amixer's
output (`hat/codec_test.go`), the pipeline's step order through a whole turn with a deferred
play (`voice/music_test.go`), and the takeover hook through a real Python sidecar
(`TestSidecarTakeoverHook`). **Not yet on a Pi.** On the lab Pi 4 (never the prod kitchen node),
with a music package installed (Spotify or Audacy):

```bash
sudo systemctl stop jarvis-node
jarvis-agent run --profile pi --voice --voice-input dsnoopmic --voice-output output
# "Hey Jarvis, play some jazz on Spotify": the reply, then the music starts (deferred play);
#   the log has "music: restore" then "music: mic gain profile engaged" (or nothing, at 49% already)
pactl list sink-inputs short           # the player's stream on jarvis_output
# "Hey Jarvis" over the music: it drops to ~20% while you speak ("music: duck ... ducked=[N]")
#   and comes back to its own level after the reply ("music: restore ... unducked=[N]")
amixer -c seeed2micvoicec sget PGA     # 49% while the music plays
# "Hey Jarvis, play Z100 on Audacy" while Spotify plays: Spotify stops ("music: takeover ...
#   stopped=[go-librespot]"), then the station plays
# "Hey Jarvis, stop the music": answered at once (go-librespot/mpd were never frozen)
sudo systemctl start jarvis-node
```

#### 17c: tentative wake and `pre_wake_speech_seconds` (2026-10-21, loop #425)

Ports `N:core/wake_loop.py:143-261,403-530` (`voice/tentative.go`, `voice/endpoint.go`
`PreWakeVAD`), with N:'s defaults fixed (`Config.Tentative`, `Config.PreWakeVAD`):

| When | Does |
|---|---|
| The node's own music plays (`Music.SelfPlaying`, the cached flag) and a chunk scores in [0.20, the wake threshold] without firing | Tentative wake: `Duck` in the background and keep scoring ("tentative wake triggered"). Not a fire: no debounce, chime, indicator or turn. At most one per 10 s (the cool-down is the pipeline's, so it outlives a turn) |
| 1.6 s pass with no fire, or listening pauses (a voice take, a room measure) or stops | `Restore` ("tentative wake expired", with the peak score and the reason) |
| A later chunk inside the window scores above 0.35 (`FireThreshold`, or the wake threshold if lower; loop #503, [decision](../decisions/2026-10-22-pi-node-wake-over-own-music.md)) and passes the wake gate | It fires: through the canceller the music holds "hey Jarvis" to ~0.33-0.51, under a calibrated 0.50. The chunk that opens the tentative never fires below the threshold on its own |
| The wake word fires inside the window | The fire completes it (`Turn.Tentative`; "wake" logs `tentative_triggered` / `tentative_completed`). The turn's own duck is a no-op on the levels: `Ducker.Duck` keeps the first saved volume |
| While the band is armed | The wake clip's ring holds 2 s + 1.6 s; a fire that completes a tentative sends all of it (the phrase from before the duck), any other fire the usual 2 s |
| Every wake turn | `/voice/command/stream` carries `pre_wake_speech_seconds` (chunks of the 5 s pre-wake window above clamp(median × 3, 300, 2500), × 80 ms; the static 2500 with no floor or during the node's own music) and `self_playback` (+ `self_playback_kind: "music"`), snapshotted at the fire. "wake" logs the threshold and its source (`auto`, `static`, `static_self_playback`) |

Not ported here: N:'s self-playback bypass of the adaptive *silence* threshold
(`wake_loop.py:575`); 17b left it open too (below). Note that command-center's wake
verification transcribes the leading 2.2 s of `speaker_audio`; a tentative-completed clip
(up to 3.6 s) puts the oldest audio there, as N:'s does.

**Verified** with unit tests (`voice/tentative_test.go`: the duck-then-expire, the completing
fire and its clip, the jarvis-pi4 scores of #503 completing below the 0.50 threshold, the lower
bar only inside a window, the cool-down, no music, stopping mid-window, the pre-wake seconds table and
the JSON). **Not yet on a Pi.** On the lab Pi 4, with music playing through a music package:
"Hey Jarvis" over it; the log has "tentative wake triggered", then "wake ...
tentative_completed=true", the music ducking before the chime. A lyric that scores in the band
logs "tentative wake expired ... reason=window" and the music comes back within ~2 s.

#### 17b: echo cancellation during music (2026-10-22, loop #424)

N:'s `core/echo_cancel.py` moves a Pulse capture stream onto `jarvis_ec_source`, but both nodes
capture from the HAT through ALSA dsnoop and Pulse has no mic source, so on a Pi it never
engages (§1.4). The Go node does real AEC instead
([decision](../decisions/2026-10-22-pi-node-echo-cancel.md)): `music/echocancel.go`, the
mic as a `device.Switch`, and asound.conf's `pcm.jarvis_ec` (type pulse, device
`jarvis_ec_source`; libasound2-plugins). With the HAT only.

| When | Does |
|---|---|
| The node's own music starts (a deferred play), or a restore finds it playing (also music a phone started) | `EchoCancel.Engage`: `module-alsa-source device=dsnoopmic source_name=jarvis_mic_raw rate=16000 channels=1`, `module-echo-cancel aec_method=webrtc` (its gain controls off; the PGA is the music profile's) with `source_master=jarvis_mic_raw sink_master=jarvis_output`, `set-default-sink jarvis_ec_sink`, the players' streams moved there, and the pipeline's mic switched to `jarvis_ec` before its next read ("music: echo cancel engaged") |
| A wake fires during the music | Nothing: a duck never touches the canceller, so the command is recorded on the same device as the wake |
| A wake fires while a restore's engage or disengage still runs (the background queue) | The pipeline holds its mic (`Switch.Hold`) from the wake to the end of the turn, and for a take; the switch asked for meanwhile applies at the release, so the command and its follow-ups stay on one device |
| The restore after a turn whose duck saw no music | `Disengage`: the mic back on `dsnoopmic`, the default sink back to what it was, the streams on `jarvis_ec_sink` moved there, the two modules it loaded unloaded (last first) |
| An engage step fails | What it loaded is unloaded, capture stays on `dsnoopmic`, one warning; no retry until the music stops and starts again |
| `jarvis_ec` won't open, errors, or reads 3 s of pure digital silence (\|s\| ≤ 1) | The switch goes back to `dsnoopmic` by itself (no read error reaches the pipeline), then the canceller disengages; no retry until the next start ("voice: echo-cancelled capture failed") |
| The agent starts | `jarvis_ec_*` modules a crash left loaded are unloaded |

The canceller follows the cached "self playing" flag, which only a duck refreshes (as N:), so
after "stop the music" it stays loaded until the next turn's restore. Still open: N:'s
self-playback bypass of the adaptive silence threshold (`wake_loop.py:575`).

**Verified** with unit tests over a fake Pulse (`music/echocancel_test.go`: the engage and
disengage calls in order and exactly the modules loaded, each failing step leaving nothing,
no retry before a rearm, the stale-module cleanup, the Ducker's edges) and fake captures
(`device/switch_test.go`: switching only between reads, a Use held until the Release, the
open/read/silence fallbacks), the pipeline holding the mic for a turn and releasing it before
the restore (`voice/music_test.go`), and `voice.Serve` reading through the switch started on
the input (`voice/serve_test.go`).
**Not yet on a Pi**, and the webrtc canceller's CPU on a Zero 2 W is unmeasured. On the lab Pi:

```bash
# "Hey Jarvis, play some jazz on Spotify"; after the reply the log has "music: echo cancel engaged"
pactl list short modules | grep -E 'jarvis_(mic_raw|ec)'   # the two modules
pactl list short sink-inputs                              # the player on jarvis_ec_sink
top -b -n 1 | grep pulseaudio                             # the canceller's CPU (Zero 2 W: QA)
# "Hey Jarvis" over loud music: it fires (compare with 17a alone)
# "Hey Jarvis, stop the music", then any turn: "music: echo cancel disengaged", modules gone
# a wake right after a turn during music: the command still transcribes across the switch
```

Raw vs cancelled wake score (#514): does the canceller — or its webrtc `noise_suppression` /
`high_pass_filter`, both on by default — attenuate "hey Jarvis"? On the lab Pi, with music
playing at low volume and the log showing "music: echo cancel engaged":

```bash
jarvis-agent wake-compare   # records jarvis_mic_raw and jarvis_ec_source side by side for 8 s;
                            # say "hey Jarvis" once while it runs, then compare the two maxima
                            # (the node answers too; the wake frames score before the duck)
# the A/B: a webrtc filter off, restart, play music again (re-engages), compare again
sudo systemctl edit jarvis-agent.service   # [Service] Environment=JARVIS_AGENT_EC_NOISE_SUPPRESSION=0
                                           # and/or Environment=JARVIS_AGENT_EC_HIGH_PASS_FILTER=0
sudo systemctl restart jarvis-agent.service
```

If the cancelled max trails the raw one with a filter on and recovers with it off, set that
filter to 0 in `echocancel.go`'s `aecArgs` (a one-line follow-up); the shipped default stays
webrtc's until the measurement says otherwise. The engage is best-effort as ever: a Pulse
that refuses the extra aec_args keys falls back to the plain mic ("echo cancel not engaged").

### 3.17 Parity 5.9a: the smart-home device registry (2026-10-19, loop #176)

The Pi profile answers the app's device requests over the household's device families, which
are Pantry packages' Python (`device_families/<name>/protocol.py`, SDK `IJarvisDeviceProtocol`;
managers in `device_managers/<name>/manager.py`). The package sidecar loads them (a Pi's
`commands.json` lists `families` and `managers` per package) and does what N:'s
`device_*_handler.py` did; Go (`internal/agent/devices`, wired by
`cmd/jarvis-agent/devices.go`) owns MQTT, the uploads and the registry. Decisions:
[decisions/2026-10-19-pi-node-device-registry.md](../decisions/2026-10-19-pi-node-device-registry.md).

| Message | Does | Reply |
|---|---|---|
| `device-scan` (`request_id`) | every configured family's `discover()` (10 s, together), deduplicated by MAC > IP > cloud id > entity id, a taken `entity_id` suffixed `_<protocol>`; `extra` dropped, the family's `supported_actions` added | `POST …/device-scan/{rid}/results` `{"devices": […]}` or `{"devices": [], "error"}` |
| `device-list` (`request_id`, `manager_name`; jarvisd sends `all`) | `jarvis_direct` (the families' devices as manager devices), a Pantry manager's `collect_devices()`, or all of them | `POST …/device-list/{rid}/results` `{"devices", "manager_name", "can_edit_devices"}` or the error shape |
| `device-state` (`request_id`, `entity_id`, `domain`, `protocol`, `source`, `local_ip`, `mac_address`, `cloud_id`) | the family's `get_state()`, normalised by N:'s domain handler (light, climate, switch, lock, camera, cover, media_player, kettle) with UI hints; `needs_pairing` → `control_type: pairing` | `POST /device-state-results/{rid}` |
| verb `invalidate_device_cache` | drop the registry; the next read fetches it | – |
| verb `device_removed` (the device) | the family's `on_removed()` (a HomeKit unpair), then drop the registry | – |
| `jarvis/auth/{provider}/ready` (`provider`, `node_id`) | this node's only: `GET /oauth/provider/{p}/credentials` (once), `user_id` as the SDK user, `base_url` also as `_base_url`, to the command or family whose `authentication.provider` matches (`store_auth_values`); beat again | – |

The registry is `GET /api/v0/node/devices` (direct devices), fetched on the first connect and
every 5 minutes, for device control (#177). The heartbeat sends `protocols` (the configured
families, sorted) and beats again when the sidecar comes up, so jarvisd's `pickNode` sends a
family's device state and removal here. The settings snapshot lists `device_families` and
`device_managers` in N:'s shape, so the app can set a family's secrets on this node. The
families are described again for every snapshot and after each secrets push (which also beats),
so a family the app configures with an API key shows `is_configured` and joins `protocols`
without a restart.

**Verified** with sidecar tests over a fake family package
(`internal/agent/sidecar/testdata/packages/lights`: scan, collisions, list, state per domain with
Python's rounding, pairing, `on_removed`, OAuth turning a family on), handler and registry tests
over a fake jarvisd, the runner's subscription to `jarvis/auth/+/ready` on the real broker, and
end to end on a sandbox jarvisd: the real `jarvis-agent --profile pi` (`scripts/sandbox node-sim`)
with that package, through jarvisd's own routes (scan request → completed with the devices,
list, import, state of a light and a thermostat, delete → `on_removed`). **Not yet on a Pi** or
with a real family. On the lab Pi 4 (never the prod kitchen node), with a family package from
Pantry whose devices are on the LAN (Kasa, LIFX, Govee):

```bash
sudo systemctl stop jarvis-node
jarvis-agent run --profile pi --voice --voice-input dsnoopmic --voice-output output
# The app: install the family's package on this node (Pantry), then Settings → Smart home:
#   primary node = this node
#   Devices → Scan: the LAN devices are listed; the log has "devices: scan requested"
#   Add them; open one: its state and controls show (the log has "devices: state requested")
#   A cloud family with OAuth: sign in; the log has "devices: auth credentials stored", and the
#     next scan lists its devices
#   Delete a device: the log has "devices: device removed ... ran=true"
sudo systemctl start jarvis-node
```

### 3.18 Parity 5.9b: device control, actions, callbacks, toggle_command (2026-10-19, loop #177)

Three verbs from N:'s `mqtt_tts_listener.py` (`handle_action` `:193-298`, `handle_callback`
`:302-452`, `handle_toggle_command` `:1009-1040`), in `internal/agent/actions` (wired by
`cmd/jarvis-agent/actions.go` on **every profile**). The commands and families run in the
sidecar (`action` and `callback` methods in `jarvis_sidecar.py`); Go verifies, fetches and posts.
Decisions: [decisions/2026-10-19-pi-node-device-control.md](../decisions/2026-10-19-pi-node-device-control.md).

| Verb (details) | Does | Reply |
|---|---|---|
| `action` (`request_id`, `command_name`, `action_name`, `context`, `user_id`?, `reply_request_id`?) | `POST /commands/{rid}/verify` first (dropped, no reply, if it fails: jarvisd never sends `trusted`); then the command's `handle_action(action, context)` as the user, or for `control_device` with no such command (or one the household turned off) the family named by `context.protocol`: `control(device, action, context)`, the device built from the context as N: does and passed where the SDK says `ip`, on one persistent event loop so a pairing session survives `pair_start` → `pair_finish`. A voice-originated action (no `reply_request_id`) has its `message` said between turns | `POST /device-control-results/{reply_request_id or request_id}` `{"success", "error", "input_required"?}` (top level, not `{"output"}`) |
| `callback` (`request_id` = the job) | `GET /callbacks/{job}` (the verification; nothing is posted if it fails), then the command's `@callback` (`get_callbacks()[name](data, RequestInformation)`) as the user | `POST /callbacks/{job}/result` `{"success", "error", "context_data"?}`; every failure after the fetch is a result too (N:'s messages: unknown command, no callbacks, unknown callback, a raise) |
| `toggle_command` (`command_name`, `enabled` read as N: does: true/1/yes) | the settings store's switch (the `command_registry` push's): the command leaves `report_tools` and voice turns, and `action`/`callback` treat it as absent | – |

**Verified** with handler tests over a fake jarvisd (verification, both reply ids, the top-level
body, input_required, the spoken message, every callback failure, the lax bool), real-sidecar
tests (the fake `fakelight` family: the device it is handed, a raise, unknown protocol, pairing
across calls with a scan in between; a test command's `handle_action`, SDK `cancel_click`,
`@callback`s and their failures), and end to end on a sandbox jarvisd: the real
`jarvis-agent run --profile pi` with the fake family, through jarvisd's
`POST /households/{hh}/devices/{id}/control` (turn on/off → 200 `success: true`, a raising
family → `success: false` with its error, `pair_start` → `input_required`, `pair_finish` with the
PIN → `success: true`) and `PUT …/smart-home/config` `use_external_devices` → `toggle_command`.
A routine's device step does not reach a Go node yet: jarvisd sends routines as the `routine`
verb (§1 table, still to port), not as `action`.
**Not yet on a Pi** or with a real family. On the lab Pi 4 (never the prod kitchen node), with a
family package from Pantry whose device is on the LAN (Kasa, LIFX, Govee), after §3.17's steps:

```bash
sudo systemctl stop jarvis-node
jarvis-agent run --profile pi --voice --voice-input dsnoopmic --voice-output output
# The app: Devices → the family's device → toggle it on and off: the device follows, no
#   "node did not respond"; the log has "msg=action command=control_device ... success=true"
# Settings → Smart home → use external devices on and off: the log has "msg=toggle_command"
sudo systemctl start jarvis-node
```

### 3.19 The soak, the parity checklist and the Python node's retirement (2026-10-19, loop #181)

**Status:** the soak tooling is written and tested without a Pi; **the soak has not run** (no
runner reaches a Pi: it is the user's QA, below). **The retirement is BLOCKED** on the open parity
gaps (a)–(e) in the checklist and on a passing soak. The user chose this order on 2026-10-19:
the docs and the script now, the gaps as their own items.

**`scripts/pi-soak.sh`** runs on the Pi as the node's user. `run --lab-host NAME` refuses unless
`hostname` is NAME, and refuses any host whose name contains "kitchen". It stops
`jarvis-node.service`, starts the installed `jarvis-agent.service` (§6; its `Conflicts=` keeps the
two off the HAT together) and puts both back as they were on any exit: the end, Ctrl-C, TERM or an
error. It writes to one directory:

- `samples.csv`: one row every `--interval` (60 s). The agent's CPU as % of one core (from
  `/proc/PID/stat` deltas, as §3.11 measured it), its RSS, PSS, anonymous and swap
  (`smaps_rollup`, read with `sudo -n cat`: the agent is non-dumpable under its unit, so the file
  is EACCES to the node's own user), the same for its child processes (the command sidecar),
  MemAvailable, swap in use, load, SoC temperature, `vcgencmd get_throttled`, and the unit's
  `NRestarts`. A smaps_rollup that still cannot be read leaves those columns empty (never 0), the
  run warns once, and the summary counts those samples and says `unreadable` instead of 0 MB.
- `turns.log`: one scripted turn per command family every `--turns-every` (4 h; the first round
  5 min in). `<family>.wav` from `--wavs DIR` plays out of the HAT's speaker (`aplay -D output`)
  to the running node. It passes when the node logs `msg="voice turn" outcome=spoken` within
  `--turn-wait` (90 s), and when the line matches `<family>.expect` (an extended regex), if that
  file exists.
- `journal.log`: the unit's journal for the run, taken after the stop so that it holds the
  capture stats.
- `summary.md`: the numbers for the results table below. Overruns and dropped chunks are the sums
  of `msg="voice: capture stats"` (printed when a pipeline stops, so at every restart and at the
  end). It also counts pipeline restarts (`voice: pipeline stopped; restarting`), wakes
  (`msg=wake`), turn outcomes, false wakes (`non_speech` + `not_for_me` turns), and ERROR/WARN
  lines and panics.

`scripts/pi-soak-check.sh` (CI `deb` job, next to `install-pi-check.sh`) runs it against a fake
`/proc`, `/sys`, systemctl, journalctl and hostname. It checks the refusals, the stop/start order,
the sample columns, the turn verdicts, every summary count, `summarize` again from the files, and
a TERM partway through that still puts the Python node back. The awk is POSIX (Raspberry Pi OS
ships mawk); the check also passes under `gawk --posix`.

**Running the soak** (user QA). Use the lab Zero 2 W (<dev-node>), or the lab Pi 4 (<pi4-node>)
for a first short run. **Never the prod kitchen node.** The §3.11 rules apply: the Python node
stays installed and the script starts it again at the end. Do not reboot during the run: the
Python unit is still enabled, so a boot would bring it back and stop the Go node (the samples
show `pid` 0).

```bash
# Your machine: the script to the lab Pi.
scp scripts/pi-soak.sh <user>@<dev-node>.local:~/
ssh <user>@<dev-node>.local

# The Pi, once: the Go node next to the Python node, NOT activated (the unit stays disabled).
curl -fsSLo install-pi.sh https://github.com/alexberardi/jarvis-server/releases/download/<tag>/install-pi.sh && sudo sh install-pi.sh
# Its own identity, as the node's user (pi, the unit's User=): approve the code it prints in the
# app or the admin Nodes page. It is a new node, not the Python node's (the retirement proposal).
/opt/jarvis-agent/jarvis-agent enroll --server <prod-host> --room lab

# The turn recordings, once, with the Python node stopped so that the mic is free. Say each
# phrase when "say:" appears; 5 s each. Families: the built-ins in the sidecar plus music.
sudo systemctl stop jarvis-node
mkdir -p ~/soak-wavs && cd ~/soak-wavs
rec() { echo "say: $2"; sleep 1; arecord -q -D dsnoopmic -f S16_LE -r 16000 -c 1 -d 5 "$1.wav"; }
rec time       "hey Jarvis, what time is it?"
rec weather    "hey Jarvis, what's the weather today?"
rec timer      "hey Jarvis, set a timer for one minute"
rec reminder   "hey Jarvis, remind me in two minutes to check the soak"
rec calculate  "hey Jarvis, what is twelve times nineteen?"
rec joke       "hey Jarvis, tell me a joke"
rec whats_up   "hey Jarvis, what's up?"
rec music      "hey Jarvis, play some jazz on Spotify"
rec music_stop "hey Jarvis, stop the music"
echo '228' >calculate.expect
echo 'timer' >timer.expect
cd ~ && sudo systemctl start jarvis-node

# The soak: 24 h inside tmux so that it outlives the ssh session (sudo -v first: it uses sudo -n).
sudo -v
# Type the lab Pi's hostname yourself: the guard is that it must equal `hostname`.
tmux new -s soak "./pi-soak.sh run --lab-host <dev-node> --duration 24h --wavs ~/soak-wavs --out ~/pi-soak-zero2w; bash"
# Detach with Ctrl-b d; check on it with: tail -3 ~/pi-soak-zero2w/samples.csv; cat ~/pi-soak-zero2w/turns.log
# Stop it early with Ctrl-C in the tmux window: the Python node is started again and the summary written.

# Afterwards, from your machine:
scp -r <user>@<dev-node>.local:pi-soak-zero2w .
ssh <user>@<dev-node>.local systemctl is-active jarvis-node jarvis-agent   # active, inactive
```

By hand, once during the soak (not scriptable): device control from the app (Devices → toggle a
device, §3.18; voice control is gap (b)); Bluetooth status in the node's Hardware tab (§3.15); a
reminder heard on its own when due (§3.11); the LEDs through a turn and a short button press.
Write the results in the table.

**Results** (to fill in from `summary.md`; the Python column is §3.11's 5-minute measurement on
the same Zero 2 W):

| | Go node, 24 h soak, Zero 2 W | Python node (§3.11, 5 min idle) |
|---|---|---|
| Date, agent version | | 2026-10-12 |
| CPU, % of one core: mean / p95 / max | | 87.1% (mean) |
| RSS MB: min / mean / max (first → last, for a leak) | | – |
| Anonymous MB: mean / max (first → last) | | 129 MB |
| Sidecar RSS MB: mean / max | | (in the process) |
| Swap in use, max | | 106 MB |
| SoC temperature max; throttling | | |
| Unit restarts / pipeline restarts | | 0 / 0 (5 min) |
| ALSA overruns / dropped chunks | | 0 (5 min) |
| Wakes / false wakes | | |
| Scripted turns passed (per family in `summary.md`) | | |
| Errors / warnings / panics | | |
| By hand: device control, Bluetooth, reminder, LEDs and button | | |

**Pass bar to propose** (the user's to change): no unit or pipeline restarts; overruns at most 1
an hour; the last hour's anonymous memory within 10% of the first hour's (no leak); a mean CPU at
or below §3.11's 55% plus the sidecar's idle cost; no throttling; every scripted turn passes; at
most 2 false wakes a day in the lab.

#### Parity checklist (main at 94952cd, 2026-10-19)

"Built" means unit tests or a sandbox jarvisd. "On a Pi" means checked on the lab Pi 4 or the
Zero 2 W. Every Pi item from loop #159–#180, #220 and #229 is still in QA, and none has been
checked on the lab Pi 4 since it was built.

| §7 item | Status | On a Pi |
|---|---|---|
| 4.1 HAT audio, LEDs, button | built | **verified** (§3.11: every pattern over SPI, the button line, a `wav:` turn, idle CPU/RSS, 0 overruns) |
| 4.1 Pi profile, HAT codec self-heal | built | no |
| 4.2 self-update on the Pi | built | partly: `systemd-run --user` reaches pi's manager; **open (e)**: the rollback repro after rerunning `install-pi.sh` (#220) |
| 4.3 `install-pi.sh` (3a, 3b), the release | built | no; no published release yet |
| 4.4 provisioning API, NetworkManager backend, recovery, setup mode at boot | built | no; setup mode at boot (#292) unit-tested, pending the lab Pi |
| 4.5 the built-in commands in the sidecar; reminders, alerts, button | built | no; **open (b)**: `control_device`, `routine`, `send_link` are skipped (`internal/agent/packages/python.go:58`) |
| 5.1 follow-up | built (heard on the desktop) | no |
| 5.2 barge-in | built (rig tests) | no |
| 5.3 client tools / continue | built | no |
| 5.4 `update_node_config`, ambient noise, LED preview | built | no |
| 5.4 the app's Voice Settings: silence threshold and duration, follow-up minimums (#295) | built | no: "Set Automatically", then a turn that ends on the new silence |
| 5.4 Pi volume and mute from the app (`volume_percent`, `is_muted`; #295) | built (`sdkhost.Audio`, persisted in `volume.json`) | no: the slider heard to change the HAT's speaker, and kept after a restart |
| 5.5 K2 + settings snapshot | built for the desktop agent (`internal/agent/nodesettings`); the Pi reports its `node_config`, volume included | **open (c)**: not marked done or checked for the Pi profile |
| 5.6 packages (apt, post_install, test-install) | built | no |
| 5.7 factory reset, routines sync, context requests | built | no; a reset node boots into setup mode (#292), pending the lab Pi; jarvisd's broker drops `context/operations` answers (ACL, held for the user) |
| 5.8 Bluetooth | built | no |
| 5.9 device registry (14a), app-driven control (14b) | built | no; **open (b)**: voice control (`control_device` tool) |
| 5.10 voice-profile takes | built | no |
| 5.11 music ducking and takeover (17a); echo cancellation during music (17b, loop #424); tentative wake and `pre_wake_speech_seconds` (17c, loop #425); wake auto-calibration and the not_for_me cool-down (17d, §3.20) | built | no; 17b's CPU on a Zero 2 W unmeasured; **open (a)**: 17e (#427) built: the greeting prefetch and the maintenance restart, and the ACT-LED fallback (`hat.NewACTLEDs`, opened without a HAT since #441) ([decision](../decisions/2026-10-21-pi-node-17e.md)); pending the lab Pi |

What each open gap blocks, if the Python node were retired today:

- **(a)** echo cancellation (17b), the tentative wake (17c), the auto-calibration and the
  not_for_me cool-down (17d) are built, not yet on a Pi; and the
  ACT-LED fallback for a Pi without the HAT (#441) is built, not yet seen on one.
- **(b)** "turn on the kitchen light", routines and "send me a link" by voice stop working on
  the node. The app's device control still works.
- **(c)** the app's node settings (and their secrets' Sync) may not reach a Pi; not confirmed.
- **(d)** ~~a factory reset leaves a node that is offline with no hotspot~~: built (#292), `run
  --profile pi` with no credentials is in setup mode (hotspot, provisioning API, the HAT's blue
  `setup` breath) until the app provisions it; pending the lab Pi check (reset from the app, the
  phone sees `jarvis-<id8>` within ~60 s, provisioning brings it back).
- **(e)** a bad release that crash-loops may not roll back by itself on a Pi.

#### Retirement proposal (BLOCKED on (a)–(e) and the soak; for the user to approve)

Nothing here is done. It is the order proposed once the checklist has no open row, every Pi item
is checked on the lab Pi 4 and the soak meets the pass bar:

1. **The `install_mode` switch.** Today `install-pi.sh` installs `jarvis-agent.service` disabled
   and `--activate` swaps the nodes
   ([decision](../decisions/2026-10-17-pi-node-activation.md)); jarvisd tells them apart by the
   heartbeat (`install_mode "tarball"`, `runtime "go"`, §8 Q6). Proposed: on a Pi with no
   `jarvis-node.service`, `install-pi.sh` activates by default; on a Pi with one, it stays
   opt-in. After the kitchen node has run on Go for a week, `--activate` becomes the default
   everywhere, and `--keep-python` the way out.
2. **The identity.** The Go node enrolls as a new node (`~/.config/jarvis-agent/node.json`). No
   code takes over the Python node's node id, room or settings: there is none for this in
   `internal/agent` or `cmd/jarvis-agent`. Either a migration is built (an item of its own:
   read the Python node's credentials, keep the node id), or the swap is "enroll, move the room,
   delete the old node" and the docs say so. **The user decides which.**
3. **The docs.** README's Raspberry Pi section (it still says there is no installer, hotspot,
   self-update or package support, and that jarvis-node-setup is the supported Pi node), §6's
   install as the one way to set up a Pi, docs/install's `00-installers.md` (the install-pi.sh
   section becomes the Pi's only installer), `docs/STATUS.md`, and jarvis-docs (STATUS's open
   docs item).
4. **jarvis-node-setup.** Archive the **node** on GitHub, after a final release whose README
   points to `install-pi.sh`: `install.sh`, the service and the node code. **Keep the commands
   package:** the sidecar still pip-installs jarvis-node-setup at a pinned commit
   (`packages.NodeCommandsRequirement`, `internal/agent/packages/python.go:48`) for the
   built-in commands. Before the repository is archived, either move those commands into their
   own package (or Pantry), or keep the repository unarchived and read-only for the node part.
   An archived repository still serves the pinned tarball, but no fix can land in it.
5. **Last:** jarvisd's Python-node-only paths (the jarvis-node-setup release as "latest" for
   nodes with no runtime, §7 4.2) stay until no heartbeat in the field reports a Python node.

### 3.20 Parity 5.11: wake auto-calibration and the not_for_me cool-down (2026-10-21, loop #426 17d)

`voice/wakegate.go` ports N:'s wake gate (`N:core/wake_detector.py` `decide_wake_fire`,
`N:core/voice_filters.py`) and its calibrator (`N:core/wake_calibration.py`)
([decision](../decisions/2026-10-21-pi-node-wake-calibration-and-cooldown.md)).

| When | Does |
|---|---|
| A score is above the threshold | It fires unless the gate is shut. A fire shuts it for the 8 s debounce and drops any override. A held score leaves the gate as it was (log `wake-suppressed-gate` above 0.3) |
| A turn ends `not_for_me` (the woken command or a follow-up answer) | The gate shuts for 20 s, or 60 s with 2 or more verdicts in 120 s, never earlier than it already was, with a 0.6 override: a score of 0.6 or more fires anyway (`wake-override-cooldown`) and leaves only the debounce |
| The woken command ends spoken, silent or interrupted, without an error, not over the node's own music | Its wake score joins the last 20 in `<cache>/jarvis-agent/wake_scores.json`, even when a follow-up answer then ends `not_for_me` (that also arms the cool-down) |
| `wake_word_threshold_auto` is on | The threshold is the 20th percentile of those scores less 15%, in [0.10, 0.50]; `wake_word_threshold` until there are 5 |

The app's `update_node_config` sets `wake_word_threshold_auto` (agent.json
`wake_threshold_auto`, `--wake-threshold-auto`) and the snapshot reports it; the cool-down's
tunables are N:'s defaults, fixed. Not ported: `N:core/false_wake.py` (it needs the node's own
transcript, which the Go node doesn't have before CC; see the decision).

**Verified** with unit tests: N:'s `tests/test_wake_calibration.py` cases and the gate with a
fake clock (`voice/wakegate_test.go`), and through the pipeline with the fake detector: a 0.5
wake held after a `not_for_me` turn, or after a spoken turn whose follow-up answer was `not_for_me`,
and a 0.95 one let through; a spoken turn recorded (with a `not_for_me` follow-up too), a wake
that heard no words and a wake over the node's own music not, and the calibrated threshold letting a 0.5 wake through a static
0.6. **Not yet on a Pi**: the scores a room gives, and the cool-down against a TV. On the lab
Pi 4 (`--wake-threshold-auto`, or the setting from the app), the log has `not-for-me-cooldown-armed`
after a side conversation, then `wake-suppressed-gate` or `wake-override-cooldown`.

## 4. Memory budget (Zero 2 W: 512 MB, ~416 MB usable)

RSS in MB. Measured values come from the Pi 4; arm64 userland memory is the same on a Zero 2 W.
Values marked "est." are estimates.

| Component | Python node today | Python + quick win | Go + sidecar + Pulse | Go + sidecar, no Pulse | All-Go, no Pulse |
|---|---|---|---|---|---|
| Node process | 300 (after a voice turn, measured tonight); 377–385 seen idle with discovery failing | ~215–300 (−84) | – | – | – |
| Go core (enroll, MQTT, heartbeat, dispatch) | – | – | 9 | 9 | 9 |
| Wake: ORT 1.28 + 3 oww sessions + ALSA capture + decimator | (inside the node) | (inside the node) | 31 (40 measured, minus a 9 MB Go runtime already counted) | 31 | 31 |
| Playback, HTTP/TLS, STT and TTS buffers | (inside) | (inside) | ~8 est. | ~8 est. | ~8 est. |
| Python sidecar with the built-in commands | – | – | 70 | 70 | – |
| PulseAudio (+ paplay keepalive) | 31 + 5 | 31 + 5 | 31 + 5 | – | – |
| **Total RSS** | **~340–420** | **~255–340** | **~165** | **~120** | **~50** |

**Notes:**
- RSS double-counts shared pages: `libonnxruntime.so` is a 34.6 MB file of which about 12 MB is
  touched. USS for the Go wake process was 36 MB.
- A minimal ORT build (`--minimal_build` with only the ops these four models use, ORT-format
  models) would cut the library to a few MB. Not needed at these numbers.
- Silero, if added, is +~14 MB while loaded.
- **CPU on a Zero 2 W (proxy):** the always-on wake pipeline is ~50% of one core in Go vs ~66% in
  Python. The Pulse keepalive adds ~4% (on the Pi 4); a Go-held silent ALSA stream would cost less
  than 1%.

---

## 5. Sidecar extraction plan

### 5.1 What the 21 built-in commands need

Commands are in `N:commands/`. Today only `act_on_items` imports with the SDK alone. Most others
fail only because they import 3-line `core.*` files that re-export SDK classes:
`core/command_response.py`, `ijarvis_parameter.py`, `ijarvis_secret.py`, `request_information.py`,
`alert.py`.

| Command | Node dependencies beyond the shims | After the shim + optional-logger fix |
|---|---|---|
| act_on_items | none | runs |
| answer_question, calculator, chat, measurement_conversion, timezone | none | runs |
| tell_a_joke | lazy `JarvisCommandCenterClient.chat_text` (`/node/llm/chat`); has a fallback | runs (degraded without LLM) |
| send_link | `jarvis_log_client`; lazy `RestClient` + discovery → `/node/send-link` | needs **CC POST** |
| shopping_list, todo_list | `jarvis_log_client`; SDK `JarvisStorage` | needs **Storage** backend |
| export_shopping_list, export_todo_list | as above, plus a hand-rolled `/node/inbox-item` POST (should use SDK `JarvisInbox`) | needs Storage + Inbox |
| reminder | `services.reminder_service` (itself only JarvisStorage + logger); firing is in `agents/reminder_agent.py` | needs Storage; the **host** owns the due-poll and announcement (loop #175: the sidecar's `reminders_due`, polled by `internal/agent/reminders`) |
| set_timer, cancel_timer, check_timers | `services.timer_service` (SQLCipher `SessionLocal`, `threading.Timer`, TTS on expiry) | needs **Timer** backend |
| control_node | `utils.audio_volume` (pactl/amixer, config write) | needs **AudioControl** backend |
| whats_up | CC `chat_text`, `services.alert_queue_service` (in-memory), discovery | needs **LLM** + **AlertQueue** |
| routine | CC `chat_text`; `db.SessionLocal` + `CommandDataRepository` (should be JarvisStorage); `command_discovery_service` (runs other commands); `secret_service` | needs **CommandInvoker** + Storage + LLM |
| control_device | lazy `device_families`, `device_family_discovery_service`, `direct_device_service` (httpx, refreshed from CC) | imports; needs **Devices** to run |

### 5.2 Recommendation: SDK backends, not a new package

The SDK already has the pattern: `StorageBackend`/`set_backend`, `InboxBackend`, `SignalsBackend`
(`jarvis_command_sdk/storage.py:30-86`, `inbox.py:62-98`, `signals.py:43-66`). Add backends for
what is left:

| Backend | Minimal API | Python node implements with | Go sidecar implements with |
|---|---|---|---|
| Logger | `get_logger(name)`, with an SDK fallback to `logging` | `jarvis_log_client` | stderr → Go → jarvis-logs |
| LLM | `llm_chat_text(prompt) -> str \| None` | `/node/llm/chat` | an RPC to Go, which holds the key |
| CC HTTP | `cc_post(path, json) -> dict \| None` | `RestClient` | an RPC to Go |
| Timer | `set(seconds, label)`, `cancel(id)`, `list_active()`, `get(id)`, `find_by_label`, `clear_all`; the host fires and speaks | `timer_service` | Go (persisted in the node's state dir; Go speaks on expiry) |
| AudioControl | `set_volume_percent`, `adjust_volume_percent`, `set_muted`, `speak(text)` | `audio_volume`, TTS provider | Go (amixer/pactl, or ALSA mixer via purego) |
| AlertQueue | `pending()`, `flush()`, `remove(ids)`, `count()` | `alert_queue_service` | Go |
| CommandInvoker | `invoke(name, request_info, **args) -> CommandResponse`; the host resolves secrets | `command_discovery_service` + `secret_service` | the sidecar itself (in-process) |
| Devices | `list()`, `control(...)`, `protocols.get(name)` | `direct_device_service`, device families | the sidecar (Python protocol plugins stay Python) |
| Storage (exists) | JarvisStorage | SQLCipher | a Go-owned store over RPC, or a sidecar-local SQLite in the node data dir |

**Rationale.** The Python node, the desktop agent's sidecar and the Pi's sidecar all import the
same commands. The only difference is which backends are registered at startup. One code path, no
new package.

**The sidecar protocol needs one addition.** DESIGN.md §6 has Go → sidecar requests only. Backends
need **sidecar → Go requests on the same stdio channel**: requests with an `id` flowing both ways,
like JSON-RPC.

**Order:**
1. Shim imports → `from jarvis_command_sdk import …` (the 7 shim-only commands import).
2. Optional logger (also frees send_link, shopping_list and todo_list).
3. routine → JarvisStorage; exports → JarvisInbox.
4. Add the Timer, AudioControl, AlertQueue, LLM, CC HTTP and CommandInvoker backends.

### 5.3 The Python quick win: a small separate PR to jarvis-node-setup (plan only; not opened)

**Measured:**

| Variant | RSS / USS |
|---|---|
| Live wake loop, stock | 217 / 197 MB |
| **With both changes** | **133 / 113 MB (−84 MB)** |
| Stub only | 192 MB (−25 MB) |

- Scores are unchanged: pos_tts peaks at 0.9973 with both imports, and negatives stay below 0.06.
- CPU is unchanged.
- Import time drops from 3.2–3.4 s to 0.65 s.

**Changes:**
1. **New `core/oww_import.py`.** Stub `openwakeword.custom_verifier_model` in `sys.modules` before
   any openwakeword import. `openwakeword/__init__.py:4` imports it eagerly, and it imports scipy
   and sklearn at top level; it is training-only, and the node passes no custom verifiers
   (`N:scripts/voice_listener.py:253-254`). Import the helper first in
   `N:scripts/voice_listener.py:25` and before `N:core/wake_models.py:116`.
2. **New `core/resample.py`.** `decimate_int16(x, down)` with `np.convolve` and resample_poly's
   filter (`sinc(t/3)·kaiser(61, 5.0)`, normalised). Replace `N:core/wake_loop.py:124-135,379-381`
   and `N:core/barge_in.py:34-44,258-260,276-278`. Per-chunk behaviour is unchanged (zero-padded
   edges, as today); optionally make it stateful later, like the Go decimator.
3. **Requirements.** Drop `scipy` and `scikit-learn` from `requirements-pi.txt:14,22`,
   `requirements.txt:7,12` and `requirements-audio.txt:11-12`. Keep them in
   `tools/wake_model_training`. Keep `tqdm` and `requests`, which `openwakeword/utils.py` imports.
   `libopenblas-dev` (`N:install.sh:215`) can probably go.
4. **Tests:**
   - Importing `scripts.voice_listener` leaves `scipy` and `sklearn` out of `sys.modules`.
   - `decimate_int16` matches `resample_poly` within ±1 LSB (a dev-only test that keeps scipy as a
     test dependency).
5. **Verify on the Pi 4, then on a Zero 2 W.** Use `memsnap.sh` after a voice turn; expect about
   −80 MB.

---

## 6. Install and system: what the Go node still needs from install.sh

**Install** (64-bit Raspberry Pi OS; each release ships `install-pi.sh` stamped with its tag and
listed in the signed `SHA256SUMS`, #162):

```sh
curl -fsSLo install-pi.sh https://github.com/alexberardi/jarvis-server/releases/download/<tag>/install-pi.sh && sudo sh install-pi.sh
```

To check the script before running it as root, fetch `SHA256SUMS` and `SHA256SUMS.minisig` from
the same release, `minisign -Vm SHA256SUMS -P RWRyW6ICtU+iyX4p4RnS24ju0gRsWpxvv6B8pI9G+ZS01q8t8oupAQ8L`,
then `sha256sum -c --ignore-missing SHA256SUMS`. With minisign installed, the script itself
refuses an agent archive whose signature doesn't verify. The archive is the desktop's
`jarvis-agent-<v>-linux-arm64.tar.gz`, onnxruntime embedded
([decision](../decisions/2026-10-18-pi-ort-embedded.md), superseding §8 Q3).

These are **still needed**; install.sh step lines are cited. `scripts/install-pi.sh` (§7 4.3) does
all of them except `jarvis-alsa-store` (not yet run on a Pi):
- The ReSpeaker overlay compiled from the vendored DTS (`:912-934`).
- config.txt edits (`dtparam=spi=on`, `i2c`, `audio=off`) (`:936-984`).
- The modprobe index (`:991-993`).
- `/etc/asound.conf` with `dsnoopmic` and `output` (`:1019-1064`).
- The amixer baseline + `alsactl store` (`:1132-1167`), and `/usr/local/sbin/jarvis-alsa-store`
  with its NOPASSWD sudoers line (`:1802-1852`): the Go node's runtime self-heal persists through
  it. The self-heal itself (`N:utils/audio_volume.py:271,459`, ADC HPF and output baseline) is
  ported: `internal/agent/hat/codec.go`, run at every start of the Pi profile with the HAT
  (2026-10-17; unit tests, not yet run on a Pi).
- Groups `spi`/`gpio`/`i2c`/`bluetooth`, and linger (`:1174-1178,1611-1625`).
- The Pulse config (`:365-536`), kept for v1 (§8 Q2).
- Wi-Fi powersave off and the NM profile reconciliation (`:1208-1226,2148-2191`).
- hostapd/dnsmasq installed and masked (`:206-252`).
- journald, swappiness, swapfile (`:107-154,889-901,2090-2114`).
- The net watchdog (`:2116-2146`).
- The sudoers file (`:1819-1862`).
- The package wrappers `jarvis-apt-install`, `jarvis-apt-source`, `jarvis-post-install` and their
  sudoers lines (`:1662-1800,1842-1844`): **done 2026-10-19** (§7 parity 5.6).

**Replaced by the Go binary or its packaging:**
- The systemd unit's ExecStart and environment (`:1865-1958`); the unit keeps `User=pi`,
  `XDG_RUNTIME_DIR` and `Restart=always`.
- Download, verify, extract and rollback (`:553-870`), via `internal/update`.
- Database migrations (alembic): the Go node keeps its own small state store.
- Wake-model staging: install-pi.sh puts the pinned openWakeWord models in
  `/usr/local/share/jarvis-agent/openwakeword` (the unit's `--wake-models`; root's, like the
  overlay source, since `/opt/jarvis-agent` is the node user's for self-update), from
  `--from-dir`/`--base-url` when it has them, else openWakeWord's release. The release (#162)
  doesn't ship them yet, so an install fetches them from openWakeWord's release.

**Python node only (or sidecar only):**
- python/venv, portaudio, libopenblas, sqlcipher dev headers.
- `rebuild_venv`, the openwakeword install, `register_commands`, `restore_pantry_pip_deps`.
- The sidecar keeps a **uv-managed** venv, per DESIGN.md. install.sh uses pip today.

---

## 7. Phased plan

1. **Python quick win (§5.3).** One PR to jarvis-node-setup: −84 MB on every node now. **Done** (§8 Q8).
2. **Move the agent into jarvis-server** as `cmd/jarvis-agent`, as already planned. **Done 2026-10-10.** It shares:
   - `internal/update` (signed self-update);
   - the embedded ORT libraries (`internal/voice/sherpa/libs`; add `internal/voice/ort` beside it,
     since the same `libonnxruntime.so` serves both);
   - `internal/audio` (WAV).
3. **Desktop voice, Linux and macOS** (§8 Q10). **Landed 2026-10-10, see §3.7.**
   - Pipeline: ALSA `default` capture (works through pipewire-alsa/pulse) → decimator → oww → RMS
     endpointing → the MVS turn (§1.1 rows 7–14) → ALSA playback.
   - This exercises the whole voice protocol on hardware we sit at, without touching the Pi.
   - macOS (AudioQueue via purego) and Windows (WASAPI over COM) follow.
4. **Pi node.** In order:
   1. HAT audio (`dsnoopmic`/`output`), LEDs and button. **Done 2026-10-12 (§3.11).**
      A Pi profile (`--profile pi`, auto on a Pi with the HAT or `/etc/jarvis-node`):
      heartbeat `install_mode "tarball"`, no `node_kind`, runtime `go` (§8 Q6); no desktop,
      screen, dictation or browser tools; headless (no tray, island or setup page); the settings
      snapshot's `node_config` reports the HAT and the voice/LED settings. **Done 2026-10-17**
      (unit tests; not yet run on a Pi).
      The HAT codec self-heal (ADC HPF, output mixer baseline; §6) at every start. **Done
      2026-10-17** (unit tests with a fake amixer; not yet run on a Pi).
   2. Heartbeat `pending_update` → updater. **Server side done 2026-10-17:** jarvisd stores
      `version_info.runtime` (`cc_nodes.runtime`, on the node response); a `runtime "go"` node
      follows this server like a desktop agent (heartbeat `server_version`, "latest" = jarvisd's
      own tag, never the jarvis-node-setup release). Python nodes are unchanged. **Node side built
      2026-10-18** (loop #161): the desktop agent's self-update (stage, swap, health gate,
      rollback, failures on `/nodes/tasks/{id}/status`) runs on the Pi install. `install-pi.sh`
      gives `/opt/jarvis-agent` to the node's user (root never runs or writes a file there; the
      overlay source moved to `/usr/local/share/jarvis-agent`); the restart is sudoers'
      `sudo -n /usr/bin/systemctl restart jarvis-agent.service` with no quit first, and the
      installer runs in a `systemd-run --user` unit of the node user's manager (linger), never
      detached (the unit's restart stops its whole cgroup). CI: `agent-upgrade-e2e -pi` (the Pi
      layout under a fake systemd). On-device (lab Pi 4, 2026-10-18): `systemd-run --user` does
      reach pi's manager from the system unit; a crash-looping release hit the unit's start limit
      and the rollback's restart was refused (loop #220). Since then every restart runs sudoers'
      `systemctl reset-failed jarvis-agent.service` first, and the -pi lane's fake systemd models
      the start limit. **Pending on-device:** the rollback repro again after rerunning
      `install-pi.sh` (for the new sudoers line).
   3. The tarball + install.sh variant (§6).
      **3a built 2026-10-17:** `scripts/install-pi.sh` (agent in `/opt/jarvis-agent`; overlay, config.txt,
      modprobe, asound.conf, mixer baseline, groups + linger, sudoers, journald, swappiness, swap, the
      `/etc/jarvis-node` marker, `jarvis-agent.service`); golden-tested against a scratch root
      (`scripts/install-pi-check.sh`), **pending on-device** (lab Pi 4).
      **3b built 2026-10-18 (#160):** apt packages (pulseaudio, iw, hostapd, dnsmasq, dtc,
      alsa-utils), PipeWire masked for PulseAudio with the HAT as the fixed `jarvis_output` sink
      (udev PULSE_IGNORE, no suspend, no stream restore), hostapd/dnsmasq masked, Wi-Fi
      power-save off, NM same-SSID profile reconciliation, the net watchdog
      (`scripts/packaging/pi/jarvis-net-watchdog`, N:'s byte for byte) and its 60 s timer, and the
      wake models staged at install in `/usr/local/share/jarvis-agent/openwakeword` against the
      hashes in `voice.WakeModels` (a unit test keeps them equal); same golden check, **pending
      on-device** (lab Pi 4).
      **Release built 2026-10-18 (#162):** each release ships `install-pi.sh` stamped with its tag
      (the copy at a release's URL installs that release) and, unlike the other one-line
      installers, listed in the signed `SHA256SUMS`; the agent is the existing linux-arm64 archive
      with onnxruntime embedded ([decision](../decisions/2026-10-18-pi-ort-embedded.md)). The
      release notes carry the one-liner (§6). Job `install-pi` (`ubuntu-24.04-arm`) runs the stamped
      copy under `--root` against the run's own files: pinned to the tag, installs that agent,
      checksum only and with minisign (a dry run's throwaway signature refused); `verify` checks
      it against the signed list. **Pending:** the first real release, and a run on the lab Pi.
   4. The provisioning AP + HTTP API port. **HTTP API done 2026-10-17** (loop #169):
      `internal/agent/provisioning` (routes, state machine, registration retries, rollback and
      hotspot restore, golden-tested against `N:provisioning/models.py`) behind a `WiFi`
      interface, served by `jarvis-agent provision` on :8080 (`--captive-listen :80` optional).
      Verified with a simulated radio against a sandbox jarvisd; differences from Python in
      [decisions/2026-10-17-pi-provisioning-api.md](../decisions/2026-10-17-pi-provisioning-api.md).
      **NetworkManager backend and recovery cycle built 2026-10-18** (loop #170, 8b):
      `provisioning.NetworkManager` over the system D-Bus (scan with a pre-hotspot cache, join
      through a `jarvis-<ssid>` profile with commit at priority 999 or rollback, the open
      `jarvis-setup-ap` hotspot on 192.168.4.1 in NM's shared mode) and `provisioning.Recovery`
      (N:'s AP↔STA cycle and its `JARVIS_RECOVERY_*` knobs), behind `jarvis-agent provision
      --wifi networkmanager`, which scans, starts the hotspot and, for a node provisioned before,
      runs the cycle. `install-pi.sh` adds a polkit rule for the node's user, the hotspot's
      captive DNS and a net-watchdog guard for the hotspot. Unit-tested over a fake bus; **pending
      on-device** (lab Pi 4, provisioned from the app). hostapd stays unbuilt unless NM's AP
      fails on the chip. Differences from N: in
      [decisions/2026-10-18-pi-wifi-networkmanager-dbus.md](../decisions/2026-10-18-pi-wifi-networkmanager-dbus.md).
      **Setup mode at boot (#292):** `run --profile pi` with no `node.json` (a factory reset,
      an install not enrolled by code) runs the same scan, hotspot and API on :8080 (captive
      checks on :80; the unit grants `CAP_NET_BIND_SERVICE`), the HAT on the `setup` pattern, and
      becomes the node in the same process once provisioned. Unit-tested; pending on-device.
      [decision](../decisions/2026-10-19-pi-setup-mode-at-boot.md).
      A Pi whose wlan0 is already on a network keeps it and serves the API on its LAN address
      (#423), advertising it over mDNS as `_jarvis-setup._tcp` (instance = node id, TXT
      `node_id`, `hardware`) until it is provisioned (#508), so the app finds it without the
      hotspot. [decision](../decisions/2026-10-22-pi-setup-mdns.md).
   5. The sidecar with the built-in commands once the SDK backends (§5.2) land. **Built
      2026-10-18** (loop #168, [decision](../decisions/2026-10-18-pi-builtin-commands.md)): the Pi
      profile pip-installs jarvis-node-setup (pinned) into the packages venv at boot and the
      sidecar loads its commands next to the Pantry packages, less control_device, routine and
      send_link (they still call the Python node's code). Their SDK backends ask
      the Go agent over the child→Go RPC (`internal/agent/sdkhost`): LLM and CC HTTP
      (`/api/v0/node/...` only) as the node, Inbox, Signals, the timers
      (`internal/agent/timers`, persisted in the state dir, said on expiry between turns), the
      alert queue (in memory), the volume (pactl on the default sink or
      `jarvis_output`, else amixer; persisted) and speech; CommandInvoker runs in the sidecar.
      Tests: the real commands (calculate, timers, control_node, check_alerts, shopping_list,
      tell_joke) against the real Go backends with a real Python, each backend's RPC, and the
      timers' restart and expiry with a fake clock. **Pending on-device** (lab Pi 4): the first
      boot's install, a voice turn per family, a timer heard on expiry, the volume heard to
      change. `update_node_config`'s `volume_percent`/`is_muted` and the settings snapshot's
      volume use the same `sdkhost.Audio` (§3.14, #295).
      **Reminders, alerts and the button built 2026-10-19** (loop #175,
      [decision](../decisions/2026-10-19-pi-reminders-alerts.md)): `reminder` loads (the sidecar
      stands in for `jarvis_log_client` and restores the reminders when it loads them); the
      reminder agent's run is the sidecar's `reminders_due`, polled by `internal/agent/reminders`
      (60 s while one is pending, 300 s otherwise, as N:reminder_agent.py), each due reminder a
      priority-3 alert for 10 minutes; `sdkhost.Announcer` says the alerts at priority 3 and up
      between turns (N:alert_announcer.py) and removes what it said; the `alert` LED pattern and
      the button's short press (§3.11). Not ported: Python's 8 s inline listen after an
      announcement (a reply to it needs the wake word). Tests: the queue's watcher, the
      announcer and the poller with an injected clock, the button through `hat.Button`, and the
      pinned reminder command set, restored and fired once (with its push) through a real
      sidecar. **Pending on-device** (lab Pi 4, §3.11).

   Ship it to the lab Pi alongside the Python node (`install_mode` decides).
5. **Parity**, in order:
   1. Follow-up. **Desktop agent done 2026-10-10 (§3.9)**, with validation questions.
   2. Barge-in (re-use the wake detector at threshold 0.04 plus the RMS gate). **Done
      2026-10-17 (§3.13)** in the shared pipeline (desktop and Pi); lockstep rig tests only,
      not yet heard on a Pi or a desktop.
   3. Client tools / continue. **Desktop agent done 2026-10-10 (§3.8)**; the Pi gets it with the
      sidecar: **built 2026-10-18** (§7 4.5), not yet run on a Pi.
   4. `update_node_config`, `measure_ambient_noise`, LED preview. **Done 2026-10-17 (§3.14)**
      (unit tests and a sandbox jarvisd end to end; not yet on a Pi).
   5. K2 + settings snapshot (AES-GCM).
   6. Packages. **Built 2026-10-19** (loop #174,
      [decision](../decisions/2026-10-19-pi-node-package-system-setup.md)): the Pi profile runs a
      package's `apt_sources`, `apt_packages` and `post_install` through the Python node's root
      wrappers (`sudo -n /usr/local/sbin/jarvis-apt-source|jarvis-apt-install|jarvis-post-install`,
      `internal/agent/packages/system.go`), removes its drop-ins on uninstall and failed installs,
      reruns the previous version's ops on revert, heals orphaned drop-ins at boot, and answers
      `test-install` (Forge drafts served 20 minutes). `install-pi.sh` ships the wrappers (byte for
      byte, checked by `install-pi-check.sh`) and their sudoers lines. Tests: exec fakes for every
      wrapper call (argv, stdin JSON, order, rollback), a fake jarvisd + Pantry for test-install,
      the golden install, and N:'s 68 wrapper tests against the vendored copies. **Pending
      on-device** (lab Pi 4): Music Assistant installed from the app (shairport-sync, its drop-in,
      a "play …" turn), uninstalled, and a Forge test install; the commands are in the PR.
   7. factory-reset. **Built 2026-10-19** (loop #173, with routines sync and context requests;
      [decision](../decisions/2026-10-19-pi-node-factory-reset.md)): `internal/agent/reset`
      handles jarvisd's tracked task (`in_progress` with `X-Reset-Token` first, which is also the
      anti-spoof check, then the wipe, `success` or `failed`, then `sudo -n systemctl reboot`) and
      the legacy verify-reset; the settings store and the timers are sealed before the wipe, so
      nothing writes a file back before the reboot; `reset.Wipe` (K2, the node DB, timers, Bluetooth devices, Pantry
      packages, node.json last) is also the provisioning API's `POST /api/v1/factory-reset`.
      `nodesettings.Routines` pulls `GET /nodes/{id}/routines` into `routine/<slug>` command data
      on `routines/sync` and every connect (defaults and Pantry routines never pruned; nothing
      changes when jarvisd can't be reached): a cache until the routine command runs (4.5 skips
      it). `nodesettings.Context` answers `context/operations` and `context/query` from the
      sidecar's declared context operations (describe's `context_operations`, a
      `context_query` round trip as the requesting user, 128 KiB cap, every failure answered).
      Unit tests (an httptest jarvisd, a temp state dir) and a real sidecar for context.
      After the reboot the node has no credentials and `run` starts setup mode (4.4, #292).
      **Open:** jarvisd's broker drops `context/operations` answers (ACL, held for the user).
      **Pending on-device** (lab Pi 4): a reset from the app, watched to `success`.
   8. Bluetooth. **Done 2026-10-18 (§3.15)** (unit tests over a fake bus, the D-Bus client
      against a fake `org.bluez`, jarvisd's routes end to end; not yet on a Pi).
   9. Device control. **14a, the device registry: built 2026-10-19 (§3.17)**: scan, list and
      state over the sidecar's Pantry device families, `invalidate_device_cache`,
      `device_removed`, OAuth's auth-ready and the heartbeat's `protocols` (unit tests and a
      sandbox jarvisd end to end; not yet on a Pi). **14b, control: built 2026-10-19 (§3.18,
      loop #177)**: the `action`, `callback` and `toggle_command` verbs; the app's device control
      reaches the family's `control()` (pairing included) through the sidecar (unit tests, a real
      sidecar, and a sandbox jarvisd end to end; not yet on a Pi). Voice control (the
      `control_device` tool, which uses the registry) is still to do.
   10. Voice-profile takes (`enroll_voice` / `verify_voice`, the app's "record on this node").
       **Done 2026-10-17** for the Pi profile: `voice.Mic` hands a fixed-length take to the
       running pipeline, which records it between turns with the wake word paused (cue, 1.5 s
       preroll, 48 kHz WAV like a voice turn's upload); `voice.VoiceProfiles` uploads it to
       M4/M5 and posts V9 (Python's success and `audio_bus_unavailable` / `record_failed` /
       `upload_failed` / `verify_failed` shapes). Unit tests; not yet run on a Pi.
   11. Music ducking and takeover (loop #180 17a). **Built 2026-10-19 (§3.16)**: duck on wake
       and each follow-up, restore before the deferred play, the mic gain profile, takeover on a
       music command's play. Unit tests; not yet on a Pi. 17c (tentative wake and
       `pre_wake_speech_seconds`, §1.1 rows 21–22) built 2026-10-21 (loop #425, §3.16), not yet
       on a Pi. 17b (echo cancellation during music) built 2026-10-22 (loop #424, §3.16), not
       yet on a Pi. Still to build from #180, in order
       ([decision](../decisions/2026-10-19-pi-node-music-ducking.md)): 17e the wake greeting
       prefetch (row 20), maintenance restart and the ACT-LED fallback without a HAT. 17c
       (tentative wake and `pre_wake_speech_seconds`) and 17d (wake auto-calibration and the
       not_for_me cool-down) **built (§3.16, §3.20)**; unit tests, not yet on a Pi.

   Then retire the Python node. **Proposed 2026-10-19, BLOCKED** (loop #181, §3.19): the soak
   script (`scripts/pi-soak.sh`) and its commands, the parity checklist and the retirement path
   are written; the 24 h soak on the lab Zero 2 W is the user's to run, and the retirement waits
   on the open gaps: (a) #180's 17b–17e, (b) voice `control_device`/`routine`/`send_link` in the
   sidecar, (c) K2 and the settings snapshot on the Pi, (d) setup mode at boot, (e) the on-device
   rollback repro. It also needs the user's choice on the node identity (§3.19 step 2).

---

## 8. Decisions on the open questions (user, 2026-10-10)

| # | Question | Decision |
|---|---|---|
| 1 | STT upload rate | **48 kHz first** for parity; A/B speaker ID at 16 kHz, switch if it holds up. |
| 2 | PulseAudio on the Pi | **Keep for v1** (Bluetooth, echo cancellation, ducking). ALSA-only mode later for HAT-only nodes. |
| 3 | ORT packaging | ~~Separate file in the Pi tarball; embedded in the desktop binary.~~ **Superseded 2026-10-18 (#162): embedded on the Pi too**, the same linux-arm64 archive as the desktop ([decision](../decisions/2026-10-18-pi-ort-embedded.md)). |
| 4 | Sidecar start | **Eagerly at boot.** Revisit only if a Zero 2 W gets tight. |
| 5 | Wake engine | **openWakeWord `hey_jarvis`** (bit-exact, thresholds carry over). |
| 6 | Heartbeat identity | **`install_mode: "tarball"` + `version_info.runtime: "go"`.** |
| 7 | Provisioning AP | **NetworkManager over D-Bus** (not the recommended hostapd exec). Both lab Pis run Debian 13 trixie with NetworkManager active, so this is the native path; it also serves desktop Linux. hostapd exec stays the fallback if NM's AP mode fails on a given Wi-Fi chip. |
| 8 | Python quick win | **Done:** jarvis-node-setup #144 (−77 MB RSS), shipped in node v0.3.5. |
| 9 | Runtime package vs SDK backends | **SDK backends (§5.2).** |
| 10 | Desktop voice order | **Linux and macOS together** (not Linux-first). ALSA via purego and AudioQueue via purego land in the same phase; Windows (WASAPI) follows. |
| 11 | Which node runs on a Pi with both (2026-10-17, #159) | **`--activate`/`--deactivate` plus `Conflicts=jarvis-node.service`** in `jarvis-agent.service`: install-pi.sh installs the unit disabled; `--activate` disables and stops the Python node and enables the Go one, `--deactivate` the reverse; Conflicts= keeps the two off the HAT at once ([decision](../decisions/2026-10-17-pi-node-activation.md)). |

Phase 3 of §7 therefore covers both Linux and macOS desktop voice, and phase 4.4 ports
provisioning to NetworkManager D-Bus rather than hostapd.

---

## 9. Risks

- **(Resolved 2026-10-10, §3.6: measured on a real Zero 2 W.)** Previously: the Zero 2 W was not measured directly. CPU comes from a 600 MHz A72 proxy (Go wake ~49% of
  one core) and memory is from the Pi 4. Thermal behaviour and the tiny dsnoop buffer (85 ms)
  under load are untested. Run `wakelive` on a real Zero 2 W before committing to the Pi phase.
- **purego means dynamic linking against glibc.** The binary needs a compatible glibc for ORT, and
  musl is unsupported. The pointer-lifetime rules in §3.5 need tests, as sherpa has
  (`TestStructLayout`).
- **ORT's size.** The library is 34.6 MB; about 12 MB of RSS is shared file pages. A minimal build
  is the escape hatch.
- **Protocol coupling to frozen-node behaviour.** jarvisd keeps Python-node status codes (400 vs
  422, 202 fallback shapes, the 8 s idle stream timeout assumptions). The Go node must match them,
  or the server's compat branches must be retired deliberately (`S:voice_routes.go:24-27`).
- **The long tail.** About 25 MQTT verbs, K2 crypto, Bluetooth, music ducking and AEC. MVS is small;
  parity is not.
- **Sidecar coupling.** The commands depend on node internals until the SDK backends land. Until
  then a Go Pi node runs about 8 of the 21 built-ins.
- **GC pauses vs small audio buffers.** No late chunks were seen (worst 24 ms per chunk at 1.8 GHz,
  0 xruns over minutes), but there is no long soak yet.

---

## Appendix: spike inventory (`spikes/pinode/`)

| Path | What |
|---|---|
| `ort/` | onnxruntime C API over purego (env, session, float32/int64 tensors, Run) |
| `oww/` | openWakeWord 0.6.0 streaming pipeline (mel → embedding → wake), with per-stage timing |
| `alsa/` | libasound over purego (open by name, set_params, readi/writei with xrun recovery) |
| `dsp/` | streaming Kaiser FIR decimator (resample_poly's filter) + test |
| `meter/` | CPU (getrusage), RSS/USS (smaps_rollup), WAV I/O |
| `cmd/owwbench` | per-frame scores (`scores`) and CPU/RSS soak (`soak`) on WAV files |
| `cmd/wakelive` | the always-on loop: ALSA → decimate → oww → fires |
| `cmd/audiospike` | capture/play via purego ALSA or arecord/aplay, with CPU/RSS |
| `cmd/vadbench` | silero VAD via the same ORT path |
| `cmd/hat` | APA102 LEDs (spidev) and the GPIO17 button (gpiocdev), static binary |
| `py/` | `prep.py` (test set), `owwref.py` (Python reference), `pywakelive.py` (the node's wake loop, live) |
| `tools/ortidx.py` | computes OrtApi slot indices from `onnxruntime_c_api.h` |
| `results/` | `py_scores.json`, `go_scores.json`, `q_scores.json` (int8), `live.json` |

**To run on a Pi:**
```
GOTOOLCHAIN=local CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o owwbench ./cmd/owwbench
owwbench -ort libonnxruntime.so -models <oww models dir> scores a.wav …
```
`libonnxruntime.so` is jarvisd's `internal/voice/sherpa/libs/linux-arm64` copy.
