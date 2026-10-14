# Every autonomous action asks by default

- **Date:** 2026-10-14 (user)
- **Refines** [2026-10-13-household-rules-may-let-background-actions-run-without-a-tap.md](2026-10-13-household-rules-may-let-background-actions-run-without-a-tap.md). It replaces the draft's fixed floor ("phone calls and risky steps always ask").

Nothing is hard-wired to always ask. The first match wins: a goal- or watch-scoped rule (the per-task opt-in), then a household rule for the tool, then the tool's own declaration, then **ask**. A tool declares itself with a new SDK property, `run_without_confirmation` (default False, next to `is_risky`); server tools use a Go interface. A tool that is `is_risky`, or has a `business` counterparty, ignores its own declaration: it can skip confirmation only through an explicit rule. Nodes that don't send the field count as False. D21 unknown-speaker refusals still apply. Plan: [autonomy/PLAN.md](../autonomy/PLAN.md) §1.
