# 06 — Media and voice identity

Scope: the TTS/STT media proxy (`/api/v0/media/*`), mobile audio (`/api/v0/mobile/stt`, `/tts`), voice-profile enrollment and verification (phone-mic and node-mic), and how a recognised speaker becomes the user that a voice turn is attributed to (membership check, stickiness, name resolution, the `identify_speaker` tool).

Paths below are relative to `jarvis-command-center/app/` unless prefixed. `WA:` = `jarvis-whisper-api/`, `TTS:` = `jarvis-tts/`, `NODE:` = `jarvis-node-setup/`, `MOB:` = `jarvis-node-mobile/`.

Voice is **greenfield** in Go (PLAN §2, D7, Appendix C). Kokoro TTS and speaker embeddings (ERes2Net or TitaNet) run in-binary via sherpa-onnx, and users re-enroll (PLAN §5). So this doc treats the Python whisper/tts internals as a **behaviour reference**. The things that must survive unchanged are the **wire contracts** (§2.1) and the **identity rules** (§3.4–3.6).

---

> **Decision D2 (2026-10-06, user):** the node "last speaker" mechanism (§3.5 step 1–2, §8 item 1) is **dropped entirely, not fixed**. Go ignores `node_context.speaker_user_id`/`speaker_confidence` on `/conversation/start`. See `QUESTIONS.md`.
>
> **Decision D3 (2026-10-06, user):** CC's per-node stickiness (§3.6) is **also dropped**. Speaker identity is per conversation only: set from turns identified in the current conversation, and cleared when the conversation ends or expires. Nothing persists per node or across conversations. Reason: sensitive-info permission gates are keyed on speaker ID, so cross-conversation memory leaks.

## 0. Decisions applied (2026-10-06)

Source: `QUESTIONS.md`. Sections below still describe today's Python behaviour; inline "Changed by" notes and §11 say what Go does instead.

- **D2:** the node "last speaker" is dropped. Go accepts and **ignores** `node_context.speaker_user_id` / `speaker_confidence` on `/conversation/start`. No node change.
- **D3:** CC's 30 s per-node stickiness is dropped (`speaker_stickiness.py`, `voice.stickiness_*`). Speaker identity is **per conversation only**: from turns identified in this conversation, gone when it ends or expires.
- **D4:** V9 (`POST /mobile/voice-profile-results/{rid}`) requires node auth, and the rid must belong to that node. The rest of 06.Q5 (V10 poller check, M4/M5 bound to a pending request and the node's household) follows the same policy.
- **D8:** known bugs fixed as intended differences (e.g. profile-store not-found → 404, empty TTS text → 400 JSON).
- **D9:** phone-mic routes V2–V5 are cut.
- **D19 (owned by 01/04):** a voice turn writes a transcript only when the speaker is confidently identified.
- **D20:** account deletion hard-deletes voiceprints in every household and clears in-memory speaker/name caches, in the same in-process transaction as the rest of the user's data.
- **D21:** an unknown **or ambiguous** speaker means per-user things refuse ("I'm not sure who's speaking"); household-level things still work. No fallback to the node owner, no per-household knob. The server passes the speaker identity (or its absence) to every command, and each command decides what to refuse.
- **D33:** one `voice.similarity_threshold` plus `voice.min_speaker_margin`; verify uses the same threshold. Short/long knobs and the fixed 0.45 verify threshold go. Recalibrated for ERes2Net on jarvis-dev enrollments.
- **D34:** voiceprints only. No raw enrollment audio kept; WAVs are discarded once embedded. Each embedding is tagged with its model id; a model change (including ECAPA → ERes2Net at cutover) requires re-enrollment. Legacy voiceprints are not imported.
- **D35:** speaker recognition stays **off by default**; enrolling does not turn it on. **M14:** when it is off, refusals and the enrollment screen say "speaker recognition is off", not "I'm not sure who's speaking".
- **D36:** voiceprints stay per (household, user), no node id. Per-node profiles are future work (PLAN §9).
- **D37:** enrollment quality gate: reject a take with under ~3 s of VAD speech, or one scoring far below the user's other takes, with `success:false, error:"low_quality"`. No mobile change.
- **D38:** the affect/emotion pass is cut. `affect` stays in the STT response and `/voice/command` as `null`. `voice.emotion_*` dropped.
- **D40 (01.Q8, verify on Pi):** audio routes emit the real engine sample rate in the `X-Audio-*` headers.
- **M13:** STT `language`/`task` fields are accepted and ignored; honour `language` later if the engine supports it. **M15:** in-memory speaker state on deletion is covered by D20.

## 1. Purpose

- **Nodes** use CC as their only audio backend. A node never talks to whisper or tts directly. It sends STT uploads, gets TTS back as a WAV or as a raw PCM stream, and uploads voice-profile samples it records itself. CC adds app-to-app auth plus household context headers, and runs the speaker pass scoped to the node's household members.
- **Mobile** uses JWT-authenticated twins: push-to-talk STT (the speaker comes from the JWT, never from voice), TTS for read-aloud, and the voice-profile wizard. Today the wizard is **node-mic only**: mobile asks CC to tell a node over MQTT to record, then polls for the result.
- **The voice pipeline (doc 01)** consumes the speaker identity. It uses it to load the right user's memories, to attribute transcripts, to answer "who am I?", to write presence signals and to drive the direction-hint `speaker_known` flag.
- **Account deletion** (`api/me.py:23-37`) purges voiceprints across every household.

---

## 2. Entry points

All routes are mounted under `/api/v0`: `media.router` at `/api/v0` with prefix `/media` (`main.py:719`), and the mobile routers at `/api/v0/mobile` (`main.py:817-818`). Validation failures return **400** `{"error":"validation_error","message":…,"details":[…]}` (`main.py:111-124`), not FastAPI's 422.

| # | Method + path | Auth | Live caller | Disposition |
|---|---|---|---|---|
| M1 | `POST /api/v0/media/tts/speak` | node `X-API-Key` | `NODE:tts_providers/jarvis_tts_api.py:42` (blocking + fallback), `NODE:core/wake_response.py:315,374` | keep |
| M2 | `POST /api/v0/media/tts/speak/stream` | node | `NODE:tts_providers/jarvis_tts_api.py:80` | keep |
| M3 | `POST /api/v0/media/whisper/transcribe` | node | `NODE:stt_providers/jarvis_whisper_client.py` (`transcribe_with_speaker`), `NODE:clients/jarvis_whisper_client.py:36` | keep (hot path) |
| M4 | `POST /api/v0/media/whisper/voice-profiles/enroll?user_id=` | node | `NODE:scripts/mqtt_tts_listener.py:647` (`enroll_voice` handler) | keep |
| M5 | `POST /api/v0/media/whisper/voice-profiles/verify?user_id=&household_id=` | node | `NODE:scripts/mqtt_tts_listener.py:766` (`verify_voice` handler) | keep |
| — | `DELETE /api/v0/media/whisper/voice-profiles/{user_id}`, `GET /api/v0/media/whisper/voice-profiles` | node | none (PLAN App. A) | **cut** |
| A1 | `POST /api/v0/mobile/stt` | JWT + member role | `MOB:src/api/chatApi.ts:235` | keep |
| A2 | `POST /api/v0/mobile/tts` | JWT + member | `MOB:src/api/chatApi.ts:257` (raw fetch → expo-av) | keep |
| V1 | `GET /api/v0/mobile/voice-profile/status?household_id=` | JWT + member | `MOB:VoiceProfileScreen`, `SettingsScreen`, `Nodes/HardwareTab` | keep |
| V2 | `POST /api/v0/mobile/voice-profile/enroll` | JWT + member | **API wrapper only**; no screen calls it (`MOB:VoiceProfileScreen.tsx:1-7`, "phone-mic enrollment was dropped") | see Q3 |
| V3 | `GET /api/v0/mobile/voice-profile/samples` | JWT + member | wrapper only | see Q3 |
| V4 | `DELETE /api/v0/mobile/voice-profile/samples/{sample_index}` | JWT + member | wrapper only | see Q3 |
| V5 | `POST /api/v0/mobile/voice-profile/verify` | JWT + member | wrapper only | see Q3 |
| V6 | `DELETE /api/v0/mobile/voice-profile?household_id=` | JWT + member | `MOB:VoiceProfileScreen.tsx:290,385` (delete, and "Re-Record All") | keep |
| V7 | `POST /api/v0/mobile/voice-profile/start-node-enrollment` | JWT (+ member of the *node's* household) | `MOB:VoiceProfileScreen.tsx:207` | keep |
| V8 | `POST /api/v0/mobile/voice-profile/start-node-verification` | JWT (+ member) | `MOB:VoiceProfileScreen.tsx:317` | keep |
| V9 | `POST /api/v0/mobile/voice-profile-results/{request_id}` (hidden, `include_in_schema=False`) | **none** | `NODE:scripts/mqtt_tts_listener.py:819` (sends `X-API-Key` anyway via `RestClient.post`) | keep, tighten (Q5) |
| V10 | `GET /api/v0/mobile/voice-profile-results/{request_id}` | JWT (no ownership check) | `MOB:voiceProfileApi.ts:240` (1 s poll, 60 s deadline) | keep |

**MQTT (outbound only).** Topic `jarvis/nodes/{node_id}/commands`. The payload is a JSON **array** `[{"command": "enroll_voice"|"verify_voice", "details": {...,"request_id"}}]` (`services/node_command_service.py:64-68`). The node dispatches it via `NODE:scripts/mqtt_tts_listener.py:1392-1393` on a 4-worker thread pool. Doc 05 owns the publisher.

**Internal callers.**

- `TTSClient.speak_stream`: every streaming voice response (`core/streaming_handler.py:73-178`, `core/conversation_handler.py:1494-2307`).
- `TTSClient.get_audio_format`: voice-stream response headers (`main.py:1442,1485,1532,2141`).
- `WhisperClient.transcribe(speaker_recognition=False)`: wake verification (`core/wake_verification.py:234-242`; doc 01).
- `WhisperClient.delete_all_voice_profiles`: account purge (`api/me.py:35`).
- `resolve_speaker_name`: `main.py:1063`, `core/conversation_handler.py:3580`, `api/mobile_chat.py:164`, `services/phone_call_service.py:769`.
- `resolve_member_names`: `main.py:922`.

**Server tool:** `identify_speaker` (`core/tools/identify_speaker_tool.py`; doc 02 owns the registry).

**Background loops:** none. Stickiness state is lazily expired (`core/utils/speaker_stickiness.py:103-106`).

### 2.1 Wire contracts (must be byte-compatible)

**M1 `/media/tts/speak`** and **A2 `/mobile/tts`**

| | M1 | A2 |
|---|---|---|
| Request | JSON `{"text": str}` | JSON `{"text": str, "household_id": str}` |
| Response | `200`, `Content-Type: audio/wav` | same |

- The body is a complete RIFF WAV built by `TTS:app/main.py:120-145` (`wave` module, header plus all frames), so it is not streamed. With Kokoro it is mono, 16-bit, **24000 Hz** (`TTS:app/providers/kokoro_provider.py:24-29`).
- The text goes through `clean_for_tts` first (`core/tts_text.py:104`), which strips emoji and markdown.
- Empty text after cleaning: jarvis-tts returns **200 JSON `{"error":"No text provided"}`** (`TTS:app/main.py:133-134`), and CC relabels those bytes `audio/wav` (`api/media.py:53`). This is a bug; see §8.

**M2 `/media/tts/speak/stream`** (the PCM stream contract, shared with the `/voice/command/stream` family in doc 01)

- Request: JSON `{"text": str}`.
- Response `200`, `Content-Type: audio/raw`, with these headers (`api/media.py:71-79`):
  - `X-Audio-Sample-Rate: <int>`: 24000 for Kokoro, 22050 for Piper (cut).
  - `X-Audio-Channels: 1`
  - `X-Audio-Sample-Width: 2`, meaning bytes per sample.
- The body is **headerless signed 16-bit little-endian PCM, interleaved, chunked transfer**. CC re-chunks at 4096 bytes (`core/clients/tts_client.py:157`), but chunk boundaries carry no meaning, and a boundary may fall mid-sample.
- Upstream also sends `X-Audio-Provider`; CC **does not forward it** (`api/media.py:74-78`).
- If an upstream header is missing, CC defaults to `22050/1/2` (`tts_client.py:147-151`). The node uses the same defaults (`NODE:jarvis_tts_api.py:87-89`).
- The voice-stream routes instead fall back to **16000** when `/audio/format` fails (`main.py:1443-1444`). That is inconsistent (§8), and Go must always send the real values.
- Node behaviour: `play_pcm_stream(iter_content(4096), rate, channels, width)`. On any failure it falls back to M1.

**M3 `/media/whisper/transcribe`**

Request: `multipart/form-data`.

| Field | Required | Notes |
|---|---|---|
| `file` | yes | Command audio (WAV). Used for text. |
| `speaker_audio` | no | Wake clip (about 2 s) plus command concatenated (`NODE:core/wake_transcription.py:62-76`). Used **only** for the speaker pass, and also the source of the wake-verification slice. |
| `conversation_id` | no | Keys the STT latency trace and triggers wake verification. |
| `language`, `task` | no | Forwarded as form data, but **whisper ignores them**. `/transcribe` declares no such fields, and the language is hard-coded `"en"` (`WA:app/utils.py:124`). |

Response `200`: whisper's JSON is passed through verbatim (`api/media.py:187-189`):
```json
{"text": "turn off the lights",
 "segments": [{"t0_ms": 0, "t1_ms": 1840, "text": " turn off the lights"}],
 "speaker": {"user_id": 7, "confidence": 0.612},
 "affect": null}
```

- `speaker` is **always present**. With no match it is `{"user_id": null, "confidence": <best score, possibly > 0>}` (`WA:app/utils.py:715-718`). With recognition disabled it is `{"user_id": null, "confidence": 0.0}`.
- `affect` is `null`, or `{"read": str, "arousal": str, "confidence": float}` when `voice.emotion_enabled` is on (`WA:app/affect.py:117-132`).
- Whisper errors (500 `{"error","stderr"}`, 413 over 25 MB) raise `httpx.HTTPStatusError` in CC, which surfaces as an **unhandled 500**. There is no exception handler (`main.py:111` only handles validation).

**M4 `/media/whisper/voice-profiles/enroll?user_id=<int>`**

- Request: multipart `file`. The node does **not** send `household_id` or `sample_index`; CC uses the node's household (`api/media.py:208-214`).
- Response: whisper's `{"status":"enrolled","user_id":int,"household_id":str,"sample_index":int,"total_samples":int}` (`WA:app/api/voice_profiles.py:101-107`). The index is auto-allocated as the next free one.

**M5 `/media/whisper/voice-profiles/verify?user_id=<int>&household_id=<str>`**

- Request: multipart `file`. **`household_id` comes from the query, not from the node's auth context** (`api/media.py:218-231`).
- Response: `{"matched": bool, "confidence": float(4dp), "user_id": int}`.
- No enrolled profile → whisper 404 → CC **500**.
- The fixed threshold of **0.45** is used, not the adaptive curve (`WA:app/api/voice_profiles.py:296`).

**A1 `/mobile/stt`**

- Request: multipart `file`, `household_id` (form, required), `language` (form, ignored downstream).
- Response:
  ```json
  {"text": str, "raw": {<whisper result>, "speaker": {"user_id": <jwt user>, "confidence": 1.0, "source": "jwt"}}}
  ```
  The speaker pass is skipped (`?speaker_recognition=false`), and `raw.speaker` is overwritten from the JWT (`api/mobile_audio.py:60-64`).
- Mobile reads only `text`.

**V1 status**: `{"has_profile": bool, "sample_count": int}` (`api/mobile_voice_profiles.py:38-55`).

**V2 enroll**

- Request: multipart `file`, `household_id`, optional `sample_index` (an explicit index overwrites that slot, which must be in 0–999).
- Response: whisper's enroll dict, as in M4.

**V3 samples**: `{"household_id","user_id","samples":[{"index","filename","size_bytes"}]}`.

**V4 delete sample**: `{"status":"deleted","user_id","sample_index","remaining_samples"}`. A missing sample gives whisper 404 → CC 500.

**V5 verify**: multipart `file`, `household_id`; returns `{"matched": bool, "confidence": float}` (trimmed, `api/mobile_voice_profiles.py:149-152`).

**V6 delete profile**: `{"status":"deleted","user_id","household_id"}`.

- No profile → whisper 404 → CC **500**.
- Mobile's "Re-Record All" calls V6 first and aborts the wizard on error (`MOB:VoiceProfileScreen.tsx:286-297`). So re-recording when the profile was already deleted elsewhere fails.

**V7 / V8 start-node-enrollment / verification**

- Request: JSON `{"node_id": str, "prompt_text": str|null, "duration_secs": float|null}`.
- Response: `{"request_id": "<uuid4>"}`.
- Errors: 404 `Node not found`; 409 `Node is offline` (`last_seen` older than 15 min, `models.py:16,57-62`); 403/502/503 from the role check (`deps.py:343-385`).
- The duration defaults to 8.0 s for enrollment and 5.0 s for verification.
- The MQTT `details` are `{request_id, user_id: <jwt user>, household_id: node.household_id, prompt_text, duration_secs}` (`api/mobile_voice_profiles.py:216-227`). `prompt_text` is unused by the node, which speaks a fixed cue (`NODE:mqtt_tts_listener.py:622`).

**V9 node result POST**

- Request: JSON, any dict. The node sends one of:
  - enroll success: `{"success": true, "user_id", "response": <M4 body>, "duration_secs"}`
  - verify success: `{"success": true, "matched", "confidence"}`
  - failure: `{"success": false, "error": "audio_bus_unavailable"|"record_failed: …"|"upload_failed: …"|"verify_failed: …"}`

  (`NODE:mqtt_tts_listener.py:610-686,800-806`.)
- Response: `{"status":"ok"}`.

**V10 poll**: while pending it returns **202 `{"detail":"pending"}`**; once the result is in, 200 with the stored dict, which is **consumed on read**. A corrupt file gives 500.

---

## 3. Behaviour

### 3.1 Media proxy (Python today)

URL resolution, in order:

1. the per-household/node setting `tts.url` / `whisper.url`
2. config-service discovery
3. `localhost:7707` / `:7706`

(`core/clients/tts_client.py:61-73`, `whisper_client.py:57-65`.)

Each request carries:

- app-to-app headers;
- `X-Context-Household-Id`, `-Node-Id`, `-User-Id`;
- **`X-Context-Household-Member-Ids`**, a comma list from the node's auth validation (`jarvis-auth-client/headers.py:44-78`).

The member list is what scopes the speaker search. Whisper only scores the profiles of the listed members (`WA:app/utils.py:529-570,630`). An empty list means no profiles get loaded, which means `user_id: null` every time. Legacy local-key nodes get an empty list (`deps.py:209-214`).

Timeouts:

| Call | Timeout |
|---|---|
| TTS | 30 s |
| transcribe | 60 s |
| enroll / verify | 30 s |
| list / check / delete | 10 s |

There is no retry, and a new `httpx.AsyncClient` is created per call.

### 3.2 STT request (M3) sequence

1. Open a latency trace keyed `stt-<uuid>`, **not** `conversation_id`, because the warmup trace may still be open under that id. Set `request_id = conversation_id` so the rows join (`api/media.py:110-122`).
2. Forward to whisper `/transcribe`. Whisper does the following (`WA:app/main.py:196-420`):
   - transcribe `file` (beam from `whisper.default_beam_size`, default 2);
   - if `voice.recognition_enabled` (whisper default **false**) **and** `speaker_recognition`, embed `speaker_audio` (or `file`) and score it against the averaged, L2-normalised centroid of each member. The match rule, which is the abstain rule, is in §3.5.
3. **After** the transcribe returns, if `speaker_audio` and `conversation_id` are both present and `voice.wake_verification_mode != off`, fire-and-forget `run_wake_verification` on the leading slice (`api/media.py:166-185`). It runs later on purpose, because whisper serialises model access (a GGML assert crashed it under concurrency). The details are in doc 01.
4. Return whisper's dict untouched.

### 3.3 Node-mic enrollment and verification sequence

```
Mobile                 CC                          MQTT            Node                       whisper
  │ POST start-node-enrollment {node_id,…}            │               │                            │
  │──────────────────────▶│ node exists? online? role │               │                            │
  │                       │ rid=uuid4                 │               │                            │
  │                       │── publish commands ──────▶│──────────────▶│ enroll_voice(details)      │
  │◀── {request_id} ──────│                           │               │ wake_paused():             │
  │ poll GET results/rid  │                           │               │  TTS cue (via M1)          │
  │ every 1s ≤60s ──────▶ │ 202 pending               │               │  sleep preroll 1.5s        │
  │                       │                           │               │  record_fixed_duration 8s  │
  │                       │◀── POST media/…/enroll?user_id (X-API-Key, wav) ───────────│            │
  │                       │──────────── POST /voice-profiles/enroll?user_id&household_id ─────────▶│
  │                       │◀──────────── {status,sample_index,total_samples} ─────────────────────│
  │                       │── 200 ───────────────────────────────────▶│                            │
  │                       │◀── POST mobile/voice-profile-results/rid {success,…} ──│               │
  │ poll ───────────────▶ │ 200 {…} (file deleted)    │               │                            │
  │ GET status (refresh sample_count)                 │               │                            │
  │ next take (3 total) … │                           │               │                            │
  │ then start-node-verification → same shape, verify endpoint (5 s), 0.45 threshold               │
```

Wizard facts (`MOB:VoiceProfileScreen.tsx`):

- **`TARGET_TAKES = 3`**. There are three fixed prompts with varied prosody: command, question, statement. They deliberately avoid the wake word (`:67-85`).
- The verify prompt is fixed (`:87-88`).
- Each take allows a 60 s poll deadline at 1 s intervals (`:92-93`).
- "Re-Record All" = V6 delete + 3 takes. "Add one more sample" = 1 take with no delete (`:276-300,449-458`).
- Only online nodes are offered (`:184`).
- The result shows `matched` and `round(confidence*100)%` (`:337-338,588`).
- There is no explicit-index retake in the node-mic flow (`:58-60`).
- If any take fails, the wizard exits, but the samples already enrolled are **kept**. A partial profile is therefore possible.

The node pauses wake detection during cue and record (`NODE:mqtt_tts_listener.py:617`). It deletes the temporary WAV afterwards, and it silently drops the command if `user_id`, `household_id` or `request_id` is missing. In that case mobile times out after 60 s.

If MQTT is down, CC still returns `request_id` (`node_command_service.py:70-73`), so the user waits 60 s for a timeout.

### 3.4 Speaker → user: where identity enters a turn

Identity is **asserted by the node**. CC never reads whisper's `speaker` itself on the voice path; it passes the whole result back to the node, and the node echoes it into later calls:

1. `/media/whisper/transcribe` → the node reads `speaker.user_id`/`confidence` (`NODE:stt_providers/jarvis_whisper_client.py:77-82`). If it is non-null, the node stores it in **module-global `_last_speaker_user_id/_confidence`**, which is **never cleared or aged** (`NODE:core/wake_transcription.py:85-96,303-305`).
2. On the next wake, the node's parallel warmup sends `/conversation/start` with `node_context.speaker_user_id`/`speaker_confidence` = **the last speaker ever identified on that node** (`NODE:core/wake_loop.py:725`, `clients/jarvis_command_center_client.py:612-615`).
3. The command turn sends `/voice/command/stream` with `speaker_user_id` = **this utterance's** STT result, which may be null (`NODE:wake_transcription.py:299,317-319`, `jarvis_command_center_client.py:194-195`).

> **Changed by D2/D3/D21:** Go ignores the node's claimed `speaker_user_id` on `/conversation/start` and keeps no per-node state. The conversation's speaker comes only from turns CC identified in-process during that conversation. An unknown or ambiguous turn speaker leaves per-user tools refusing, and the identity (or its absence) is passed to every command.

CC handling at **`/conversation/start`** (`main.py:1018-1070`):

- `validated_speaker_user_id(claimed, household_member_ids)` (`core/utils/speaker_membership.py:23-63`):
  - `None` → `None`;
  - empty member list → **fail open** (the claimed value is returned unchanged and *uncoerced*);
  - otherwise `int(claimed)`, and if that is not a member, log and return `None`.
- If the result is `None`, `inherit_speaker_for_node(node_id)`; else `record_speaker_for_node(node_id, uid, confidence)`.
- If a uid is set, put `speaker_user_id` into `node_context` and resolve `speaker_name` via the auth batch call. A failure is logged, and the key is left unset.
- At warmup it also resolves **all household member names** once, into `node_context.household_member_names`, for the "addressed to another member" hint (`main.py:912-928`).

CC handling per **turn** (`/voice/command[/stream|/continue]`, `main.py:1403-1410` → `core/conversation_handler.py:3475-3561`):

- Validate the turn's `speaker_user_id` against membership (no stickiness here).
- `effective = turn_speaker if not None else warmup_speaker`.
- If `effective != warmup_speaker`, re-resolve the name and memories into the **live cached** `node_context` (`_resolve_speaker_into_context`, `:3563-3625`). Memories are loaded only when memory is enabled, and `user_memories` is always overwritten so no previous speaker's memories leak through.
- Emit a trailing speaker block (`build_speaker_block(name or "default", memories)`; doc 03) after the cached prefix. The warmup prefix is speaker-agnostic.
- `decision_source` is logged as `stt | session | none` (`:3510-3522`).
- **Only a confident STT speaker** (`turn_speaker is not None`) writes `presence.seen` (when ambient is enabled, `:3536-3559`; doc 10) and sets `speaker_known=True` for the direction hint (`:924`).

### 3.5 Confidence thresholds and abstain (whisper today; re-calibrated in Go)

Match iff `best > threshold` **and not** ambiguous (`WA:app/utils.py:587-718`):

| Clip duration | Threshold | Setting (whisper settings DB) |
|---|---|---|
| < `voice.short_cutoff_seconds` (1.0) | 0.65 | `voice.threshold_short` |
| > `voice.long_cutoff_seconds` (3.0) | 0.40 | `voice.threshold_long` |
| otherwise | 0.50 | `voice.similarity_threshold` (prod ≈0.49 per spike) |
| verify endpoint | **0.45 fixed** | none |

- **Ambiguous** means 2 or more profiles loaded **and** `best − second < voice.min_speaker_margin` (0.05).
- On abstain, `user_id` is null but `confidence` still carries the best score.
- Scoring is cosine (inner product of L2-normalised vectors) against the per-user centroid, which is the mean of every sample's embedding.
- Duration is measured on the speaker-pass clip (wake plus command), so a short "delete it" usually lands in the normal band.

The spike (`spikes/voice-onnx/RESULTS.md:17,31,73`) found ERes2Net's EER point at about 0.33–0.36, so **all numbers change**. The structure (adaptive vs single threshold, margin gate) is a Q7 decision.

> **Changed by D33:** one `voice.similarity_threshold` plus the margin gate; verify uses the same threshold. Recognition stays off by default (D35).

### 3.6 Stickiness (CC)

`core/utils/speaker_stickiness.py` keeps a per-node `{user_id, confidence, ts}`.

- **Record** only when `confidence ≥ voice.stickiness_min_confidence` (0.55). **Inherit** when the age is within `voice.stickiness_ttl_seconds` (30). Both are read from settings on every call (`:31-48,63-110`).
- `/conversation/end` → `reset_node_history` (`main.py:1100-1120`).
- It is consulted **only** at `/conversation/start`, never per turn.
- It is process-local, so it is lost on restart. It is unsynchronised, which is fine under asyncio.

> **Changed by D3:** not ported. `speaker_stickiness.py` and `voice.stickiness_*` are dropped.

### 3.7 Name resolution

`resolve_speaker_name` / `resolve_member_names` (`core/utils/speaker_resolver.py`):

- They call `GET {auth}/internal/users/batch?user_ids=1&user_ids=2` (repeated params; a comma-joined list was a prod 422 bug, `:89-97`) → `{"users": {"<id>": "<username>"}}`.
- There is a 5-min TTL in-process cache, which is never invalidated on rename.
- They **never raise**; any failure means no name.
- The request is unauthenticated (`rest_client.get` without app headers). In Go this becomes an in-process auth-module call.

### 3.8 `identify_speaker` tool

`core/tools/identify_speaker_tool.py:70-109` reads `conversation_cache.get_node_context(cid)`:

| Situation | Returns |
|---|---|
| no conversation or no context | `{"speaker_name": null, "error": "no_conversation"|"no_context", "message": …}` |
| speaker missing, or name in `{"default","user",""}` | `{"speaker_name": null, "message": "I don't recognize your voice yet. Enroll a voice profile…"}` |
| otherwise | `{"speaker_name": name}` |

It reports whatever `node_context` holds, **including the session/warmup fallback**, so it can answer with the previous speaker's name. **Changed by D2/D3:** in Go it reports only the speaker identified in this conversation. Its `included_system_prompt_text` (`:59-68`) is prompt-bearing and must be ported byte-exact (doc 03).

### 3.9 Mobile STT/TTS

- `/mobile/stt` → role check, then whisper with `speaker_recognition=false` and **no member ids** (`api/mobile_audio.py:41-64`).
- `/mobile/tts` → role check, then `speak()` WAV.
- Mobile chat sets the speaker from the JWT (`api/mobile_chat.py:162-164`; doc 13).

---

## 4. Data

| Where (today) | What | Lifecycle |
|---|---|---|
| whisper FS `voice_profiles/{household_id}/{sha256(user_id)[:16]}/sample_NNN.wav` (`WA:app/utils.py:145,378-392`) | Raw enrollment WAVs, 0–999 per user per household. Legacy `{hash}.wav` single files are migrated on the next enroll. | Until V6/V4/account purge. **Not imported to Go** (PLAN §5). |
| whisper memory `_member_embedding_cache[(hh, uid)]` | Centroid per member, including negative entries | Invalidated per household on any write, or on an embed-prep settings change |
| CC `/tmp/jarvis-voice-profile-results/{rid}.json` (`api/mobile_voice_profiles.py:35,295-330`) | Node → mobile handoff | Deleted on first successful GET. **Never expires** if never polled. |
| CC memory `_node_speaker_history` | Stickiness | TTL 30 s, lazy |
| CC memory `_speaker_cache` | `uid → username` | 5 min |
| CC memory `NodeCommandService._pending_commands` | `request_id` registry | 5 min (doc 05) |
| CC `node_context` in conversation cache | `speaker_user_id`, `speaker_name`, `user_memories`, `household_member_names` | Conversation lifetime (doc 01) |

CC itself has **no voice-related tables**.

---

## 5. Settings

**CC** (settings DB, `services/settings_definitions.py:570-595`):

| Key | Default | Notes |
|---|---|---|
| `voice.stickiness_min_confidence` | 0.55 | ECAPA-calibrated; meaningless under a new model. **Dropped (D3)** |
| `voice.stickiness_ttl_seconds` | 30.0 | **Dropped (D3)** |
| `tts.url`, `whisper.url` | none | Read per household/node (`tts_client.py:66`, `whisper_client.py:58`) but **not declared** in the definitions. They disappear in Go, which is in-process. |
| `voice.wake_verification_mode` / `_phrase` | — | Gate the M3 side effect; owned by doc 01 |

**whisper** (moves into the Go STT/speaker module):

- `voice.recognition_enabled` (**false**)
- `voice.encoder` (ecapa)
- `voice.similarity_threshold` 0.5
- `voice.threshold_short` 0.65
- `voice.threshold_long` 0.4
- `voice.short_cutoff_seconds` 1.0
- `voice.long_cutoff_seconds` 3.0
- `voice.min_speaker_margin` 0.05
- `voice.embed_preprocess_enabled` false, `voice.embed_target_rms_db` −23, `voice.embed_trim_silence_db` −40
- `voice.emotion_enabled` false, `voice.emotion_min_confidence` 0.45
- `whisper.default_beam_size` 2

(`WA:app/services/settings_definitions.py:93-240`.) Env: `WHISPER_MAX_UPLOAD_BYTES` (25 MB).

> **Changed by D33/D35/D38:** Go keeps `voice.recognition_enabled` (default **false**), `voice.similarity_threshold` and `voice.min_speaker_margin` (recalibrated). The short/long thresholds and cutoffs, `voice.encoder` (model is fixed per build, tagged per embedding, D34) and `voice.emotion_*` are dropped.

**tts:** active provider and voice (Kokoro `bm_george`, speed 1.25 per PLAN §3).

---

## 6. Dependencies

- **Doc 01 (voice pipeline):**
  - consumes the identity (§3.4) and owns `/conversation/start|end`, wake verification and the streaming handlers that call `speak_stream`;
  - shares the PCM header contract;
  - `clean_for_tts` runs after the provider's `sanitize_text`.
- **Doc 02:** the `identify_speaker` registration. **Doc 03:** `build_speaker_block`, the tool's prompt text. **Doc 04:** memory load per speaker, and transcript attribution. **Doc 05:** `NodeCommandService`, MQTT and `Node.is_online`. **Doc 10:** `record_voice_presence`. **Doc 11:** phone name resolution. **Doc 13:** mobile chat speaker.
- **jarvis-auth:**
  - `/internal/validate-node`, which supplies `household_member_ids`;
  - `/internal/validate-household-access`, for the role check;
  - `/internal/users/batch`, for names.
- **whisper-api** and **jarvis-tts**: replaced by in-process modules in Go.
- **No LLM calls** in this subsystem.

---

## 7. Invariants and non-obvious behaviour (a port must preserve)

1. **Speaker search is scoped to the node's validated household members**: the context header in Python, a function argument in Go. A profile outside the member list can never match (`WA:app/utils.py:562-566`).
2. **Node-asserted `speaker_user_id` is honoured only if it is in `household_member_ids`.** It is coerced to int and otherwise becomes `None` (`speaker_membership.py:49-63`). Unknown membership fails **open**, which preserves legacy behaviour.
3. **Mobile STT never runs the voice pass**, and it reports `speaker={"user_id": jwt, "confidence": 1.0, "source": "jwt"}` inside `raw` (`mobile_audio.py:55-63`).
4. **`speaker_audio` is used only for the speaker pass; `file` is used for text** (`WA:app/main.py:310`).
5. **Wake verification starts after the transcribe returns**, never concurrently (`api/media.py:166-185`). In Go the STT engine still serialises.
6. **Abstain returns `user_id: null` with a non-zero `confidence`.** Consumers must key on `user_id`, never on confidence > 0.
7. Only a **confident per-turn STT id** drives presence signals and `speaker_known`. The session fallback never does (`conversation_handler.py:3536-3541`).
8. When the turn speaker differs from the warmup speaker, `user_memories` is **always overwritten**, to an empty string if memory is off (`:3586-3593`). The cached system prompt is **never rebuilt** (that rebuild was the prefix/timezone clobber bug).
9. Stickiness records **only at or above min confidence**, inherits only within the TTL, and is reset on `/conversation/end`.
10. The V10 poll returns **202 while pending**, and the result is **single-use**. Mobile treats 202 as pending, and tolerates axios throwing on 202 (`MOB:voiceProfileApi.ts:239-253`).
11. Node enrollment always uses the **JWT user** as `user_id` and the **node's** household (`api/mobile_voice_profiles.py:216-227`). A user can therefore enroll only themselves, in the node's household.
12. **The PCM stream is raw s16le with the three `X-Audio-*` headers on every audio route**; the node initialises playback from them.
13. Account deletion purges voiceprints across **all** households, best-effort. A failure never blocks deletion (`api/me.py:23-37`).
14. Each voiceprint is per **(household, user)**, so a user in two households enrolls twice (Q8).

> **Changed by decisions:** invariants 2 and 9 do not carry over (D2/D3: the node claim is ignored and there is no stickiness). Invariant 7's "session fallback" becomes "speaker from an earlier turn of this conversation". Invariant 13 becomes part of the D20 transaction. Invariant 14 is confirmed by D36.

---

## 8. Oddities

1. **The node's "last speaker" never expires.** `NODE:wake_transcription.py:85-96` sends the last-ever identified speaker on every `/conversation/start`. CC records it into stickiness, which re-arms the 30 s TTL with stale data, and treats it as the session speaker. Any later turn with `speaker_user_id=null` inherits it (`source=session`). This is the cross-user misattribution vector the B7 comment names (`conversation_handler.py:3507-3511`), and it defeats `/conversation/end`'s reset. See Q1.
2. **CC stickiness is nearly redundant.** It only fires when the node sends *no* speaker, which in practice means right after a node restart.
3. **Unauthenticated `POST /voice-profile-results/{rid}`.** Anyone who can reach CC can write a result, and with a crafted rid can fill `/tmp` (no size cap, no expiry). There is no path traversal check on `request_id`, though the `.json` suffix plus `os.path.join` with a `/`-free FastAPI path parameter limits this. **V10 does not check that the poller is the user who started the request.**
4. **M4 enroll accepts an arbitrary `user_id`** from any authenticated node. There is no membership check and no binding to a pending `enroll_voice` command. A compromised node can plant a voiceprint for any user id in its household directory.
5. **M5 verify takes `household_id` from the query** instead of the node's auth context, so it can probe another household's profile.
6. Whisper 404s (verify with no profile, delete of a missing profile or sample) surface as **CC 500 with a plain-text body**.
7. Empty text after `clean_for_tts` (e.g. all-emoji) → TTS returns JSON `{"error":…}` with 200 → CC serves it as `audio/wav`, or as `audio/raw` with default headers.
8. Header fallbacks disagree: 22050 (TTS client, node) vs 16000 (voice-stream routes) vs the actual 24000 (Kokoro).
9. The `language`/`task` form fields on M3 and A1 are dead. Whisper hard-codes `en`.
10. `prompt_text` is sent to the node but ignored. The node speaks a fixed cue, and mobile shows the prompt on screen.
11. `tts.url`/`whisper.url` are read but never defined as settings. `TTSClient.generate_wake_response` (`tts_client.py:207-228`) is dead (the route was removed, `api/media.py:262-266`).
12. Four mobile phone-mic routes (V2–V5) have no UI caller. The mobile header comment says the phone-mic flow was dropped.
13. A **partial enrollment** (1–2 of 3 takes) is left in place when the wizard aborts, and `has_profile` reports true.
14. The STT trace's `user_command` is set from the transcript (`api/media.py:187-188`); a failed transcribe still ends the trace with `trace_status=error`.

---

## 9. Tests

| File | Tests | Covers |
|---|---|---|
| `tests/test_media.py` | 14 | M1/M3 auth, proxying, context, params |
| `tests/test_mobile_audio.py` | 1 | A1 |
| `tests/test_tts_client.py` | 15 | URL resolution, stream headers/defaults, spans |
| `tests/test_whisper_client.py` | 19 | Every whisper call shape, `speaker_recognition=false` query |
| `tests/test_speaker_membership.py` | 8 | Fail-open, coercion, rejection |
| `tests/test_speaker_stickiness.py` | 12 | Record gate, TTL, reset |
| `tests/test_speaker_resolver.py` | 11 | Cache, repeated-param query, never-raise |
| `tests/test_identify_speaker_tool.py` | 12 | Placeholder names, missing context |
| `tests/test_wake_verification.py`, `test_wake_verified_wiring.py` | — | M3 side effect (doc 01) |

**Gaps:** `mobile_voice_profiles.py` has **no tests** (V1–V10). The turn-speaker resolution (`_build_turn_speaker_message`) is covered only indirectly.

**Golden / contract candidates for Go:**

1. **M2 PCM stream:** `Content-Type: audio/raw`, the three headers equal to 24000/1/2, body length even and `== samples*2`. Run a TTS→STT round trip in the GPU lane (PLAN §4).
2. **M3 response shape:** keys `text, segments[{t0_ms,t1_ms,text}], speaker{user_id,confidence}, affect`. Cover abstain (`user_id:null`, `confidence>0`) and `recognition disabled` (`0.0`).
3. **Enrollment loop black-box:** fake node on embedded MQTT → V7 → assert topic/payload array shape → M4 upload → V9 → V10 returns 202 then 200 then 202 (consumed).
4. **The A1 `raw.speaker` override** and the **400 validation envelope** on a missing `household_id`.
5. **Identity table test:** (warmup speaker, turn speaker, membership, stickiness) → (effective, source, presence emitted). Every row is in §3.4/§3.6.
6. **Node-setup integration suite and install-e2e:** the node's real `enroll_voice`/`verify_voice` handlers against `jarvisd`.

---

## 10. Questions for the user

1. **[behaviour] What should an unidentified turn be attributed to?**
   - Today the node replays its *last-ever* identified speaker (no expiry). CC adopts it as the session speaker and loads that person's memories for any turn whose voice didn't match (Oddity 1). In Go, STT runs in-process, so CC itself knows exactly who it identified on which node and when.
   - *Why it matters:* wrong-person memories, transcripts and "who am I" answers. It is the main privacy failure mode of shared nodes.
   - Options:
     - (a) Port as-is.
     - (b) CC becomes the source of truth. It records `{node, user, confidence, t}` from its own STT and ignores the node-asserted `speaker_user_id` except as a membership-validated hint, honouring it only if CC saw that id on that node within the TTL. Within a conversation, inherit the first confident turn's speaker. Across conversations, inherit only within the TTL.
     - (c) Strict: no inheritance at all; unknown means unknown.
   - **Recommendation: (b).** The node's wire contract is unchanged, the stale-speaker leak disappears, and `/conversation/end` reset starts working again.

   **Decided (D2/D3):** close to (c). The node's claimed speaker is ignored entirely; nothing is kept per node or across conversations. Within a conversation the speaker persists from turns identified in that conversation, and dies with it.

2. **[behaviour] With 2+ enrolled members and an ambiguous or unknown match, should per-user scopes be withheld?**
   - Per-user scopes are memories, a user's email or calendar tools, and reminders. Today an unknown speaker just gets no speaker block. Any per-user data still reached through the session fallback is exposed.
   - Options:
     - (a) Unknown means household-default context, and per-user tools run as the node's owner (`node.user`).
     - (b) Unknown means per-user tools refuse with "I'm not sure who's speaking."
     - (c) Make it configurable per household.
   - **Recommendation: (b) by default, with (c) as a later knob.** It pairs with Q1, and abstaining is cheaper than misattribution (the whisper margin-gate comment already argues this).

   **Decided (D21):** (b), no per-household knob. Ambiguous counts as unknown; no fallback to the node owner. The server passes the speaker identity (or none) to every command, and each command decides what to refuse. With recognition off, the message says so (M14).

3. **[scope] Should the phone-mic routes V2–V5 be cut?**
   - The routes are `POST /voice-profile/enroll`, `GET /samples`, `DELETE /samples/{i}` and `POST /verify`. No screen calls them, and the mobile app says phone-mic enrollment was dropped for acoustic mismatch.
   - *Why it matters:* 4 fewer routes and no explicit-index slot management. The only risk is older app builds in the wild.
   - Options: (a) cut; (b) keep as thin wrappers.
   - **Recommendation: (a) cut.** Users re-enroll anyway under greenfield voice, and the current app only uses the node-mic flow.

   **Decided (D9):** (a) cut.

4. **[behaviour] Should Go keep the raw enrollment audio, or only embeddings?**
   - *Why it matters:* WAVs let us switch ERes2Net ↔ TitaNet or re-calibrate without asking every user to re-enroll again. But they are biometric recordings, which raises the privacy surface.
   - Options:
     - (a) Embeddings only, tagged with model id; a model change forces re-enroll.
     - (b) Keep the WAVs (SQLite blob or `~/.jarvis/voice/`) plus cached embeddings, and re-embed on model change.
     - (c) Keep the WAVs only until the model choice is final (Phase 4), then purge.
   - **Recommendation: (b).** It is self-hosted local data, account deletion already purges it, and it avoids a second forced re-enroll.

   **Decided (D34):** (a), not the recommendation. Embeddings only, tagged with model id; WAVs discarded once embedded; a model change requires re-enrollment. Legacy ECAPA voiceprints are not imported.

5. **[behaviour] Should the enrollment handoff routes (V9/V10, M4/M5) be tightened?**
   - Today V9 is unauthenticated; V10 doesn't check who polls; M4 accepts any `user_id`; M5 takes `household_id` from the query.
   - Proposal: V9 requires node `X-API-Key` (the node already sends it) and must match the node the command was published to. V10 must be the user who started it. M4/M5 require a pending `enroll_voice`/`verify_voice` request for that node and user, and use the node's household. Results expire after about 5 min, held in an in-memory map instead of `/tmp`.
   - None of this changes the node or mobile wire shapes.
   - **Recommendation: yes, do all of it.** The only thing it rejects is behaviour no legitimate client exhibits.

   **Decided (D4):** yes. V9 needs node auth bound to the issuing node; V10, M4 and M5 are tightened under the same close-the-holes policy, with no wire change.

6. **[behaviour] Enrollment UX under the new model: keep 3 × 8 s takes and a 5 s verify, or add a server-side quality gate?**
   - Today a silent or noisy take is accepted, and a failed wizard leaves a partial profile (Oddities 13, 10).
   - Options:
     - (a) Same as today.
     - (b) Gate each take: reject when VAD speech is under about 3 s, or when the take scores below X against the user's existing takes. Return `success:false, error:"low_quality"`, which mobile already displays.
     - (c) (b), plus treating a profile as "enrolled" only at 3 or more good takes.
   - **Recommendation: (b).** It needs no mobile change. (c) needs a `has_profile` semantics change, so leave it unless you want it.

   **Decided (D37):** (b), treated as a D8 bug fix. No mobile change.

7. **[change] Threshold model for Go speaker ID.**
   - Today there are 5 knobs: short, normal and long thresholds plus two cutoffs. There is also a margin gate and a separate fixed verify threshold of 0.45, all ECAPA-calibrated.
   - Options:
     - (a) Port the structure and recalibrate every number.
     - (b) Simplify: one threshold plus the margin gate, with verify using the same threshold, recalibrated on jarvis-dev enrollments (the spike showed flatter EER across 1.5 s and 3 s for ERes2Net).
   - **Recommendation: (b).** Keep the setting key names `voice.similarity_threshold` and `voice.min_speaker_margin`, and drop the short/long ones.

   **Decided (D33):** (b).

8. **[behaviour] Should a voiceprint be per user, or per (user, household)?**
   - Today it is per (household, user). A user in two households enrolls twice, and V6 deletes only one copy.
   - Options:
     - (a) Keep per household.
     - (b) One voiceprint per user, scored inside any household they belong to. Node-mic differences argue for per-node-or-household samples, though.
   - **Recommendation: (a) keep per household.** It matches the node-mic rationale, and multi-household users are rare. But tell me if the shared-across-homes case matters to you.

   **Decided (D36):** (a) keep per (household, user). Future work: per-node profiles (PLAN §9).

9. **[behaviour] Should speaker recognition default to on?**
   - Whisper's `voice.recognition_enabled` defaults to **false**. A fresh install that enrolls a voice gets no matches until an admin flips the flag, and the logs flag this case as easy to misdiagnose.
   - Options: (a) keep the off flag; (b) on whenever any member of the household has a profile; (c) remove the flag.
   - **Recommendation: (b).**

   **Decided (D35):** (a), not the recommendation. Off by default for privacy; enrolling does not turn it on. M14 covers the UX consequence.

10. **[scope] Should the affect/emotion pass be cut?**
    - `voice.emotion_enabled` (default off) adds `affect` to M3, using librosa pitch features. librosa is cut (PLAN §7), and the node forwards `affect` to `/voice/command`.
    - Options: (a) cut, always returning `"affect": null`; (b) reimplement the features in Go (pitch variance, pause ratio, spectral centroid).
    - **Recommendation: (a).** Keep the key as `null` for contract stability, and revisit if you were actively tuning it.

    **Decided (D38):** (a). `affect` stays `null`; `voice.emotion_*` dropped.

11. **[minor] Should account deletion also clear in-memory speaker state?**
    - "Who am I" uses the session fallback, and the 5-min name cache keeps old usernames.
    - **Recommendation:** on account deletion or membership change, purge stickiness entries and the name cache for that user. Doing it in-process is trivial.

    **Decided (D20/M15):** yes. Account deletion clears in-memory caches for the user. There are no stickiness entries to purge (D3).

12. **[minor] How should the dead `language`/`task` fields be handled?**
    - Options: (a) accept and ignore them (zero contract risk); (b) honour `language` if the D7 engine supports it.
    - **Recommendation: (a) now, (b) after D7.**

    **Decided (M13/D47):** (a) now; (b) later if the engine supports it.

---

## 11. Go port notes

**Shape.**

- `internal/modules/stt` has the engine (whisper.cpp or sherpa ASR per D7) behind a mutex or single worker, which preserves invariant 5.
- `internal/voice/speaker` wraps the sherpa embedding extractor plus a `ProfileStore`.
- `internal/voice/kokoro` streams per sentence.
- In `cc/media` the routes in §2.1 become thin adapters over three interfaces:
  - `STT.Transcribe(ctx, audio, speakerAudio []byte, scope SpeakerScope) (Result, error)`, where `SpeakerScope{HouseholdID, MemberIDs, Enabled}`;
  - `TTS.Stream(ctx, text) (Format, <-chan []byte)`;
  - `Speaker.Enroll / Verify / Delete / List`.
- No HTTP hop, no `X-Context-*` headers, no URL settings.

**Storage.** SQLite `voice_samples(household_id, user_id, idx, model_id, embedding BLOB, created_at)` plus an in-memory centroid cache per `(household, user, model_id)` that is invalidated on write. Raw `user_id`, not a sha prefix; the DB is local. **No WAV column (D34):** enrollment audio is discarded once embedded. Embeddings with a `model_id` other than the active model are ignored, so a model change means re-enrollment. No node id (D36). Account deletion deletes the user's rows in every household inside the D20 transaction.

**Identity service (`cc/voice/identity`).** It replaces `speaker_membership`, `speaker_stickiness` and the node echo:

- Every in-process transcribe records `{conversation_id, user_id|nil, score, t}` against the **conversation**, never the node (D3).
- `/conversation/start` ignores the claimed speaker (D2). Per-turn handlers ask `identity.Resolve(conv, turnResult)`, which returns `{effective, source: stt|conversation|none}`. There is no `sticky` source. State is dropped when the conversation ends or expires.
- Ambiguous (margin gate) resolves to unknown (D21). The resolved identity, or its absence, is passed to every command and server tool; per-user server tools (memory, phone, errands) refuse on unknown, and say "speaker recognition is off" when it is (M14).
- The membership check comes from the auth module in-process.
- Names come from the auth module directly, so the 5-min cache becomes optional.

**Enrollment handoff.** `map[requestID]pending{nodeID, userID, kind, expires}` is created by V7/V8. M4/M5 must match a pending entry (Q5, D4), V9 requires node auth and the issuing node (D4) and fills in the result, and V10 consumes it (poller must be the starting user). M4 applies the D37 quality gate before storing an embedding; verify (M5) uses `voice.similarity_threshold` (D33). A 5-min janitor runs on the existing ticker infrastructure. The `/tmp` file is gone, and the MQTT publish goes through the embedded broker (doc 05).

**PCM streaming.** Use `http.Flusher`. Write the headers before the first synth so the node starts its player early, and flush after each sentence. Always send the active voice's real format (`24000/1/2` for Kokoro; D40 01.Q8, verify on the Pi). Return **400 JSON** for empty text, rather than a 200 JSON body labelled as audio: the node already treats non-2xx as "fall back", so this is safe. A `context` cancel on client disconnect must stop synthesis.

**Error mapping.** Map not-found from the profile store to **404** (Python gave 500). Mobile treats any error as a failure, so this is safe. Keep 202 `{"detail":"pending"}` exactly.

**Risks.**

- **Threshold calibration** (Phase 4) needs real node-mic enrollments; the spike set was tiny.
- **The node keeps sending stale `speaker_user_id`** (a frozen client). Go accepts the field and ignores it (D2).
- **TTS→STT parity in the GPU lane** depends on D7.
- **The `audio/wav` blocking route** (M1, A2) must emit a correct RIFF header with the real sample rate, because the node's wake-response path plays it as a file.

**Simplifications this enables.**

- Delete the whisper and tts proxies and their clients, the context headers, URL discovery, the per-call `httpx` clients, the `/tmp` handoff and the CC↔whisper member-id plumbing.
- Collapse the 15 whisper voice settings to about 4 (D33): `voice.recognition_enabled` (default off, D35), `voice.similarity_threshold`, `voice.min_speaker_margin`, `whisper.default_beam_size`. `voice.stickiness_*` (D3) and `voice.emotion_*` (D38) go.
- Cut V2–V5 (D9) and the 2 media list/delete routes.
- No affect pass (D38): `affect` is always `null`. `language`/`task` accepted and ignored (M13).
- Move account purge and identity state into one module.
