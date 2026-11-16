# A run's end state is judged on a fresh screenshot, whatever else is checked

- **Date:** 2026-11-15 (user; live test of a taught spreadsheet workflow)

When a workflow has an end state, the run asks the model whether it is true of a fresh
screenshot before it calls the result verified. An address or a text on the page is still
checked, but it is a sign, not proof. A download or a file check is proof on the disk and is
not asked of the screen ("the statement is in Downloads" is not something a screenshot shows).

Why: a run that typed 1600 into November verified against "the page shows 1500", because
October's row held 1500.

User: "i feel like it should like screenshot the page and have the LLM verify the end goal was
achieved based on that picture alone?"

Rejected: a check that names the cell ("November row, Amount column is 1600"). It is sturdier
for sheets but only for sheets, and the learner would have to write it.

Cost: a result check needs a model that sees screenshots; without one it fails and the run
asks, it never passes unchecked. A "no" from a weak vision model makes the run work toward the
result and then ask, where a text check alone would have passed.
