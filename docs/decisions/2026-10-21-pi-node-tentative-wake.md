# Pi node: tentative wake and pre_wake_speech_seconds (17c)

- **Date:** 2026-10-21 (build agent, loop #425; slice 17c of
  [the music-ducking split](2026-10-19-pi-node-music-ducking.md))

What is built is in [go-pi-node.md §3.16](../agent/go-pi-node.md) (17c). The choices made in it:

- **`self_playback` is sent with `pre_wake_speech_seconds`.** N: sends both (plus
  `self_playback_kind: "music"`), and jarvisd already reads them: without the flag, a wake over
  the node's own music would get the "room was quiet" direction hint from a window that is the
  music's bleed. The flag is the music's cached `SelfPlaying` (refreshed at every duck and set by a
  deferred play), snapshotted once at the fire, as N: does.
- **The tunables are N:'s defaults, fixed** (`Config.Tentative`, `Config.PreWakeVAD`), like 17a's:
  band 0.20, window 1.6 s, cool-down 10 s; pre-wake speech threshold clamp(median × 3, 300, 2500),
  static 2500. Nothing tunes them yet.
- **The band is [0.20, the wake threshold], inclusive.** The Go pipeline fires on
  `score > threshold` where N: fires on `>=`, so a score exactly at the threshold is a tentative
  here (N:'s band is `[0.20, threshold)`).
- **Only a fire that completes a tentative sends the longer clip.** N: grows the wake clip by the
  window whenever the band is armed and sends whatever the ring holds. Here the ring grows the
  same way, but a fire with no tentative open still sends the usual 2 s: command-center's wake
  verification transcribes the clip's leading 2.2 s, so a longer clip only where it buys the
  pre-duck "hey jar-" keeps the common case as it was.
- **A take or a room measure closes an open tentative** before listening pauses (N:'s
  alert-drain break does the same), and so does the pipeline stopping, so the music is never left
  ducked by a tentative.
