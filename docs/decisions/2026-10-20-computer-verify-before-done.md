# Computer tasks: which check decides "done", and the extra budget

- **Date:** 2026-10-20 (build agent, plan P3 piece 12; [COMPUTER_CONTROL.md](../agent/COMPUTER_CONTROL.md) C4)

A computer task reports done only after `computer.Loop.Verify` passed. The check it runs is the
task's own success check (`Task.Success`: the planner's `success`, or a taught workflow's) when it
has one, and only otherwise the `verify` the model's done carries. The task's check was set before
the model saw any page, so a model can't swap it for an easier one; the two are not merged, because
a model's guessed text on top of a known-good check would only add false failures.

A done with no check at all is sent back once with a request for one; a second one fails the task
("I couldn't confirm the task was finished"). A check that fails goes back to the model as the next
round's feedback, and the run gets 5 more rounds from the first failure (inside `computer.max_rounds`,
not on top of it) to fix it and say done again; then it fails with "I couldn't confirm <check>:
<why>". The "asked once" flag and the round of the first failure are kept on the run
(`cc_computer_runs.verify`, migration 00280), so a confirmation or a question in between doesn't
reset them.

Text checks (`text_present`) look in the snapshot first (title, element names and values). The
extension's snapshot holds interactive elements only, so text it lacks (a heading saying "Order
1234") is asked of the screenshot with the same yes/no vision call as `screen`. Without a vision
model those checks fail; they never pass unchecked. Download checks list `~/Downloads` through the
agent's `computer_files` and skip unfinished downloads (`.crdownload`, `.part`, …).
