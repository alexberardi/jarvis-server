# Call plans gather context from the household's nodes

- **Date:** 2026-11-05 (user)

A phone call's plan asks the household's commands for what the call may need, over the
SDK's existing plan-time context operations (`ContextOperation` / `execute_context_operation`,
answered by the node on `jarvis/nodes/{id}/context/query`):

- **`availability`** (the calendar package's): free and busy windows for the next week, which
  become the brief's "Acceptable times / Do not book" and drive the in-call time check.
- **`call_context`**, a new well-known operation: given the business and the goal, a command
  returns details for the call (a loyalty number, a vehicle's VIN), each one to state or to give
  only if asked. A command without it, or on an older SDK, is skipped.

Decided:

- **Plan time only.** The context is gathered once, while the plan is made, and baked into
  the plan's brief; nothing is fetched over MQTT during the call. The brief is on the plan card,
  so the user sees what Jarvis will know and can delete any of it before tapping Call now.
  Fields that are given only if asked are kept with the session for the spoken-output guard,
  which covers those still present in the confirmed brief.
- **Every active node in the household**, asked in parallel with a short timeout and merged:
  a calendar on the desktop agent and a store package on the kitchen Pi both count. A node that
  is offline or slow is skipped; nothing blocks the plan.
- **A per-command switch, on by default, enforced on the node.** "Share with phone calls" in
  the command's settings; a command switched off doesn't answer context queries, so its data
  never leaves the node. It travels as a `context_sharing` config push, like the command and
  fast-path switches.
- **A description of what is shared**, written by the command for the user
  (`call_context_description`: "Your free and busy times for the next week, so Jarvis only
  books slots that fit"), shown under the switch. Without one, the operation's own description.

Supersedes the unwired `PhoneConfig.Calendar` placeholder ("fill in your availability").
See [the phone doc](../cc/11-phone.md) §7 (availability envelope).
