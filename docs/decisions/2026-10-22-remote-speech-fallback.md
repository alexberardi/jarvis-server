# Remote speech fallback: per-request backoff, a separate CPU engine, whole-sentence re-render

- **Date:** 2026-10-22 (agent, loop item #494, remote speech piece 4 of the plan in
  [../llm/07-remote-speech.md](../llm/07-remote-speech.md) §6)

Choices made building `llm.stt.fallback` and `tts.fallback` (`off` default | `cpu`) that the
design left open:

- **No flapping: decide per request, with a backoff after a failure.** Every request asks the
  remote unless it failed recently. A failure that falls back opens a window (30 s, doubling per
  consecutive failure up to 5 min) in which requests go straight to the local engine without
  asking the provider; the first request after the window probes the remote, and a success
  closes it. A request never switches path twice. If the local engine fails inside the window
  (model removed meanwhile), the remote is asked after all. One shared helper
  (`internal/voice/fallback`), one gate per module (stt, tts), counters since jarvisd started.
- **Which errors fall back** is the design's list, for both: transport error, timeout, 401/403
  (logged at error level), 429, 5xx, and a 200 the client can't read. 400/413/422 never fall back
  (stt: our bug or a bad clip; tts: the sentence is skipped as before) and open no window.
  `fallback=off`, and those errors, still record the last remote error for the status.
- **The STT CPU engine is a separate instance bound as `stt:cpu`**, not the label's own binding:
  it runs the stt label's `model` with `gpu_backend=cpu`, `gpu_layers=0` on a CPU build, so its
  instance key differs and it never replaces anything. It starts on the first fallback (a cold
  start: the harness saw ~8 s for small.en including the failed remote call) and stops after
  **10 minutes unused**, or at the next reconcile once the label is no longer remote with
  `fallback=cpu` or its model is gone. There was no idle rule for engines before; this is the
  only engine that has one, because it exists only while the provider is failing.
- **TTS re-renders the failed sentence whole** on Kokoro, then the rest of the reply. If the
  provider died mid-sentence the listener hears that sentence's start twice (remote voice, then
  Kokoro) rather than losing its end. Kokoro uses `tts.kokoro_voice`, speed and gain (a caller's
  voice only if it is a Kokoro voice). The stream's format and `X-Audio-Provider: remote` stay
  as announced: Kokoro renders 24 kHz, the remote stream's rate; a Kokoro model at another rate
  refuses to stand in rather than resample.
- **No model, no fallback, a clear reason.** `fallback=cpu` without a whisper model (stt) or
  Kokoro (tts) fails with the remote error plus `(local CPU fallback unavailable: …)`; nothing is
  downloaded. The labels API (`GET /v1/models/labels`) shows it before it matters: the stt label
  and the tts voice label gain `fallback {mode, fallbacks, last_path, last_remote_error,
  last_remote_error_at, backoff_until, warning}`, `warning` saying to install a model on the
  Models page. Piece 5 (admin Speech section) shows these; no UI here.
- **Which path served** is in each `stt: transcribed` log line (`engine=remote|local-cpu|local`),
  in `stt.Result.Engine` (not on the `/transcribe` wire, whose shape is the legacy contract), in the
  tts warn/error lines, and in `last_path`.

Builds on [2026-10-21-remote-speech-providers.md](2026-10-21-remote-speech-providers.md) and
[2026-10-22-remote-tts.md](2026-10-22-remote-tts.md) ("the fallback acts at that point").
