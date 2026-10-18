# The Go Pi node's settings verbs: what applies live, what is skipped

- **Date:** 2026-10-17 (build agent, loop #171; for review)

Choices made porting `update_node_config`, `preview_led_pattern` and `measure_ambient_noise` to
the Go Pi node ([agent/go-pi-node.md §3.14](../agent/go-pi-node.md)):

- **Persisted in agent.json, not a config.json.** The Go agent's settings file is agent.json;
  each Python key the Go node supports maps to an agent.json key that is also a `run` flag
  (new: `follow_up`, `follow_up_silence`, `leds`, `led_brightness`). Writers are serialized in
  the process (`settings.Update`); the K2 `node_config` push doesn't write agent.json.
- **Trust-anchor filtering ported exactly**, including the update/egress consent keys, before
  anything is applied or saved.
- **Live apply to the voice pipeline between chunks, never mid-turn** (`voice.Live.Tune`), and
  replayed onto a pipeline that starts later (the mic reopened, voice switched back on).
- **`volume_percent` and `is_muted` are not applied or saved.** The Go node has no Pi volume
  control (ALSA/Pulse) yet; saving a value that does nothing would claim support it lacks. They
  are logged. Revisit with a mixer setter.
- **No restart.** Python restarts only for `wake_word_model`; the Go node has one wake word and
  ignores that key, and everything it takes applies live.
- **A measurement waits for a turn in progress** (up to 30 s past the window), then reports
  `capture_failed: the voice pipeline stayed busy`, rather than failing at once: the app polls
  for 5 minutes, and a turn usually ends within seconds.
