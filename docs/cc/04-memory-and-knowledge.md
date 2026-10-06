# 04: Memory and knowledge

Scope: user memories (remember, recall, forget, pinned injection), agent-injected household context, passive memory extraction, the embedding sweep, characterization synthesis (the "person view"), the transcript buffer and its rating UI, the `DELETE /me/data` purge, deep research, and quick search.

All paths are relative to `jarvis-command-center/` unless noted otherwise. "CH" means `app/core/conversation_handler.py`.

---

## 1. Purpose

Jarvis remembers facts about the people it talks to and about the household.

- **User memories** are per-person facts, preferences and notes. They come from three places:
  - an explicit voice request ("remember that I take my coffee black") through the `remember` tool;
  - the mobile Memories screen;
  - a background LLM that reads recent conversation transcripts, called **passive extraction**.

  They reach the LLM in two ways. A small "User Profile" block is put into every turn, and the `recall` tool does semantic search when the model asks for it.
- **Household context** is weather, calendar, news and reminders. The node's background agents push it in through `POST /memories/inject`. It is stored as `user_memories` rows with `user_id IS NULL` and has a time-to-live (TTL). Two features read it: a per-turn vector-matched "you already know…" block, and the ambient snapshot.
- **Characterization** is one evolving prose document per person ("the view of the person"). A background LLM synthesizes it from that person's facts and recent transcripts. It can optionally be appended to the system prompt.
- **Transcripts** are a 7-day buffer of user and assistant exchanges. Passive extraction and characterization feed on it, and mobile shows it on the "Recent Commands" rating screen.
- **The `/me/data` purge** is CC's part of account deletion. jarvis-auth calls it.
- **Web knowledge** comes from two tools:
  - `quick_search` is an inline DuckDuckGo search plus a scrape of the top 2 results, as a blocking tool call.
  - `deep_research` searches, scrapes 3 or 6 pages, and asks the background LLM for a summary. The result goes to the notifications inbox and a push is sent.

**Users:**

| User | What it does here |
|---|---|
| Nodes | Voice turns, which run the tools; `/memories/inject` |
| Mobile | `/mobile/memories*` and `/transcripts/*` |
| jarvis-auth | `/me/data` |
| Background loops in CC | Five loops (section 2.3) |
| llm-proxy's queue worker | The callbacks |

## 2. Entry points

### 2.1 Routes (all under `/api/v0`)

| Method and path | Auth | Live caller (PLAN Appendix A) | Port? |
|---|---|---|---|
| `POST /memories/inject` | node `X-API-Key` **or** app-to-app (`app/api/memories.py:212-258`) | node-setup `clients/rest_client.py:190-230` (`inject_memories`) | **Yes** |
| `GET /mobile/memories` | user JWT and household role | node-mobile `src/api/memoriesApi.ts:49` | Yes |
| `POST /mobile/memories` | user JWT | `memoriesApi.ts:71` | Yes |
| `GET /mobile/memories/{id}` | user JWT | `memoriesApi.ts:60` | Yes |
| `PUT /mobile/memories/{id}` | user JWT | `memoriesApi.ts:84` | Yes |
| `DELETE /mobile/memories/{id}` | user JWT | `memoriesApi.ts:95` | Yes |
| `GET /transcripts/recent` | user JWT (`app/api/transcripts.py:88`) | node-mobile `transcriptsApi.ts:36` | Yes |
| `POST /transcripts/{id}/rate` | user JWT (`transcripts.py:101`) | `transcriptsApi.ts:48` | Yes |
| `DELETE /me/data` | user JWT (`app/api/me.py:40`) | jarvis-auth `services/account_deletion.py:30,120` | **Yes**. It collides with notifications' route of the same name, so it is served on the 7703 listener. |
| `GET\|POST /memories`, `GET\|PUT\|DELETE /memories/{id}` | provisioning auth (admin key or JWT) (`memories.py:71-172`) | none | **Cut** |
| `GET /characterizations`, `POST /characterizations/synthesize[?sync=true]` | provisioning auth (`app/api/characterizations.py:68,83`) | none | **Cut** (see Q4) |
| `POST /memory-extraction/callback` | callback bearer (`app/main.py:1957`) | llm-proxy | Becomes an in-process call |
| `POST /characterization-synthesis/callback` | callback bearer (`main.py:1997`) | llm-proxy | Becomes an in-process call |
| `POST /deep-research/callback` | callback bearer (`main.py:1937`) | llm-proxy | Becomes an in-process call |

Callback auth is `_verify_callback_auth` (`main.py:1850`). It checks the bearer `JARVIS_ADAPTER_CALLBACK_TOKEN` and **fails closed with 503** unless `JARVIS_ALLOW_INSECURE_CALLBACKS` is set. Every callback handler swallows its own exceptions and returns `{"status":"ok"}` (`main.py:1948-1954`, `1968-1974`, `2010-2016`).

### 2.2 Server tools

Tools are `IServerTool`s, executed synchronously by `tool_registry.execute_tool` (`app/core/tool_registry.py:105-133`).

| Tool | When it is offered (CH warmup) |
|---|---|
| `remember`, `forget` | A speaker is identified **and** `memory.enabled` (CH:356-362) |
| `recall` | The same, **and** `memory.recall_enabled` (CH:363-364) |
| `quick_search`, `deep_research` | `web_search.enabled` (CH:352-353). This is fail-closed, and each tool re-checks it at `execute()`. |

### 2.3 Background loops (`app/main.py`, `startup_event`)

| Loop | Cadence | Gate | Lines |
|---|---|---|---|
| Passive memory extraction | `memory.extraction_interval_seconds` (default 300), read every iteration; it sleeps *first* | Global `memory.extraction_enabled`. Unset means it runs. | 243-259 |
| Embedding sweep | `memory.embedding_interval_seconds` (default 60) | `memory.embedding_enabled`. Neither key is in `settings_definitions`, so the defaults in code apply. | 266-292 |
| Characterization synthesis | `characterization.synthesis_interval_seconds` (default 3600) | Global `characterization.synthesis_enabled`. It must be explicitly true. | 298-316 |
| Transcript TTL | every 86400s | `memory.transcript_ttl_days` (7) | 319-335 |
| Expired-memory deactivation | sleeps 60s, then every 1800s | none | 360-376 |

### 2.4 Internal callers (other subsystems that read this data)

- **Voice warmup** (01):
  - pinned/profile memories go into `node_context["user_memories"]` (CH:399-431);
  - the characterization goes into `node_context["characterization"]` (CH:433-462);
  - the ambient bundle reads household memories in the categories `weather`, `calendar` and `reminder` (CH:471, 3192-3285).
- **Per turn** (01):
  - the speaker block `build_speaker_block(name, memories)` (CH:3477-3555, `core_rules.py:280-330`);
  - agent context, appended to the user message (CH:909-912, 967-968);
  - the `_apply_characterization_swap` reconciliation (CH:180-210, called at CH:870, 1260, 1690).
- **Transcript writers:**
  - `_log_transcript` (`main.py:1124-1157`), called **only** from non-stream `POST /voice/command` (`main.py:1265`);
  - `_log_mobile_transcript` (`app/api/mobile_chat.py:60-87`, called at 373).

  **No streaming voice path writes a transcript.** See section 8.

## 3. Behaviour

### 3.1 Memory scopes

There is one table, `user_memories`, and the scope is encoded by `user_id`:

| Scope | `user_id` | Written by | Read by |
|---|---|---|---|
| Personal | the speaker's or JWT user's id | `remember` (source `voice`); passive extraction (source `passive`); mobile `scope=user` (source `ui`); inject with `user_id` set | the profile block, `recall`, `forget`, mobile, the extraction context, characterization facts |
| Household | `NULL` | inject (source `agent`, or whatever the agent sends); mobile `scope=household` (POWER_USER or above) | agent context (vector), the ambient bundle (latest per category), mobile (POWER_USER or above) |

- There is **no speaker or voice-profile scope** separate from `user_id`. The speaker id *is* the auth user id, resolved by whisper and speaker stickiness (chapter 06).
- Every query also filters `household_id`, so the same user in two households has disjoint memories.

### 3.2 Save and upsert (`memory_service.py:88-158`)

1. **Matching.** If a `key` is supplied, the service looks for an *active* row with the same `(household_id, key, user_id)`, using `IS NULL` for household rows (115-126). If one exists:
   - it overwrites `content`, `category`, `source`, `is_pinned` and `expires_at`, and bumps `updated_at`;
   - **it NULLs `embedding` only if the content changed** (131-132), and the sweep re-embeds the row later.
2. Otherwise it inserts a new row. Without a key, every save inserts.
3. **Embedding at write time differs by path:**

| Path | When the embedding is written |
|---|---|
| `remember` | Immediately and synchronously (`remember_tool.py:111,118-143`) |
| inject | Batch-embedded after the save (`memories.py:378-413`) |
| extraction | Embedded *before* the save, for dedup (`memory_extraction_service.py:286-321`) |
| mobile create | Never. It relies on the sweep. |

### 3.3 Prompt injection: what the model actually sees

**A. The "User Profile" block** (every turn, trailing system message).

- `get_memories_for_prompt(user, hh, max_chars=memory.pinned_max_chars=500)` (`memory_service.py:536-580`):
  - it uses **pinned** active memories;
  - **if the user has zero pinned memories, it falls back to *all* active memories** (557-560). Only mobile can set `is_pinned`, so this fallback is the normal case.
- Sort order:
  - category priority first: `preference`=0, `fact`=1, `note`=2, `general`=3, anything else=99 (`:23`);
  - then `updated_at` descending.
- Each memory becomes a line `- {content}`, and lines are added greedily until the next line would exceed `max_chars`. There is no top-k and no similarity filter.
- **Loading.** The text is loaded at warmup for the predicted speaker (CH:399-431). It is reloaded per turn **only when** the turn's speaker id differs from the warmup speaker (CH:3497-3500, 3564-3624). When memory is disabled, the reload writes an empty string, so one speaker's memories never carry into another speaker's turn (3589-3593).
- **Wrapping.** `build_speaker_block` wraps it as "You are speaking with X. User Profile — … answer DIRECTLY … do NOT call recall …" (`core_rules.py:310-330`). With no name and no memories it returns `UNKNOWN_SPEAKER_BLOCK`. The block is a trailing system message, so the cached prefix is untouched.

**B. The `recall` tool** (`recall_tool.py:49-152`).

- **Inputs.**
  - `category="general"` is treated as a wildcard (87-88).
  - `limit` is `memory.recall_max_results` (5); `threshold` is `memory.recall_similarity_threshold` (0.3) (188-205).
- **Search.**
  1. It embeds the query, then runs `search_memories`: cosine ≥ threshold, `ORDER BY` similarity, `LIMIT` k, scoped to the speaker's own rows only (`memory_service.py:239-302`).
  2. It falls back to substring search (words longer than 2 characters, `ILIKE` any word, score = matched words / total words; `memory_service.py:304-360`) **only when the embedding call fails or returns empty**. A vector search that returns zero hits above the threshold returns "no_results" and does not fall back (`recall_tool.py:109`).
- **Output.** `{content, category, similarity}` triples.

**C. Agent context** (per turn, appended to the user message).

- **Gate:** `model.advanced_context` must be true for the household (CH:3087-3100). The default is **False**.
- **Settings:** `memory.agent_context_enabled` (True), `_max_results` (5), `_max_chars` (500) and `_similarity_threshold` (0.25) (CH:3126-3147).
- **Search** (`AgentContextService.get_relevant_context`, `agent_context_service.py:43-108`):
  1. it embeds the utterance;
  2. it runs `search_household_memories` over `user_id IS NULL` rows that have not expired, with cosine ≥ 0.25 (`memory_service.py:366-420`);
  3. if that is empty, it falls back to substring search.
- **Output:** a header, "You already know the following — weave relevant facts into your answer:", followed by `- ` lines up to 500 characters.
- `PRIORITY_CATEGORIES` is now `[]` (`agent_context_service.py:35`), so the priority path is dead.

**D. The characterization tail.**

- **Gate:** `characterization.injection_enabled`, per household, default False, fail-closed (CH:3439-3475).
- At warmup it loads `rendered` (CH:441-462). `_get_system_prompt` stashes `_system_prompt_base` and appends `<person_view>…</person_view>` as the very last block (CH:3316-3330, `core_rules.py:447-473`).
- Each turn, `_apply_characterization_swap` replaces `messages[0]` with base + section. It is a no-op when the result is identical, which preserves the KV cache.

**E. The ambient bundle** is owned by 01 and 10. It reads only household memories of category `weather`/`calendar`/`reminder`, newest first, preferring content that starts with "current weather" (CH:3221-3248). It is gated by `ambient_context.enabled` *and* `memory.enabled` (CH:471).

### 3.4 Passive extraction (two-phase, `memory_extraction_service.py`)

**Phase 1: `run_extraction_batch`** (90-131), every tick:

1. `reset_stale_jobs(30)` clears `extraction_job_id` on unprocessed transcripts **whose `created_at`** is more than 30 minutes old (`transcript_service.py:115-135`).
2. `get_users_with_unprocessed()` returns the distinct `(user_id, household_id)` pairs with `is_processed=false AND extraction_job_id IS NULL`.
3. For each pair:
   - it takes up to **20** unprocessed transcripts, oldest first;
   - it builds "existing memories" from `get_memories_for_prompt(max_chars=1000)` (the same pinned-else-all logic);
   - it calls `_enqueue_extraction`.
4. **The prompt** (134-218) is:
   - system: `_EXTRACTION_SYSTEM_PROMPT` (17-72). It says to extract from user turns only, not to invent relationships, to skip secrets, and to use the `ttl_days` policy: permanent / 30 / 7;
   - user: `"Existing memories for this user:\n…\n\nRecent conversations:\n"`, then the transcripts joined with `\n---\n`. Each transcript is `User: …`, an optional `\nJarvis: …`, and an optional `\n[Tools called: a, b]`.
5. **The queue job:**
   - `job_type=chat`, `model="background"`, `temperature 0.0`, `reasoning_budget 0`, `ttl_seconds 600`;
   - `idempotency_key = job_id` (a fresh uuid4, so there is **no real dedup**);
   - metadata `{type, user_id, household_id, transcript_count}`.
6. The transcripts are stamped in-flight **after** a successful enqueue (231-233).

**Phase 2: `handle_extraction_callback`** (242-332):

1. If the status is not `succeeded`, it logs and returns; the transcripts stay in flight until the stale reset.
2. If the content is empty, it marks the job processed.
3. `_parse_extraction_response` (335-381):
   - it strips closed `<think>` blocks;
   - a truncated `<think>` is cut at the first `[`;
   - code fences are stripped;
   - it calls `json.loads`, falls back to the first `\[…\]` regex match, and requires a list;
   - it keeps only dict items with a non-empty `content`.
4. It batch-embeds every extracted `content`. For each item:
   - if a vector exists and `check_content_similarity(hh, vec, 0.9, user_id)` finds a near-duplicate among the **user's own** active, non-expired rows, it skips the item;
   - otherwise it saves with `source="passive"` and `expires_at = now + ttl_days` (an unparseable value means permanent; `_expires_at_from_ttl` 75-87), then writes the vector.
5. `mark_processed(job_id)`.

### 3.5 The embedding sweep

`embed_missing(limit=100)` (`memory_service.py:173-193`) takes up to 100 active rows with `embedding IS NULL`, makes one batched `/v1/embeddings` call, and writes each vector. It runs in `asyncio.to_thread` (`main.py:283`). Expired-but-active rows are included.

### 3.6 Characterization synthesis (`characterization_synthesis_service.py`)

1. `run_synthesis_batch(max_users=50)` (70-119):
   - for every distinct `(user, household)` that has *any* transcript, it compares the newest `created_at` with `char.last_transcript_at` and skips the pair if nothing is new;
   - it then gathers its inputs: **all** active facts (`- [category] content`), the last `characterization.max_transcripts` (50) transcripts oldest first (user and Jarvis lines only, no tool names), and the prior `body` JSON (192-218).
2. The prompt is `_SYNTHESIS_SYSTEM_PROMPT` (35-62), with the output JSON `{summary, current_focus[], communication_style[], relationships[], constraints[], confidence, rendered}`.
3. The job is enqueued with `temperature 0.4`, `ttl 600` and **no `reasoning_budget`**, and metadata carries `last_transcript_at`. There is no in-flight tracking (252-315).
4. The callback (318-358) parses the first `{…}` object and calls `CharacterizationService.upsert`. That overwrites `body`, `rendered`, `confidence` and `last_transcript_at`, and bumps `version`. No history is kept (`characterization_service.py:39-88`).
5. `synthesize_for_user_sync` (134-175) calls the background model inline with `max_tokens 3000`. It is used only by the route that is being cut.

### 3.7 Agent inject (`memories.py:261-420`)

1. **Household.** It is resolved from the body or the node. A node must not name another household: a mismatch returns 403 (288-293).
2. **Gates:**
   - `memory.enabled` false returns **409**;
   - `model.advanced_context` false returns **200 with all counts 0, and nothing is stored** (308-317);
   - a settings exception means proceed.
3. **Dedup layer 1:** last write wins per `(key, user_id)` within the batch.
4. **Dedup layer 2:** each item is upserted by key via `save_memory`, with `expires_at = now + ttl_hours`. `ttl_hours` is in (0, 720] and defaults to 24.
5. **Dedup layer 3:**
   - it batch-embeds and writes all vectors;
   - then, for each item, it runs `check_content_similarity` against the *household* (`user_id IS NULL`) pool;
   - on a hit with a different id it deactivates the older row.
6. Limits: at most 100 items per batch; `content` up to 2000 characters; `key` up to 500 characters (180-193).

### 3.8 Forget and delete

- `forget` runs `forget_memory(user, hh, content_match=…)`, which matches with `ILIKE %match%` on the speaker's own active rows only and **soft-deletes** them (`is_active=false`) (`memory_service.py:195-237`). Keys are never matched from the tool.
- Mobile and admin DELETE also soft-delete (`mobile_memories.py:304-319`).
- `cleanup_expired` soft-deletes rows past `expires_at` (582-605).
- **Nothing ever hard-deletes a memory except the `/me/data` purge.**

### 3.9 Mobile permission matrix (`mobile_memories.py:1-127`)

- **Role.** `resolve_household_role` round-trips to auth on every request.

  | Role | Read | Write |
  |---|---|---|
  | MEMBER | own | own |
  | POWER_USER | own, plus household | own, plus non-agent household rows |
  | ADMIN or superuser | everything in the household | everything in the household |

  A row counts as agent-injected when `source=="agent"` or `category=="agent_context"`.
- **Create:**
  - `source` is forced to `"ui"`;
  - `scope=household` requires POWER_USER or above;
  - `category=agent_context` requires ADMIN.
- **Responses:**
  - a row from another household, or one the caller can't read, returns **404**, so its existence is not leaked;
  - a write the caller can't make returns 403, with a specific message for agent rows;
  - every row carries an `editable` flag.
- The list excludes inactive and expired rows.

### 3.10 The `/me/data` purge (`me.py:40-54`, `user_data_service.py:27-92`)

**Who calls it.** jarvis-auth's `DELETE /auth/me` fans out *before* it deletes locally:
- 2xx or 404 counts as OK;
- a connection error or timeout (5s) means **continue** with the deletion;
- a 5xx or other 4xx **aborts** the deletion (`jarvis-auth/.../account_deletion.py:118-170`).

**What CC deletes,** in one transaction, keyed only on `user_id` from the JWT `sub`:
1. `request_traces` whose `conversation_id` appears in the user's transcripts. The ids are collected first, because traces have no `user_id`.
2. All of the user's `user_memories`, in **every household**, active or not.
3. All of the user's `conversation_transcripts`.
4. `settings` rows where `user_id` = the user.
5. `auth_sessions` where `user_id` = the user.

It commits, returns the per-table counts, and on error rolls back and re-raises, which gives a 500 and so aborts the auth deletion. After that it calls `WhisperClient.delete_all_voice_profiles(user_id)` as a best-effort step that swallows errors (`me.py:23-37`). The endpoint returns **204** and is idempotent.

**What it does *not* delete** is listed in section 8, item 3.

### 3.11 Deep research (end to end)

1. **The tool** (`deep_research_tool.py:76-139`):
   - it validates `query`;
   - it gets `household_id` from the conversation cache;
   - it re-checks `web_search.enabled` (fail-closed);
   - `depth` must be `quick` or `thorough`, otherwise it becomes `quick`;
   - it calls `loop.create_task(_run_research_background)` and **returns immediately** with `{"status":"accepted","message":"Research started on: …. I'll send you a notification…"}`. The voice reply is therefore instant.
2. **The background task** (`deep_research_service.py:34-101`) has no timeout of its own:
   - it searches DDG through `ddgs` for 3 or 6 results;
   - it scrapes with `jarvis_web_scraper.WebScraper(FetchConfig(enable_jina_fallback=web_scraping.allow_external))` and `batch_fetch(max_concurrent=3, max_chars=6000)`. That gate is fail-closed and defaults off (216-259). The scraper blocks private hosts by default (`FetchConfig.block_private_hosts=True`);
   - it builds `## Source i: title\nURL\n\ntext`, joined with `---`;
   - it enqueues a background-model job with `temperature 0.3` and `ttl 600`. The metadata carries the query, depth, household, speaker, sources and timings.
3. **Any exception** in step 2, including no results or nothing scraped, sends a household push, "Research Failed" (`deep_research_tool.py:158-171`).
4. **The callback** (`deep_research_service.py:104-186`):
   - on failure or an empty summary it sends the failure push;
   - otherwise it strips `<think>` for the 200-character preview;
   - it POSTs to the notifications `/api/v0/inbox` with `category=deep_research`, `user_id=speaker` (which may be None) and the **raw** summary as `body` (`<think>` is not stripped from the body);
   - then it POSTs `/api/v0/notify` to the **whole household**: `target_type=household`, "Research Complete", `data.inbox_item_id`.
5. **Latency budget.** Nothing is enforced end to end. Search plus scrape takes seconds. The LLM job may wait in the queue for up to its 600s TTL, and the elapsed time is only recorded in the inbox metadata.

### 3.12 Quick search (`quick_search_tool.py`)

It runs synchronously **on the event loop**, because `execute_tool` is sync. The steps:

1. a fail-closed gate, which resolves the household from the conversation cache;
2. DDG returns 2 results;
3. for each result:
   - an `httpx.Client(timeout=8, follow_redirects=False)` fetches the page;
   - a manual redirect walk (at most 5 hops) re-validates every hop against `_is_blocked_host`. That check resolves DNS, blocks if **any** address is non-global, and fails closed when the name doesn't resolve. `Authorization`/`Cookie` headers are dropped when the host changes (67-149);
   - a crude regex strips the HTML to text, capped at 4000 characters;
   - if the scrape fails, it falls back to the search snippet.
4. It returns `{query, sources[{title,url,content}], elapsed_seconds}`, and the model synthesizes the answer inline.

The system-prompt text "you MUST call quick_search…" is contributed through `included_system_prompt_text` (171-180).

## 4. Data

| Table | Key columns | Lifecycle |
|---|---|---|
| `user_memories` (`app/models.py:368-388`) | `id`, `user_id` (NULL = household), `household_id`, `category` (free text: preference/fact/note/general/agent_context/weather/calendar/reminder/news…), `key` (≤255), `content`, `source` (voice/ui/passive/agent/…), `is_active`, `is_pinned`, `embedding vector(384)`, `created_at`, `updated_at`, `expires_at` | Soft-deleted only. Expired rows are deactivated every 30 minutes and kept forever. |
| `conversation_transcripts` (`models.py:461-487`) | `user_id`, `household_id`, `conversation_id`, `user_message`, `assistant_message`, `tool_calls_json`, `is_processed`, `processed_at`, `extraction_job_id`, `user_rating` (−1/0/1), `rating_notes`, `rated_at` | Hard-deleted when `created_at` < now − `transcript_ttl_days` (7), daily. **Rated rows are deleted too.** |
| `person_characterizations` (`models.py:391-421`) | unique `(user_id, household_id)`, `body` JSON text, `rendered`, `confidence`, `version`, `last_transcript_at`, `model` | Never deleted. |

**Indexes.**
- `ix_user_memories_lookup (user_id, household_id, is_active)` and `ix_user_memories_category` (`alembic/versions/d8017a84f38b`).
- An HNSW cosine index on `embedding` (`e9f0a1b2c3d4…:36`). It is approximate; Go brute force will be exact.

**Embeddings.**
- Model: **all-MiniLM-L6-v2**, 384 dimensions, **L2-normalized**.
- Location: llm-proxy's CPU `sentence-transformers` (`jarvis-llm-proxy-api/managers/embedding_manager.py:19,75-88`), reached through `POST /v1/embeddings` with app auth.
- CC client: `create_embeddings_sync`, a **blocking** httpx call with a 30s timeout (`app/core/llm_proxy_client.py:348-366`).
- Every threshold below assumes this exact model.

**Thresholds** (all cosine):

| Use | Threshold |
|---|---|
| recall | 0.3 |
| agent context | 0.25 |
| dedup (extraction and inject) | 0.9 |

**In-memory state.**
- In `node_context`, in the conversation cache: `user_memories`, `characterization`, `_system_prompt_base`, `ambient_context`.
- Deep research tasks are bare `create_task`s with no handle, so they are lost on restart before the enqueue.

## 5. Settings

| Key | Default | Scope read | Effect |
|---|---|---|---|
| `memory.enabled` | True | household (CH:3350) | Offers remember/forget/recall, loads the profile and ambient context, and gates inject (409). **Fail-open** on errors (CH:3362-3364). |
| `memory.recall_enabled` | True | household | Offers the `recall` tool. |
| `memory.pinned_max_chars` | 500 | global | Character budget of the profile block. |
| `memory.recall_similarity_threshold` / `recall_max_results` | 0.3 / 5 | global | Recall. |
| `memory.extraction_enabled` | **True** (`settings_definitions.py:197-201`) | **global only** | Passive extraction loop. |
| `memory.extraction_interval_seconds` | 300 | global | Loop sleep. |
| `memory.transcript_ttl_days` | 7 | global | Transcript deletion. |
| `memory.embedding_enabled` / `embedding_interval_seconds` | (undefined) on / 60 | global | Sweep. |
| `memory.agent_context_enabled` / `_max_results` / `_max_chars` / `_similarity_threshold` | True / 5 / 500 / 0.25 | household | Agent context block. |
| `model.advanced_context` | **False** | household | Master switch for agent context **and for whether inject stores anything**. |
| `ambient_context.enabled` | False | household | Ambient bundle (01/10). |
| `characterization.synthesis_enabled` / `_interval_seconds` / `max_transcripts` | False / 3600 / 50 | global | Synthesis. |
| `characterization.injection_enabled` | False | household, fail-closed | `<person_view>` tail. |
| `web_search.enabled` | False | household, fail-closed; mobile-settable (`mobile_household_settings.py:38`) | quick_search and deep_research. |
| `web_scraping.allow_external` | False | household, fail-closed; mobile-settable | r.jina.ai fallback in deep research only. |
| `network.public_url` | — | global | The callback URL. It goes away in Go. |

None of the `memory.*` or `characterization.*` keys are in the mobile household-settings allowlist (`mobile_household_settings.py:38-61`). Households can only change them through the superuser `/settings` API.

## 6. Dependencies

**Inside CC:**
- the conversation cache and speaker resolution (01, 06);
- `core_rules` builders (03);
- the settings service (00);
- the tool registry (02);
- the Signal Bus presence emit, which lives next to the speaker block (10).

**Other services:**

| Service | Used for |
|---|---|
| llm-proxy | `/v1/embeddings`; `/internal/queue/enqueue` with `model="background"` for extraction, synthesis and research; `chat_completion` for sync synthesis |
| jarvis-auth | `/internal/validate-node` and `/internal/app-ping` (inject auth); household role (mobile) |
| notifications | `/api/v0/inbox`, `/api/v0/notify` (deep research) |
| whisper | voiceprint purge |

**LLM prompts.** None of them use a prompt provider; all are raw messages to the background slot:

| Prompt | Location | Sampling |
|---|---|---|
| Extraction | `memory_extraction_service.py:17-72` | temp 0, thinking off |
| Synthesis | `characterization_synthesis_service.py:35-62` | temp 0.4, thinking default |
| Research summary | `deep_research_service.py:23-31` | temp 0.3 |

**Third parties:**
- DuckDuckGo, through `ddgs>=7` (`pyproject.toml:34`);
- `jarvis-web-scraper` pinned at `16d912fe` (`pyproject.toml:38`), which uses trafilatura with a BeautifulSoup fallback;
- optionally r.jina.ai.

## 7. Invariants and non-obvious behaviour (preserve)

1. **Never carry the previous speaker's memories.** When the turn speaker changes, `user_memories` is overwritten, and with `""` when memory is off (CH:3586-3593).
2. **The speaker, profile, ambient and agent blocks are all *after* the cached prefix.** `messages[0]` stays byte-stable. The only per-turn edit to `messages[0]` is the characterization swap, which always *replaces* the dict and never mutates it (CH:197-210).
3. **With characterization injection off, the prompt is byte-identical to the path without it** (CH:3316-3330). The same holds for the empty ambient and agent blocks.
4. **The User Profile wording "answer DIRECTLY … do NOT call recall" is load-bearing** for latency (`core_rules.py:316-322`).
5. **Upsert NULL semantics.** Household upserts match `user_id IS NULL`, not `= NULL` (`memory_service.py:121-125`).
6. **When content changes on upsert, the embedding is cleared,** so recall can never match stale text (`memory_service.py:129-132`).
7. **Recall's `category="general"` is a wildcard** (`recall_tool.py:82-88`).
8. **Extraction:**
   - temp 0;
   - `reasoning_budget 0`;
   - extract only from user turns;
   - unparseable `ttl_days` means permanent;
   - truncated-`<think>` salvage;
   - transcripts stay in flight on a failed callback.
9. **Inject auth:**
   - node first, then app;
   - a node cannot name a foreign household (403);
   - an app caller must supply `household_id` (400).
10. **Mobile:**
    - 404 (not 403) for rows that are invisible to the caller;
    - `source` is forced to `ui`;
    - creating or re-categorizing into `agent_context` is ADMIN-only;
    - the `editable` flag.
11. **`/me/data`:**
    - the user comes from the JWT only;
    - one transaction;
    - 204 even with no rows;
    - a voiceprint failure never fails the request;
    - a 5xx aborts the auth-side deletion, so a purge failure must surface as a 5xx.
12. **Web search:**
    - fail-closed at both the warmup whitelist and `execute()`;
    - the quick_search SSRF guard re-validates every redirect hop, fails closed on unresolvable names, and strips credentials when crossing hosts.

## 8. Oddities, bugs, and contradictions

1. **Streaming voice never writes transcripts.**
   - `_log_transcript` is only called from non-stream `/voice/command` (`main.py:1265`). That route is used only by install-e2e (Appendix A).
   - `/voice/command/stream` (1367), `/continue/stream` (2070) and `/continue` (2171) log nothing.
   - So passive extraction, characterization and the Recent Commands screen effectively see **only mobile chat**, plus e2e runs.
   - It also means `/me/data` can't find the trace ids of voice conversations (item 3). See Q1.
2. **CLAUDE.md is wrong that extraction is opt-in** (invariant 11). The default is `True`, and the gate treats unset as on (`main.py:251-252`). It is also **global only**: a household with `memory.enabled=false` still has transcripts logged and extracted into `user_memories`, which are then just not shown. See Q2.
3. **The purge misses user data:**
   - `person_characterizations`, which is a derived profile of the person;
   - `signals` (presence `user_id`);
   - `phone_call_sessions`, `errand_plans`, `workflows`, `schedules`, `callback_jobs`, `attention_feedback`, `proposal_suppressions` and `bluetooth_scan_requests` (all have `user_id`; `models.py`);
   - request traces from any conversation without a transcript (item 1, and anything older than the transcript TTL);
   - household memories the user authored (no author column);
   - the in-memory conversation cache.

   Two other gaps:
   - **in-flight extraction and synthesis callbacks** carry `user_id` in their metadata and will **re-create** memories or a characterization after the purge;
   - auth's 5s timeout is treated as "unreachable → continue", so a slow purge can be skipped silently.

   See Q3.
4. **Characterization swap leaks across speakers.** On a speaker change, `_resolve_speaker_into_context` reloads memories but **never** `node_context["characterization"]` (CH:3564-3624; the only writer is CH:454). The "swap" therefore keeps the *warmup speaker's* `<person_view>` for a different person. This is latent while `injection_enabled` defaults off.
5. **Pinned-else-all cliff.**
   - Nothing pins by default: neither `remember` nor extraction set `is_pinned`. So the profile is "all memories, priority-sorted, cut at 500 characters".
   - The moment a user pins one memory in mobile, every unpinned fact disappears from the prompt (`memory_service.py:555-560`).
   - The same function also feeds the extraction dedup context. See Q5.
6. **Key upsert clobbers user intent.** `save_memory` overwrites `is_pinned` (default False), `source` and `expires_at` on a key match (`memory_service.py:133-137`). So a passive extraction or `remember` reusing a key **unpins** a user-pinned row and can make a permanent fact expire. LLM-chosen keys like `brother_name` also mean a second brother overwrites the first.
7. **Inject dedup layer 3 is effectively dead.** All new vectors are written *before* the similarity check. `check_content_similarity` does `ORDER BY similarity DESC LIMIT 1`, so it returns the new row itself at about 1.0, and the `existing.id != memory.id` guard then skips it (`memories.py:386-411`). This conclusion comes from reading the code; no test covers it.
8. **Stale-job reset keys on the transcript's `created_at`, not on the enqueue time** (`transcript_service.py:117-124`).
   - Any transcript older than 30 minutes is un-stamped on the next tick even if its job is still queued. That produces duplicate extraction jobs.
   - The late callback's `mark_processed(old_job_id)` then matches 0 rows.
   - The fresh-uuid idempotency key gives no protection.
9. **Gaps in expiry and staleness:**
   - `search_memories` (recall) **does not filter `expires_at`** (`memory_service.py:263-271`), unlike every other query. Expired personal rows are recallable until the 30-minute deactivation.
   - Mobile and admin `PUT` change `content` **without clearing `embedding`** (`mobile_memories.py:295-297`, `memories.py:146-148`), so recall matches the old text forever. Mobile creates rely on the sweep.
10. **Blocking calls on the event loop:**
    - `quick_search` (DDG plus up to 2×8s fetches);
    - every `create_embeddings_sync` from tools, the per-turn agent-context search (`agent_context_service.py:135-139`), and the `async` extraction callback;
    - `_verify_inject_auth`'s sync `httpx.get`.

    All of them stall every other request while they run.
11. **Deep research problems:**
    - the push goes to the **whole household** with the query in the body, even though the inbox item is user-scoped (`deep_research_service.py:363-371`);
    - the inbox `body` keeps any `<think>` content;
    - nothing persists the job before the enqueue;
    - the tool description says thorough is "5-8 sources", while the code uses 6.
12. **`recall`'s comment says it falls back when vector search "returns nothing", but it doesn't** (`recall_tool.py:108-109`).
13. **The inject gate couples two features.** With default settings (`model.advanced_context=False`), `/memories/inject` returns 200 with zeros and stores nothing. The ambient bundle therefore only ever sees weather and calendar if `advanced_context` is also on, even though only `ambient_context.enabled` gates the ambient feature itself.
14. **Inject trusts the item's `user_id`.** A node or app can write personal memories for any `user_id`; there is no membership check (`memories.py:185,357-365`).
15. **Smaller items:**
    - characterization synthesis does not set `reasoning_budget: 0`, so it thinks on the shared GPU;
    - synthesis has no in-flight tracking, so a backed-up queue re-enqueues every hour;
    - `_gather` reads all 50 transcripts even when only one is new;
    - the callback's missing metadata defaults to `user_id=0`.
16. **Dead code and stale documentation:**
    - `PRIORITY_CATEGORIES=[]` and `_get_priority_context` are dead;
    - `remember` defaults `category="general"`, which is outside its own enum;
    - CLAUDE.md calls the memory cleanup "removes"; it soft-deletes.
17. **Rating data is lost.** Transcript ratings (whose stated purpose is the "Phase 3 training-data extractor") are hard-deleted after 7 days along with everything else. The consumer was the LoRA pipeline, which is cut. See Q8.

## 9. Tests

| Area | Files |
|---|---|
| Service and SQL | `tests/test_memory_service.py`, `test_memory_flow.py` (including user-scoped vs household dedup, 254-264), `test_memory_ttl.py`, `test_memory_embedding_integration.py` (538 lines) |
| Routes and authz | `test_memory_api.py`, `test_memories_authz.py`, `test_mobile_memories.py` (436 lines, the role matrix), `test_transcripts_api.py`, `test_transcript_service_ratings.py` |
| Extraction and characterization | `test_memory_extraction_enqueue.py`, `test_characterization.py`, `test_characterization_wiring.py`, `test_characterization_injection.py` |
| Purge | `test_me_data_purge.py`, `test_user_data_purge.py`, `test_me_data_voiceprint_purge.py` |
| Tools and web | `test_remember_forget_tools.py`, `test_web_search_gate.py`, `test_quick_search_ssrf.py`, `test_deep_research_jina_gate.py` |
| Evals (live models, GPT judge) | `tools/memory_extraction_eval.py`, `tools/memory_recall_eval.py`, `tools/memory_eval_scenarios.json` (18 extraction and 18 recall scenarios) |

**Golden fixtures (Python → JSON):**
- `_parse_extraction_response` and `_parse_synthesis_response` over a corpus of messy outputs (think and truncated-think, fences, prose around the JSON);
- `_expires_at_from_ttl`;
- `get_memories_for_prompt` ordering and truncation;
- the substring scorer;
- `build_speaker_block`, `build_characterization_section` and `AgentContextService._format_results` byte-exact;
- the extraction user message for a fixed transcript set;
- the SSRF `_is_blocked_host` table.

**Embedding parity:** for about 500 strings, cosine(Go vector, Python vector) ≥ 0.99. Also check that recall and agent hit sets at 0.3 and 0.25 match on a seeded corpus.

**Black-box contract:**
- the mobile role matrix (MEMBER, POWER_USER, ADMIN × own, household, agent × each verb, including the 404-vs-403 split);
- the inject response counts and the 400/403/409 cases;
- `/me/data` returning 204, idempotent, and 401 without a JWT;
- the transcripts `limit` bounds (1..200), and rate values outside −1/0/1 returning 400.

## 10. Questions for the user

1. **[behaviour] Should streaming voice turns be logged as transcripts?**
   - *Why it matters.* Today only mobile chat and the e2e-only non-stream route log (section 8.1). Passive extraction, characterization and "Recent Commands" are therefore close to inert for voice, which is the main way people use Jarvis. Turning logging on in Go would sharply increase what Jarvis silently learns and stores.
   - *Options:*
     - (a) log every voice turn with an identified speaker, as the design clearly intended;
     - (b) keep the parity bug;
     - (c) log, but only when the household opts in.
   - **Recommendation:** (a), together with the per-household gate from Q2. Log only turns with a confident speaker id, not ones resolved through the stickiness fallback.

2. **[behaviour] Should passive extraction be per-household opt-in, or stay globally on by default?**
   - *Why it matters.* The default is on and global (section 8.2). A household that turned `memory.enabled` off is still mined. That is a privacy surprise and contradicts CLAUDE.md.
   - *Options:*
     - (a) honour `memory.enabled` and a new household-scoped `memory.extraction_enabled` (default on), and skip transcript logging entirely when memory is off;
     - (b) the same, with default off;
     - (c) keep as is.
   - **Recommendation:** (a), and add both keys to the mobile household-settings allowlist so a household admin can turn them off.

3. **[behaviour] What exactly must `/me/data` erase?**
   - *Why it matters.* Characterizations, signals, phone sessions, errands, workflows, schedules, callback jobs, feedback, suppressions and most voice traces survive today. In-flight jobs can also re-create memories after the purge (section 8.3).
   - *Options:*
     - (a) purge every table with a user column, plus a tombstone so late job completions for a purged user are dropped;
     - (b) purge only the personal-profile data (memories, transcripts, characterization, traces) and anonymize the operational rows by setting `user_id` to NULL;
     - (c) keep today's list.
   - **Recommendation:** (b). Add a `user_id` column to traces in Go, and cancel queued jobs whose dedup key names the user.
   - *Also decide:* should memories in a **solo household that auth auto-deletes** (including `user_id IS NULL` rows) be purged too? Today they are orphaned.

4. **[scope] Is characterization still wanted?**
   - *Why it matters.* Synthesis and injection are off by default, the inspection routes have no caller (Appendix A), and the speaker-change swap shows the wrong person's view (section 8.4). It is also a derived profile that "forget" never reaches: a fact you tell Jarvis to forget lives on in the `body`.
   - *Options:*
     - (a) cut it entirely: table, loop, prompt and tail;
     - (b) port it dormant (off), fix the swap, and make `forget` and purge trigger a re-synthesis or deletion;
     - (c) port it and turn it on.
   - **Recommendation:** (a) unless you're actively evaluating it, because it is the most complex consumer of the background slot and has no user-facing surface.

5. **[behaviour] What should the prompt "User Profile" contain?**
   - *Why it matters.* Pinning is effectively unused, so today it is "all facts by category, then recency, cut at 500 characters". Pinning one item hides everything else (section 8.5). As passive memories accumulate, the 500-character cut decides which facts Jarvis "knows" without calling `recall`.
   - *Options:*
     - (a) keep pinned-else-all exactly;
     - (b) always pinned first, then fill the rest of the budget with unpinned memories by priority and recency;
     - (c) pinned plus the top-k memories by similarity to the utterance, per turn.
   - **Recommendation:** (b). It is deterministic and cache-friendly and removes the cliff. Option (c) costs an embedding on the hot path.

6. **[behaviour] Should forget and delete stay soft-deletes?**
   - *Why it matters.* "Forget that" and mobile delete only set `is_active=false`. Expired agent rows also accumulate forever. A user who says "forget" probably expects the text to be gone.
   - *Options:*
     - (a) hard-delete on user forget and delete, and hard-delete expired rows after N days;
     - (b) soft-delete, then purge after 30 days;
     - (c) keep soft-delete forever.
   - **Recommendation:** (a) for user-initiated deletes, and (b) for TTL expiry, which also bounds the brute-force scan.

7. **[behaviour] How should a key collision on upsert behave?**
   - *Why it matters.* A passive or voice upsert currently unpins a user-pinned row, overwrites `source="ui"` and can add an expiry to a permanent fact. LLM keys like `brother_name` also collapse distinct facts (section 8.6).
   - *Options:*
     - (a) preserve `is_pinned` and a user-authored (`ui`) row against passive writes; a passive write never shortens an existing permanent expiry;
     - (b) scope passive keys (`passive:` prefix) so they never hit voice or UI keys;
     - (c) keep as is.
   - **Recommendation:** (a), plus never letting passive overwrite `source=ui`.

8. **[scope] Do transcript ratings still matter now that LoRA is cut?**
   - *Why it matters.* The rating UI is live in mobile, but its only consumer was the training extractor, and rated rows are deleted after 7 days anyway.
   - *Options:*
     - (a) keep the routes as is;
     - (b) keep them, and exempt rated rows from the TTL for an offline eval corpus;
     - (c) drop the routes, which needs a mobile change.
   - **Recommendation:** (a) for contract parity. Decide (b) separately if you want a feedback corpus.

9. **[behaviour] Deep research delivery and limits.**
   - *Why it matters.* The completion push goes to every household device with the query text. Research has no overall deadline, and it is lost on restart before the enqueue.
   - *Options:*
     - (a) push to the speaker (or the household if the speaker is unknown), make the whole pipeline one durable job type with a 10-minute deadline, and send a failure push on timeout;
     - (b) keep household-wide delivery.
   - **Recommendation:** (a). Also strip `<think>` from the stored body.

10. **[behaviour] Should `/memories/inject` store anything when `model.advanced_context` is off?**
    - *Why it matters.* By default inject is a 200 no-op, which starves the ambient snapshot (section 8.13).
    - *Options:*
      - (a) always store, and let `advanced_context` gate only per-turn injection;
      - (b) keep the coupling.
    - **Recommendation:** (a). Embedding cost is trivial in-binary.
    - *Also:* should inject reject `user_id`s that aren't household members? Recommendation: yes.

11. **[minor] Should recall fall back to substring search when the vector search finds nothing above the threshold?** Today it falls back only when the embedder errors. **Recommendation:** yes; match the comment and union the results. It is cheap and helps name lookups ("Leo") that MiniLM scores low.

12. **[minor] What should happen to memories when a user leaves a household?** Their rows keyed to that household stay. **Recommendation:** hard-delete that user's rows for that household on leave. That needs an auth→CC hook, which is in-process in Go.

## 11. Go port notes

**Package shape.** `internal/cc/memory` contains:
- `Store`, using sqlc over `cc_user_memories`, `cc_conversation_transcripts` and `cc_person_characterizations`;
- `Embedder`, an interface with one implementation;
- `Search`;
- `Extractor` and `Synthesizer` as job handlers;
- the `Purge` function;
- the HTTP handlers for the 9 live routes.

The tools live in `internal/cc/tools/{remember,recall,forget,quicksearch,deepresearch}.go` against a `MemoryAPI` interface.

**Vectors.**
- Store each vector as a 1536-byte float32 little-endian BLOB, already L2-normalized, so cosine is a dot product.
- Search loads candidates with `WHERE household_id=? AND user_id IS ?/=? AND is_active AND (expires_at IS NULL OR expires_at>now)` and scores them in Go.
- Thousands of rows per household take well under 1 ms.
- Keep exact (not HNSW) semantics. Results may differ slightly from today's approximate index, but they will be better.
- Apply the `expires_at` filter **everywhere**, including recall (fixes section 8.9).

**Embeddings.**
- Use the same model: all-MiniLM-L6-v2 with mean pooling and L2 normalization. Run it either as `llama-server --embedding` with a MiniLM GGUF or through sherpa-onnx / onnxruntime in-binary.
- The thresholds 0.25, 0.3 and 0.9 are calibrated to this model, so gate the choice on the parity test (section 9).
- In-binary ONNX through purego is preferred, so that embeddings never contend with the LLM engine.
- **Do not import Postgres vectors.** Re-embed everything with the sweep on first start, as PLAN §3.3 says.

**Jobs.** These run on the embedded queue and replace the four callbacks.

| Job | Dedup key | Retries |
|---|---|---|
| `memory.extract` | `extract:{user}:{hh}` | 2 |
| `character.synthesize` | `char:{user}:{hh}` | none |
| `research.run` | the whole search → scrape → summarize chain as one job | none |

- `memory.extract` claims transcripts transactionally by stamping `extraction_job_id` in the same transaction as the enqueue, and on a lease expiry it releases them by **job age, not transcript age** (fixes section 8.8).
- `research.run` has a 10-minute deadline and sends a failure push on timeout or error.
- All LLM jobs go through the "background LLM" concurrency class (cap 1).
- The periodic loops become scheduled enqueues. Settings are still re-read on every tick.

**Hot path.**
- Profile and characterization loads stay off the critical path: they are read at warmup and copied into the conversation context.
- A per-turn agent-context embedding must run with a short timeout (for example 150 ms) and drop the block on timeout.
- Nothing blocks a shared loop, because Go handlers are goroutines, so section 8.10 disappears by construction. `quick_search` still needs an overall deadline of about 12s.

**Speaker change.** Reload both memories **and** the characterization in one `ResolveSpeaker(ctx, userID)` (fixes section 8.4 even if Q4 keeps the feature).

**Purge.**
- Run it as a single SQLite transaction over the table list decided in Q3.
- Then cancel or tombstone queued jobs for that user.
- Then evict the conversation-cache entries whose speaker is that user.
- Then delete the voiceprints. Speaker ID is in-binary now, so this becomes part of the same purge, not a best-effort HTTP call.
- Map an error to 500 so that auth (in-process, or external during the strangler phase) aborts.

**Web.**
- Port the SSRF guard as a `net.Dialer.Control` hook that checks the *connected* IP. That is stronger than resolve-then-fetch, because it also closes DNS rebinding.
- Use a `CheckRedirect` that strips credentials when crossing hosts.
- DDG has no official API: `ddgs` scrapes `html.duckduckgo.com`. Go needs its own small HTML-endpoint client, and that is the brittlest part of this chapter, so budget a fixture-based test.
- For readability extraction use `go-shiori/go-readability` (the trafilatura equivalent), and keep the regex stripper for quick search for parity, or unify on readability.

**Simplifications:**
- The callback routes, tokens, `network.public_url`, `LLM_PROXY_INTERNAL_TOKEN` and the sync-synthesis path all go away.
- The notifications inbox and push become in-process calls to the notifications module.
