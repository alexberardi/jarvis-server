# Autonomy is built in worktrees beside the release

- **Date:** 2026-10-13 (user)

The autonomy phases are built in parallel with the v0.1.0 release work, each lane in its own `.claude/worktrees/` checkout with its own `scripts/sandbox`. Because the sandboxes share the installed model's single slot, P0a (live-first LLM admission, with harness calls labelled background) goes first. Plan: [autonomy/PLAN.md](../autonomy/PLAN.md) "Building it alongside the release".
