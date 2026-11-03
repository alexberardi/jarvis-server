# Clean up task-owned temporary artifacts

- **Date:** 2026-10-30 (user request)

Agents must clean up their task's temporary artifacts and processes before handoff,
including after failed or interrupted verification. Repeated per-task caches and
abandoned scratch builds fill temporary storage and can prevent unrelated work.
Reuse disk-backed compiler/package caches; use uniquely named, short-lived scratch
directories and remove finished sandboxes and worktrees when no longer needed.

Cleanup is limited to artifacts the task owns. Preserve shared caches, credentials,
other tasks' processes and unfinished work. Retain only needed review/debug evidence
and report its location and purpose. The concrete workflow lives in
[AGENTS.md](../../AGENTS.md#temporary-files-and-cleanup) and the
[land-pr checklist](../../.claude/skills/land-pr/SKILL.md#7-cleanup-before-handoff).
