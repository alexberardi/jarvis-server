# What the owner writes about a watched run becomes a proposed revision to review

- **Date:** 2026-11-13 (user; verified-replay Q3a of [WORKFLOW-SKILLS.md](../agent/WORKFLOW-SKILLS.md))

During or after a watched run the owner writes what went wrong. The model reads it with the
run's record (the step, what was expected, what was seen) and proposes specific edits to that
workflow: a step's goal or expectation, a note, a step marked "Ask me first", a prerequisite.
The workflow page shows them as a draft, as Relearn does, with what changed marked. The owner
saves or discards, then runs again, until a run verifies and the workflow is proven. Feedback
the model can't map to an edit is proposed as a note, still as a draft. Nothing changes a
workflow without the owner seeing the change.

User: "a seems right."

Rejected: saving the sentence word for word as a note (steps and expectations stay wrong, and
notes pile up as prose); steering the run live from the text box (a second control channel into
a running task, and what is kept is hard to make predictable).

Scope: feedback proposes edits only to the workflow being watched. App know-how shared across
workflows stays with `refine`.
