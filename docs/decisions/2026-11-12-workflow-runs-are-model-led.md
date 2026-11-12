# A taught workflow's run is model-led; the recording acts directly only on an identity match

- **Date:** 2026-11-12 (user; Q1 of [WORKFLOW-SKILLS.md](../agent/WORKFLOW-SKILLS.md))

A run of a taught workflow is first of all correct, not fast (user, 2026-11-11: "I don't even
know if we want a fast path necessarily. First and foremost we want a correct and accurate
path"). The screen at run time will rarely look like the recording: other windows are open, pages
are scrolled, windows have moved.

- The base is a model loop guided by the workflow's skill (its goal, starting state, step purposes
  and expectations). The skill must be enough to do the task without the recording.
- A step skips the model only when its recorded element is found uniquely, by identity, in the
  right app. Recorded coordinates never drive an action.
- Every step is look, act, verify, judged by observed state. A step that can't be verified stops
  and asks.

The other option, the model choosing every action with no direct path, was rejected: on an exact
identity match the recorded action is known to be right, and a local model choosing it again can
only add errors.

This replaces "direct first, one model round, then the user" of
[2026-10-20-replay-target-first.md](2026-10-20-replay-target-first.md) once `verified-replay` is
built; until then replays run as that decision says.
