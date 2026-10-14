# Chat images and compaction built as designed

- **Date:** 2026-10-09
- **Moved from** the STATUS.md decision log on 2026-10-14.

**Chat images and compaction built as designed** ([cc/chat-images.md](../cc/chat-images.md) CI1–CI6, §7). Decided while building: the compaction summary is a `system` message right after `messages[0]`; the trigger is the last live call's `usage.prompt_tokens`; `cc.compaction.hard_threshold` is clamped to ≥ `cc.compaction.threshold`; descriptions are prepended to the user's text; a remote slot's window is `llm.<slot>.context` (0 = no compaction).
