# Computer tasks follow the household rules wherever they start; new apps allowed on first use

- **Date:** 2026-10-19 (user, plan P3 brief/blueprint)

A computer task is an autonomous loop wherever it starts, so a task started live from a voice or
chat turn goes through the same rules as a background one: every action is checked by
`policy.Decide` (Q3 in [agent/COMPUTER-CONTROL.md](../agent/COMPUTER-CONTROL.md)). The existing
rule that live turns aren't gated (A2) stays true for ordinary single tool calls; only the
computer-task loop is gated, whatever started it.

First use of a new desktop app is **allowed**, per the autonomy-first default (C2): the app shows
in the progress stream and the audit log, and the household can switch it to ask with the
`computer.new_app` setting (`allow` by default, `ask` to confirm each new app) (Q4).

This settles Q3 and Q4 as the plan recommended.
