# command-center specification

This directory is the working spec for porting jarvis-command-center ("CC") to Go. Python is **frozen**: nothing lands in the Python repos during the migration. These docs plus the user's answers are the source of truth for Phase 5 and for CC's contract tests.

Source: `/home/alex/jarvis/jarvis-command-center` (about 62k LOC, `app/`).

## Subsystems

| # | Doc | Scope |
|---|---|---|
| 00 | [00-platform.md](00-platform.md) | App lifecycle, the 18 background loops index, auth modes (`deps.py`), table→subsystem map, migrations, settings framework, service config, logging/latency, LoRA entanglements to cut |
| 01 | [01-voice-pipeline.md](01-voice-pipeline.md) | Conversation start/end, voice command (+stream/continue), conversation handler, cache, warmup, wake verification, transcript filter, turn context, hints, acknowledgments |
| 02 | [02-tool-loop.md](02-tool-loop.md) | Tool execution engine, registry, routing (fastText), parsing and repair, param validation, generic server tools, model service, llm-proxy client, `/chat` |
| 03 | [03-prompts-and-dates.md](03-prompts-and-dates.md) | Kept prompt providers + shared, system prompt builder, general context, date detection/resolution, personas, prompt-provider install (being cut) |
| 04 | [04-memory-and-knowledge.md](04-memory-and-knowledge.md) | Memories, extraction, characterization, transcripts, user data purge, deep research, quick search |
| 05 | [05-nodes.md](05-nodes.md) | Node registry, provisioning, node settings and K2, MQTT client and request/response, node commands, liveness, updates, ambient noise, traces |
| 06 | [06-media-and-voice-identity.md](06-media-and-voice-identity.md) | TTS/Whisper media proxy, mobile audio, voice profiles (enroll/verify), speaker resolution and stickiness |
| 07 | [07-smart-home.md](07-smart-home.md) | Rooms, devices, control/state, scans, config push, cameras/HLS, Bluetooth, provider OAuth, device tools |
| 08 | [08-routines-and-schedules.md](08-routines-and-schedules.md) | Routines CRUD/run, routine scheduler, schedules, time windows, step value resolution |
| 09 | [09-errands-and-workflows.md](09-errands-and-workflows.md) | Errand planner/executor/service, workflow engine, errand tools |
| 10 | [10-signals-attention-proposals.md](10-signals-attention-proposals.md) | Signals ingest and reactions, signal automations, attention broker, proposals and suppressions, situation matcher, presence |
| 11 | [11-phone.md](11-phone.md) | Phone calls, call context, number search, phone-gateway sessions, contacts, phone tool |
| 12 | [12-packages-and-command-data.md](12-packages-and-command-data.md) | Package install/uninstall/revert, test install, mobile command-data browser, node tools view |
| 13 | [13-mobile-chat-inbox-settings.md](13-mobile-chat-inbox-settings.md) | Mobile SSE chat, inbox notifications, callbacks, household settings, settings definitions |

## Doc template (every subsystem doc uses these headings)

1. **Purpose.** What it is for, in user terms. Who uses it: a node, mobile, admin, another service, or a background loop.
2. **Entry points.** Routes (method, path, auth, live caller from PLAN Appendix A), MQTT topics, background loops (cadence), server tools, and internal callers.
3. **Behaviour.** A walkthrough of the main flows: sequence, branching and failure handling. Cite `file:line`.
4. **Data.** Tables and columns that matter, lifecycle and TTLs, in-memory state, files on disk.
5. **Settings.** The keys read, with defaults and effects.
6. **Dependencies.** Other CC subsystems, other services (llm-proxy, whisper, tts, auth, notifications, …), LLM calls (which prompt and which model slot), and third parties.
7. **Invariants and non-obvious behaviour.** Things a port must preserve, with `file:line`.
8. **Oddities.** Suspected dead code, bugs, half-finished features, and contradictions between docs and code.
9. **Tests.** Which test files cover this, and the candidates for golden fixtures or black-box contract tests.
10. **Questions for the user.** Numbered. Each question has: the question; *why it matters*; the options; **my recommendation**. Tag each `[scope]` (keep/cut/change), `[behaviour]` (what should happen) or `[minor]`. Put the most consequential first, with at most about 12.
11. **Go port notes.** Shape in Go, risks, and simplifications enabled by the single-binary design: embedded queue, embedded MQTT, SQLite, in-process calls instead of HTTP callbacks.
