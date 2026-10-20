# A computer errand asks only at its confirmation

- **Date:** 2026-10-20 (user, answering the build of plan P3 piece 14)

A request that needs the computer ("order me …", "fill in …", "download my statement …") is an
errand, started from the live turn with `run_errand` when a computer has computer control on, and
an errand whose plan is only computer tasks and the computer's file tools (`computer_task`,
`read_computer_file`, `write_spreadsheet`, `request_replan`) runs at once: **no plan card**. The
user is asked only by the computer task itself, at the moment that needs it: the one checkout
confirmation before paying (product, price, seller, ship-to, card), a form's send, or a question
(a page that tries to give instructions). Its replans that stay within those commands go on
without a delta card. The household's rules still decide every action, whatever started it
([computer tasks follow the rules](2026-10-19-computer-tasks-follow-the-rules.md) when recorded).

A plan that mixes a computer task with other commands (a reminder, a phone call) still shows its
plan card. So does a plan with a desktop task: that one runs on its own only when the user asked
for it at that computer a moment ago, and autonomy is on (C9); otherwise its plan card waits for a tap.

Why: the brief's promise is one confirmation on the phone before paying
([COMPUTER-CONTROL.md](../agent/COMPUTER-CONTROL.md)); a plan card in front of it made it two, and
the suite's cases ("one confirmation, approved") could not pass.

Code: `errands.isComputerPlan` / `startComputerPlan` (plan.go), the replan gate (replan.go), the
live turn's hint (`computerOnHint`, computer_control.go). Supersedes nothing.
