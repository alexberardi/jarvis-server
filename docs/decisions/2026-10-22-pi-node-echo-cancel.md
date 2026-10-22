# Pi node: real echo cancellation during music (17b), not N:'s pactl layer

- **Date:** 2026-10-22 (user, loop #424 triage; built by the build agent)

N:'s `core/echo_cancel.py` loads `module-echo-cancel` and moves the node's Pulse capture stream
onto `jarvis_ec_source`. Neither the Python nor the Go node captures through Pulse: both read the
HAT through ALSA dsnoop (`dsnoopmic`), and `install-pi.sh` keeps Pulse off the HAT's capture side
(PULSE_IGNORE, `jarvis_output` playback only). On a Pi, N:'s engage fails with
`source_output_not_found`, so a faithful port would do nothing.

The user chose real echo cancellation (option (a) of three; the others were the pactl layer alone,
inert on dsnoop, or closing 17b):

- **While the node's own music plays,** Pulse gets a source on the same dsnoop
  (`module-alsa-source device=dsnoopmic`, `jarvis_mic_raw`, 16 kHz mono) and the webrtc
  canceller runs between it and `jarvis_output`. The music plays into `jarvis_ec_sink` (the
  default sink while engaged, so a just-started player and the replies land there too), parked
  streams are restored there, and the voice pipeline reads `jarvis_ec_source` through
  asound.conf's `pcm.jarvis_ec` (the ALSA pulse plugin). The mic is a `device.Switch` that
  changes device only between reads.
- **When it changes:** at a restore (the turn is over) and when a deferred play starts, both
  between recordings, following the "self playing" flag the duck refreshes. Never at a duck: the
  wake has just fired and the command must be recorded on the same device. So after the music
  stops the canceller stays loaded until the next turn's restore.
- **Fail open.** A failed engage step unloads what it loaded and leaves the plain mic. A
  cancelled capture that won't open, errors or reads 3 s of pure digital silence (N:'s
  silent-capture watchdog) puts the mic back on `dsnoopmic` by itself and disengages. Either way
  there is no retry until the music stops and starts again. Stale `jarvis_ec_*` modules are
  unloaded at start.
- **The canceller's own gain controls are off** (`analog_gain_control=0
  digital_gain_control=0`): the HAT's PGA is the music profile's (17a).
- **With the HAT only;** a Pi without it, or a voice input other than `dsnoopmic` (a `wav:` file,
  a USB mic), never engages.
- **QA, not in the build:** whether the wake word fires over loud music, and the webrtc
  canceller's CPU on a Zero 2 W (now running the Go node for the #181 soak).

Left open: N:'s self-playback bypass of the adaptive silence threshold (`wake_loop.py:575`),
parked with 17b in [go-pi-node.md](../agent/go-pi-node.md) §3.16.

Supersedes the "until 17b" restore target in
[2026-10-19-pi-node-music-ducking.md](2026-10-19-pi-node-music-ducking.md).
