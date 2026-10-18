# The E2E voice lane on a real Pi

- **Date:** 2026-10-18 (user, in loop item #156: the Pi 4 lab node with its Seeed 2-mic HAT,
  the node build for linux-arm64 paired to the e2e sandbox and never prod, the utterance played
  out of the Pi's own speaker, the same cases and asserts as the virtual lane, the Pi left as
  found, overnight only). Design by agent, loop item #156, E2E piece 14. Extends
  [`2026-10-17-e2e-voice-lane.md`](2026-10-17-e2e-voice-lane.md), which named a Pi as the
  real-hardware fallback; how to run it: [`../dev/e2e.md`](../dev/e2e.md#on-a-real-pi---audio-pi-overnight-only).

`scripts/e2e voice --audio pi --pi-host <user>@<pi4-node>` is a third way into the same
runner (`tools/voicelane`), next to WAV files and the private PipeWire.

- **The node, on the Pi.** The release's target build (`GOOS=linux GOARCH=arm64
  CGO_ENABLED=0`, onnxruntime embedded) is copied with the sandbox's seeded node identity into
  a fresh `mktemp -d` directory on the Pi and runs there with `HOME` and XDG dirs inside it, so
  the Pi's own node, its identity and settings are never read or written. One long `listen
  --profile pi` per run, as with PipeWire, so barge-in runs too.
- **Reaching the sandbox: ssh reverse forwards.** The sandbox listens on the dev box's
  127.0.0.1 only and stays that way. The ssh session that runs the node forwards the sandbox's
  node-facing ports (config, auth, logs, cc, whisper, tts, notifications, MQTT) to the same
  ports on the Pi's loopback (`-R 127.0.0.1:P:127.0.0.1:P`, `ExitOnForwardFailure`), so the
  node's config URL and the service URLs discovery returns (`127.0.0.1:P`) are the sandbox
  as they are on the dev box. voicelane refuses a sandbox whose services aren't on 127.0.0.1:
  the node can't be pointed at prod or any other server. Nothing on the Pi's network can reach
  the sandbox, and nothing is rebound.
- **Un-pairing.** The node uses the sandbox's seeded identity; it lives only in the run's
  directory on the Pi, which is deleted when the run ends, and the sandbox (with the node in
  it) is purged afterwards unless `--keep`.
- **Leaving the Pi as found.** `tools/voicelane/pi.sh` runs on the Pi under the ssh session. It
  records the active `jarvis*`, `pulseaudio*`, `pipewire*` and `wireplumber*` services (system
  and `--user`), stops every Jarvis node (it would hear the cases and answer through its own
  server, prod's) and stops the audio servers only when the HAT's card is busy (sockets first,
  so nothing restarts them). It refuses to run when the card is still busy, naming the holder,
  rather than kill a process no service owns. When the run ends it stops the test node, starts
  exactly what it stopped, in reverse, and deletes the run's directory. It does that when its
  ssh stdin closes, so the Pi is restored however voicelane ends: done, failed, interrupted or
  killed, or the connection lost. Both states go in the run's log and `results.json`.
- **The utterance stays outside any echo canceller's reference.** The node has no echo
  cancellation today (loop item #180 is to add it). The test's audio goes out of the Pi's
  speaker with `aplay` into the card's dmix, a stream of its own beside the node's, never
  through the node's output (`e2e_node_out`). So the rule for #180: **the canceller's reference
  must be the node's own playback buffer (the samples it writes), never a loopback or monitor
  of the card**, nor a PulseAudio/PipeWire echo-cancel source whose reference is the sink's
  monitor (that would include every stream on it, the test's too). With a card-level reference
  the canceller would subtract the test's own wake word and command, and every case that
  expects a turn would fail with "no turn". If #180 ever needs a hardware reference, this lane
  has to play the utterance from a second speaker instead (another device in the room).
- **Recording the reply: a tee, not the mic.** The node's speaker is an ALSA chain in an
  `.asoundrc` in the run's `HOME` (the Pi's `/etc/asound.conf` is not touched): a plug to
  16 kHz mono, a `file` plugin that appends what the node plays to the run's directory, then
  the card's dmix. Per case the new bytes are fetched and whisper on the dev box transcribes
  them. The mic would carry the room and the test's own utterance; the tee holds only the node,
  so a silent case is judged on what the node played, not on room noise. The node's mic is a
  dsnoop of the HAT, so the room (and the test's speech through the air) is what it hears.
- **Response time.** The tee has no clock, so it is the turn's time less its recording and its
  reply's playback (speech-to-text, the model and TTS), the WAV mode's measure.
- **Recorded voices.** `--voices DIR` says a line with `DIR/<slug of the text>.wav` where there
  is one (any rate, resampled), in every mode; other lines stay the TTS.
- **Overnight only.** It plays speech out loud for the length of the suite and holds the lab
  Pi, so it runs on a schedule when nobody is in the room (loop item #152 schedules the E2E
  runs), or by hand. `scripts/e2e all` never includes it.

Not verified when this was written: a run on the Pi itself. The loop's build sandbox has no
ssh access to it, so the script was checked against a fake Pi (`tools/voicelane/pi_test.go`:
the real `pi.sh` with fake `ssh`, `systemctl`, `sudo`, `aplay` and `arecord`). The first real
run settles the card detection, the ALSA chain and the room's levels.
