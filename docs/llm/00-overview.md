# llm 00: overview, topology, callers and the keep/cut map

This is the Phase 3 spec for porting **jarvis-llm-proxy-api** into jarvisd's `llm` module. It has six docs:

| Doc | Covers |
|---|---|
| 00 | this overview |
| [01](01-backends-and-engines.md) | backends, engines, slots and GPUs |
| [02](02-api-and-streaming.md) | the HTTP API and streaming |
| [03](03-queue-and-jobs.md) | the queue |
| [04](04-prompt-shaping.md) | prompt and response shaping |
| [05](05-models-and-downloads.md) | models, downloads and settings |

Open questions live in [QUESTIONS.md](QUESTIONS.md).

**Sources:**
- `/home/alex/jarvis/jarvis-llm-proxy-api` at `e04d0b1`, about 25.5k lines of Python excluding tests. Prod runs this build (image revision `e04d0b13cc95`).
- Prod (`jarvis@10.0.0.107`) and the MBP (`10.0.0.103`), both read on 2026-10-06, read-only.

**Decisions that apply:**
- **PLAN §3.3:** llama-server per slot; macOS on Metal; GPUs auto-detected, always configurable, multi-GPU.
- **PLAN §7:** cuts are in-process TRANSFORMERS/vLLM/MLX (vLLM and MLX stay reachable as remote OpenAI-compatible endpoints), LoRA/adapters/training, unused `swap_*`, legacy `cache/` and the WIP pipeline routes.
- **D9:** fastText is cut, including llm-proxy's date fallback.
- **D11:** drop unread settings.
- **D22/D23:** byte-exact prompts.
- **D40:** date extraction runs once per turn on the raw transcript, regex only.

## 1. Purpose

The proxy is Jarvis's single door to an LLM. It does seven jobs:

1. Gives every service an OpenAI-shaped chat API with two named **slots**: `live` (low-latency voice) and `background` (slow, thorough jobs).
2. Streams tokens in a Jarvis-specific SSE frame format, with cancel.
3. Extracts date keys from the user's words (`include_date_context` → `date_keys`).
4. Makes JSON output reliable: inject an instruction, repair, validate, retry.
5. Controls "thinking" on reasoning models.
6. Runs background jobs from a queue and calls the caller back.
7. Serves text embeddings for CC's memory.

**Users:**
- command-center: almost all traffic.
- recipes, OCR and TTS: a little traffic each.
- admin and the `jarvis` CLI: configuration through `/settings` and `/health`.

## 2. Entry points

### 2.1 Topology today (prod, 2026-10-06)

```
                ┌──────────── container jarvis-llm-proxy-api (image dev-cuda, GPU count:all) ────────────┐
CC / recipes ──▶│ :7704 uvicorn main:app  (API: auth, routing, /health, /settings, /v1/embeddings ← MiniLM │
 (app creds)    │       in THIS process, ~360 MiB on GPU 0)                                              │
                │   │ X-Internal-Token                                                                    │
                │   ▼                                                                                     │
                │ :7705 uvicorn services.model_service:app (supervised by scripts/serve.sh; ModelManager) │
                │       live → RestClient ─────────────┐   background → RestClient ──────────┐            │
                └──────────────────────────────────────┼─────────────────────────────────────┼────────────┘
                                                       ▼                                     ▼
            llama-server  (GPU 1, b10499)                           llama-server-bg (GPU 0, b10499)
            Qwen3.8-27B Q4_K_M + mmproj-F16, -c 13312,             Qwen3.8-27B Q4_K_M, -c 131072 -np 1,
            4 auto slots, --jinja, enable_thinking=false,          -fa on, KV q8_0, thinking on
            --reasoning-budget 0
container llm-proxy-worker: python scripts/queue_worker.py ── Redis (rq:queue:llm_proxy_jobs) ──▶ POST :7705/internal/model/chat ──▶ callback
```

**Processes and containers:**

| Process | Detail |
|---|---|
| API | `main.py:56-68` mounts 9 routers. Startup pre-warms the embedding model in a thread (`main.py:103-127`). |
| Model service | `services/model_service.py`. Binds first, loads in the background, retries failed slots (01 §3.2). |
| Worker | `scripts/queue_worker.py`. One RQ worker, so concurrency is 1 (03). |
| llama-server sidecars | Defined in prod's hand-edited `~/.jarvis/compose/docker-compose.yml`, with flags from `.env` (05 §3.4). |
| `ollama` container | Defined but exited, unused since 2026-09-29. |

**MBP dev.** The same three processes run natively under launchd (no Docker). The model service uses the **in-process GGUF backend on Metal**, Qwen3-8B shared by both slots. jarvisd replaces that with a local llama-server Metal build (01 §3.5).

### 2.2 Request flows

**Live, non-stream.** This is CC's tool loop, warmup, refinement and formatting; about 98% of prod's 524 chat calls.
1. CC → `POST :7704/v1/chat/completions {model:"live", include_date_context:true, …}`.
2. App auth: a round trip to auth `/internal/app-ping` (`auth/app_auth.py:11-58`).
3. Proxy → `POST :7705/internal/model/chat` with `X-Internal-Token` (`api/chat_routes.py:353-381`).
4. Slot check → date keys (regex) → `run_chat_completion` (`services/chat_runner.py:542`) → `RestClient.generate_text_chat`, serialised per slot (01 §3.3).
5. Upstream: `POST http://llama-server:8080/v1/chat/completions`.
6. Optional JSON repair and retry.
7. Back through `create_openai_response` with `date_keys` (02 §3.1).

**Live, stream.** Used by CC's continue/format streams (`conversation_handler.py:2217,2848`) and the phone gateway.
- Same front half, then 7705 `/internal/model/chat/stream`.
- A producer thread pumps `RestClient.generate_text_chat_stream`, which parses upstream SSE into Jarvis frames.
- The API re-emits the lines verbatim (02 §3.2).
- Cancel: `POST /v1/chat/completions/cancel/{X-Request-Id}`.
- Prod logs can't tell stream from non-stream: uvicorn access lines don't show it.

**Background, synchronous.** `model:"background"` on the same route:
- CC: errand planner, signal automations, phone drafting, sync characterization (`errand_planner.py:403`, `signal_automation_executor.py:142`, `phone_call_service.py:783`, `characterization_synthesis_service.py:149`).
- Recipes: grocery matching (`llm_client.py:862`).
- OCR LLM vision (`llm_proxy_provider.py:308,349`).

**Background, queued.**
1. CC → `POST /internal/queue/enqueue {job_type:"chat", request{model:"background",…}, callback{url, bearer}}`.
2. Redis dedupe key, then RQ enqueue (03 §3.1).
3. The worker POSTs to 7705, which runs it on background.
4. The worker POSTs the envelope to CC `/api/v0/<x>/callback` (03 §3.2).

The four producers are memory extraction, characterization, deep research and the situation matcher. Prod saw 3 jobs in 3 days.

**Embeddings.** CC memory → `POST /v1/embeddings` → MiniLM in the API process (01 §3.7). Prod: 0 calls in the logged window, even though memory features exist.

**Discovery.** CC warmup → `GET /v1/engine` (reads `allows_caching`) and `GET /v1/adapters/date-keys` (reads `static_keys`). 37 each in 3 days.

### 2.3 Callers (non-test)

Cites are `file:line` in each caller's repo.

**command-center.**
- Client: `app/core/llm_proxy_client.py`.
- Live: `conversation_handler.py:484,543,554-556,1398,1784,1879,2217,2545,2687,2848`, `tool_execution_engine.py:728,746` (reads `date_keys` at `:799`, `usage` at `:807`), `param_refinement.py:194`, `tools/resolve_relative_date_tool.py:289`, `api/wake_response.py:87`, `api/node_commands.py:468` (`/node/llm/chat`), `chat.py:22,34` (passthrough).
- Background: `errand_planner.py:403`, `proposal_matcher.py:236`, `signal_automation_executor.py:142`, `phone_call_service.py:783`, `characterization_synthesis_service.py:149`.
- Queue: the four producers in 03 §2.
- Embeddings: six sites (02 §2).
- Dead or offline:
  - `malformed_json_extractor.py:80,121`: instantiated, never called.
  - `command_example_expander.py:346`: an offline script that hits 7705 directly.
  - `training_orchestrator.py:110,136`: LoRA.
  - `adapter_example_expansion.py:61`: LoRA.
  - `get_conversation_status` (`llm_proxy_client.py:256`): no callers.

**jarvis-recipes-server** (external add-on; stays on HTTP).
- `jarvis_recipes/app/services/llm_client.py:366,485,611,725,862` and `url_parsing/extractors/llm.py:166`.
- Every call is `json_object`, non-stream, mostly live. The grocery matcher uses background.

**jarvis-ocr-service** → the jarvisd OCR module (Phase 2, done). It calls an `LLMURL` today (`internal/modules/ocr/engines.go:361-470`, `ocr.go:91-94`). Legacy calls:
- validation: `provider_manager.py:220`, model `ocr.validation_model` (default live)
- vision: `providers/llm_proxy_provider.py:133`, background
- vision validation: `llm_proxy_provider.py:191`, live
- `llm_queue_client.py:137`: dead (03 §2)

**jarvis-tts.** `app/main.py:234`, the deprecated `/generate-wake-response` shim. It is cut with the TTS rebuild (PLAN §7).

**node-setup.** `scripts/sync_date_keys.py:19`, a dev script calling `GET /v1/adapters/date-keys`.

**admin** (`server/src/routes/llm-setup.ts:46,68,153`, `quick-sets.ts:97,102,188`, `services/bg-model-config.ts:80`, `services/orchestrator.ts:66,75,110`):
- `/health`
- `/settings/model.live.name`
- `/settings/model.main.context_window`
- bulk `PUT /settings/`
- Docker exec downloads (05 §2)

**config-service.** Settings gateway (`app/routes/settings_gateway.py:43,185`) and health fan-out (`services.py:111,196`).

**`jarvis` CLI.** `jarvis:1915` (bulk settings PUT) and `:1952,1995` (health).

**jarvis-mcp** (dropped). `tools/health.py:20,120,145` (`/v1/health`) and `tools/debug.py:53,83,114`.

**install-e2e.**
- `gpu/test_gpu_inference.py:111`, `quickstart/run_quickstart.sh:200,212`, `gpu/benchmark_models.py:186-201`.
- `docker-compose.ci-override.yaml:17-35`: the OpenAI REST backend for the behaviour corpus.

## 3. Behaviour summary

| Concern | Today | jarvisd | Doc |
|---|---|---|---|
| Process hops | 7704 → 7705 → llama-server | `llm` module → llama-server (subprocess) or remote | 01 |
| Slots | `live`, `background` with a frozen config, retry and cooldown | per-slot engine Supervisor; background may be `shared` | 01 §3.2 |
| Engines | REST in prod; in-process GGUF on the MBP | llama-server, local or remote OpenAI-compatible | 01 §3.1 |
| GPU | Docker `device_ids` (prod); `gpu_select.py` for AMD/Vulkan pins | detection via `llama-server --list-devices` plus vendor tools; settings override | 01 §3.4 |
| Streaming | Jarvis frames; cancel by id; stream drops tools and thinking | same frames; parity with non-stream; ctx cancel | 02 §3.2 |
| Dates | regex, plus a dead fastText fallback | regex `dates.Extract`; CC calls it once per turn | 04 §3.1 |
| JSON | inject, repair, validate, retry; no grammar | same, byte-exact | 04 §3.2 |
| Thinking | `chat_template_kwargs.enable_thinking`; GGUF prefill | kwarg on every path | 04 §3.3 |
| Queue | Redis/RQ, one worker, HTTP callbacks, no retries | platform queue; in-process completion; callbacks only for external callers | 03 |
| Embeddings | sentence-transformers in the API process | llama-server `--embedding` with MiniLM GGUF | 01 §3.7 |
| Models | `.env` + settings + Docker exec downloads | catalog + resumable downloads + per-slot settings | 05 |

## 4. Data

**Today:**
- Postgres `jarvis_llm_proxy`: `settings` (58 global rows in prod), `training_jobs` (0 rows), `service_configs` (the discovery cache).
- Redis: RQ queue keys and dedupe keys.

**jarvisd:**
- The baseline migration is empty (`docs/schema/llm.md`).
- Phase 3 adds `llm_models`, the download/registry table (05 §4), and `llm_dedupe` (03 §11).
- Settings live under the `llm_` platform table.

## 5. Settings

The full disposition is in 05 §5. In short, the 115 legacy keys come to about 25, mostly under `llm.<slot>.*`.

## 6. Dependencies

- **Platform:** `engines` (built), `queue` (built), `settings`, `authn` (in-process app validation), `httpx`.
- **External:**
  - llama.cpp release binaries (pinned)
  - Hugging Face (weights)
  - optional remote OpenAI-compatible endpoints
- **Consumers in jarvisd:**
  - CC (`llm.Service` in-process)
  - OCR module (swap `LLMURL` HTTP for in-process when the llm module is enabled; keep HTTP for strangler mode)
  - phone (D16; streams)
  - notifications: none

## 7. Invariants (cross-doc)

1. The external wire contract on 7704 is exactly what `contract/llm_test.go` freezes (02 §7).
2. The two slot names only. Unknown names become `live`.
3. The queue never uses the live slot, and by default runs one background call at a time.
4. CC's prompts reach the engine unchanged. The only exception is JSON-mode injection (04 §7.5).
5. The settings that describe a slot generate its engine. They can't drift (05 §7).

## 8. Oddities and the biggest bugs found

**Behaviour bugs:**

| Bug | Fix | Doc |
|---|---|---|
| `temperature: 0` silently becomes 0.7 on every path | D8 | 04 §8.5 |
| The stream path drops tools, `response_format`, thinking control and image checks | D8 | 02 §8.1 |
| Native tool history (`tool_calls`/`tool_call_id`) is flattened away on REST | D8 | 04 §8.3 |
| Slot sharing ignores `rest_url`, so two REST slots with the same label would both hit the live server | | 01 §3.2 |
| The expired-job callback raises `TypeError`, so the caller never hears back | | 03 §8.1 |
| The REST text path always reports `finish_reason: "stop"` | | 04 §8.7 |

**Operational problems:**

| Problem | Doc |
|---|---|
| The proxy never health-checks llama-server; a dead sidecar means 500s and a "healthy" `/health` | 01 §8.2 |
| Non-stream requests are serialised per slot, wasting llama-server's parallel slots | 01 §3.3 |
| Prod's working config is hand-edited; admin's generator would produce a broken live sidecar (no `--jinja`, no mmproj, backend not REST) | 05 §8.2 |
| OCR's LLM vision targets `background`, which has no vision in prod | 01 §3.8 |

**Documentation is wrong.** CLAUDE.md documents routes that don't exist: `/api/v1/model/info`, `/api/v1/model-swap`, `/v1/adapters` list/activate, `POST /v1/training`.

## 9. KEEP / CUT map

Every tracked non-test source file is listed below. **Keep** means the behaviour survives "in spirit" as Go code. **Cut** means not ported.

| File(s) | Lines | Disposition | Go home / reason |
|---|---|---|---|
| `main.py` | 136 | Keep (spirit) | module wiring on the 7704 listener |
| `api/chat_routes.py` | 245 | **Keep** | `llm/http` chat and cancel handlers (02) |
| `api/embedding_routes.py` | 94 | **Keep** | embeddings handler |
| `api/queue_routes.py` | 171 | **Keep** (minus `adapter_train`) | enqueue handler (03) |
| `api/model_routes.py` | 105 | **Keep** | `/v1/models`, `/v1/engine` |
| `api/health_routes.py` | 198 | **Keep** (simplified; no `busy`) | `/health`, `/v1/health` |
| `api/adapter_routes.py` | 44 | Keep `/date-keys`; **cut** `/date-keys/adapters` | `llm/dates` vocabulary |
| `api/settings_routes.py` | 337 | Replaced | platform settings router, plus bulk PUT (02 §8.6) |
| `api/training_routes.py` | 61 | **Cut** | LoRA |
| `api/pipeline_routes.py`, `services/pipeline_service.py`, `models/pipeline_models.py` | 99+366+124 | **Cut** | WIP pipeline (PLAN §7) |
| `auth/app_auth.py` | 58 | Replaced | `authn` in-process |
| `models/api_models.py` | 149 | **Keep** | `llm` request/response types (minus `AdapterSettings`, accepted and ignored) |
| `models/queue_models.py` | 90 | **Keep** (minus `AdapterTrainRequest`) | job types |
| `services/model_service.py` | 808 | Keep (spirit): stream registry, cancel, slot guard, date hook | `llm/slots`, `llm/http`; the 7705 hop itself is cut |
| `services/chat_runner.py` | 753 | **Keep** | `llm/jsonmode`, generation orchestration |
| `services/streaming.py` | 48 | Cut (Python artefact) | Go `http.Flusher` and ctx |
| `services/response_helpers.py` | 105 | **Keep** | envelopes and errors (G11) |
| `services/settings_helpers.py` | 91 | Keep `resolve_slot_reasoning_budget`; the rest is the platform's job | `llm/slots` |
| `services/settings_service.py` | 1105 | Replaced | about 25 `Definition`s (05 §5.2) |
| `services/date_key_matcher.py` | 420 | **Keep, exact** | `llm/dates.Extract` (G6) |
| `services/date_keys.py` | 680 | Keep the vocabulary (`:33-151`); **cut** fastText and the LLM/Hybrid extractors (`:166-680`) | D9 |
| `services/date_key_adapter.py` | 219 | **Cut** | LoRA |
| `services/json_grammar.py` | 175 | **Cut** | dead (test-only) |
| `services/json_repair_service.py`, `services/message_service.py` | 699+180 | **Cut** | dead (test-only import chain) |
| `services/vision_inference.py` | 301 | **Cut** | no enqueuer; swap pattern obsolete |
| `services/adapter_cache.py`, `adapter_storage.py`, `adapter_training.py`, `training_job_service.py`, `storage/object_store.py` | 270+295+626+65+199 | **Cut** | LoRA, S3 |
| `managers/model_manager.py` | 817 | Keep (spirit): slot states, readiness, 503 contract; **cut** `swap_*` (`:609-700`) and the date adapter (`:592-603`) | `llm/slots` + `engines.Supervisor` |
| `managers/embedding_manager.py` | 91 | Keep (spirit) | embed engine (01 §3.7) |
| `managers/chat_types.py` | 62 | **Keep** | `GenerationParams` becomes `ChatRequest` |
| `backends/rest_backend.py` | 830 | **Keep** | `llm/engine/openai.go` (local and remote) |
| `backends/base.py` | 215 | Keep (spirit) | the `engine.Client` interface |
| `backends/gguf_backend.py`, `gguf_vision_backend.py`, `chat_formats.py`, `power_metrics.py` | 991+254+130+87 | **Cut** | in-process llama.cpp (replaced by llama-server) |
| `backends/mlx_backend.py`, `mlx_vision_backend.py` | 514+197 | **Cut** | PLAN §7; remote MLX server instead |
| `backends/vllm_backend.py`, `vllm_vision_backend.py`, `vllm_availability.py` | 513+429+38 | **Cut** | PLAN §7; remote vLLM instead |
| `backends/transformers_backend.py`, `transformers_vision_backend.py` | 751+274 | **Cut** | PLAN §7 |
| `backends/mock_backend.py` | 105 | Cut | Go fake engine in tests |
| `gpu_select.py` | 401 | **Keep** (rules as data; subprocess-only) | `llm/gpu` (01 §3.4) |
| `queues/redis_queue.py`, `queues/tasks.py`, `scripts/queue_worker.py` | 58+427+167 | Keep (spirit) | platform queue job types (03 §11) |
| `cache/*` | 253 | **Cut** | dead (no importer; PLAN §7) |
| `config/service_config.py`, `logging_config.py`, `debug_config.py` | 130+75+56 | Replaced / cut | in-process discovery; platform logging; debugpy cut |
| `db/*`, `alembic/*`, `apply_migrations.sh`, `make_migration.sh`, `scripts/make_migration.py` | – | Replaced | goose baseline (empty) plus the 2 new tables |
| `scripts/serve.sh`, `run*.sh`, `setup*.sh`, `stop-server.sh`, `cleanup-processes.sh`, `deploy-launchd.sh`, `scripts/vendor/setup_llama_cpp.sh`, `scripts/common.sh` | – | **Cut** | `engines.Supervisor`; jarvisd install (Phase 6) |
| `scripts/seed_settings.py`, `scripts/patch_settings.py` | 113+341 | Cut | definitions' defaults; legacy import (05 §4) |
| `scripts/{train_*,convert_to_*,merge_adapter,load_adapter,quantize_awq,build_jarvis_model,expand_training_data,generate_jarvis_training_data,spike_adapter_timing,validate_*}.py`, `scripts/runpod/*` | about 5k | Not ported (offline Python tooling, PLAN §7) | stays in the archived repo |
| `scripts/train_fasttext_date_keys.py`, `validate_fasttext_date_keys.py`, `train_date_key_classifier.py`, `validate_hybrid_date_keys.py` | – | **Cut** | D9 |
| `data/jarvis_training.jsonl` | 4,987 rows | **Keep** as a test fixture | G6 input |
| `version.py` | 13 | Keep (spirit) | jarvisd version in `/health` |

**Rough size of what survives.** About 4.5k of the 25.5k lines: the REST backend, chat runner, matcher, routes, queue semantics, GPU rules and slot state. That is under PLAN's "~60% orchestration" estimate, because most of the bulk is training and in-process backends.

## 10. Questions for the user

See [QUESTIONS.md](QUESTIONS.md). Seven questions, most consequential first.

## 11. Go port notes (module shape)

```
internal/modules/llm/
  llm.go          Module: Definitions, Mount(7704 listener), Start (engines, queue handlers)
  service.go      llm.Service — Chat / Stream / Embed / Enqueue / OnComplete (in-process API for CC, OCR, phone)
  http/           chat, cancel, embeddings, models, engine, date-keys, enqueue, health handlers (pyjson output)
  slots/          slot config → engine (local Supervisor | remote client | shared), readiness, semaphores
  engine/         OpenAI-compatible client: payload translation (G8), SSE → frames (G10), thinking, errors
  jsonmode/       inject / repair / validate / correction turn (G7)
  dates/          Extract + Vocabulary (G6) — shared with cc/prompts and cc/dates
  gpu/            detection + proposal (subprocess only)
  catalog/        embedded model catalog
  download/       resumable HF + engine downloads (queue job)
  jobs.go         llm.chat, llm.callback, llm.download job types; llm_dedupe
  migrations/     00002_models_and_dedupe.sql
```

**Build order:**

1. **The engine client against a fake server:** G8 and G10 green.
2. **dates and jsonmode:** G6 and G7 green.
3. **The HTTP surface on 7704 with a remote engine** pointed at prod's llama-server via tunnel, or the MBP. The contract suite should go green here, before any engine management exists.
4. **Engine supervision and GPU detection,** with downloads after. Verify on this box (CUDA), the MBP (Metal) and a CPU-only run.
5. **Queue and in-process `OnComplete`.**
6. **Embeddings engine.**

**Strangler mode.**
- While CC is still Python (until Phase 5), jarvisd's llm module serves 7704 to legacy CC over HTTP, with exactly today's contract.
- Python CC's HTTP callbacks still work, because the `llm.callback` job POSTs the legacy envelope.
- This is why the external enqueue route and callbacks are fully implemented, not stubbed.
