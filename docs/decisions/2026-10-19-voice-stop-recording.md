# Voice stops a recording; the agent reports its computer activity per turn

- **Date:** 2026-10-19 (build agent, item #288, from the user's test of "record a macro called test2")

The desktop agent sends `computer_activity` (`{kind: recording|controlling|paused, task, stopped}`)
with every voice turn, wake and follow-up, rather than once in `node_context`: a recording often
starts during the conversation ("record a macro …"), so the state at `/conversation/start` is stale
by the follow-up that says "I'm done".

A stop phrase (`computeruse.IsStopPhrase`, one list shared by the agent and jarvisd) stops the task
on the agent at once and the turn still goes to jarvisd with `stopped: true`. Because the agent's
own stop uploads the trace (as the tray's does), `stop_recording` then neither asks the agent again
(it would answer `not_recording`) nor asks for a name: it says the recording is saved, under the
name `start_recording` was given. A name given only after the stop is not applied.

While the node records, jarvisd: skips the follow-up gate and the not-for-me double check for a stop
phrase; nudges the model once to call `stop_recording` (even over `<not_for_me/>`); and if it still
doesn't, calls it itself and speaks its message. A reply claiming the recording stopped or was saved
without `stop_recording` is nudged once, then replaced with "I'm still recording". A stop phrase
the agent already carried out while controlling the computer that the model calls not-for-me is
answered "Okay, I stopped." Not recording, a stop phrase is an ordinary turn.

The island keeps the voice state's pill during a turn and draws the activity as a dot in its
colour at the pill's right end, so a recording that starts mid-turn shows at once (it used to show
only once the follow-up window closed). See [COMPUTER-CONTROL.md](../agent/COMPUTER-CONTROL.md).
