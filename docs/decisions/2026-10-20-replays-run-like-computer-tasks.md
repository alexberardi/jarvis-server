# Replays run like computer tasks

- **Date:** 2026-10-20 (agent, plan P3 piece 32; follows the user's 2026-10-20 "a computer errand starts at once")

A plan that only replays taught browser workflows (`run_workflow` steps, with the computer's file
tools and checkpoints) is a computer errand: it starts at once, with no plan card, like a plan of
computer tasks. The replay still asks for what its steps need (a purchase's confirmation, a
masked value) under the household's rules. A `run_workflow` step whose workflow works in a desktop
app, or whose name doesn't resolve to exactly one workflow, keeps the plan card unless the user is
at the computer (C9). The planner is told to prefer a saved workflow to a computer task for the
same task (`workflowRule`, only with `run_workflow` on its menu).

A replay opens its task window on its first step's recorded address, after the blocked-site
check a computer task makes, instead of `https://<site>`.

A computer task that is over (done or failed) ends on the computer, browser tasks included
(`computer_stop`): before, only desktop tasks did, and the agent kept showing "controlling" after
a browser task, so it refused to start a recording ("Jarvis is already using this computer")
until the user pressed Stop. The task's window stays open.

Reason: the teach cases (`docs/dev/computer-suite.md`) did not pass without each of these.
