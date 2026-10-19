# Computer control is a per-computer opt-in, off by default

- **Date:** 2026-10-19 (user, loop #272)

Jarvis must never take over a computer implicitly from something you said. Each computer opts in
with a **"Computer control"** checkbox in the desktop agent's menu, right after
"Listen for "Hey Jarvis"" (also `jarvis-agent computer on|off` and the control socket). It is
**off on every install and after an upgrade** (agent.json's `computer_control`; unset is off).

The agent enforces it (C8): while it is off, every computer action (browser and desktop targets)
and recording is refused with an error that says where to turn it on, and turning it off
mid-task stops the task the way "Stop Jarvis" does. The agent reports the switch in its
heartbeat (`computer_control: on|off`), and jarvisd uses it only to choose targets: a computer's
computer tools are offered only while its switch is on, and a request that needs a computer when
none has it on gets "computer control is off, turn it on in the Jarvis Automation menu on
<computer>" instead of an errand or any other fallback. The admin's Nodes page shows the state,
read-only; it is set only on the computer itself.

This is decision C11 in [agent/COMPUTER-CONTROL.md](../agent/COMPUTER-CONTROL.md); it adds to
C8 and supersedes nothing.
