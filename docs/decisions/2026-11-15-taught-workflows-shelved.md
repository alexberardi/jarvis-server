# Taught workflows are shelved: hidden from the product, the code stays

- **Date:** 2026-11-15 (user; after a live test of [WORKFLOW-SKILLS.md](../agent/WORKFLOW-SKILLS.md))

Taught workflows (record what you do, Jarvis repeats it) are not viable for now and are hidden:
no Workflows or Recordings link in the web app, no "Record a macro…" or "Record the desktop…"
in the agent's menu, and `run_workflow` is not offered to the model. Nothing is removed: the
pages, routes, API, recorder, `jarvis-agent computer record` and the tests stay, behind
`workflowsOffered` (cc) and `teachInMenu` (jarvis-agent).

User: "we just hide workflows/macros. don't remove them, just deprecate the feature and remove
from the UI as not viable at the moment."

Why: the user recorded five ordinary desktop tasks once each, with no fixes in between (a cell
entry, a new spreadsheet row, editing a letter, moving a file, copying a total between two
apps). None was learned correctly. The main cause is the recording, not the model: the
recorder keeps what an app names, and has no name for a place in a document, an empty cell's
row, a selection or a drag. On sway there is no pointer position, and a keyframe is every
monitor scaled into one image, too small to read. A stronger model has nothing to work from.

The browser side passed its two suite cases (4 of 4 runs, `replay_changed_param` and
`replay_moved_button`), but only on scripted demonstrations of a fixture site. No person has
recorded a real site, and the desktop also looked fine until one did.

Before bringing it back: a person records real tasks once each and most are learned and run
untouched. For the desktop that needs a different recording (the working window at full size
before and after each action, the pointer, text selection and caret, drags), judged on the same
five tasks. Desktop recordings also capture every monitor, which needs fixing before anyone
else records.

The fixes made during the test (sway click targets, the end state judged on a screenshot,
document opening, typed numbers as values, a reordered workflow page) are parked unmerged on
branch `fix/sway-click-targets`.
