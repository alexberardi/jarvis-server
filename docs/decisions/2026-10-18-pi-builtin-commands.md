# The Pi's built-in commands: jarvis-node-setup pip-installed into the sidecar's venv

- **Date:** 2026-10-18 (loop #168, build agent; not yet reviewed by the user)

The Go Pi node runs the Python node's own built-in commands, unchanged, in the package sidecar
([agent/go-pi-node.md](../agent/go-pi-node.md) §5.2, §7 4.5).

**Pip, not vendoring.** In the Pi profile the agent pip-installs jarvis-node-setup, pinned to a
commit (`packages.NodeCommandsRequirement`), into the packages venv that uv already manages for
Pantry packages, once per pin (`<state>/packages/builtins.txt` records what was installed). Its
pyproject packages `commands*` (and `core*`, `services*`, `utils*`, `clients*`, `scripts*`) and
depends only on jarvis-command-sdk >= 0.13.0, so the install is small and the commands stay in
one place: a fix in jarvis-node-setup reaches both nodes by moving the pin. Vendoring would copy
21 commands into this repo and fork them. Moving the pin means moving CI's copy too
(`.github/workflows/ci.yml`, the sidecar tests step). The first boot needs the network (as a
Pantry install does) and runs in the background; offline at a new pin, the venv's previous copy
keeps serving. `JARVIS_AGENT_NODE_COMMANDS` overrides the requirement (a local checkout).

**Which commands.** Every `commands/*_command.py` module loads, except four that still call the
Python node's own code at run time and would be offered to the model only to fail:
`control_device` (Devices: #176/#177), `routine` (node-setup's command discovery and secret
service; #173), `reminder` (`services.reminder_service` imports `jarvis_log_client`, and firing
reminders is #175) and `send_link` (node-setup's `RestClient` and service discovery, not the
SDK's `cc_post`). Each comes back when it uses only the SDK (a change in jarvis-node-setup)
or when its Go side lands; the list is `packages.PiBuiltins`. A module that fails to import is
reported alone (`builtin <module>` in the sidecar's failed list) and the others load. A Pantry
package can't take a built-in's name. `tell_joke` still asks the LLM through node-setup's own
client, which fails here, so it uses its canned jokes until it moves to `llm_chat_text`.

**Backends only on the Pi.** The sidecar registers the SDK's node backends (LLM, CC HTTP,
Timer, AudioControl, AlertQueue, Inbox, Signals, CommandInvoker, the logger's stderr fallback)
only with `--backends`, which the Pi profile passes. The desktop agent keeps its behaviour:
a Pantry package there gets the SDK's safe defaults, as before. Turning them on for the desktop
is a later choice (timers and speech would need the desktop's voice pipeline in the same way).

**Go owns the state.** Timers live in `<state>/timers.json` (re-armed at start; one that ended
while the agent was down is said at once), the volume in `<state>/volume.json`, the alert queue
in memory (as the Python node's). Expiry speech and `audio_speak` go through the voice
pipeline's microphone between turns, queued, never awaited, since a command may speak mid-turn.
`cc_post` reaches only `/api/v0/node/...` paths.
