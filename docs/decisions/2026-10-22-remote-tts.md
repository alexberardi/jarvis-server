# Remote TTS: no Kokoro gain or speed, provider "remote", idle timeout, WAV resampled

- **Date:** 2026-10-22 (agent, loop item #492, remote speech piece 2 of the plan in
  [../llm/07-remote-speech.md](../llm/07-remote-speech.md) §6)

Choices made building `tts.provider=remote` (openai dialect, `internal/modules/tts/remote.go`)
that the design left open or that change it:

- **`tts.kokoro_gain` does not apply to a remote voice** (the design said it applies to both;
  this supersedes that line of §3). Its default of 2.0 compensates for Kokoro's quiet output
  (peaks ~0.3-0.5); OpenAI's and speaches' voices come normalised near full scale, so 2.0 would
  clip every loud syllable. Remote audio is passed through at gain 1. `tts.kokoro_speed` (1.25,
  tuned for Kokoro) doesn't apply either: the request says `speed: 1`. A caller's explicit
  `SpeakOptions` gain or speed still applies. If a remote voice needs a level or pace setting
  later, it gets its own key (`tts.remote_gain`), not Kokoro's.
- **The provider string is `remote`** (`/audio/format`'s `provider`, `X-Audio-Provider`), the
  `tts.provider` value, whatever the dialect: callers only compare the rate and headers, and the
  string stays stable when ElevenLabs is added.
- **`tts.remote_timeout_ms` is an idle timeout, not a total one**: it bounds the wait for the
  answer and each silence in the body after it. A long sentence may stream for longer than 8 s
  from a provider that sends at about real time; the clock also stops while the caller hasn't
  taken the last chunk (a slow reader is not a slow provider).
- **Which errors end the reply**: a 400/413/422 is about that sentence's text, so it is skipped
  and the next sentence is still asked, as Kokoro skips a sentence it can't render. Anything else
  (transport error, timeout, 401/403, 429, 5xx) ends the stream with the error: the next sentence
  would only wait for the same answer. The fallback (piece 4) acts at that point.
- **Requests are sequential, read as they arrive**: sentence N+1 is requested when N's body
  ends, not while it plays. First audio is the first sentence's first chunk; providers send
  faster than real time, so the gap between sentences is one round trip at most.
- **WAV answers are resampled in-house**: a body starting `RIFF` is parsed as it streams (its
  data size is ignored: streaming servers write 0xFFFFFFFF), downmixed and resampled to 24 kHz by
  `audio.Resampler` (the Decimator's filter for an integer step down, linear interpolation
  otherwise; enough for speech). 16-bit PCM only; anything else is an error naming the format.
  Raw `pcm` is taken as 24 kHz mono, as OpenAI documents. **speaches** (checked 2026-10-22,
  `ghcr.io/speaches-ai/speaches:latest-cpu` with `speaches-ai/Kokoro-82M-v1.0-ONNX`): its `pcm`
  is 24 kHz mono too (same byte count as its WAV's data at 24000 Hz), and it serves
  `GET /v1/audio/voices` as `{"voices": [{"id", "name", …}]}`.
- **No defaults point anywhere**: `tts.remote_url`, `tts.remote_model` and `tts.remote_voice`
  default to empty (the admin's presets fill them, piece 5). `tts.provider=remote` with no URL is
  `ErrNotInstalled` (503 on `/speak`, 409 on the admin sample), never a request to a guessed
  host.
- **Voices**: `/api/tts/voices` lists the active provider's voices (`provider`, `voices`,
  `current`, `default`, `setting`, `free_text`): Kokoro's; OpenAI's fixed list when the URL's
  host is `api.openai.com` (default `marin`); otherwise `GET {url}/audio/voices` (a list of names,
  or of objects with `voice_id`/`id`/`name`, bare or under `voices`/`data`), and free text when
  that fails. `setting` says where a choice goes (`tts/tts.remote_voice` for remote).
