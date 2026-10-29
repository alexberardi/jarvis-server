# One element layer for voice and computer control

- **Date:** 2026-10-28 (user)

We won't out-do Apple or Microsoft at accessibility tools. What Jarvis builds from the
accessibility tree is first of all **building blocks for model-driven computer control**: when
the model sees a window's elements well and acts on them reliably, Jarvis can do what those tools
can't (act on descriptions instead of labels, carry out goals instead of steps). Voice steering
("click Images") is a "good enough" front end on the same layer, not a separate system.

So one element model (`computeruse.Snapshot`) is filled by every source (the browser extension,
AT-SPI, later Windows UI Automation and macOS AX), and both voice and computer tasks read it.
Every model feature lands as steps the layer can do and check, with success taken from observed
state (Q32, [2026-10-27](2026-10-27-direct-steps-goal-oriented-model.md)). The user also set the
process: a full spec first, then two agents split the work and review each other's slices
adversarially. The spec and its milestone-1 scope (Linux and the browser) were approved by the
user on 2026-10-28: [ELEMENT-LAYER-WORK.md](../agent/ELEMENT-LAYER-WORK.md). The technical
details there were agreed between the agents; only the direction, the process and the approval
are the user's.

**Model pick confidence (user, 2026-10-28).** When the narrow model pick names an element without
a confidence, it counts as sure and acts; the element is still checked against the snapshot. An
explicit confidence below 0.75 asks the user to repeat with a detail. This supersedes the
earlier working-doc proposal that a missing confidence never acts (A2, superseded in A4).

Builds on [2026-10-28 on-screen names first](2026-10-28-on-screen-names-first.md).
