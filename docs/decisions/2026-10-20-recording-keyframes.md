# A recording's click keyframes and crops are kept on the server

- **Date:** 2026-10-20 (agent, #344; the limits are a proposal the user may change)

Supersedes the "crops stay on the computer" part of
[2026-10-20-desktop-recording-wayland.md](2026-10-20-desktop-recording-wayland.md) and the
"jarvisd stores only their count" part of [2026-10-19-agent-recording.md](2026-10-19-agent-recording.md).

**What goes up.** After the trace, the computer's agent sends each keyframe taken at a click (the
event with the same `seq` has `kind: click`; on Wayland it is the marked one) and its crop, one
JPEG per request: `PUT /api/v0/computer/recordings/{id}/keyframes/{seq}` and `…/{seq}/crop`,
node auth. Only the computer that uploaded the recording adds to it, and only for a click of its
trace (else 404 / 422). The same one again replaces it, so the agent's retries are safe. The
trace upload no longer carries the four sampled `image_attachments` (jarvisd dropped them, and
they made the body up to ~12 MiB); `computer_record_stop`'s answer still has them.

This works for both ways a recording stops. Stopped on the computer, the agent uploads the trace,
then the keyframes. Stopped by jarvisd (`stop_recording`, through `computer_record_stop`), the
agent sends only the keyframes, 3 s later, once jarvisd has stored the trace. A recording
jarvisd doesn't have yet (404) is tried again on each sweep (every 5 minutes) for a day. An
`<id>.keyframes` marker next to the trace says the keyframes are still to send. A 5xx stops the
sweep, and the next one sends that recording's keyframes again from the first.

**Storage.** jarvisd's blob store, `recordings/<id>/<seq>.jpg` and `<seq>.crop.jpg`. There is no
table: the blob listing is the index (the limits and the recording's `keyframe_images` in its
GET come from it). `cc_recordings.keyframes` stays the count the agent took.

**Limits.** A keyframe is at most 2 MiB and a crop 256 KiB (413). The agent's frames are about
1568 px JPEGs, a few hundred KiB, and crops 256 px. A recording keeps at most 300 keyframes or
100 MiB of keyframes and crops, whichever comes first (413 `keyframe_limit`). The trace stays and
the agent stops sending that recording's keyframes. 30 minutes of clicks at one keyframe a second
(Wayland) can be more than 300, but a workflow worth learning has far fewer clicks. These are
constants, not a setting yet.

**Retention.** The same as the recording's (`computer.recording_retention_days`). The keyframes
are deleted with it: by its owner, by the daily sweep when it expires, and by that sweep when
their recording is gone (an upload that raced a delete). The agent's own copies go with its local
recording (`computer_record_delete`), as before.

How a vision model uses them (#319) is not decided here.
