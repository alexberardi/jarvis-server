# Computer control is a tool set inside the errand runtime

- **Date:** 2026-10-19 (user, plan P3 brief/blueprint)

Computer control is not a separate system: it is a new errand step command,
`computer_task {goal, target: browser|desktop, app?, site?, success?}`, that the planner emits for
goals that need the computer (decision C6 in [agent/COMPUTER-CONTROL.md](../agent/COMPUTER-CONTROL.md)).
The errand runner drives it as a loop in a new package `internal/modules/cc/computer` — observe
(element snapshot first, screenshot as the fallback), ask the model for a short batch of actions
each with an `intent`, run the household policy per action, send the batch to the agent, repeat
until done, then verify the result itself (C4) — with budgets, a durable stop flag and per-action
audit rows, and its state in a `cc_computer_runs` table so a parked confirmation resumes the same
task.

Enforcement lives on the computer: the action schema, the risk classifier, the built-in deny list
and the approval-token check sit in one shared package, `internal/computeruse`, used by both cc and
the agent. The agent re-checks every action at execution (C8), so a bad model output can't bypass
the rules, and model text can never grant anything.

This settles the approach of plan P3's blueprint; the plan's C6 is unchanged.
