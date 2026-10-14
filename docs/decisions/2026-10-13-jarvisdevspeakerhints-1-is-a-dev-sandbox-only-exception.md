# JARVIS_DEV_SPEAKER_HINTS=1 is a dev-sandbox-only exception to D2

- **Date:** 2026-10-13
- **Moved from** the STATUS.md decision log on 2026-10-14.

**`JARVIS_DEV_SPEAKER_HINTS=1` is a dev-sandbox-only exception to D2.** With it, command-center takes a node turn's `speaker_user_id` as the voice match (household members only), so text-only pipeline tests can play a known speaker and reach the memory tools (D21). Off by default, warned at startup, set only by `scripts/sandbox`.
