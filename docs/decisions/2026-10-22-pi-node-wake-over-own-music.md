# Pi node: a lower fire bar inside a tentative wake (wake over the node's own music)

- **Date:** 2026-10-22 (build agent, loop #503; reopens #425)

On jarvis-pi4 with `wake_word_threshold_auto` settled at its 0.50 ceiling, "hey Jarvis" over the
node's own music at low volume scored 0.33-0.51 through the echo canceller (0.6-0.98 with no
music). The tentative wake (17c) ducked the music at 0.413, the window peaked at 0.469 and
expired: the music dipped but the node never listened.

**Decision.** Inside an open tentative's window (the node's own music, already ducked) a frame
fires at `TentativeConfig.FireThreshold`, 0.35, or the wake threshold if that is lower
(`Pipeline.fireThreshold`). Outside a window the wake threshold is unchanged. The frame that
opens the tentative does not fire below the threshold on its own: it is scored before the
window opens, so a lone lyric spike only ducks and restores, as before. The fire still goes
through the wake gate (debounce, the not_for_me cool-down and its override) and sends the
longer tentative clip, so command-center's wake verification on `speaker_audio` remains the
backstop against a false wake from the music. The tentative log now carries `fire_threshold`.

0.35 sits under the whole observed band (0.327 was the lowest, from before the duck) and well
above the tentative band's 0.20 floor, so a window needs a second, stronger frame after the duck.

**Not changed (yet).**
- The window (1.6 s) and the cool-down (10 s): the observed miss had its peak inside the window,
  so the lower bar alone covers it. Revisit if the Pi re-test shows the "-vis" landing after it.
- The echo canceller's webrtc noise suppression (`module-echo-cancel` loads with
  `analog_gain_control=0 digital_gain_control=0`; `noise_suppression` and `high_pass_filter`
  default on and may cost wake score). Changing it needs a measurement on the Pi: score the same
  phrase on the raw `jarvis_mic_raw` and the cancelled `jarvis_ec` capture. Follow-up.
- A recorded fixture of the wake word over the node's music through the canceller needs a
  capture on the Pi (`arecord -D jarvis_ec` while music plays); none exists in the repo yet.
  The offline stand-in, `TestWakeOverLowMusicClearsTheTentativeFireBar` (internal/voice/oww),
  mixes `hey_jarvis_emma.wav` with synthetic music from 10 dB under the speech to as loud as it:
  it peaks at 0.998 (music alone 0.000). Synthetic music masks the phrase far less than the
  Pi's capture did, so it checks the model side only, not the canceller.

Builds on [17c](../agent/go-pi-node.md) (§3.16) and
[the echo-cancel decision](2026-10-22-pi-node-echo-cancel.md).
