# Replaying taught workflows: role+name first, what "healed" means, and who runs what

- **Date:** 2026-10-20 (build agent, plan P3 piece 31; [COMPUTER_CONTROL.md](../agent/COMPUTER_CONTROL.md))

**Matching is by role and name for now.** The blueprint has the replay try a step's recorded
selector (browser) or AX path (desktop) before role and name. Neither snapshot carries one yet:
the extension's `snapshot` and the AX/AT-SPI walkers send ref, role, name and value only, and only
the recorder computes selectors. Adding them means an extension release and agent changes on
three platforms, so this piece stays server-side and matches the one element whose role and name
equal the recorded ones (case and spacing aside; a recorded element without a role matches on
name). None, or more than one, is "not found". Selector and AX-path matching is a follow-up: the
recorded `selector`/`ax_path` stay in the workflow for it.

**Not found means one model round, then the user.** The round's task is the step's intent, with
the recorded element as a hint and the direct try's failure as feedback. If the model acts on
another element and the run then passes its success check, that step is *healed*: its element
becomes the one acted on (role and name; the old selector and AX path are dropped because they
named the old element), the workflow's version goes up by one and the card says "<name> moved; I
updated the step". A heal is stored only when the workflow is still at the version the replay
started from: an edit made meanwhile wins. A run whose check fails stores nothing. If the model
asks, or its actions fail, the run asks the user (Continue with an answer resumes that step).

**Policy is never skipped.** A direct step goes through the same `decide` → hold/mint → act path as
the loop's actions, so a risky step (Place order) still waits for its one confirmation, and every
action has its audit row. A step that typed a masked value (a password, a card number) asks the
user to type it on the computer and tap Continue; Jarvis never stores or types it.

**No success check → a screen check.** C4 says a task is done only once checked. A workflow saved
without a success check is checked with a yes/no screen question ("Is the task … finished?").

**Who runs what.** By voice and from the admin's Run button, a member runs their own workflows and
the household's shared ones; a household admin can edit a member's private workflow but not run it.
A routine is the household's, not one member's, so its `run_workflow` steps run shared workflows
only. A schedule plans as its creator, so the planner offers that user's own and shared ones. Each
run is an errand with one `computer_task` step carrying `taught_workflow_id` and `params` (not
`workflow_id`, which in errands is the errand itself); a planned `run_workflow {name, params}` step
resolves the name when it runs.

**Routines.** `run_workflow` steps start their errand on the server and count as passed when it
started (the replay can take minutes; its own card reports the outcome). The node gets the rest of
the routine, or nothing when nothing is left.

**Voice offers it only with a workflow taught.** `run_workflow` is in a voice conversation only when
the household has a computer, a taught workflow and a known speaker (`ToolGates.Workflows`). With
none taught it could only say so, and the extra tool beside `start_recording` and `stop_recording`
made the live model (qwen3.5-9b) answer "record this macro" and "I'm done" without calling them in
the pipeline scenarios. A workflow taught mid-conversation is offered from the next one (the gates
are fixed per conversation, like the others).
