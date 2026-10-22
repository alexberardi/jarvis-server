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
  cancelled, stop recording, recording not run, switch not run), a native request sends `tool_choice: "required"`.
  The must-call retry and the other nudges stay `auto`, so the model can still answer in prose there.
- The follow-through nudges also fire when the reply ends in `<exchange_complete/>`. Not every
  nudge needs both a request and a claim. The memory, recall and stop-watching nudges fire only on
  a matching request **and** a matching claim, and the recording-claim nudge on the claim (a stop
  claimed while the node still records), so the marker no longer exempts a false claim. Three fire
  on the request alone, when the call it needs is missing: the recording-not-run nudge
  (`recordingNotRunNag`: "record a macro …" or "stop recording" with no recording call), the
  typing nudge ("open X and type Y" with no typing) and the switch nudge below. So a clarifying
  question after "record a macro" is nudged too, and its retry must call a tool
  (`tool_choice: "required"`).
- The computer control switch tools (`enable_computer_control`, `disable_computer_control`,
  `computer_control_status`) are sent to the model only on a turn whose words say "control"
  (`askedForControl`; the warmup leaves them out, like most turns). Offered on every turn, the 9B
  called `enable_computer_control` for "turn on the computer" in 10 of 10 runs, and with only that
  one left out it called the other two instead; each tool's own refusal comes after the call. A
  "computer control" request answered in prose with no switch tool gets one call-now nudge
  (`switchNotRunNag`, request-only like the recording one).
- `not_for_me` on its own, after a tool already ran this turn, gets one nudge to answer instead.
- A second `no_speaker` refusal in one turn ends the turn with that refusal, word for word.

Not adopted: an `llm.force_tools` setting. The harness sandbox reaches the installed model through
a remote label, so its force-tools guard is off, while a local install's is on. Turning it on in the
sandbox changed the #435 scenarios by noise only.
