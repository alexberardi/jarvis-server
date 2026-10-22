# Claimed-but-not-done turns are caught by follow-through guards, not longer tool wording

- **Date:** 2026-10-22 (agent, loop item #435; proposed for the user's review)

After the prompt diet (`2026-10-20-prompt-diet.md`), the Qwen 3.5 9B GGUF answered recording requests
in prose ("I'm recording a macro called test2.", "Alright, recording's stopped.") and called
nothing. Restoring the pre-diet tool descriptions fixed those scenarios but moved others: any "turn on the
computer" wording in `enable_computer_control` took "who am I" on a signed-out desktop from 9/10 to
2–4/10, and "Jarvis, watch me" from 10/10 to 6/10. With a 9B model, description wording trades one
scenario against another.

So the diet's descriptions and prompt text stay as they are. The fixes are deterministic checks in
the tool loop (`engine.go`, `followthrough.go`, `computer_activity.go`):

- A request to start or stop a recording (`record a macro …`, `watch me`, `learn how I do this`,
  `stop recording`) answered with no `start_recording`/`stop_recording` call gets one call-now nudge.
- After a nudge that names the one call still missing (not saved, recall first, the typing, not
  cancelled, stop recording, recording not run), a native request sends `tool_choice: "required"`.
  The must-call retry and the other nudges stay `auto`, so the model can still answer in prose there.
- The follow-through nudges also fire when the reply ends in `<exchange_complete/>`. Each nudge
  needs both a matching request and a matching claim, so the marker no longer exempts a false claim.
- `not_for_me` on its own, after a tool already ran this turn, gets one nudge to answer instead.
- A second `no_speaker` refusal in one turn ends the turn with that refusal, word for word.

Not adopted: an `llm.force_tools` setting. The harness sandbox reaches the installed model through
a remote label, so its force-tools guard is off, while a local install's is on. Turning it on in the
sandbox changed the #435 scenarios by noise only.
