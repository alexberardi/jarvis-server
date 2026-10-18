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
| 6 | Wake ack: cached greeting WAV, else a bundled `sounds/wake/*.wav`, else TTS "Yes?"; wake paused while it plays | `N:core/wake_response.py:149-248` | – | MVS (a bundled WAV is enough) |
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
| 20 | Processing ack ("One moment."), 3 s delayed ack `/voice/acknowledge`, next-greeting prefetch `/wake-response` | `N:core/wake_response.py:73-80,294-380`, `N:utils/command_execution_service.py:610-620` | `S:voice_routes.go:489-541` | later |
| 21 | Pre-wake speech seconds hint (RMS over a 5 s ring) | `N:core/wake_loop.py:113-116,361-367,523-529` | `S:hints/` | later (cheap) |
| 22 | Tentative wake (duck music in [0.2, 0.4)), not_for_me cool-down, auto threshold | `N:core/wake_loop.py:224-456`, `N:core/wake_calibration.py` | – | later |
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
`.../context/query/response/{cid}` (`internal/platform/mqtt/auth.go:48-75`).

**Verify before acting.** jarvisd never sends `trusted`, so the node verifies `action`,
`measure_ambient_noise` and `tool_call` with `POST /commands/{rid}/verify` (`S:bus.go:96-150`).

| Verb / topic | Python | jarvisd publisher | Reply sink | Tag |
|---|---|---|---|---|
| `report_tools` | `:454-551` | `S:nodetools.go:100` | `POST /mobile/node-tool-reports/{rid}` | done in Go core |
| `tool_call` | `:830-923` | `S:mobile_chat.go:598`, `S:errands.go:208`, `S:signals_reactions.go:348` | `POST /device-control-results/{rid} {"output":…}` | done in Go core |
| `action` (mobile action / device control) | `:192-298` | `S:commands.go:44`, `S:smarthome_control.go:116` | `/device-control-results/{rid}` | later |
| `callback` | `:301-422` | `S:callbacks.go:460` | `GET /callbacks/{job}`, `POST …/result` | later |
| `routine` (carries the full definition) | `:926-982` | `S:routines_run.go:104-110` | `/device-control-results/{rid}` | later |
| `enroll_voice` / `verify_voice` | `:554-803` | `S:media.go:495` | `/media/whisper/voice-profiles/*`, `/mobile/voice-profile-results/{rid}` | later |
| `update_node_config` (settings push; restart only for `wake_word_model`) | `:1092-1177` | `S:commands.go:91` | – | later |
| `preview_led_pattern` | `:1180-1221` | `S:commands.go:112` | – | later |
| `measure_ambient_noise` | `:1257-1383` | `S:ambient.go:64` | `POST /nodes/{id}/ambient-noise-measurements/{rid}/result` | later |
| `toggle_command`, `invalidate_device_cache`, `device_removed` | `:1005-1254` | `S:smarthome*.go` | – | later |
| `/k2/provision` (K2 AES key) | `:1622-1669` | `S:settingsreq.go:316` | `GET …/k2/provision/{rid}`, `POST …/k2/ack/{rid}` | built for the desktop agent (`internal/agent/nodesettings`, [DESIGN §16](DESIGN.md)); the Go Pi node reuses it |
| `/settings/request` (AES-256-GCM snapshot with K2, AAD `{node}:settings:snapshot`), plus a backstop poll on connect | `:1837-1876`, `N:services/settings_snapshot_service.py:749-857` | `S:settingsreq.go:92` | `PUT …/settings/requests/{rid}/snapshot` | built for the desktop agent (`internal/agent/nodesettings`), including the app's secret Sync as source (`include_values` + user) and target (`settings:secrets` push), pinned by `sync_golden.json`; the Pi adds its node_config and hardware |
| `/config/push`, `/routines/sync` | `:1769-1812` | `S:settingsreq.go:424`, `S:routines.go:867` | GET pending / ack | config push built for the desktop agent (`internal/agent/nodesettings`: secrets, command_registry); routines later |
| `/package-install`, `-uninstall`, `-revert`, `/test-install` | `:1940-2020,2207-2224` | `S:packages.go:96-451`, `S:testinstall.go` | verify + results | later (sidecar) |
| `/factory-reset` (tracked with `task_id` + `X-Reset-Token`, or legacy) | `:2023-2204` | `S:reset.go:84,189` | `POST /nodes/factory-reset/{task}/status` | later |
| `/device-list`, `/device-scan`, `/device-state` | `:1879-1917,2357-2374` | `S:smarthome*.go` | results POSTs | later |
| `/bluetooth-*` (scan, pair, disconnect, discoverable, release, auto-connect) | `:2227-2354` | `S:smarthome.go:102-111` | results POSTs | later |
| `jarvis/auth/{provider}/ready` | `:1505-1596` | `S:smarthome_oauth.go:602` | `GET /oauth/provider/{p}/credentials` | later |
| `/command-data/{op}`, `/context/{op}` (the node **publishes** responses) | `:2484-2501` | `S:commanddata.go:119` | MQTT publish | command-data built for the desktop agent (`nodesettings.CommandData` over the sidecar's packages, pinned by the Python node's own handler, [DESIGN §16](DESIGN.md)); context later |
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
- `N:core/echo_cancel.py:22-24` claims capture goes through Pulse; it goes through dsnoop.
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
| 11 | Join the warmup ≤ 10 s, then `/voice/command/stream` `{voice_command, conversation_id, turn_source:"wake", wake_confidence, affect}` | yes (no `pre_wake_speech_seconds` yet) |
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
- **Not ported:** `act_on_items`, `post_process_tool_call`, music takeover. (`wait_for_input`,
  `clear_history` and `on_response_complete` landed with follow-up, §3.9.)

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
wake cool-down, an `end_of_exchange` header on the stream route (server).

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

**Node defaults now** (Python's in brackets): window 8 s, then 6, then 5 (4, 3); an answer ends
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
  every release. A short press logs only: the Go node has no alert queue yet (Python reads
  queued alerts). The MQTT `button/*` publishes are not sent (the ACL denies them, §1.4).
- Not ported: the ACT-LED fallback for a Pi without the HAT (`led_service.py`), alert-queue
  wiring of the `alert` pattern, `preview_led_pattern` over MQTT (parity item 5.4).

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
| reminder | `services.reminder_service` (itself only JarvisStorage + logger); firing is in `agents/reminder_agent.py` | needs Storage; the **host** owns the due-poll and announcement |
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

These are **still needed**; install.sh step lines are cited:
- The ReSpeaker overlay compiled from the vendored DTS (`:912-934`).
- config.txt edits (`dtparam=spi=on`, `i2c`, `audio=off`) (`:936-984`).
- The modprobe index (`:991-993`).
- `/etc/asound.conf` with `dsnoopmic` and `output` (`:1019-1064`).
- The amixer baseline + `alsactl store` (`:1132-1167`). The runtime self-heal in
  `N:utils/audio_volume.py:271,459` (ADC HPF, output baseline) must be ported to Go.
- Groups `spi`/`gpio`/`i2c`/`bluetooth`, and linger (`:1174-1178,1611-1625`).
- The Pulse config (`:365-536`), if D9 keeps Pulse.
- Wi-Fi powersave off and the NM profile reconciliation (`:1208-1226,2148-2191`).
- hostapd/dnsmasq installed and masked (`:206-252`).
- journald, swappiness, swapfile (`:107-154,889-901,2090-2114`).
- The net watchdog (`:2116-2146`).
- The sudoers file (`:1819-1862`).

**Replaced by the Go binary or its packaging:**
- The systemd unit's ExecStart and environment (`:1865-1958`); the unit keeps `User=pi`,
  `XDG_RUNTIME_DIR` and `Restart=always`.
- Download, verify, extract and rollback (`:553-870`), via `internal/update`.
- Database migrations (alembic): the Go node keeps its own small state store.
- Wake-model staging (ship the models in the tarball).

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
   2. Heartbeat `pending_update` → updater.
   3. The tarball + install.sh variant (§6).
   4. The provisioning AP + HTTP API port.
   5. The sidecar with the built-in commands once the SDK backends (§5.2) land.

   Ship it to the lab Pi alongside the Python node (`install_mode` decides).
5. **Parity**, in order:
   1. Follow-up. **Desktop agent done 2026-10-10 (§3.9)**, with validation questions.
   2. Barge-in (re-use the wake detector at threshold 0.04 plus the RMS gate). **Done
      2026-10-17 (§3.13)** in the shared pipeline (desktop and Pi); lockstep rig tests only,
      not yet heard on a Pi or a desktop.
   3. Client tools / continue. **Desktop agent done 2026-10-10 (§3.8)**; the Pi gets it with the sidecar.
   4. `update_node_config`, `measure_ambient_noise`, LED preview.
   5. K2 + settings snapshot (AES-GCM).
   6. Packages.
   7. factory-reset.
   8. Bluetooth.
   9. Device control.

   Then retire the Python node.

---

## 8. Decisions on the open questions (user, 2026-10-10)

| # | Question | Decision |
|---|---|---|
| 1 | STT upload rate | **48 kHz first** for parity; A/B speaker ID at 16 kHz, switch if it holds up. |
| 2 | PulseAudio on the Pi | **Keep for v1** (Bluetooth, echo cancellation, ducking). ALSA-only mode later for HAT-only nodes. |
| 3 | ORT packaging | **Separate file in the Pi tarball; embedded in the desktop binary.** |
| 4 | Sidecar start | **Eagerly at boot.** Revisit only if a Zero 2 W gets tight. |
| 5 | Wake engine | **openWakeWord `hey_jarvis`** (bit-exact, thresholds carry over). |
| 6 | Heartbeat identity | **`install_mode: "tarball"` + `version_info.runtime: "go"`.** |
| 7 | Provisioning AP | **NetworkManager over D-Bus** (not the recommended hostapd exec). Both lab Pis run Debian 13 trixie with NetworkManager active, so this is the native path; it also serves desktop Linux. hostapd exec stays the fallback if NM's AP mode fails on a given Wi-Fi chip. |
| 8 | Python quick win | **Done:** jarvis-node-setup #144 (−77 MB RSS), shipped in node v0.3.5. |
| 9 | Runtime package vs SDK backends | **SDK backends (§5.2).** |
| 10 | Desktop voice order | **Linux and macOS together** (not Linux-first). ALSA via purego and AudioQueue via purego land in the same phase; Windows (WASAPI) follows. |

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
