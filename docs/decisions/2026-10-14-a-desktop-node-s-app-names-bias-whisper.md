# A desktop node's app names bias whisper, via node_context.stt_vocabulary

- **Date:** 2026-10-14
- **Moved from** the STATUS.md decision log on 2026-10-14.

**A desktop node's app names bias whisper, via `node_context.stt_vocabulary`** at `/conversation/start` (no heartbeat field, no cache: the start goes out at every wake anyway), ≤ 400 chars, before `voice.stt_prompt`; never in the LLM's messages. One narrow exception to "no app names in the LLM prompt": when the user's own utterance starts with or asks to open one of the node's app names, the turn gets a one-line hint repeating that word ("is an app on this computer, not a person"), because a follow-up "NeoVim, can you open it?" was otherwise dropped as speech to someone else ([agent/DESIGN.md §14](../agent/DESIGN.md)).
