# Call follow-ups are proposable actions that opt in to being offered after calls

- **Date:** 2026-11-08 (user)

After a call, installed commands can offer buttons on the Call finished card, such as the
calendar's "Add to Calendar". The user chose the shape: commands expose actions the way they
expose tools, and after the call the background model picks the ones that apply to the transcript
and fills in their params ("post-phone-call tool calls where commands can expose tools
themselves").

They are built on the existing **proposable actions** (SDK `ProposableAction`, the
`jarvis.proposable_action` dispatcher), not on a new follow-up type: a command opts in by setting
one new field, `offer_after_calls`, a sentence saying when the action fits a call. The typed
params, labels, blast tier, idempotency and the dispatcher's checks (household, still declared,
params validated, run once) are reused as they are. The user agreed this is slimmer and easier for
authors than a separate contract.

Rejected on the way: a typed call outcome (appointment / reservation / order) that each command
matches in code, judged a hard sell for third-party authors; and a separate `FollowUp` type with
its own table and callback, which duplicated proposable actions.

The model's picks are suggestions: the server keeps only declared params of the declared types,
dates and times said on the call, no private call details, at most 3 buttons, and nothing runs
without a tap. Only commands with Share with phone calls on are considered.

The first follow-ups (user): add to calendar, a reminder (the day before, to call back, to pick
up), add to the to-do list, draft an email, and save a web-found number to the phonebook. Not the
medication tracker.

Spec: [../cc/phone-followups.md](../cc/phone-followups.md). Open: how follow-ups pass the
`proposals.enabled` gate (Q1 there).
