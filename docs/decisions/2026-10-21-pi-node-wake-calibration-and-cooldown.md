# Pi node: wake auto-calibration and the not_for_me cool-down (17d, loop #426)

- **Date:** 2026-10-21 (build agent, loop #426; slice 17d of
  [the #180 split](2026-10-19-pi-node-music-ducking.md))

The Go agent's wake gate now matches N:'s (`N:core/wake_detector.py` `decide_wake_fire`,
`N:core/voice_filters.py`, `N:core/wake_calibration.py`); see
[go-pi-node.md §3.20](../agent/go-pi-node.md). Choices made:

- **Auto-calibration is opt-in, as in N:.** `wake_word_threshold_auto` (the app's key; agent.json
  `wake_threshold_auto`, flag `--wake-threshold-auto`) defaults to off. The history is
  `<cache>/jarvis-agent/wake_scores.json`, N:'s format (a JSON list of the last 20 floats), so it
  survives a restart. A sample is a wake whose command (the first one, before any follow-up)
  ended spoken, silent or interrupted, with no error and not over the node's own music; a
  `not_for_me` command, errors and a wake that heard no words are not (N: records only a wake
  whose transcription returned a result). Recording and the cool-down are independent: a spoken
  command whose follow-up answer ends `not_for_me` is a sample and arms the cool-down, as in N:
  (`N:core/wake_loop.py:829-834` records before the follow-up loop,
  `N:core/follow_up_loop.py:353-358` arms).
- **The cool-down's tunables are N:'s defaults, fixed:** `not_for_me_quiet_seconds` 20,
  `not_for_me_escalated_quiet_seconds` 60, `not_for_me_escalation_window_seconds` 120,
  `not_for_me_override_threshold` 0.6. The app does not send them today (jarvis-node-mobile has
  `not_for_me_quiet_seconds` in its snapshot type only, with no control), so they are
  `voice.DefaultNotForMe()` rather than node settings, as 17a did for its own.
- **It is armed by the turn's result**, once the turn is over: the woken command's `not_for_me` and
  a follow-up answer's (`EndNotForMe`), not a follow-up answer the gate ignored with
  `keep_listening`. The gate (debounce and cool-down) lives in the pipeline, so a pipeline restart
  (a microphone error) clears it; the calibration history is shared across restarts.
- **`N:core/false_wake.py` is not ported.** It runs on the node's own transcript before CC sees
  it (`N:core/wake_transcription.py:288`); the Go node streams the audio to
  `/voice/command/stream` and has no transcript before CC. CC's `not_for_me` and the text filter
  cover that path, and in N: a node-side false wake neither records a score nor arms the
  cool-down, so nothing here depends on it.
