# llm 02: HTTP API and streaming

This doc defines the kept external surface on port 7704, the frozen wire shapes and how errors are reported. It is cross-checked against `contract/llm_test.go` and `docs/contract/README.md` (the LLM rows and LEGACY-BUGs).

Source: `/home/alex/jarvis/jarvis-llm-proxy-api`, HEAD `e04d0b1`.

## 1. Purpose

Port 7704 stays an OpenAI-compatible endpoint, with Jarvis extensions, for callers outside jarvisd:

- jarvis-recipes-server
- the e2e suites
- a remote "LLM satellite" consumer
- jarvis-tts's deprecated shim, until TTS moves in-process
- anything a user points at it

Inside jarvisd, CC and OCR call the llm module through Go (§11). They no longer use HTTP.

## 2. Entry points: route inventory and disposition

**Auth names used in the tables:**
- **app** = `require_app_auth`, a round trip to auth `/internal/app-ping` (`auth/app_auth.py:11-58`).
- **combined** = `create_combined_auth`, which accepts a superuser JWT *or* app credentials.

### 2.1 Kept routes

| Method | Path | Auth | Live callers (non-test) | Prod use (3 days) |
|---|---|---|---|---|
| POST | `/v1/chat/completions` | app | CC `LLMProxyClient` (`jarvis-command-center/app/core/llm_proxy_client.py:86-300`, about 25 call sites), recipes (`jarvis_recipes/app/services/llm_client.py:366,485,611,725,862`, `url_parsing/extractors/llm.py:166`), OCR (`jarvis-ocr-service/app/provider_manager.py:220`, `providers/llm_proxy_provider.py:133,191`), TTS (`jarvis-tts/app/main.py:234`) | 524 requests |
| POST | `/v1/chat/completions/cancel/{request_id}` | app | No caller on disk. The commit `2b42986` says phone-gateway in-call turns use the stream (the gateway repo is not on disk). | 0 |
| POST | `/v1/embeddings` | app | CC memory: `remember_tool.py:130`, `recall_tool.py:169`, `api/memories.py:384`, `agent_context_service.py:144`, `memory_service.py:187`, `memory_extraction_service.py:288` | 0 |
| GET | `/v1/models` | none | none outside tests and admin's status | 2 |
| GET | `/v1/engine` | none | CC warmup `conversation_handler.py:554` (reads `allows_caching`) | 37 |
| GET | `/v1/adapters/date-keys` | none | CC warmup `conversation_handler.py:484`; node-setup `scripts/sync_date_keys.py:19` | 37 |
| POST | `/internal/queue/enqueue` | app | CC: deep research, memory extraction, situation matcher, characterization (03 §2) | 3 |
| GET | `/health` | none | Docker healthcheck, admin `llm-setup.ts:46`, `quick-sets.ts:97`, config-service `services.py:111,196`, mcp `debug.py` | 11,482 per 48 h |
| GET | `/v1/health` | none | mcp `tools/health.py:20` (mcp is dropped) | – |
| * | `/settings/*` | combined | admin (`llm-setup.ts:68,153`, `quick-sets.ts:102,188`, `bg-model-config.ts:80`), config-service gateway (`settings_gateway.py:43,185`), `jarvis` CLI `:1915` | – |

### 2.2 Routes not ported

| Method | Path | Why not ported |
|---|---|---|
| GET | `/v1/adapters/date-keys/adapters` | LoRA; admin dropdown that no code uses. **Cut.** |
| GET | `/v1/training/status/{id}` | LoRA. Its only caller is CC's dormant adapter scheduler. **Cut.** |
| * | `/v1/pipeline/*` (5 routes) | WIP, superuser JWT, zero callers. **Cut** (PLAN §7). |
| – | `/api/v1/model/info`, `/api/v1/model-swap`, `/v1/adapters` list/activate, `POST /v1/training` | **Don't exist in code**, only in CLAUDE.md. |
| * | 7705 `/internal/model/*`, `/internal/log(s)` | Internal hop; gone (01 §2). |

**What happens to the kept routes:**

| Route | jarvisd disposition |
|---|---|
| `/v1/chat/completions` | **Keep, frozen.** CC and OCR move in-process. |
| `/v1/chat/completions/cancel/{request_id}` | **Keep, frozen.** Phone is in-process after D16, so it uses context cancel. |
| `/v1/embeddings` | **Keep, frozen.** CC goes in-process. |
| `/v1/models` | **Keep, frozen.** |
| `/v1/engine` | **Keep, frozen.** CC goes in-process. |
| `/v1/adapters/date-keys` | **Keep, frozen.** The path keeps its "adapters" name for node-setup. |
| `/internal/queue/enqueue` | **Keep** for external producers (none after Phase 5; see 03 §2), frozen by `TestLLMQueueEnqueue`. CC goes in-process. |
| `/health` | **Keep, frozen** (`TestHealth`). |
| `/v1/health` | **Keep** as an alias: it costs nothing. |
| `/settings/*` | **Keep.** Platform router, with the bulk-PUT gap (§8.6). |

## 3. Behaviour

### 3.1 `POST /v1/chat/completions` (non-stream)

**Request** (`ChatCompletionRequest`, `models/api_models.py:84-100`):

- **Fields:**

  | Field | Notes |
  |---|---|
  | `model` | Required |
  | `messages` | Required |
  | `temperature`, `top_p`, `max_tokens`, `seed` | Sampling |
  | `stream` | Bool |
  | `response_format{type, json_schema}` | JSON mode (04 §3.2) |
  | `tools[{type:"function", function{name, description, parameters}}]` | Native tool calling |
  | `tool_choice` | String or object |
  | `include_date_context` | Jarvis extension |
  | `adapter_settings{hash, scale, enabled}` | LoRA; Go accepts and ignores it |
  | `reasoning_budget` | Jarvis extension |

- **Unknown fields are accepted and ignored.** `extra="allow"` covers CC's `conversation_id`.
- **Messages:**
  - `role` is any string.
  - `content` is a string, null, or a list of `{type:"text",text}` / `{type:"image_url",image_url{url,detail?}}`. The list is discriminated on `type`; a bad part is 422.
  - Optional `tool_calls` and `tool_call_id`.
- **Missing `messages` or `model`:** 422 FastAPI validation (`TestLLMChatCompletion/validation`).

**Model and slot.** A `model` other than `live`/`background` (case-insensitive) is forced to `live`. The response echoes the **forced** name (`api/chat_routes.py:272-274,373`).

**Steps:**

1. **Slot ready?** If not, 503 `model_not_loaded` (01 §3.2).
2. **Date keys** (04 §3.1).
3. **Normalise messages.** An image URL that isn't a data URL is rejected with 400: "Only data URLs are supported for images. HTTP(S) URLs are not yet supported." (`services/chat_runner.py:72-83`).
4. **Image checks:**
   - Images on a slot without `supports_images` get 400 `"Model '<slot>' does not support images. Use a vision-capable model instead."` (`chat_runner.py:563-567`).
   - **The text uses the slot alias, not the model id.**
   - Images on a backend lacking vision get 500 (`:621-626`).
5. **Generate, then JSON mode** (04 §3.2).

**Response** (`create_openai_response`, `services/response_helpers.py:20-67`). The full pydantic `response_model` is serialised, so absent optionals are `null`:

```json
{"id":"chatcmpl-<8 hex>","object":"chat.completion","created":<unix>,"model":"live|background",
 "choices":[{"index":0,"message":{"role":"assistant","content":"…|null","tool_calls":null|[{"id","type":"function","function":{"name","arguments":"<json string>"}}],"tool_call_id":null},"finish_reason":"stop|length|tool_calls"}],
 "usage":{"prompt_tokens","completion_tokens","total_tokens"},
 "date_keys":null|[...]}
```

- **`usage`:** a missing usage becomes all zeros (`:32-33`).
- **Tool call ids:** a tool call without an id gets `call_<12 hex>` (`:45`).
- **`finish_reason`:** defaults to `tool_calls` when calls exist, else `stop` (`:36-37`).
- **Extra keys.** The pydantic model has `extra="allow"`, so any extra keys from the model service would serialise. None are sent today. `reasoning_content` is **not** forwarded (04 §3.6), although CC's errand planner reads it (`errand_planner.py:403`), so its diagnostic is always empty.

### 3.2 `POST /v1/chat/completions` with `stream: true`

**Response headers** (`chat_routes.py:342-351`):

| Header | Value |
|---|---|
| Status | 200 |
| `Content-Type` | `text/event-stream` |
| Transfer | chunked |
| `Cache-Control` | `no-cache` |
| `Connection` | `keep-alive` |
| `X-Accel-Buffering` | `no` |
| `X-Request-Id` | The caller's header, else `uuid4().hex` (32 lowercase hex) |

**Frames.** Each frame is exactly `data: <json>\n\n`, with no `event:` or `id:` lines and no `[DONE]` sentinel:

| Frame | When |
|---|---|
| `{"delta":"<text>"}` | Each non-empty content piece |
| `{"done":true,"content":"<all deltas concatenated>","usage":{…},"tool_calls":null,"finish_reason":"stop|length"}` | Last frame on success |
| `{"error":"<message>"}` | The backend raised mid-stream (`services/model_service.py:496-498`); the stream then ends |
| `{"error":"Model service error <status>"}` | The 7705 hop answered non-200, for example 503 `model_not_loaded` (`chat_routes.py:311-314`). **Still HTTP 200.** |
| `{"error":"Model service connection error: <e>"}` | Transport failure (`:318-324`) |
| `{"cancelled":true}` | Last frame after a cancel. **No done frame follows** (`model_service.py:683-686`). |

**JSON encoding.** Frames are Python `json.dumps` with default separators (`", "`, `": "`) and `ensure_ascii=True`, so non-ASCII deltas are `\uXXXX`-escaped.
- CC parses JSON, so the bytes are not load-bearing.
- **Decided:** Go emits the same separators and escaping anyway (`pyjson`, 04 §11), so a byte-diffing shadow replay stays clean.

**Stream path differences from non-stream.** These are LEGACY-BUGs, fixed in Go per D8:
- Tools, `response_format` and `reasoning_budget` are dropped (04 §8.1).
- Images are not checked (`TestLLMImageToTextModel/stream`).
- `date_keys` are computed but not emitted. **Keep not emitting:** this is frozen.
- `tool_calls` is always `null` because tools never reach the backend. **Go fixes this:** when tools are sent, deltas stream as usual and the done frame carries the merged `tool_calls` with `finish_reason:"tool_calls"`. The contract test sends no tools, so `null` stays frozen for that case.
- `usage` is `{}` on the in-process GGUF backend. On REST it is whatever upstream sent. **Go always sends the three counts** (it sets `stream_options.include_usage` upstream).

**Cancellation and disconnect:**
- **Registry.** Streams register `request_id → handle` in `_active_streams` (`model_service.py:457-458,615-634`). A new stream with an id that is already active **supersedes** the old one: the old stream is aborted and the new one takes the id.
- **Granularity.** Abort lands at the next token boundary, via the producer thread with `gen.close()` (`:461-511`). Prompt prefill can't be interrupted.
- **Client disconnect** tears down both hops deterministically (`services/streaming.py`, `chat_routes.py:325-337`).

### 3.3 `POST /v1/chat/completions/cancel/{request_id}`

The API proxies this to 7705 (`chat_routes.py:392-439`).

| Outcome | Status | Body |
|---|---|---|
| Stream found | 200 | `{"status":"cancelling","request_id":"<id>"}` |
| Unknown id | 404 | `{"detail":{"error":{"type":"not_found","message":"No active stream with request id '<id>'","code":"request_not_found"}}}` |
| Other model-service failure | 500 | `internal_server_error` |

All three are frozen (`TestLLMChatStream/cancel`). **Any app** can cancel any stream by id: there is no ownership check. That is acceptable under D5, since app credentials are infrastructure.

### 3.4 `POST /v1/embeddings`

**Request.** `{input: str | [str], model?: str = "all-MiniLM-L6-v2"}`.
- `model` is ignored.
- A missing `input` is 422 (`TestLLMEmbeddings`).

**Response** (`api/embedding_routes.py:59-94`):

```json
{"object":"list","data":[{"object":"embedding","embedding":[…384 floats…],"index":i}],"model":"<loaded model name>","usage":{"prompt_tokens":n,"total_tokens":n}}
```

- `n = max(1, sum(len)//4)`.
- `[]` input returns empty `data`, zero usage, and `model` = the request's `model` (`:70-75`).

**Go behaviour:**
- `model` reports the configured embedding model id.
- Vectors are L2-normalised.
- **Decided:** keep the chars//4 usage approximation (frozen key set only).

### 3.5 `GET /v1/models`

No auth. Response (`api/model_routes.py:21-64`):

```json
{"object":"list","data":[{"id":"<model id/label>","object":"model","created":0,"owned_by":"jarvis","supports_images":bool,"context_length":int|null}]}
```

- **One entry per distinct backend.** Shared slots give one entry.
- **Ids.** Today the ids are the configured labels (e.g. `.models/Qwen3.8-27B-UD-Q4_K_M.gguf`).
- **Go:** ids are the catalog id or file basename. `context_length` is the engine's real `-c`, divided by `parallel` when greater than 1, because that is the per-request window.

### 3.6 `GET /v1/engine`

No auth. Response: `{inference_engine, backend_type, allows_caching, description}` (`model_service.py:764-807`).

**Go values:**

| Key | Value |
|---|---|
| `inference_engine` | `"llama_cpp"` for a local slot; `"rest"` for remote |
| `backend_type` | `"LLAMA_SERVER"` or `"REST"` |
| `allows_caching` | `true` for both (prod REST is true today; CC primes the prefix cache on it) |
| `description` | A real string, not "Unknown engine" |

These describe the **live** slot. The key set is frozen (`TestLLMEngine`). CC reads only `allows_caching`.

### 3.7 `GET /health`

No auth. The status code is load-bearing (`api/health_routes.py:5-10`).

| Status | Body `status` | When |
|---|---|---|
| 200 | `"healthy"` | live slot ready |
| 200 | `"initializing"` | live loading, under 15 minutes since first seen not-ready |
| 503 | `"degraded"` | live failed, past the grace window, or engine unreachable |

- `/health` also includes `version` and, today, `model_service: {…7705 body…}`. The `model_service` blob is **not frozen** (contract README "Not frozen").
- `TestHealth` freezes the shared shape. Admin reads `model_service.status`, `model_service.aliases.live` and `models[0]` (`llm-setup.ts:46-50`, `quick-sets.ts:97-101`). That is Phase 6 admin territory.
- **Decided:** jarvisd keeps a `model_service` object with `{status, models, aliases, slots}`, built from engine states, so admin keeps working until it is rewritten.
- The `busy` state (`:93-107`) can't happen without the 7705 hop. **Drop it.**

### 3.8 `POST /internal/queue/enqueue`

Covered in 03 §3.1. Frozen by `TestLLMQueueEnqueue`. The enqueue response is `{"accepted":true,"job_id":"<id>","deduped":bool}`.

### 3.9 Errors

| Source | Shape |
|---|---|
| Proxy errors (`openai_error`, `response_helpers.py:91-105`) | `{"detail":{"error":{"type","message","code":null}}}` |
| Model-service errors | The same shape, propagated with their status since #86 (`chat_routes.py:226-242,357-366`) |
| Auth (`app_auth.py:161-186`) | 401 `{"detail":"Missing app credentials"}` / `{"detail":"Invalid app credentials"}`. Other auth-service statuses pass through with `"App auth failed"`. An auth outage is 502 `"Auth service unavailable when calling …"`. |
| Validation | FastAPI 422 |

**Status → `type` mapping** (`error_type_for_status`, `:73-88`):

| Status | `type` |
|---|---|
| 404 | `not_found_error` |
| 429 | `rate_limit_error` |
| ≥500 | `internal_server_error` |
| other 4xx | `invalid_request_error` |

**Upstream llama-server errors.**
- A context overflow (400 with `n_prompt_tokens`) is passed through as 400 with upstream's body as `message` (`chat_runner.py:740-749`).
- Prod's Jinja template error ("System message must be at the beginning", 2026-10-04) was returned as **500**, so the upstream status was 500 or the error raised before #87.
- **Go:** pass the upstream 4xx and 5xx through unchanged.

**Contract-frozen statuses:**
- `TestLLMImageToTextModel/non-stream` accepts either 400 (the #86 fix, which Go answers) or the legacy 500.
- Go answers the stream case with a 400 too: the image check happens before the stream opens. That is a deliberate departure from the frozen legacy 200, recorded as a D8 fix. The contract test needs a `JARVIS_CONTRACT_IMPL=jarvisd` branch.

## 4. Data

- **In-memory only.** The active-stream registry is a `map[string]*streamHandle` guarded by a mutex in Go.
- **`X-Request-Id`** is echoed on stream responses only. Non-stream responses don't carry it today; adding it is harmless (§10).

## 5. Settings

None specific to the API. `llm.request_timeout_seconds` (05) bounds upstream calls.
- **Prod's timeouts:** 240 s (`model_service.timeout_seconds`, `rest.timeout_seconds`).
- **The 60 s default** would kill bg jobs with thinking on.

## 6. Dependencies

- Auth module: `authn.Authority` for app credentials, in-process (no round trip).
- Platform settings router (`internal/platform/settings/router.go`).
- Engine client (01) and job queue (03).

## 7. Invariants (frozen by `contract/llm_test.go`)

**Non-stream response:**
1. The key set and nulls are as in §3.1, and `id` matches `^chatcmpl-[0-9a-f]{8}$`.
2. `model` echoes the slot alias, and unknown values become `live`.
3. `date_keys` is `null` unless `include_date_context:true`; then it is an array, possibly `[]`.

**Stream:**
4. Framing as in §3.2: deltas, then one of done, error or cancelled; no `[DONE]`; done `content` equals the concatenated deltas; no `date_keys` on done.
5. Headers as in §3.2, including the generated 32-hex `X-Request-Id`.
6. Cancel: 200 `cancelling`, the stream ends with `{"cancelled":true}`, and a later cancel for the same id is 404 with the exact message.

**Other routes:**
7. `/v1/models`, `/v1/engine` and `/v1/adapters/date-keys` need no auth, and their key sets are frozen.
8. Embeddings: indices follow input order; `[]` input gives empty data and zero usage; 422 without `input`.
9. App auth: 401 detail strings on chat, cancel, embeddings and enqueue.

**Not frozen:** the queue callback envelope (03 §7 says how Go freezes it).

## 8. Oddities and bugs

1. **BUG:** the stream path drops tools, `response_format`, thinking and the image check (§3.2; 04 §8.1).
2. **A pre-stream failure is a 200 with an `{"error"}` frame.** This includes 503 `model_not_loaded`, which the contract documents but can't trigger. **Go keeps this**, because CC's consumer expects a stream once the status is 200.
   - Go also adds a pre-flight: a slot not ready returns a **real 503** before the stream opens.
   - CC's stream consumer raises on non-200 (`llm_proxy_client.py:156-163`) and has a fallback.
   - Recorded as a D8 fix.
3. **`include_date_context` on the stream** runs the extractor for nothing (04 §8.2).
4. **Embeddings `model` echo is inconsistent.** An empty input echoes the *request's* `model`; a non-empty one echoes the loaded model's name.
5. **`TEMPERATURE 0 → 0.7`** (04 §8.5).
6. **Settings bulk `PUT /settings/` with `{settings:{…}}`.** Admin and the CLI use it (`llm-setup.ts:153`, `quick-sets.ts:188`, `bg-model-config.ts:80`, `jarvis:1915`), and it accepts **app credentials for writes** (`api/settings_routes.py:180-230`, `create_combined_auth`).
   - The platform router (`internal/platform/settings/router.go`) has no bulk PUT and requires superuser for writes, matching the other services' contract.
   - **Decided:** add bulk PUT to the platform router as a superuser-only route. Admin sends the user's JWT, so it is unaffected. The `jarvis` CLI's app-credential write breaks, which is fine: the CLI is rewritten in Phase 6.
7. **`/v1/models` ids are labels**, not what the engine loaded (01 §8.4).
8. **CC's phone draft reads the wrong field.** `phone_call_service.py:825` reads `result["content"]`/`["message"]` instead of `choices`, so the draft is always empty. That is a CC bug, logged for 11-phone's port. It is irrelevant once the call is in-process with typed results.

## 9. Tests

**Contract (`contract/llm_test.go`).** Green against the MBP (Qwen3-8B, in-process GGUF):
- `TestLLMChatCompletion`, `TestLLMChatStream`, `TestLLMImageToTextModel`
- `TestLLMModels`, `TestLLMEngine`, `TestLLMDateKeyVocabulary`, `TestLLMEmbeddings`
- `TestLLMQueueEnqueue`, `TestLLMAppAuth`

**To add:**
- **`TestLLMChatTools`:** the non-stream native `tool_calls` shape, with a tool-forcing prompt on a real model and the shape only checked.
- **`TestLLMStreamNotLoaded`:** jarvisd only.
- **`TestLLMQueueCallback`:** freezes the callback envelope via `JARVIS_CONTRACT_CALLBACK_HOST`, as OCR does (03 §9).

**Golden fixtures:**
- **G10, stream translation.** Upstream llama-server SSE transcripts, recorded from prod's b10499 and the MBP, map to Jarvis frames:
  - content only
  - with `usage`
  - with fragmented parallel `tool_calls`
  - with a malformed frame
  - with a trailing `[DONE]`

  The exporter calls `RestClient.generate_text_chat_stream` with a mock transport.
- **G11, envelopes.** `create_openai_response` and `error_type_for_status`, plus `_unwrap_model_service_error` cases.

## 10. Questions for the user

None. The behaviour changes here are D8 bug fixes:
- the stream gets parity with non-stream
- a real 503 before a stream opens
- an image refused with 400 on the stream
- usage is always present

Adding `X-Request-Id` to non-stream responses is decided as yes: it is additive.

## 11. Go port notes

**One `llm.Service` interface, used in-process by CC, OCR, phone and recipes-facing code:**

```go
type ChatRequest struct {
    Slot            Slot                 // Live | Background
    Messages        []Message            // structured content, tool_calls, tool_call_id kept
    Temperature     *float64; TopP *float64; MaxTokens *int; Seed *int
    Tools           []Tool; ToolChoice any
    ResponseFormat  *ResponseFormat      // json_object (+schema) → jsonmode
    ReasoningBudget *int
    WantDateKeys    bool                 // external API only; CC calls dates.Extract itself
    RequestID       string               // stream cancel handle
}
Chat(ctx, ChatRequest) (ChatResponse, error)
Stream(ctx, ChatRequest) (<-chan Frame, error)   // ctx cancel == cancel endpoint
Embed(ctx, []string) ([][]float32, error)
Enqueue(ctx, Job) (id string, deduped bool, err error)   // 03
```

**HTTP handlers** on the 7704 listener are thin adapters over this interface. They reproduce the legacy JSON exactly, including null-serialised optionals and `pyjson` frames.

**Stream handling:**
- The cancel registry maps `request_id` to `context.CancelFunc`, with supersede-on-reuse.
- Cancel cancels the upstream HTTP request to llama-server, which stops generation server-side. That is better than today's next-token-boundary abort on the proxy side only.
- Emit `{"cancelled":true}` when cancelled by the endpoint. A client disconnect emits nothing; the connection is gone anyway.
