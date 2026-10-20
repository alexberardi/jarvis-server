# Computer tasks in errands: what the step offers, parks on and shows

- **Date:** 2026-10-20 (build agent, loop item #245, plan P3 piece 11; the blueprint left these open)

These choices were made while wiring `computer_task` into the errand runner
([../agent/COMPUTER-CONTROL.md](../agent/COMPUTER-CONTROL.md), blueprint piece 11).

- **When the planner offers `computer_task`.** It is offered when the household has a computer
  with computer control on (the agent's `computer_control: on` capability, the same test as
  `computerControl().targets()`). The blueprint says "an agent reporting `computer_observe`", but
  cc keeps no tool list per node outside a live conversation. Only an agent that has the computer
  tools reports the switch, so the switch stands in for the tools. The computer-control tools
  themselves (`computeruse.IsInternalTool`) are never on the planner's menu.
- **The live toolset is unchanged.** C11 (#272) already filters the computer tools out of a
  live turn unless that computer's switch is on. This piece does not change that, and
  `TestComputerToolsOnlyWithTheSwitchOn` keeps its expectations.
- **Which computer.** A new task runs on the household's first computer with the switch on. A
  resumed task stays on the computer it started on, which `cc_computer_runs.node_id` records.
- **A question parks like a confirmation.** A model `ask` (or a pause because the user took over
  the computer) parks the errand on the same `approval` wait as a confirmation. Its card has
  Continue (`answer_errand_step`, with an optional typed answer in the card's one editable field,
  `instruction`, as Revise does) and Stop. The answer resumes the same task through the resume
  job's `answer`, and an approval resumes it with the held digests in `approved`. Neither is
  stored on the step.
- **Progress card for an errand the user started.** When its computer step begins, the errand
  posts one progress card ("🖥️ Errand: …", `card_type: errand_progress`, the same metadata as
  the "Ran on its own" card, with no rules section). It is updated in place and does not push,
  because the user has just tapped Run. The confirmation and the completion card still push.
  The step line is "Working in <site|app>: <last intent>" while the task runs and "Waiting for
  you in <site|app>" while it is parked. `progress` lists only actions that ran, never the
  ones held for the user.
- **Stop.** A running computer task in any errand is stopped at once: its durable flag is set
  and `computer_stop` is sent. The errand then ends `stopped` (🛑 card). Cancel on the
  confirmation lands the task `stopped` with nothing it held run. A wait that runs past its
  deadline also ends the task.
- **Spend limit.** `computer.auto_approve_cents` is compared with the checkout's price in
  cents, read from the model's price text (`$12.99`, `12,99 €`). The currency is not converted.
  It applies only when everything held is in the `pay` category.
  A price that isn't exactly one number (with at most a currency sign or code) asks: `$1.2k`
  or `$12.99 + $5.00 shipping` is never read as less than it is. The limit is per checkout,
  not per run: a task with several checkouts could approve each under the limit. An
  auto-approval is audited as "within your $X spend limit", not "approved by the user".
