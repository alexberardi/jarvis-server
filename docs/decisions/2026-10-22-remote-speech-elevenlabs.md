# Remote speech: the ElevenLabs dialect's URL, voices, timings and privacy flag

- **Date:** 2026-10-22 (agent, loop item #493, remote speech piece 3 of the plan in
  [../llm/07-remote-speech.md](../llm/07-remote-speech.md) §6)

Choices made building `remote_api=elevenlabs` for the stt label and for `tts.remote_api`
(`internal/modules/stt/remote_elevenlabs.go`, `internal/modules/tts/remote.go`):

- **The URL is the API's root, `https://api.elevenlabs.io`**, and the dialect's paths carry
  their own version (`/v1/speech-to-text`, `/v1/text-to-speech/{voice}/stream`, `/v2/voices`).
  The OpenAI presets end at `/v1` because every OpenAI path lives under it; ElevenLabs' voices
  are under `/v2`, so a `/v1` base would need a special case anyway. A pasted trailing `/v1` or
  `/v2` is dropped (`ElevenLabsBase`), so either form works.
- **The voice setting stores the voice id**, the path needs it. `/api/tts/voices` keeps
  `voices` as the ids (what `tts.remote_voice` takes) and adds `names` (id → display name),
  which the admin's Speech section (piece 5) shows instead of the id. Every page of `/v2/voices`
  is read (`page_size=100`, following `next_page_token`, at most 20 pages). The ElevenLabs
  preset has **no default voice** (its premade voices change); with none set, `Speak` is
  "not installed" (503 on `/speak`, 409 on the sample) rather than a request to an empty path.
- **Timings: one segment per sentence.** Scribe's `words` (seconds) are grouped into segments
  that end at a word ending in `.`, `?` or `!`, about whisper's segment size; `spacing` entries
  join words and `audio_event` ones ("(laughs)") are left out of the segments (the transcript
  `text` is Scribe's as given). No words gives one segment for the clip, as the openai dialect
  does; empty text gives none.
- **The clip goes as raw PCM** (`file_format=pcm_s16le_16`, the 16 kHz samples, no WAV
  header): ElevenLabs documents lower latency for it than for an encoded file.
- **Dropped:** the vocabulary prompt (Scribe has no free-text prompt field), temperature and
  beam size for STT; speed for TTS (ElevenLabs' `voice_settings.speed` is limited to 0.7-1.2,
  and a remote voice is spoken at speed 1 anyway, [2026-10-22-remote-tts.md](2026-10-22-remote-tts.md)).
- **`enable_logging=false` on every speech request**, as the design says (zero retention).
  ElevenLabs documents zero-retention mode for enterprise plans; whether other plans refuse the
  flag or ignore it is not checked (no real key in tests). If they refuse it, the fix is a
  setting, not dropping the flag silently.
- **Errors follow the openai dialect's rules**: the key goes only in `xi-api-key`, a 401/403
  says only that the key was refused, and the key is replaced by `***` in any error body (TTS
  now scrubs it for both dialects). A TTS 400/413/422 skips the sentence; anything else ends
  the reply.
- **Harness:** `scripts/sandbox up --speech fake --speech-api elevenlabs` points STT and TTS at
  `tools/fakespeech`'s ElevenLabs routes (voice `fakeVoiceAlto01`). Never the real API.
