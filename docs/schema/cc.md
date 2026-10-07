# Schema: module `cc` (command-center)

Migration: `internal/modules/cc/migrations/00001_baseline.sql` (goose, version table `goose_cc`), embedded by `internal/modules/cc/embed.go`. Test: `internal/modules/cc/migrations_test.go`.

**Source.** `pg_dump --schema-only` of `jarvis_command_center` on the MBP dev stack (10.0.0.103), 2026-10-06. `alembic_version` = `sb01signals`, which is the single head of `jarvis-command-center/alembic/versions` (57 revisions, repo at `3b32b73`). Nothing was derived from models alone. The dev DB matched `app/models.py` (42 tables + `alembic_version` + the config-client's `service_configs`).

**Result.** 31 tables (29 ported + 2 new). Legacy had 43 domain tables, of which 14 are dropped.

`cc_settings` is **not** in this migration. `internal/platform/settings` creates it (version table `goose_cc_settings`).

## Conventions

| Postgres | SQLite | Notes |
|---|---|---|
| `serial` / `integer` PK | `INTEGER PRIMARY KEY AUTOINCREMENT` | Used on `user_memories`, `conversation_transcripts`, `person_characterizations` and `signals`. AUTOINCREMENT keeps serial's never-reuse guarantee, because these ids are exposed to mobile. |
| `varchar(n)` / `text` / uuid strings | `TEXT` | Length limits are not enforced. Validate lengths in Go where the API depended on them. |
| `boolean` | `INTEGER` 0/1 | The `NOT NULL` and defaults are kept. `devices.is_controllable` and `is_active` stay nullable, as in legacy. |
| `timestamp without time zone` | `TEXT`, ISO-8601 UTC, `YYYY-MM-DDTHH:MM:SS.sssZ` | **Every legacy timestamp is naive UTC; none is tz-aware.** Server defaults use `strftime('%Y-%m-%dT%H:%M:%fZ','now')` in place of `now()`. Where legacy had no server default (an ORM-side default), there is none here either. |
| `double precision` | `REAL` | |
| JSON in `text` | `TEXT` | Unchanged, per PLAN §3.2. |
| `vector(384)` | `BLOB`, 384 × float32 little-endian = 1536 bytes, enforced by a CHECK | L2-normalised all-MiniLM-L6-v2 output. Memories are searched with brute-force cosine in Go (PLAN §3.2); because the vectors are normalised, cosine reduces to a dot product. **Since `00090_memory.sql` (LD6):** any whole number of float32s, tagged with `embedding_model`; search compares only current-model vectors and the embedding sweep re-embeds the rest (baseline rows carry no tag, so they are re-embedded). |
| app-level enums (varchar) | `TEXT` + `CHECK` | Only on closed state machines confirmed in code: node task kind/state, request statuses, OAuth session status, phone state/line type/contact source, errand/workflow/schedule state, `waiting_on`, routine `response_length`, attention `rung`, and transcript `user_rating`. Open sets get no CHECK and a comment instead: trace `request_type`/`source`, delivery `outcome`, memory `source`/`category`, device `source`, `install_mode`, `navigation_type` and Bluetooth `role`/`source`. |

- **Foreign keys.** All legacy FKs and their `ON DELETE` actions are kept. The attention cleanup depends on `cc_attention_deliveries → cc_attention_events` CASCADE, and `db.Open` sets `foreign_keys(1)` on every connection.
- **No cross-module FKs.** `user_id` and `household_id` point at the auth module but carry no FK, because modules migrate independently.
- **Indexes.** All legacy btree indexes are kept, renamed `cc_<table>_<cols>`; the unique constraints are renamed the same way. The partial `ix_user_memories_pinned` is kept as a partial index.
  - **Dropped:** `ix_user_memories_embedding_hnsw` (pgvector HNSW, partial). Search is exact brute force now. There were no GIN, ivfflat or trigram indexes.
  - **Added, for cascade and lookup performance:** an index on `node_id` for every FK child that lacked one, plus:
    - `cc_rooms_parent_room_id`, `cc_nodes_room_id`, `cc_devices_room_id`
    - `cc_phone_call_sessions_contact_id`
    - `cc_settings_requests_created_at` (24 h prune)
    - `cc_attention_events_created_at` (TTL cleanup)
    - `cc_user_memories_household`, and a partial index on `cc_user_memories.expires_at` (TTL purge)
  - **Changed:** `config_pushes` gets `(node_id, status)` for the `pending` fetch.
- **Column renames: none.** The `nodes."user"` column is quoted in SQL.

## Tables by subsystem doc

| Doc | Tables |
|---|---|
| 00 platform | none of its own. `cc_settings` comes from the platform. Legacy `service_configs` is dropped (discovery is in-process). |
| 01 voice / 02 tool loop / 03 prompts | none. Conversation state is in memory. |
| 04 memory | `cc_user_memories`, `cc_conversation_transcripts`, `cc_person_characterizations` (dormant, D30) |
| 05 nodes | `cc_nodes`, `cc_node_tasks`, `cc_provisioning_tokens`, `cc_settings_requests`, `cc_settings_snapshots`, `cc_request_traces` |
| 06 media / voice identity | none. Voiceprints belong to the stt module (D34/D36). |
| 07 smart home | `cc_rooms`, `cc_devices`, `cc_device_scan_requests`, `cc_device_list_requests`, `cc_config_pushes`, `cc_auth_sessions`, `cc_bluetooth_scan_requests`, `cc_bluetooth_pair_requests` |
| 08 routines | `cc_routines`, `cc_schedules` |
| 09 errands | `cc_errand_plans`, `cc_workflows` |
| 10 signals / attention / proposals | `cc_signals`, `cc_proposal_suppressions`, `cc_attention_events`, `cc_attention_deliveries`, **`cc_automation_actions`** (new), **`cc_reaction_claims`** (new) |
| 11 phone | `cc_phone_contacts`, `cc_phone_call_sessions` |
| 12 packages | `cc_package_install_requests` |
| 13 mobile chat / callbacks | `cc_callback_jobs` |

Scheduler triggers (D27) and durable jobs live in the platform tables `platform_triggers` and `platform_jobs`, not here. `cc_schedules.next_fire_at` and `cc_routines.schedule` remain the domain and mobile-facing projections.

## Dropped

| What | Why |
|---|---|
| `active_adapter`, `adapter_history`, `adapter_proposals`, `adapter_training_state`, `nodes.adapter_hash` | LoRA cut (PLAN §7, D9, doc 00 §7 item 5) |
| `attention_source_tiers`, `attention_consents`, `attention_feedback` | Never written (D18, D9) |
| `routine_executions` | Write-only audit; no reader (D40 08.Q11) |
| `schedules.title`, state value `paused` | Unused; nothing set `paused` (D40 08.Q12) |
| `prompt_provider_install_requests` | Community provider install is cut, and the route is a no-exec stub (D4, PLAN §7) |
| `test_install_requests` | Forge test install dropped (D5) |
| `errand_plans.routine_slug`, `cursor`, `results_json` | Vestigial after the plan/workflow split (D9 / 09.Q8). `expires_at` is **kept** for the 24 h draft TTL (D40 09.Q6). |
| `phone_contacts.overlay_json`, `phone_call_sessions.constraints`, `phone_contacts.source = 'web'` | Dead phone knobs (M12 / D47). `attempt_cap` was a setting, not a column. |
| `nodes.api_key` | Plaintext legacy node key. Node auth is the auth module's hashed credential (D40 05.Q10). |
| `service_configs` | Config-client cache; discovery is in-process (doc 00 §4) |
| `settings` | Created by the platform settings package as `cc_settings` |
| HNSW index on `user_memories.embedding` | Brute force (PLAN §3.2) |
| Situation-matcher state (D17) | It never had tables (it lived in memory), so nothing to drop |

## Added

| What | Why |
|---|---|
| `cc_node_tasks.reset_token`, `reset_token_consumed_at` (unique partial index on the token) | D10: the factory-reset token is persisted with the task. It is stored raw because jarvisd must be able to re-publish it after a restart (broker sessions are in memory, D40 05.Q7). `consumed_at` keeps `verify-reset`'s consume-once semantics (doc 05 §7 item 4) for as long as old node builds need it. |
| `cc_settings_requests.include_values`, `user_id` | D40 05.Q8 |
| `cc_request_traces.user_id` (+ index) | D20: account deletion hard-deletes the user's traces (the 04.Q3 recommendation) |
| `cc_phone_call_sessions.in_call_at` | D40 11.Q6: the minutes cap is measured from `in_call` |
| `cc_package_install_requests.verified_at` | D39: 5 min pickup deadline, then `expires_at = verified_at + 15 min`. No `action` column (D48). |
| `cc_errand_plans.state = 'expired'` | D40 09.Q6: draft TTL |
| `cc_automation_actions` | D7: an automation Confirm card carries only an opaque id; the action is stored server-side and run once after the household check |
| `cc_reaction_claims` (`WITHOUT ROWID`, PK `(household_id, claim_key)`, `expires_at`) | D40 10.Q5: leave-by claims and automation last-signature dedup are persisted with a TTL and claimed only on terminal outcomes |

**Deliberately not added:**

- `created_by_user_id` on routines: D41, routines run with no user.
- A per-model tag on voiceprints: that belongs to the stt module (D34).
- `cc_phone_call_turns`: doc 11 §11 lists it only as an option, so `transcript_json` stays.
- A source/origin column on routines for D44 Pantry-reported routines: not decided; the slug identifies them.

## Legacy import (`jarvisd import-legacy`)

General transforms:

- Every timestamp: naive → `…Z`, using the format above.
- Booleans → 0/1.
- `vector` → re-embed (PLAN §5 says memories are re-embedded). If the embedder stays all-MiniLM-L6-v2, the pgvector text form `[f1,…]` can be packed to float32 LE instead.
- Rows violating a new CHECK must be mapped or skipped, as described below.

| Legacy table | Import |
|---|---|
| `nodes` | Yes. Drop `api_key` and `adapter_hash`; node credentials come from the auth module's import. |
| `rooms`, `devices` | Yes, as-is. Camera rows import and stay listed (D29). |
| `routines` | Yes. Then seed the node default routines per household (D44). |
| `schedules` | Only `state = 'active'` (D13, D40 08.Q12). Drop `title`. |
| `user_memories` | Yes. Re-embed. **Skip `is_active = false` rows**: forget is a hard delete in Go (D40 04.Q6), and a soft-deleted memory is one the user asked to forget. Skip rows already past `expires_at`. |
| `conversation_transcripts` | Optional, only rows inside the 7-day TTL. Nothing breaks without them. |
| `person_characterizations` | Yes, as-is (prod: 0 rows). |
| `signals` | Unexpired rows only. |
| `proposal_suppressions` | Yes. Idempotency keys must stay byte-identical (doc 10 §11), which they do: they are copied, not recomputed. |
| `attention_events`, `attention_deliveries` | Yes, events within `attention.journal_ttl_days`. |
| `phone_contacts` | Yes. Drop `overlay_json`; map `source = 'web'` → `'manual'` (none expected, since current code never writes it). |
| `phone_call_sessions` | Terminal states only, as activity history (D42). Drop `constraints`; set `in_call_at` NULL. Non-terminal sessions are dropped (mark expired). |
| `config_pushes` | `status = 'pending'` and not expired, so in-flight K2 secret pushes still reach nodes. Consumed rows are not imported. |
| `node_tasks` | No. Open tasks are re-issued by the user. Legacy reset tokens were in memory and are lost anyway. |
| `errand_plans`, `workflows` | No. Drafts aren't imported, and prod has 0 plans and runs (D13). |
| `callback_jobs`, `settings_requests`, `settings_snapshots`, `provisioning_tokens`, `auth_sessions`, `request_traces`, every `*_scan_requests`/`*_list_requests`/`*_pair_requests`, `package_install_requests` | No. Short-TTL request/response rows (D40 12.Q7). OAuth sessions are 10-minute TTL, and their tokens already live on the node. |
| `routine_executions`, adapter tables, attention tier/consent/feedback, `test_install_requests`, `prompt_provider_install_requests`, `service_configs` | No: cut. |
| `settings` → `cc_settings` (platform) | Rename `llm.interface` → `llm.prompt_provider` (D11); an unknown provider name is a hard error, so validate it. Drop the keys cut by D3 (`voice.stickiness_*`), D11 (defined-but-unread), D17 (`proposals.proactive_enabled`), D18 (`attention.timezone`), D25 (`routines.scheduler_enabled`), D33 (short/long thresholds), D38 (`voice.emotion_*`), M7 (`smart_home.use_home_assistant`) and `tool_classifier.*` (D9). |
| notifications `inbox_items` (another module) | Delete `adapter_proposal` items on import (D49 00.Q7c). |

## Import blockers and open points

- **Embedder parity.** Re-embedding needs jarvisd's in-process embedder to exist at import time. Otherwise import NULL embeddings and let the 60 s embedding sweep (D11) fill them.
- **`auth_sessions` encryption.** Rows are not imported, so the legacy `JARVIS_TOKEN_ENCRYPTION_KEY` → dedicated-key re-encryption (D40 07.Q9) is avoided. If that changes, the importer needs the legacy key.
- **The node-key cutover** depends on the auth module importing hashed node keys. `nodes.api_key` is intentionally discarded.
- **Prod was not inspected** (off limits for this task). CHECK constraints were chosen from code and dev data. Run the importer's dry run against a prod snapshot to catch any out-of-set values (e.g. `node_tasks.state`, legacy errand states).
