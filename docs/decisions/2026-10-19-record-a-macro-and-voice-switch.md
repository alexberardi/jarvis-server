# Record a macro without computer control; computer control and recording by voice

- **Date:** 2026-10-19 (user, item #273; the build agent chose the points marked "build")

Changes [COMPUTER-CONTROL.md](../agent/COMPUTER-CONTROL.md) C11 and supersedes "Which computer"
in [2026-10-19-agent-recording](2026-10-19-agent-recording.md) (it asked for the switch on).

- **Recording is not gated by the "Computer control" switch** (user). Each recording is an
  explicit act ("Record a macro…" in the menu, right below "Computer control"; "Stop recording"
  in its place while it runs) and the indicator shows while it runs. Starting one doesn't turn
  the switch on. Replaying a macro is a computer action and still needs the switch on.
- **Turning the switch off doesn't end a recording** (build; the triage's open point). The
  switch is about Jarvis acting on the computer, and a recording doesn't; "Stop Jarvis" and
  "Stop recording" still end it.
- **Voice tools** (user): `enable_computer_control`, `disable_computer_control`,
  `computer_control_status`, and `start_recording` / `stop_recording` for "record this macro",
  "watch me do this", "learn how I do this", "I'm done", "stop recording".
- **Which computer** (user): the one the turn comes from; from another device the one named,
  else the only one, else ask which. **Whose** (build, for the user's review): turning control
  on, recording and the status reach only the speaker's own computers from elsewhere (the
  member signed in there, `owner_user_id`), as recording did before; at a computer, whoever
  speaks there. Turning it off works from anywhere on any of the household's computers, the
  safe direction (preferring the one that is on).
- **The switch changes on the agent** (build): jarvisd calls the agent's node tool
  `computer_switch {on}`, which is the menu's `Switch.Set` (agent.json, the tray, the heartbeat).
  jarvisd never flips its own copy: the agent stays what enforces C11 (C8). An offline computer
  is said ("I couldn't turn computer control on for the MacBook: the computer is offline.").
- **A macro needs a name** (user, "stop asks for a name if it doesn't have one"). The name is the
  recording's narration: `start_recording`'s `task` or `stop_recording`'s `name`. A stop with
  neither answers `need_name` and leaves the recording going, so nothing is lost while the user
  answers. A recording started from the menu and stopped by voice has no name yet: asked.
