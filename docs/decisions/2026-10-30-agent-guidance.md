# Shared coding-agent guidance

- **Date:** 2026-10-30 (documentation maintenance)

Maintain the server's shared coding-agent instructions in [AGENTS.md](../../AGENTS.md).
[CLAUDE.md](../../CLAUDE.md) imports it; the existing agent and web UI guides remain
scoped to their directories. Keep the root guide useful in a standalone clone or
worktree, with a code map, setup commands, verification requirements and links to
the detailed harness docs.

The merge policy carries forward the user's 2026-10-14 instruction already in the
server's CLAUDE.md: agents may squash-merge their own PRs after green CI, with the
existing review holds and a check of main's CI afterward. The land-pr skill's
blanket instruction to wait for the user contradicted that policy; it now points
to the shared guide. This does not change the hold list or verification gates.

Historical migration plans remain references for compatibility work. Current
source and later decisions govern the implementation, including jarvisd-only
features and capabilities deliberately restored after the original cut lists.
