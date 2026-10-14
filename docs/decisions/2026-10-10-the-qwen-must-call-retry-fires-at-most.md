# The Qwen must-call retry fires at most once a turn, and never after a tool ran

- **Date:** 2026-10-10
- **Moved from** the STATUS.md decision log on 2026-10-14.

**The Qwen must-call retry fires at most once a turn, and never after a tool ran** ([cc/02-tool-loop.md §3.2 i note](../cc/02-tool-loop.md)). Legacy re-armed it on every engine run with 2 retries, so each node continue could nag again: a follow-up whose tool had run looped the node tool to "Too many tool iterations", an offline node did the same, and #26's first run imported one photo three times. Now: none once any tool result (success, refusal, offline/timeout) or a `[TOOL_DEDUPE]` nudge follows the turn's user message; one nudge per turn; none on a closing remark (`IsAcknowledgementShaped`: "thanks", "got it", "no thanks"; a bare "ok"/"yes" still counts, it can accept an offer). Kept: an action request answered in prose without a tool still gets one nudge. Nag text unchanged (byte-exact "attempt 1/2").
