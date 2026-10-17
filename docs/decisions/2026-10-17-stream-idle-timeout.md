# LLM streams: idle watchdog with a first-byte allowance

- **Date:** 2026-10-17 (agent, loop item #116)

A streamed model call has no overall deadline (a long answer is legitimate), so a stalled engine
used to hold its live or background admission slot until the caller left: about 30 s for nodes,
for ever for an external `/v1` SSE client. Streams now have an idle watchdog,
`llm.stream_idle_timeout_seconds` (default 60, 0 = off): when the engine sends no byte for that
long the request is cancelled, the stream ends with an error frame and a warn log, and the slot
is freed.

Before the first byte the allowance is the larger of that and `llm.request_timeout_seconds`
(default 240), because llama.cpp sends nothing, not even headers, while it evaluates a long
prompt; a 60 s window there would cut off big background contexts. Time spent waiting for the
consumer to take a delta doesn't count: a slow client is not a quiet engine. See
[02-api-and-streaming](../llm/02-api-and-streaming.md).
