# Phone calls: barge-in

- **Date:** 2026-10-21 (build agent, loop #445; "barge-in ships before inbound": user, [phone-sip.md §7](../cc/phone-sip.md))

The far end of a phone call can now interrupt Jarvis. This is provider-neutral and lives in the call
loop (`internal/modules/cc/phone/media.go`), so Twilio and the SIP Call get the same behaviour.

- **"Jarvis is talking"** means `Call.Played()` is behind the end of the last `Play`. It is not
  "a `Play` call is in progress", because `Play` only queues and returns at once. That was why the
  old half-duplex suppress (`m.speaking`) never took effect.
- **Echo guard.** While our audio plays, the VAD starts an utterance only after 10 consecutive
  20 ms frames (200 ms) above 1000 RMS (`VADConfig.BargeInThresholdRMS`, `BargeInFrames`).
  Quieter audio is treated as our own echo or TTS bleed and dropped, and so is a shorter click.
  This replaces the normal onset of 250 RMS for 3 frames. Telephone speech measured
  ~2650–3100 RMS in the P0 spike. The numbers are a first guess: no echo has been measured yet,
  so tune them on the L1 Asterisk lane (`*43` echo) once it exists. Once an utterance has started,
  it endpoints as usual.
- **On a barge-in** the loop calls `Call.Flush()` (Twilio's `clear`) and treats playback as over.
  The utterance keeps its preroll, so its first words are kept, and it runs as the next turn. The
  model sees `live.InterruptedNote` before what the caller said, so it does not assume the rest of
  its reply was heard. The turn record's events start with `barge_in`.
- **The disclosure is never flushed.** It is a hard gate (legally it must play in full). Speech
  over it is still captured at the barge-in thresholds and answered after the disclosure, with
  no flush and no note.
- **The goodbye is never cut off.** Once a turn hangs up, the loop only waits for playback
  (`awaitPlayback`) and runs no VAD, as before.
- **"Stop the sentence stream" is the flush.** `generateReply` synthesizes the whole reply before
  its single `Play`, so no synthesis is running while the reply plays. If replies are ever played
  sentence by sentence, a barge-in must also cancel the sentences still to come.
- **Speech during the turn's thinking.** The transport holds back audio while a turn runs
  (`Call.Frames`), so a caller who kept talking reaches the VAD only once the reply is queued.
  If what they said clears the barge-in thresholds, the reply is flushed and their words get the
  turn. That is the right answer to someone still talking.
