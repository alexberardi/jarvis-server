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

_Built once all 14 drafts are in._
