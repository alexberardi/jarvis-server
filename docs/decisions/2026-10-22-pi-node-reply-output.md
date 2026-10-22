# Pi node: replies stay on the node's speaker while the music is on Bluetooth

- **Date:** 2026-10-22 (build agent, loop #486, from the user's report on jarvis-pi4: "the regular
  jarvis responses are piping through the bluetooth device"; for review)

Changes the "replies, chimes and music then play on the device" point of
[2026-10-21-pi-node-bluetooth-output-and-progress.md](2026-10-21-pi-node-bluetooth-output-and-progress.md).
The Bluetooth route itself is unchanged: a paired speaker or headphones become Pulse's default
sink, so music players (which follow the default) play there.

- **A node setting, `voice_reply_output`:** `node` (this node's speaker, the default) or
  `bluetooth` (the Bluetooth device, the old behaviour). `update_node_config` takes it, the
  settings snapshot's `node_config` reports it, agent.json keeps it as `voice_replies`
  (`--voice-replies`). It applies from the next reply: the voice pipeline asks before each reply
  and chime which device to open (`voice.Options.ReplyOutput`).
- **How the replies are pinned:** `install-pi.sh` adds `pcm.jarvis_reply { type pulse; device
  jarvis_output }` to asound.conf. A stream opened on it names its sink, so Pulse neither starts it
  on the default sink nor moves it when the default changes. Chosen over the pulse plugin's
  `pulse:DEVICE=…` argument, which was not checked on the Pi's libasound2-plugins. A Pi whose
  asound.conf predates this (install-pi.sh not run again) can't open it: the reply plays on
  `output` (the default sink, as before) with a warning in the log.
- **Only while the music is on Bluetooth.** With `node`, the agent opens `jarvis_reply` only when
  Pulse's default sink is a bluez sink, or is `jarvis_ec_sink` with a bluez master (the echo
  canceller engaged over the headphones). Otherwise it keeps `output`. With no Bluetooth that is
  the same speaker, and while the node's music plays on the HAT the reply still goes into
  `jarvis_ec_sink`, so the canceller keeps subtracting it from the mic (barge-in and follow-ups
  unchanged). A Pi without the HAT (no `jarvis_output`), a `wav:` output and a desktop are
  unchanged. Asking costs one `pactl get-default-sink` (1 s timeout; an error keeps `output`)
  per reply or chime.
- **Echo cancel:** unchanged. Engaged over the headphones, its `sink_master` is the bluez sink and
  it moves only the players' streams onto `jarvis_ec_sink`, never the agent's reply stream. The
  replies on the HAT are therefore not in the canceller's reference: the mic hears them raw, as it
  does with no music today. Since the music is in the headphones, there is no music echo to cancel
  in the room either.
- **The app** shows the setting in jarvis-node-mobile's node settings, next to Bluetooth (a picker
  "Voice replies on: This node's speaker / The Bluetooth device" writing `voice_reply_output`
  through `update_node_config`). That is a separate change in that repo.
