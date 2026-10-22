# Pi node Bluetooth: a paired speaker takes the output; progress for the app

- **Date:** 2026-10-21 (build agent, loop #433, from the user's report on jarvis-pi4; for review)

Supersedes the "audio routing stays N:'s" point of
[2026-10-18-pi-node-bluetooth.md](2026-10-18-pi-node-bluetooth.md), which left where TTS goes to
the user. The user's #433 report asked to "wait for the bluez_sink to appear in Pulse (by MAC),
then route output to it", so:

- **A speaker or headphones paired with role `source` become Pulse's default sink** once their
  `bluez_sink.<MAC>` is there (after a pair and after the reconnect loop connects them). Since
  `asound.conf`'s `output` (the agent's `--voice-output`) is Pulse's default sink, the node's
  replies, chimes and music then play on the device. The sink it replaced (normally
  `jarvis_output`, the HAT) is the default again on disconnect, release and forget; when the
  device just goes away, Pulse falls back on its own. A phone paired with role `sink` is
  unchanged (its loopback into the default sink). The volume commands already set every bluez
  sink too (`internal/agent/sdkhost/audio.go`). If the user wants replies to stay on the HAT
  with a speaker paired, this is the one place to change (`routeOutput` in
  `internal/agent/bluetooth/pulse.go`).
- **BlueZ's media transport counts as the audio profile being up.** The post-connect check used to
  poll `pactl` only, and dropped its errors, so a Pulse it couldn't reach (or one slow to make the
  sink) looked like a profile that never came up, and the device was disconnected although
  bluetoothd had the A2DP stream ready. Now a `MediaTransport1` under the device makes the
  connect succeed with a warning in the log, `pactl`'s error is logged, the wait is 20 s (N: 15 s),
  and a speaker's card that is there without a sink is set to `a2dp_sink` once. A device with
  neither is still disconnected (the AVDTP "click loop").
- **A Page Timeout that comes back within 4 s is retried once, 2 s later.** Right after an
  inquiry the first page often fails fast although the device is in pairing mode; a device out of
  range takes the whole page timeout and gets #294's message straight away.
- **Forget is idempotent and the status follows it.** `GET …/bluetooth/status` stays built from
  the latest completed scan (no live query to the node), but jarvisd now patches that scan when a
  pair succeeds (paired, connected), on disconnect and release (not connected) and on forget
  (dropped), so the app stops listing a forgotten device. On the node a remove of a device BlueZ
  no longer knows is success.
- **Progress goes to two new node routes**, `POST …/bluetooth-scan/{rid}/progress`
  `{"progress": "scanning", "devices": [...]}` and `POST …/bluetooth/pair/{rid}/progress`
  `{"progress": "finding" | "pairing" | "connecting" | "audio"}`, rather than partial posts to the
  results routes: an older jarvisd would have taken a partial scan as the result and completed
  the request early, while a new route just 404s there (the node ignores it). The app's polls
  return `progress`, and a pending scan's `devices` so far. Showing it is jarvis-node-mobile's
  part (a separate item). Scan and pair timings are unchanged (10 s BR/EDR + 10 s LE).
