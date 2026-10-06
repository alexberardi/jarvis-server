# llm 03: queue and jobs

This doc covers async LLM work today: enqueue → Redis/RQ → worker → callback. It also covers how that work maps onto jarvisd's `internal/platform/queue`.

Sources:

- `/home/alex/jarvis/jarvis-llm-proxy-api`:
  - `api/queue_routes.py`
  - `models/queue_models.py`
  - `queues/redis_queue.py`
  - `queues/tasks.py`
  - `scripts/queue_worker.py`

## 1. Purpose

The queue keeps slow background LLM calls off the live slot and out of the caller's request. It also gives the "at most one background job at a time" backpressure that slow hardware needs.

In practice it is CC's way to run four background prompts on the `background` slot and get the answer back through an HTTP callback. Prod ran **3 jobs in 3 days**, all `chat` on background, all succeeded (prod worker logs 2026-10-03..06).

## 2. Entry points

**Enqueue route.**

| Route | Auth | Detail |
|---|---|---|
| `POST /internal/queue/enqueue` | app creds | `api/queue_routes.py:44-171`. CC also sends `X-Internal-Token`, which the route **ignores**. |

**Live producers.** All are CC, all are `job_type:"chat"`, and all use `model:"background"`. Each has a TTL of 600 s and uses `idempotency_key = job_id`.

| Producer | Site | Callback route → handler |
|---|---|---|
| Deep-research summarisation | `jarvis-command-center/app/services/deep_research_service.py:262-310` (`sampling.temperature 0.3`; no `reasoning_budget`) | `/api/v0/deep-research/callback` → `main.py:1937` → `deep_research_service.py:104` |
| Memory extraction | `memory_extraction_service.py:134-229` (temp 0.0, `reasoning_budget:0`) | `/api/v0/memory-extraction/callback` → `main.py:1957` → `:242` |
| Situation matcher | `situation_matcher_service.py:152-200` (temp 0.0, `reasoning_budget:0`, job_id `situation-<hex>`) | `/api/v0/situation-matcher/callback` → `main.py:1977` → `:219` |
| Characterization synthesis | `characterization_synthesis_service.py:252-311` (temp 0.4) | `/api/v0/characterization-synthesis/callback` → `main.py:1997` → `:318` |

Callback auth is `{auth_type:"bearer", token: JARVIS_ADAPTER_CALLBACK_TOKEN}`. CC checks it fail-closed (`main.py:1850`).

**Dead producers and job types.**

| Job type | Producer | Status |
|---|---|---|
| `adapter_train` | CC `main.py:1823` (`/api/v0/adapters/train`), `training_orchestrator.py:110` | LoRA; **cut** (PLAN §7) |
| `chat_completion` | jarvis-ocr-service `app/llm_queue_client.py:137-148` | **Unreachable.** Its callback router `validation_callback.py` is never mounted, and the payload would 422: it has no `created_at` or `idempotency_key`. The Go OCR module already dropped it (docs/cc and PLAN: "dead async-validation modules"). |
| `vision_inference` | none anywhere | Dead (01 §3.8) |

**Consumers.**

- **Worker process:** `scripts/queue_worker.py:158-163`, one RQ `Worker` on queue `LLM_PROXY_QUEUE_NAME` (default `llm_proxy_jobs`), so concurrency is 1.
- **Handler:** `queues.tasks.process_llm_job` (`tasks.py:41-58`). It dispatches on `job_type`: `adapter_train`, `vision_inference`, and everything else is chat.

## 3. Behaviour

### 3.1 Enqueue (`queue_routes.py:44-171`)

**1. Body validation.** The body is `EnqueueRequest` (`models/queue_models.py:69-82`). A missing required field is a 422.

| Field | Required | Default |
|---|---|---|
| `job_id` | yes | |
| `job_type` | yes | |
| `created_at` | yes | |
| `idempotency_key` | yes | |
| `request` (dict) | yes | |
| `callback{url, auth_type?, token?}` | yes | |
| `priority` | no | `"normal"`, ignored |
| `trace_id` | no | |
| `job_type_version` | no | `"v1"`, ignored |
| `ttl_seconds` | no | 86400 |
| `metadata` | no | |

**2. Request validation.**

- **Non-`adapter_train`:** `request` must parse as `QueueRequest` (`queue_models.py:45-56`). Its fields:

  | Field | Notes |
  |---|---|
  | `model` | |
  | `messages` | |
  | `response_format` | |
  | `sampling{temperature, top_p, max_tokens, seed}` | |
  | `timeouts{overall_seconds, per_attempt_seconds}` | |
  | `reasoning_budget` | |
  | `artifacts` | |

  - **There is no `tools` field.** A queued job can't do native tool calls.
  - A parse failure is 400 `invalid_request_error` / code `invalid_request`, with message `"Invalid request: <pydantic text>"`.
- **`response_format.type=="json_object"` with no `json_schema`:** 400 code `missing_schema` (`:83-97`).

**3. Expiry.** `created_at` is parsed as ISO (with `Z`), or as epoch seconds, or falls back to now (`:33-41`). If `created + ttl < now` the result is 400 code `expired`, `"Job already expired"`.

**4. Dedupe.**

- The key is `llmproxy:dedupe:{job_id}:{idempotency_key}` (`redis_queue.py:30-44`).
- **If the key exists:** answer `{accepted:true, job_id, deduped:true}`.
- **Otherwise:** `SET NX EX ttl_seconds`. Losing the race also returns `deduped:true`.
- **The window lasts `ttl_seconds` from enqueue, regardless of completion.** Re-sending the same pair after the job finished, but within the TTL, is still deduped.

**5. Enqueue.**

- The payload is `req.dict()` plus `received_at` and `queue_name` (`:139-143`).
- It is enqueued as `queues.tasks.process_llm_job` with `job_timeout=ttl_seconds` (`redis_queue.py:47-53`). RQ kills a job running longer than its TTL.
- On failure the dedupe key is deleted and the response is 500 code `enqueue_failed`.

**6. Response:** `{accepted:true, job_id, deduped:false}`.

### 3.2 Worker: chat job (`tasks.py:61-192`)

1. **Expiry re-check** (`:80-99`). **BUG:** it calls `_send_callback(callback, job_id, status=…, error=…, result=…, timing=…, trace_id=…)` without the required `job_type` and `metadata` keyword arguments. That raises a `TypeError`, so an expired job **never sends its `expired` callback**. RQ marks it failed and the caller never hears back. The same bug is at `:216-224`; the vision path at `:283-293` is correct.
2. **Payload logging.** The full payload, including user content, is printed to stdout (`:106`).
3. **Request construction** (`:114-129`). It builds `ChatCompletionRequest` with these fields:

   | Field | Source |
   |---|---|
   | `model` | forced to `"background"` |
   | `temperature` | `sampling.temperature`, then `request.temperature`, then **0.7** |
   | `max_tokens` | `sampling.max_tokens`, then `request.max_tokens` |
   | `top_p`, `seed` | `sampling` |
   | `response_format` | forwarded |
   | `reasoning_budget` | forwarded |

   **Not forwarded:** `include_date_context`, `tools`.
4. **Model service call.** POST `{model_service.url}/internal/model/chat` with `X-Internal-Token` and timeout `model_service.timeout_seconds` (default 60; prod 240) (`:131-149`). The fallback with no URL runs the model in-process (`:150-169`), which **is cut**.
5. **Result.**
   - On success: `{"content": <content>}`. **Only content is kept**: no usage, no `finish_reason`, no `date_keys`.
   - A non-200 raises and becomes error `{code:"exception", message:"Model service error <status>: <body>", traceback}`.
   - An `HTTPException` becomes `{code: <detail.error.type>, message: str(detail)}`.
6. **Dead code.** `per_attempt_timeout` is computed and **never used** (`:72-78`). `timeouts.overall_seconds` is never read. There are **no retries**: RQ's default is none, and a failure produces one `failed` callback.
7. **Callback** (`_send_callback`, `:351-426`).
   - **Envelope:**

     ```json
     {"job_id","job_type","finished_at":"YYYY-MM-DDTHH:MM:SSZ","status":"succeeded|failed","result":{"content":…}|null,"error":{"code","message",…}|null,"timing":{"processing_ms":n},"metadata":{…echoed…}}
     ```

   - **Headers:** `Content-Type: application/json` and `X-Trace-Id` (when `trace_id` is set).
     - `Authorization: Bearer <token>` is added when `auth_type=="bearer"`.
     - It also sends **the proxy's own app credentials** (`JARVIS_APP_ID`/`KEY`) whenever `auth_type` is empty, `internal` or `bearer`.
   - **Unsupported auth types.** Any other `auth_type` is recorded as unsupported and **no POST is made** (`:398-402`). An empty `url` is skipped.
   - **Delivery.** One attempt, timeout `queue.callback_timeout_seconds` (10 s), **no retry**. The outcome is only stored in RQ's job result.
   - **History.** A 2026-07 bug once dropped every bearer callback (comment `:382-387`).

### 3.3 What callers do with results

CC's four handlers read `status`, `result.content`, `error.message` and the echoed `metadata` (deep research also reads `deduped` from the enqueue response). They parse `result.content` as JSON themselves (docs/cc/04, docs/cc/10). Each handler then:

- stamps transcripts (memory extraction, characterization)
- writes the inbox and sends a push (deep research)
- creates a proposal (matcher)

## 4. Data

**Redis:**

- the RQ queue `rq:queue:llm_proxy_jobs` (prod: empty)
- `rq:failed:llm_proxy_jobs` (prod: 1 old entry)
- the dedupe keys (prod: 0 live)

**Durability gaps:**

- RQ jobs survive a worker restart only while they are queued.
- A job that is running when the worker dies goes to RQ's failed registry, with **no callback**.
- No job state lives in Postgres. Only `training_jobs`, which is cut.

## 5. Settings

| Key | Reader | jarvisd |
|---|---|---|
| `queue.name` / env `LLM_PROXY_QUEUE_NAME` | `queue_routes.py:141`, `redis_queue.py:23`. The worker reads **env only** (`queue_worker.py:160`), so changing the setting splits producer and consumer (**BUG**). | Drop |
| `queue.per_attempt_timeout_seconds` | `tasks.py:72-78`, unused | Drop |
| `queue.callback_timeout_seconds` | `tasks.py:413` | Drop. The callback job uses a fixed 10 s per attempt. |
| `model_service.timeout_seconds` | `tasks.py:138` | Becomes `llm.request_timeout_seconds` (05) |

## 6. Dependencies

- **Redis and RQ.** Gone in jarvisd: the platform queue replaces them.
- **Model service:** gone (01).
- **CC callbacks:** become in-process (PLAN App. A; docs/cc/00 §2, 04, 10).

## 7. Invariants

1. **One `background` job at a time by default** (PLAN §3.2). The live slot is never used by the queue: the worker forces `model="background"` (`tasks.py:119`).
2. **Dedupe on `(job_id, idempotency_key)` within `ttl_seconds` of first enqueue.** The second enqueue answers `deduped:true` (frozen by `TestLLMQueueEnqueue`).
3. **Validation errors and codes are exact:** `expired`, `invalid_request`, `missing_schema`, and 422 without `callback` (frozen).
4. **A job past its TTL is never run.** The caller gets a `failed`/`expired` result: the intended behaviour, which today is broken (§3.2.1).
5. **Callbacks carry the envelope keys exactly.** `metadata` is echoed verbatim; it is the caller's correlation channel.

## 8. Oddities and bugs

1. **BUG:** the expired-callback `TypeError` (`tasks.py:91-99,216-224`). Already noted in docs/contract/README.md.
2. **BUG:** `queue.name` is read by the API but not the worker (§5).
3. **No retries and no callback retry.** A transient model or callback failure loses the result. CC compensates partly by re-picking stale transcripts after a timeout (docs/cc/04 §8.8).
4. **`priority` and `job_type_version` are accepted and ignored.** `timeouts.*` is ignored.
5. **Payload logging** prints user transcripts to worker stdout (`tasks.py:106,231,300`) and to `logs/queue_worker.log` (`queue_worker.py:106-125`, rotating 10 MB × 5).
6. **`X-Internal-Token` on enqueue is ignored.** `LLM_PROXY_INTERNAL_TOKEN` is unset in prod anyway (prod env).
7. **Proxy credentials leak to callback URLs.** The proxy sends its own app key to whatever `callback.url` the caller names. **Go:** send jarvisd's app credentials only to URLs the caller is entitled to receive results on. Practically, keep sending them (OCR does the same, `internal/modules/ocr/jobs.go:384-414`), because callers are authenticated apps and D5 treats them as trusted infrastructure.
8. **Queued jobs can't use tools.** Signal automations therefore call the chat route synchronously (`signal_automation_executor.py:142`) instead of queueing. docs/cc/10 §11 proposes moving them onto the queue, which in-process needs no `QueueRequest` change: the Go job carries a full `ChatRequest`.

## 9. Tests

**Python:**

- `tests/test_queue_tasks.py` covers the worker paths.
- `tests/test_callback_dispatch.py` covers callback auth dispatch.
- CC has `test_memory_extraction_enqueue`, `test_async_job_callback_auth`, `test_situation_matcher_enqueue`, `test_situation_matcher_callback`.

**Contract:**

- Existing: `TestLLMQueueEnqueue`.
- To add: **`TestLLMQueueCallback`**. It enqueues with `callback.url = http://$JARVIS_CONTRACT_CALLBACK_HOST:<port>/cb` and freezes:
  - the envelope key set
  - `status`
  - `result.content` as a string
  - `metadata` echoed verbatim
  - `X-Trace-Id`
  - bearer auth

  An expired-job variant asserts the D8-fixed `failed` envelope with `error.code:"expired"` (jarvisd only; legacy sends nothing).

**Golden:**

- **G12, queue envelopes.** `_build_callback_envelope` for success, model-error and exception results. Enqueue-validation outcomes for a table of request bodies: the exact 400 messages, including pydantic's `Invalid request: …` text. The Go message only needs to be non-empty: the contract matches `NonEmptyString`, so the pydantic text itself is not reproduced.

## 10. Questions for the user

None. Retries, durability and the expired callback are D8 fixes, and the in-process conversion is already decided (PLAN App. A).

## 11. Go port notes

**Job type.** There is one queue job type, `llm.chat`:

| Property | Value |
|---|---|
| `Concurrency` | `llm.background.parallel` (default 1) |
| `Lease` | the request timeout + 60 s |
| `MaxAttempts` | 2 |
| `Backoff` | 10 s, 30 s |

Model and engine errors are retried once. 4xx answers (bad request, context overflow) are `queue.Permanent`.

**Payload.**

```go
type ChatJob struct {
    JobID, IdempotencyKey, JobType, TraceID string
    CreatedAt time.Time; TTL time.Duration
    Request   llm.ChatRequest            // Slot forced to Background
    Metadata  json.RawMessage
    Callback  *Callback                  // external callers only
    Notify    string                     // in-process completion handler name
}
```

**Dedupe.**

- The platform queue's `DedupKey` only guards *live* jobs (`internal/platform/queue/queue.go:3-11`).
- To keep the legacy "TTL window from enqueue" (invariant 2), the llm module adds a tiny table `llm_dedupe(key TEXT PRIMARY KEY, expires_at INTEGER)`. The insert is `INSERT … ON CONFLICT DO NOTHING` in the same transaction as `EnqueueTx`, and expired rows are purged by the queue's purge tick.
- **Decided:** this is the one place the legacy semantics differ from the platform default, and the contract freezes it.

**Expiry.** It is checked at enqueue (400) and again at run start. An expired job completes with the `expired` envelope (D8 fix).

**Completion.**

- **In-process callers** (CC memory extraction, characterization, deep research, matcher) register a Go handler:

  ```go
  llm.OnComplete("memory.extract", func(ctx, JobResult) error)
  ```

  The job stores the handler name in `Notify`, and on completion the worker calls it directly. **No HTTP, no bearer token, no `JARVIS_ADAPTER_CALLBACK_TOKEN`, no `network.public_url`** (docs/cc/04 §11).
- **Rather than a callback, the caller's own job type may simply call `llm.Chat` synchronously inside its job,** since both run in the same process under the queue's concurrency caps. **Decided:**
  - **CC's four producers** become their own job types (`memory.extract`, `characterization.synthesize`, `research.summarize`, `situation.match`, as docs/cc/04 and 10 propose). Each calls `llm.Chat(Slot: Background)` inline.
  - **A shared `llm.background` semaphore**, size `llm.background.parallel`, enforces "one background LLM call at a time" across all job types and synchronous background calls: errand planner, signal automations, phone draft, OCR vision, recipes over HTTP.
  - **`llm.chat` job type:** exists only for the external `/internal/queue/enqueue` route.
- **External callers.** A completion enqueues `llm.callback`, which POSTs the legacy envelope with bearer and app credentials and retries with exponential backoff up to 12 attempts. This is the same as the OCR module's callback job (`internal/modules/ocr/jobs.go:234-414`).

**Logging.** Log job ids and timings only, never message content (fixes §8.5).
