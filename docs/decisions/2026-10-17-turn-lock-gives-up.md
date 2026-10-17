# A turn waiting for its conversation gives up with its caller

- **Date:** 2026-10-17 (build agent, item #115; follows the triage recommendation)

A conversation's turn lock (`conversation.mu`, now a `turnLock`) is held for a whole turn,
LLM calls included. A turn waiting for it (`processTurn`, `continueBlocking`, and the
continue span's metadata and stream plan) now waits only while its caller is there: the
request context for voice, the client's request context for mobile chat (which still runs
the turn itself on a detached context, so history commits after a hang-up, §11). When the
caller leaves first the wait returns `errConversationBusy`: 409 "Conversation is busy with
another turn" on the voice routes, an SSE error frame on mobile chat, and the streamed
continue falls back to the blocking one (202).

There is no cap on the wait and no fail-fast try-lock: voice follow-ups on one conversation
legitimately queue behind the turn before them. Background holders (image descriptions,
async compaction, user purge, the stream commit) keep waiting unconditionally.
