# The Pi applies the app's endpointing settings and volume

- **Date:** 2026-10-19 (loop #295; agent, from the item's option (a))

Supersedes the "`volume_percent` and `is_muted` are not applied or saved" point of
[2026-10-17-pi-node-settings-verbs.md](2026-10-17-pi-node-settings-verbs.md).

- **Implement every key the app's Voice Settings sends; don't hide them in the app.** The Go
  pipeline already has the Python node's equivalents, so the item's option (b) (a
  `supported_keys` list in the snapshot, the app hiding the rest) isn't needed for these keys:
  `silence_threshold` → `Endpointing.SilenceThreshold` (truncated, as Python's `int()`),
  `silence_duration` → `Endpointing.SilenceSecs`, `follow_up_min_record_after_onset_secs` →
  `FollowUp.MinAfterOnsetSecs`, `follow_up_min_speech_secs` → `FollowUp.MinSpeechSecs`. They
  apply live (`voice.Live.Tune`) and are saved to agent.json (`silence_threshold`,
  `listen_silence`, `follow_up_min_after_onset`, `follow_up_min_speech`, each a `run` flag).
  `silence_threshold` stays the static fallback: the adaptive threshold from the pre-wake level
  wins when there is one, in both nodes.
- **Volume goes through `sdkhost.Audio`** (the built-in commands' mixer, #274), which persists
  the level and mute in `volume.json` and applies them again at start. It is not also written to
  agent.json: one file for the level, the same one "volume up" counts from. The snapshot reports
  `volume_percent` (persisted, else the mixer's) and `is_muted`; an unreadable volume is left out.
- Other Python-only keys the app may show (`min_record_seconds`, `max_record_seconds`, live
  `barge_in_enabled`, maintenance restart) are still ignored; a `supported_keys` follow-up in
  the app would cover them.
