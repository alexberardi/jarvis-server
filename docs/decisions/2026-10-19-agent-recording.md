# Recording on the agent: who owns it, where it runs, how it reaches jarvisd

- **Date:** 2026-10-19 (build agent, plan P3 piece 29; the user reviews the PR: new node route)

Choices made building "Jarvis, watch me" ([COMPUTER-CONTROL plan](../agent/COMPUTER-CONTROL.md),
blueprint piece 29):

- **One id for both copies.** The agent picks the recording's id (`rec_` + 16 hex digits, the
  shape jarvisd's own ids have) and jarvisd stores the row under it, so the retention sweep's
  `computer_record_delete {recording_id}` finds the agent's local file. `createRecording` takes a
  given id when it matches that shape; the same id uploaded twice is one recording.
- **Two ways in.** Stopped by voice ("I'm done", `stop_recording`): the trace comes back in
  `computer_record_stop`'s answer and jarvisd stores it for the speaker. Stopped on the computer
  (the menu's "Stop recording" or "Stop Jarvis", `jarvis-agent computer record stop`, voice
  "stop", computer control turned off, the 30-minute limit): the agent uploads it to
  `POST /api/v0/computer/recordings` (node auth), and it is the recording of the member signed in
  on that computer (`owner_user_id` while still in the household; nobody: 409, the trace stays
  local). Only a computer's agent may upload (a Pi: 403). One stopped while the agent isn't
  connected, or whose upload fails, is kept with a `<id>.pending` marker next to its trace and
  sent on the next connect and every 5 minutes while connected, oldest first (#298); the island
  says it was kept on the computer. A 400, 409, 413 or 422 is final: the trace stays local.
- **Which computer.** A turn on a computer records that computer. A turn elsewhere (a Pi, the
  phone) records the speaker's own computer with computer control on; never someone else's.
  (Superseded by [2026-10-19-record-a-macro-and-voice-switch](2026-10-19-record-a-macro-and-voice-switch.md):
  recording no longer needs computer control on.)
  `computer_record_stop` takes no id from jarvisd: the agent stops the one going on, so a
  restarted jarvisd can still stop it.
- **Offered only with a computer.** `start_recording` / `stop_recording` are offered (text and
  native paths) when the household has a computer, switch on or off, and the speaker is known;
  with the switch off they answer where to turn it on, like the computer-task errands.
- **Browser only for now.** `mode: desktop` answers "not supported yet" until pieces 36/37; so no
  keyframes exist yet. `computer_record_stop` already sends at most 4 sampled keyframes as
  `image_attachments` (the rest stay local); jarvisd stores only their count today. (Superseded:
  each click's keyframe and crop are now kept, [2026-10-20-recording-keyframes.md](2026-10-20-recording-keyframes.md).)
- **Learning is a hook.** Every stored recording goes to `Module.learnRecording`, nil until
  piece 30 queues the learn job there.
- **Limits.** 30 minutes, then the recording stops by itself and uploads; a trace keeps at most
  768 KiB of events (it must fit one upload), later events are dropped and the trace says
  `truncated`. Traces live in `<agent dir>/recordings/<id>.jsonl` (0600, directory 0700).
