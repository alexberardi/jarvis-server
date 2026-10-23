# 07 — Remote speech providers (STT and TTS)

Design for letting STT and TTS run on a hosted or self-hosted service, as LLM labels already can
(`engine=remote`, [06](06-model-manager-api.md)). Decision:
[../decisions/2026-10-21-remote-speech-providers.md](../decisions/2026-10-21-remote-speech-providers.md).
Written 2026-10-21 from a read of `internal/modules/{stt,tts,llm}` and the providers' docs
(links in §7).

**Nothing changes for an install that doesn't opt in**: local stays the default for both, and
the wire contracts (`/transcribe`, `/audio/format`, `/speak[/stream]`, cc's `/media/*`, mobile
`/mobile/stt|tts`) keep their shapes, so nodes, the mobile app, the desktop agent and phone calls
need no change.

---

## 1. Providers

A **preset** is a name plus a default URL, dialect and model; picking one in the admin fills the
fields, which stay editable. Presets are a static Go catalog served to the admin (so they are
tested), not a stored setting: what's stored is URL, dialect, model, key, voice.

| Preset | STT | TTS | Dialect | Notes |
|---|---|---|---|---|
| OpenAI | `gpt-transcribe` | `gpt-4o-mini-tts`, voice `marin` | openai | `whisper-1`, `gpt-4o-*-transcribe` sunset 2027-02-26; `tts-1(-hd)` 2027-01-06. Fixed voice list |
| Groq | `whisper-large-v3-turbo` at `https://api.groq.com/openai/v1` | — (its TTS models are in flux; add later) | openai | Bills each request as ≥10 s: short turns cost more than they look |
| ElevenLabs | `scribe_v2` | `eleven_flash_v2_5` (~75 ms) | elevenlabs | `xi-api-key`; voices from `GET /v2/voices`; we send `enable_logging=false` |
| Other | any `/v1/audio/transcriptions` | any `/v1/audio/speech` | openai | speaches, LocalAI, Kokoro-FastAPI, a GPU box on the LAN; voices from `GET /v1/audio/voices` if served, else free text |

Later dialects, same seam: Deepgram (`/v1/listen`, `/v1/speak`), Cartesia. Not in v1.

## 2. STT

**Settings.** The `stt` label (llm module, `engine/config.go:60`) gains `remote`: its engine
options become `local|remote|off` (today `local|off`, `config.go:230-235`; the admin mirrors it at
`LabelsEditor.tsx:30`), and `remote_url`, `remote_api` (options `openai|elevenlabs` for stt),
`remote_model`, `remote_api_key` (secret) apply to it (`only` lists at `config.go:120-127`).
New: `remote_timeout_ms` (default 8000 for stt: a voice turn can't wait long) and
`fallback` (`off` default | `cpu`).

**Seam.** `stt.STTEngine.Resolve` returns only a base URL (`stt/stt.go:50`, adapter at
`cmd/jarvisd/main.go:142-145`); it returns the resolver's whole `Endpoint` (`Remote`, `APIKey`,
`Model`, plus the dialect) instead. `infer` (`transcribe.go:167`) picks a client:
- **whisper-server** (today): unchanged.
- **openai**: multipart `POST {url}/audio/transcriptions`, `file` = the 16 kHz WAV `Transcribe`
  already builds, `model`, `language` from `whisper.language`, `prompt` when set,
  `response_format=json` (the gpt-transcribe family accepts json only). Temperature and beam size
  have no equivalent and are dropped.
- **elevenlabs**: multipart `POST {url}/v1/speech-to-text`, `model_id`, `file` as raw PCM with
  `file_format=pcm_s16le_16`, `language_code`; `words[]` timings become segments.

**Contract shape.** `/transcribe` returns `segments[{t0_ms,t1_ms,text}]`
(`contract/whisper_test.go`). A provider without timings yields one segment spanning the clip.

**Speaker recognition is untouched**: it runs locally in parallel with transcription
(`transcribe.go:105-124`, sherpa ERes2Net) and never depends on the engine.

**Fallback (`fallback=cpu`).** On a transport error, timeout, 429 or 5xx (also 401/403, logged as
an error: an expired key shouldn't silence the house), the same clip goes to a local whisper-server
**forced to CPU** with the label's `model`. A 400/413/422 is our bug or a bad clip: no fallback.
- The resolver runs one instance per label and returns early for remote
  (`resolver.go:324-352`), so the fallback is a Resolve variant that ignores `engine=remote` and
  forces `gpu_layers=0` (its instance key differs because flavour is in the key). It is started on
  first use (cold start, ~1-2 s for small.en) and stopped by the usual idle rules.
- It needs a model the user installed from the Models page. `fallback=cpu` with no model shows a
  warning on the label; at runtime it is the remote error, not a second one.
- `stt.Result` records which engine answered; the label status shows the last remote error and a
  fallback count.

**Cost note.** Wake verification re-transcribes the first slice (`cc/media.go:297-320`), so a
remote STT sees two requests per wake turn. Accepted; documented in the setting.

## 3. TTS

**Settings** (tts module, `tts/tts.go:64-73`). `tts.provider` gains options:
`kokoro` (default) | `remote`. New keys: `tts.remote_url`, `tts.remote_api`
(`openai|elevenlabs`), `tts.remote_model`, `tts.remote_api_key` (secret), `tts.remote_voice`,
`tts.remote_timeout_ms` (default 8000), `tts.fallback` (`off` default | `cpu` = local Kokoro,
which is CPU already). `tts.kokoro_*` keep applying to Kokoro only, gain included (built 2026-10-22:
remote audio passes at gain 1 and speed 1, [decision](../decisions/2026-10-22-remote-tts.md)).

**Format stays stable.** Callers read `AudioFormat()` before `Speak` to set
`X-Audio-Sample-Rate` (`cc/voice_routes.go:448-451`, `cc/media.go:90,115-123`). So a remote
provider is always delivered at **24 kHz s16le mono**, Kokoro's rate: OpenAI's and
Kokoro-FastAPI's `pcm` already are; ElevenLabs is asked for `pcm_24000`; a server that only
returns WAV (LocalAI) is sniffed (`RIFF`) and resampled. That also makes a mid-utterance fallback
to Kokoro seamless on the wire, and keeps `contract/tts_test.go` (headers equal `/audio/format`)
true.

**Streaming.** `Speak` keeps its sentence split (`split.go`) and one-ahead rendering
(`speak.go:138`); a remote provider gets **one request per sentence**, read as chunks arrive.
First audio is one sentence's round trip, as with Kokoro. Per-sentence requests also let the
fallback act per sentence (a different voice mid-reply is the accepted cost). OpenAI's 4096-char
input cap is far above a sentence.
- **openai**: `POST {url}/audio/speech` `{model, input, voice, response_format:"pcm", speed}`.
- **elevenlabs**: `POST {url}/v1/text-to-speech/{voice}/stream?output_format=pcm_24000&enable_logging=false`
  `{text, model_id}`.

**Voices.** `/api/tts/voices` and `/api/tts/sample` (admin, superuser, `admin/bff.go:117-118`)
become provider-aware: Kokoro's 54 as today; OpenAI's fixed list; ElevenLabs `GET /v2/voices`
(paged); Other: `GET /v1/audio/voices` if it answers, else the field is free text. One voice for
everything, phone calls included (decision). Phone keeps resampling to 8 kHz
(`phone/turn.go:109`).

## 4. Admin

The Models page gets a **Speech** section: per kind (STT, TTS) Local / Remote / Off, a preset
picker, URL, model, key (write-only, as for LLM labels, `LabelsEditor.tsx:290-350`), voice
(TTS), fallback with its warning, and a **Test** button (STT: a bundled 2 s clip, TTS: a sample)
showing latency and which engine answered. The wizard's voice step (`VoicePicker.tsx`) offers
the same choice.

Everything stays at today's superuser tier. The aim is to reuse existing routes (label PUT,
`/api/settings`, `/api/tts/voices|sample`); a new route (e.g. an STT test) is added to the tier
expectations at `superuser` and **holds the PR for review** per CLAUDE.md. New page sections get
their `e2e/coverage.json` entries.

## 5. Testing

No cloud keys in CI.

- **Unit**: each dialect against an `httptest` server that checks the request byte for byte
  (multipart fields, headers, query) and plays canned responses, including the error classes that
  do and don't trigger fallback, timeouts, chunked bodies split mid-sample, WAV-instead-of-PCM.
- **Contract**: the existing suites run against a sandbox with STT/TTS set to remote pointing at
  a fake (`tools/fakespeech`, the fakerelay pattern) and must pass unchanged: proves the wire
  shapes hold.
- **Real OpenAI-compatible server, no cloud**: a harness lane runs speaches (STT and TTS) in
  Docker as the "Other" preset; `pipeline say --voice` round-trips through it with the existing
  `max_wer` checks.
- **Fallback**: the fake is told to fail; the turn still answers (CPU whisper / Kokoro) and the
  status counts it.
- **Real clouds**: a manual smoke script that reads keys from `~/.config/jarvis-harness/` when
  present (`scripts/speech-smoke`), run by the user or an agent before a release that touches this.

## 6. Build order

1. STT remote, openai dialect (OpenAI, Groq, Other presets), contract via the fake. **Built**
   (choices: [../decisions/2026-10-22-remote-stt-openai-dialect.md](../decisions/2026-10-22-remote-stt-openai-dialect.md));
   harness: `scripts/sandbox up --speech fake|speaches`.
2. TTS remote, openai dialect, stable 24 kHz format, voices for OpenAI and Other.
3. ElevenLabs dialect for both.
4. Fallback for both. **Built** (`llm.stt.fallback`, `tts.fallback`; choices:
   [../decisions/2026-10-22-remote-speech-fallback.md](../decisions/2026-10-22-remote-speech-fallback.md)).
5. Admin Speech section + wizard + test buttons (UI rules in `web/admin/CLAUDE.md`).

Independent of the phone SIP work ([../cc/phone-sip.md](../cc/phone-sip.md)); can run in
parallel.

## 7. Sources (checked 2026-10-21)

OpenAI: developers.openai.com/api/docs/guides/speech-to-text, …/text-to-speech,
…/reference/resources/audio/subresources/speech/methods/create. Groq:
console.groq.com/docs/speech-to-text. ElevenLabs: elevenlabs.io/docs/api-reference/speech-to-text/convert,
…/text-to-speech/stream, …/voices/search. speaches.ai/usage/*. github.com/remsky/Kokoro-FastAPI.
localai.io/docs/features/text-to-audio. Unconfirmed at writing: provider rate limits, Groq's current
TTS models. speaches' PCM is 24 kHz mono (checked 2026-10-22, [decision](../decisions/2026-10-22-remote-tts.md)).
