# Remote speech-to-text: openai only, no auto-detect, and what a failure may say

- **Date:** 2026-10-22 (build of remote speech 1/5)

Choices made building `stt.engine=remote` (design: [../llm/07-remote-speech.md](../llm/07-remote-speech.md) §2),
for whoever adds the ElevenLabs dialect, fallback and the admin's Speech section next.

- **`stt.remote_api` has no `auto`.** Its options are `openai` only (default `openai`);
  ElevenLabs adds `elevenlabs` beside it. The LLM labels' auto-detect (anthropic for
  api.anthropic.com) never applies to stt: the Anthropic API has no speech-to-text, so
  `SettingsSource.Label` sets `RemoteAPI` to openai for a whisper label whatever the URL.
- **Per-label overrides.** The stt label shares the LLM labels' `remote_*` fields but gets its
  own descriptions, options and defaults (`sttFields` in `engine/config.go`), plus
  `remote_timeout_ms` (default 8000, stt only). The timeout travels on the resolver's
  `Endpoint.TimeoutMS` and bounds one request; 0 means the HTTP client's own.
- **A failure never says the key.** A 401/403 answers only "the provider refused the API key"
  (OpenAI's message echoes part of it); any other error body is cut at 300 characters and the
  key is replaced by `***` if it appears. The key goes only in `Authorization: Bearer` and is
  not logged; settings reads mask it (`IsSecret`) and the label status blanks it.
- **No timings → one segment.** `response_format=json` (the only format the gpt-transcribe
  family accepts) has no segments, so a transcript is one segment from 0 to the clip's length;
  an empty transcript is no segments, as whisper-server gives for silence.
- **Presets live in `internal/voice/speechpresets`**, a package of its own so TTS (2/5) can add
  its fields without importing the stt module. They are not served yet: the admin's Speech
  section (5/5) does that, so this piece adds no `/api` route.
- **The speaches lane uses `Systran/faster-whisper-small.en`**, the local default's size. A
  distil model (`faster-distil-whisper-small.en`) copies a desktop node's vocabulary prompt
  ("Alacritty, Firefox, Omawrite") into the transcript ("Open, on, on, on…"): 13 of 19 voice
  scenarios failed with it, none with the full model. This is the model's behaviour, not the
  dialect's; a user on a distil model may want to note it.
