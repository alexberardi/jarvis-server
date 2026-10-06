# command-center spec: question queue and decisions

Questions are asked **one at a time** (user preference). Each answer is recorded here first and then folded into the owning subsystem doc. Queue order is scope-changing first.

## Decisions

| # | Date | Subsystem | Decision |
|---|---|---|---|
| D1 | 2026-10-06 | README / 07 | **Caddy is orphaned and not ported.** External OAuth (e.g. Nest) goes through the cloud relay bounce; local providers (HA) use CC's own callback over LAN HTTP. |
| D2 | 2026-10-06 | 06 / 01 | **Drop the "last speaker" concept entirely.** The user says it "was supposed to be dropped" and there's "no situation where we'd want that anymore". Go CC **ignores** `node_context.speaker_user_id` / `speaker_confidence` on `/conversation/start`, which today is the node's never-expiring module-global `_last_speaker_user_id`. Speaker identity comes only from the current turn's audio, identified in-process. No node change is required: the field is accepted and ignored. Pending: whether CC's own 30 s per-node **stickiness** goes too (see Q-queue). |

## Verified facts (resolve questions without asking)

| # | Date | Fact | Effect |
|---|---|---|---|
| F1 | 2026-10-06 | **The fastText tool router is OFF in prod.** `JARVIS_TOOL_CLASSIFIER_ENABLED` is unset (code default false), there is no classifier model file, and `docker logs --since 168h` shows 0 "Router predicted" lines. | The fast stream path, the tool-stream path, the router hint and the must-call guard are all dormant in prod. The doc 02 Q1 recommendation becomes: **don't port fastText.** PLAN §3.3's pure-Go fastText inference is likely unnecessary; confirm with the user. |
| F2 | 2026-10-06 | **PLAN Appendix A corrected.** Blocking `/voice/command` and `/voice/command/continue` are core node contract: node follow-up turns use them. | Fixed in PLAN.md (doc 01). |

## Queue

All 14 drafts are in. Each doc's §10 has its full questions with context and recommendations; the IDs below are `doc.Q#`.

There are about 166 raw questions. To keep each one asked worth real thought, they are reduced in three ways:

- **Policy questions (P1–P4)** settle whole classes at once. A policy answer applies to every listed question unless the user carves out an exception.
- **Scope and product questions (S, B)** are asked one at a time.
- **Minor questions** go to the user once at the end, as one list of recommendations to accept or override.

### Pending (asked, awaiting answer)

- **Q0. Speaker stickiness.** Follow-up to D2: should CC's own 30 s per-node stickiness go too, or stay scoped to one conversation? (06.Q1)

### P: policies (resolve many at once)

- **P1. Security holes: close them, keeping wire shapes?** Unauthenticated routes and missing household or node-binding checks get fixed in Go, and well-formed clients see no change.
  - Covers: 00.Q6, 02.Q5, 05.Q2, 05.Q6, 06.Q5, 07.Q3, 07.Q7, 10.Q1 (the trusted-exec part), 10.Q10, 11.Q9, 12.Q2–Q4, 13.Q4 (partly).
  - Also: the `/prompt-providers/install` code execution (03.Q2), the OAuth `exchange_url` SSRF (07), `/admin/cache` (already cut), and MQTT trust via per-node broker credentials and ACLs (05.Q1, 07, 12.Q3).
- **P2. Known bugs: fix, not replicate,** unless a frozen client depends on the buggy behaviour, in which case keep it and document it.
  - Covers: 01.Q4, 02.Q6, 02.Q7, 03.Q1 (date bugs), 03.Q6, 04 §8 list, 07 (Bluetooth missing routes = add), 08 timing bugs, 09 restart/cancel bugs, 10 dedup bugs, 11 oddities, 12.Q6, 13.Q10–11.
- **P3. Dead and unreachable code: cut it.** This means code with no caller, behind a flag that is off everywhere, or unreachable with the kept providers.
  - Covers: 01.Q3 (tool-stream path B), 02 dead modules, 03 dead date/prompt modules, 05.Q3 (tracked-reset path), 05.Q9, 06.Q3 (phone-mic routes), 07.Q5, 09.Q5, 10 (autorun gate, attention tier tables), 11.Q12, the fastText router (F1: 02.Q1, Q2, 03.Q7).
- **P4. Settings hygiene.** Drop defined keys that have no reader. Declare read-but-undefined keys with today's hard-coded defaults. Set the `llm.interface` default to a kept provider and map dropped names to the nearest kept one.
  - Covers: 00.Q3, 00.Q10, 02.Q11, 03.Q3, 03.Q12, 05.Q12, 07.Q11.

### S: scope and product (one at a time, most consequential first)

1. **Errands:** ship in v1, and how much? (09.Q1, Q4, Q7)
2. **Phone calling:** ship in jarvisd? Absorb the gateway? How do dial jobs get handed off? (11.Q1, Q2)
3. **Signals and proposals:** product intent. Automations acting without a tap, the proactive matcher, and the attention broker. (10.Q1–Q3)
4. **Memory:** log voice turns as transcripts? Passive extraction opt-in? (01.Q1, 04.Q1, 04.Q2, 00.Q5)
5. **`/me/data`:** exactly what must be erased? (04.Q3, 04.Q12, 06.Q11)
6. **Speaker identity:** unknown or ambiguous speaker in a multi-person home, and withholding per-user data. (06.Q2, 09.Q9, 11.Q10)
7. **Prompt path:** text (14B/8B) vs native (3.5-9B), or both? Gate the native path's server tools. Continue chaining. Unify providers? (01.Q5, 02.Q3, 02.Q4, 03.Q4, 03.Q5, 03.Q10)
8. **Routines:** where do they execute, should they speak, the scheduler default, and missed runs. (08.Q1–Q3, Q5)
9. **One scheduler engine** for routines, errands and loops, with persisted last-run times? (08.Q4, 00.Q9, 09.Q2)
10. **Smart home:** device source of truth, voice-control node routing, cameras and HLS. (07.Q1, Q2, Q6, Q8)
11. **Characterization:** still wanted? (04.Q4)
12. **Mobile chat:** real token streaming? Fold the inbox into one module? (13.Q1, Q2)
13. **Voice identity under new models:** keep raw enrollment audio, per-user or per-household voiceprints, default on, cut affect. (06.Q4, Q6–Q10, 01.Q7)
14. **Packages:** slow-install expiry, Forge test install, and the Pantry URL. (12.Q1, Q8)

### B: behaviour (one at a time, after S)

The remaining behaviour questions follow S, in doc order: 01.Q2, Q6, Q8, Q9, Q10; 02.Q8, Q9, Q10, Q12; 03.Q8, Q9, Q11; 04.Q5–Q10; 05.Q4, Q5, Q7, Q8, Q10, Q11; 07.Q9, Q10, Q12; 08.Q6–Q12; 09.Q3, Q6, Q10–Q12; 10.Q4–Q9; 11.Q3–Q8, Q11; 12.Q5, Q7, Q9–Q12; 13.Q3–Q9, Q12.

Several will already be settled by the P and S answers; those get skipped.
