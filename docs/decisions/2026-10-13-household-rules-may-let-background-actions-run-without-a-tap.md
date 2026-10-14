# Household rules may let background actions run without a tap

- **Date:** 2026-10-13 (user)
- **Supersedes** D9's "no new callers yet" for errand autonomy ([cc/QUESTIONS.md](../cc/QUESTIONS.md)).

A household rule can let a tool run without a confirmation tap. This is opt-in per tool, off by default and set by household admins; `errands.autonomous_enabled` stays the master switch. The dormant `IsAutorunEligible` and `AutonomousEnabled` get their first callers through the rules, and the hard-coded `autorunAllowlist` stops being the trust boundary. How a verdict is resolved is in [2026-10-14-every-autonomous-action-asks-by-default.md](2026-10-14-every-autonomous-action-asks-by-default.md). Plan: [autonomy/PLAN.md](../autonomy/PLAN.md) §1.
