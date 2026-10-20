# Voice stops a recording; the agent reports its computer activity per turn

- **Date:** 2026-10-19 (build agent, item #288, from the user's test of "record a macro called test2")

The desktop agent sends `computer_activity` (`{kind: recording|controlling|paused, task, stopped}`)
with every voice turn, wake and follow-up, rather than once in `node_context`: a recording often
starts during the conversation ("record a macro …"), so the state at `/conversation/start` is stale
by the follow-up that says "I'm done".

The agent and jarvisd share two lists. `computeruse.IsStopPhrase` ("stop", "cancel that", "never
mind", …) stops a control task; `computeruse.IsRecordingStopPhrase` adds the recording's own words
("stop recording", "I'm done", "that's it", "done", …) and applies only while recording, so "That's
all, thanks" never ends a control task. A stop phrase stops the task on the agent at once and the
turn still goes to jarvisd with `stopped: true`. In a follow-up only a recording is stopped: a
follow-up never stops a control task, which stays held through the follow-ups (where "cancel that"
is about the conversation) and resumes after them; its `computer_activity` goes up without
`stopped`. Because the agent's own stop uploads the trace (as the tray's does), `stop_recording`
then neither asks the agent again (it would answer `not_recording`) nor asks for a name: it says
the recording is saved, under the name `start_recording` was given. A name given only after the
stop is not applied.

For a stop phrase, jarvisd skips the follow-up gate and the not-for-me double check: in a wake
turn while any task runs, in a follow-up only while recording (a follow-up "cancel that" during a
control task is gated as usual). While the node records, it also nudges the model once to call
`stop_recording` (even over `<not_for_me/>`), and if it still doesn't, calls it itself and speaks
its message. A reply claiming the recording stopped or was saved without `stop_recording` is
nudged once, then replaced with "I'm still recording". A stop phrase the agent already carried out
while controlling the computer that the model calls not-for-me is answered "Okay, I stopped." Not
recording, a recording stop phrase is an ordinary turn.

The island keeps the voice state's pill during a turn and draws the activity as a dot in its
colour at the pill's right end, so a recording that starts mid-turn shows at once (it used to show
only once the follow-up window closed). See [COMPUTER-CONTROL.md](../agent/COMPUTER-CONTROL.md).
