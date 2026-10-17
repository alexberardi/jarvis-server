# Pairing is approved by typing the code; the computer confirms the household

- **Date:** 2026-10-17 (loop #100, a security fix; the user approves the merge)

`GET /api/v0/nodes/pairings` listed every pending code on the install to a member of any
household, and approval only checked membership of the *target* household. Anyone with an
account (or a member of another household) could pick a stranger's computer from the list
during its 10-minute window and pair it into their own household, getting type-text, screen-read
and browser tools on it.

A pairing has no owner before it is approved, so knowing the code, which only the computer's
screen shows, is the only proof that the approver may enrol it. So:

- The list never shows codes, to anyone (superusers included: a superuser picking from a list has
  the same hijack within their own households). It answers `waiting` (a count), the caller's
  households, and `pairings: []` for old clients. Approve and deny take a typed code; wrong codes
  count against the existing 10-per-10-minutes limit per user.
- The approved poll answer carries `household_name` and `approved_by`. The setup page stops at
  `confirm_household` ("Add this computer to this household?") and `enroll --code` asks
  `Join this household? [y/N]`; nothing registers until the computer's user says yes.

Supersedes "the code is shown on both screens, nothing to type" in
[01-no-cli-plan §7 item 3](../install/01-no-cli-plan.md) and the list in
[DESIGN §5 item 3](../agent/DESIGN.md). The mobile app's approval must move to a typed code too.
