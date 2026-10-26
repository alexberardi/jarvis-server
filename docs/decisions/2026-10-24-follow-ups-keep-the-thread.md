# Follow-ups keep the thread: tool screenshots are described, subject-less follow-ups continue the last request

- **Date:** 2026-10-24 (agent, loop item #576)

**Tool-result screenshots get a description in history.** Until now a node tool's
`image_attachments` were for the turn that asked only: `conversation.commit` dropped them and
history kept the label with "not kept after this turn", so "what colors was it?" had nothing to
answer from. Now they stay in memory until the chat-image describe job (CI5) replaces them with
`[image N: …]` text, queued by `afterTurn` once the turn ends; the text path's formatting
message gets the same. They are never kept as chat photos (`fromTool`). The describe prompt now
asks for the main colours too. This changes [chat-images.md §10](../cc/chat-images.md).

**One line in every built-in prompt** (after the speech-to-text line): a short follow-up with no
subject of its own continues the last request, never the time, the home or recall; "what about
now?" calls the same tool again, "what colour was it?" is answered from what it found. On the
9B, without it "what about now" was answered with the time (or, with ambient data, the home
summary) and "what colors was it?" went to `recall`. The G1 prompt goldens are re-baselined
from the Go builder (`tools/golden/rebaseline.py`), as in
[2026-10-20-prompt-diet](2026-10-20-prompt-diet.md).

**Read tools (item #598):** the cross-turn `[TOOL_DEDUPE]` (120 s, [02-tool-loop.md](../cc/02-tool-loop.md)
§3.2 j.8) used to answer a repeated identical `browser_read_page` from the earlier result, so a
"what about now" within two minutes repeated the old text. Read tools (`browser_read_page`,
`desktop_read_screen`) are now exempt from it: a repeat re-reads the current state.
