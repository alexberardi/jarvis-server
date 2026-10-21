# Remote speech-to-text and text-to-speech providers

- **Date:** 2026-10-21 (user)

STT and TTS get what LLM labels already have: an engine setting `local` (default, today's
behaviour), `remote` or `off`, so installs on limited hardware can hand speech to a hosted or
self-hosted service, exactly as remote LLM labels do. Not a privacy concern beyond that: it is
opt-in, superuser-only, and the setting says what leaves the house.

- **Providers:** presets for the usual suspects with their URLs filled in (OpenAI, ElevenLabs,
  and the like; the list is in the design doc), plus **Other**: any OpenAI-compatible endpoint
  (`/v1/audio/transcriptions`, `/v1/audio/speech`), which also covers self-hosted servers on
  another box. Vendor-specific APIs are dialects behind the same setting, as Anthropic is for
  LLM labels.
- **Speaker recognition stays local** whatever the STT provider is; only transcription moves.
- **Fallback:** when the remote provider fails, fall back to the local engine **on CPU**. It is a
  setting, off by default: turning it on uses a local model the user installs from the Models
  page (a Whisper model for STT, Kokoro for TTS) and nothing is downloaded behind their back.
  On with no model installed is shown as a warning in settings, not a silent failure.
- **Voice:** a remote voice setting per provider (picked from the provider's list where it has
  one). **One voice for everything**, phone calls included; a separate phone voice can come later.
- **Known drawbacks, accepted:** added latency on every voice turn and phone turn.

Supersedes nothing. Builds on
[2026-10-12-remote-labels-can-speak-the-anthropic-messages-api.md](2026-10-12-remote-labels-can-speak-the-anthropic-messages-api.md).

Design: [../llm/07-remote-speech.md](../llm/07-remote-speech.md).
