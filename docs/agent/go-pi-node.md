# Go Pi voice node: research and spikes

Status: research, 2026-10-09 (overnight). Copied here from `jarvis-host-agent/docs/node/` on
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
| 16 | Follow-up window: 4 s, ≤5 iterations, onset detection, blocking `POST /voice/command` with `turn_source:"follow_up"` | `N:core/follow_up_loop.py:97-428`, `N:scripts/speech_to_text.py:455-691` | `S:voice_routes.go:171-220` | later |
| 17 | Barge-in: local only. oww score > 0.04 plus an RMS gate, 2 consecutive chunks → cancel playback and close the stream. Nothing is sent to the server. | `N:core/barge_in.py:100-359` | – | later |
| 18 | Client tools: `tool_calls` → run → `POST /voice/command/continue[/stream]` | `N:utils/command_execution_service.py:977-1089` | `S:voice_routes.go:260-339` | later (with the sidecar) |
| 19 | Validation: speak the question, re-listen, `POST /voice/command/continue` | `N:core/wake_transcription.py:204-231` | same | later |
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
| `/k2/provision` (K2 AES key) | `:1622-1669` | `S:settingsreq.go:316` | `GET …/k2/provision/{rid}`, `POST …/k2/ack/{rid}` | later |
| `/settings/request` (AES-256-GCM snapshot with K2, AAD `{node}:settings:snapshot`), plus a backstop poll on connect | `:1837-1876`, `N:services/settings_snapshot_service.py:749-857` | `S:settingsreq.go:92` | `PUT …/settings/requests/{rid}/snapshot` | later |
| `/config/push`, `/routines/sync` | `:1769-1812` | `S:settingsreq.go:424`, `S:routines.go:867` | GET pending / ack | later |
| `/package-install`, `-uninstall`, `-revert`, `/test-install` | `:1940-2020,2207-2224` | `S:packages.go:96-451`, `S:testinstall.go` | verify + results | later (sidecar) |
| `/factory-reset` (tracked with `task_id` + `X-Reset-Token`, or legacy) | `:2023-2204` | `S:reset.go:84,189` | `POST /nodes/factory-reset/{task}/status` | later |
| `/device-list`, `/device-scan`, `/device-state` | `:1879-1917,2357-2374` | `S:smarthome*.go` | results POSTs | later |
| `/bluetooth-*` (scan, pair, disconnect, discoverable, release, auto-connect) | `:2227-2354` | `S:smarthome.go:102-111` | results POSTs | later |
| `jarvis/auth/{provider}/ready` | `:1505-1596` | `S:smarthome_oauth.go:602` | `GET /oauth/provider/{p}/credentials` | later |
| `/command-data/{op}`, `/context/{op}` (the node **publishes** responses) | `:2484-2501` | `S:commanddata.go:119` | MQTT publish | later |
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

### 3.6 Measured on a real Pi Zero 2 W (jarvis-dev, 2026-10-10)

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
- Before this run jarvis-dev had a leaked audacy mpd drop-in crash-looping mpd (5,169 restarts, ~4 s CPU each); fixed by jarvis-node-setup #143 (self-heal on boot).
- Memory quick win (jarvis-node-setup #144, merged): −77 MB on the Pi 4, wake scores bit-identical.

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

1. **Python quick win (§5.3).** One PR to jarvis-node-setup: −84 MB on every node now.
2. **Move the agent into jarvis-server** as `cmd/jarvis-agent`, as already planned, so it can share:
   - `internal/update` (signed self-update);
   - the embedded ORT libraries (`internal/voice/sherpa/libs`; add `internal/voice/ort` beside it,
     since the same `libonnxruntime.so` serves both);
   - `internal/audio` (WAV).
3. **Desktop voice, Linux first.**
   - Pipeline: ALSA `default` capture (works through pipewire-alsa/pulse) → decimator → oww → RMS
     endpointing → the MVS turn (§1.1 rows 7–14) → ALSA playback.
   - This exercises the whole voice protocol on hardware we sit at, without touching the Pi.
   - macOS (AudioQueue via purego) and Windows (WASAPI over COM) follow.
4. **Pi node.** In order:
   1. HAT audio (`dsnoopmic`/`output`), LEDs and button.
   2. Heartbeat `pending_update` → updater.
   3. The tarball + install.sh variant (§6).
   4. The provisioning AP + HTTP API port.
   5. The sidecar with the built-in commands once the SDK backends (§5.2) land.

   Ship it to the lab Pi alongside the Python node (`install_mode` decides).
5. **Parity**, in order:
   1. Follow-up.
   2. Barge-in (re-use the wake detector at threshold 0.04 plus the RMS gate).
   3. Client tools / continue.
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
