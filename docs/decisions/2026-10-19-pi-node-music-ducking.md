# Pi node: music ducking and takeover (parity 5.11, the first slice of loop #180)

- **Date:** 2026-10-19 (build agent, loop #180; the split follows the item's triage)

Loop #180 asked for the last voice-parity pieces of the Go Pi node in one item: music
ducking/takeover, echo cancellation, tentative wake with `pre_wake_speech_seconds`, wake
auto-calibration with the false-wake cool-down, the wake greeting prefetch, the maintenance
restart and the ACT-LED fallback. Triage measured that as several days of work and split it into
five pieces, each its own PR, in this order:

1. **17a: music takeover and ducking** (this decision; [go-pi-node.md §3.16](../agent/go-pi-node.md)).
2. **17b: echo cancellation during music** (`N:core/echo_cancel.py`), needs 17a. It has to decide
   the capture path first: the Go node captures through dsnoop, not Pulse (go-pi-node.md §1.4).
3. **17c: tentative wake and `pre_wake_speech_seconds`** (`N:core/wake_loop.py:129-261`), needs 17a,
   and overlaps loop #229's listen pre-roll in `voice/pipeline.go`. **Built 2026-10-21** (loop #425,
   [decision](2026-10-21-pi-node-tentative-wake.md)).
4. **17d: wake auto-calibration and the false-wake / not_for_me cool-down**
   (`N:core/wake_calibration.py`, `N:core/false_wake.py`). Built 2026-10-21 (loop #426,
   [decision](2026-10-21-pi-node-wake-calibration-and-cooldown.md); `false_wake.py` not ported).
5. **17e: wake greeting prefetch, maintenance restart, ACT-LED fallback.**

Choices made in 17a:

- **No `module-alsa-card` reload before the deferred play.** N: reloads the card before a media
  command's `on_response_complete` because the TLV320 can't resume a suspended Pulse sink. The Go
  install (`install-pi.sh`, §7 4.3b) drives the HAT with `module-alsa-sink` (`jarvis_output`) and
  unloads `module-suspend-on-idle`, so the sink never suspends, and there is no `module-alsa-card`
  for the HAT to reload (N:'s code would reload the first one it finds, which isn't the HAT's).
- **`MusicAssistantService` is not ported.** In N: nothing calls it: `scripts/main.py` passes
  `DummyMusicAssistantService` and the MQTT listener never uses its argument. (A separate client,
  `music_assistant_shared/`, sits beside it in jarvis-node-setup, and nothing in N: imports it either.)
- **The tunables are N:'s defaults, fixed:** `music_duck_percent` 20, `music_pga_percent` 49,
  `capture_pga_normal_percent` 49 (`music.Default*`). In N: they are `config.json` keys that
  neither the app nor the settings snapshot reach; a Go node gets a setting when someone needs one.
- **The takeover hook runs on the Go side, before the sidecar call.** The sidecar's `describe`
  reports each command's `takes_over_playback_binary` property, and `sdkhost.Host.BeforeCall`
  resolves it (or N:'s legacy map: audacy → mpd, spotify → go-librespot, music → shairport-sync,
  pandora → mpv) and stops the other players when the call's `action` is `play`. N: does this in
  `command_execution_service.py`; the Go agent owns the speaker, so the Python child doesn't.
- **Until 17b, a parked stream goes back to `@DEFAULT_SINK@`.** N: restores to `jarvis_ec_sink`
  while echo cancellation is on; that comes with echo cancellation.
