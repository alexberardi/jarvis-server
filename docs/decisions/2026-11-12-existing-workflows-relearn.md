# Existing workflows keep running as they are; Relearn rebuilds a skill on request

- **Date:** 2026-11-12 (user; Q4 of [WORKFLOW-SKILLS.md](../agent/WORKFLOW-SKILLS.md))

A workflow taught before workflow skills has no goal, prerequisites, step goals or expectations.
It opens and runs unchanged. Its page has **Relearn**, which rebuilds the skill from the stored
recording (expectations inferred from the next step, since old recordings hold no results) as a
draft the owner reviews; the workflow changes only when that is saved. When the recording is past
`computer.recording_retention_days` the page says so and to record the task again.

Relearning every workflow on upgrade was rejected: a model job per workflow, unreviewed guesses,
and the owner's step edits at risk.
