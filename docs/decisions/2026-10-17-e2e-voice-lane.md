# The E2E voice lane: a virtual node on the dev box

- **Date:** 2026-10-17 (user: a virtual node over a desk node; the real model in the voice
  lane; the regressions-only rule for model-dependent cases), 2026-10-18 (user: the build
  sandbox's runs use seeded caches and the `wav:` mode; PipeWire and the real model are checked
  outside it). Design by agent, loop item #151, E2E piece 11/12.

`scripts/e2e voice` checks the voice path end to end with the real node software. How to run
it: [`../dev/e2e.md`](../dev/e2e.md#the-voice-lane-scripts-e2e-voice).

- **The real node, virtual audio.** The node is `jarvis-agent listen --profile pi`: the code a
  Pi runs (`internal/agent/voice`: openWakeWord, endpointing, cc's voice turn, TTS playback),
  built for the dev box. The Pi profile offers no desktop tools, so a case can't change the dev
  box (volume, windows, typing). It runs with its own `HOME` and XDG dirs under the run, and
  the sandbox's seeded node identity, so the user's own agent is never touched. A desk node was
  the alternative; a Pi on the network is the fallback for real hardware, loop item #156.
- **Two ways in.** `--audio wav` (default) gives the node a WAV file as its mic and another as
  its speaker (`--voice-input wav:IN --voice-output wav:OUT --once`): no audio stack, so it runs
  anywhere, including the loop's build sandbox, which forbids the unix sockets PipeWire needs.
  `--audio pipewire` starts a private PipeWire (its own runtime dir under the run, never the
  user's session; the runner refuses to start without `JARVIS_E2E_AUDIO=private`) with a virtual
  speaker (null sink, its monitor recorded) and a virtual mic (a virtual source the case is
  played into), and runs one long `listen` on ALSA's `pipewire` PCM. Only that mode plays in real
  time, so only it can check barge-in.
- **The case's audio is the sandbox's own TTS.** Kokoro says "Hey Jarvis." and the command
  (the wake word scored 0.92 to 0.99 on it), the sandbox's whisper transcribes the recorded
  reply, and noise is generated (pink noise, a chord loop on a beat), so no recording's licence
  is involved.
- **What a case asserts.** The turn's outcome, the transcribed reply (a regexp), a tool the
  model called (the run's new lines in `llm.jsonl`) or the node ran, and the response time:
  with WAV files, the turn's time less its recording (the reply is written at once); with
  PipeWire, the end of the audio to the reply's first sound. A silent case (no wake word, noise,
  talk over music) asserts that nothing happened: no turn, no reply audio, no model call.
- **The model.** `--llm shared` (default) is the real model through the tap, since this lane
  tests the assistant. Model-dependent cases run 3 times and follow the regressions-only rule:
  a case is a regression when it passed 3/3 on main and 0/3 on the change; anything in between
  is the model, not the change. `--llm scripted` (`e2e/llm/voice.json`) checks the lane itself.
- **What the cases can check today.** The Go Pi profile has the clock tool and the sidecar
  spike, nothing else, and cc offers `control_device` only to nodes that register it. So the
  timer and lights cases assert the spoken reply only; the clock question asserts its tool. Add
  the tool to those cases when the Pi node has timers or devices.
- **Seeded caches for a box without GitHub's release CDN.** The whisper-server build jarvisd
  would fetch (`$JARVIS_HARNESS_CACHE/engines/whisper-b5454-bin-ubuntu-x64/`, the extracted CPU
  release) is copied into the sandbox's engine dir before it starts, and the wake models are
  taken from `$JARVIS_HARNESS_CACHE/openwakeword/` (the agent checks their pinned SHA-256). Both
  are optional: without them jarvisd and the node download as usual.

Found by the first runs: a sentence that names Jarvis without "hey" ("I was telling Jarvis about
the weather yesterday") wakes the node: 0.43 and 0.44 against the 0.4 threshold, with and
without music behind it. The lane's music case uses talk without the name until that is
decided.
