# llm 01: backends, engines, slots and GPUs

This doc covers how a request reaches weights today, and how it will in jarvisd. Sources:

- Python: `/home/alex/jarvis/jarvis-llm-proxy-api`, HEAD `e04d0b1`, which prod runs.
- Go: `internal/platform/engines`.
- Prod facts were read on 2026-10-06 from `jarvis@10.0.0.107`.

## 1. Purpose

There are two **slots**, `live` and `background`, and each needs a model behind it:

- `live` is the voice hot path: CC tool loop, warmup, streaming.
- `background` serves queue jobs, errands, signals, phone and OCR vision.

Today the model service (7705) holds one backend object per slot. In prod both are `RestClient`s pointing at two `llama-server` sidecars, one per RTX 3090.

jarvisd replaces the model service and the sidecars as follows:

- **Local engines.** `llama-server` subprocesses, one per slot, are supervised by `internal/platform/engines`. jarvisd downloads them per GPU flavour and configures them from settings.
- **Remote engines.** An OpenAI-compatible HTTP endpoint (OpenAI, vLLM, MLX server, LM Studio, Ollama's `/v1`) is reached through the same Go client.

## 2. Entry points

**Model service routes.** All are internal and all take `X-Internal-Token`. They disappear in jarvisd and are listed for completeness.

| Route | File | Notes |
|---|---|---|
| `POST /internal/model/chat` | `services/model_service.py:383-422` | Non-stream generation. Its single external caller is CC's offline `command_example_expander.py:346` (scripts only). |
| `POST /internal/model/chat/stream` | `model_service.py:546-704` | Stream generation |
| `POST /internal/model/cancel/{id}` | `model_service.py:514-543` | Stream cancel |
| `POST /internal/model/unload`, `POST /internal/model/reload` | `model_service.py:303-363` | Callers: the vision job (`services/vision_inference.py:62-133`), adapter training and `scripts/patch_settings.py`. Prod saw one reload in 3 days. |
| `GET /internal/model/models`, `GET /internal/model/engine` | `model_service.py:748-807` | |
| `GET /health` (7705) | `model_service.py:707-745` | Always 200; the body carries the slot state. |
| `POST /internal/log(s)` | `model_service.py:128-159` | Worker log forwarding. The worker's HTTP handler is disabled (`scripts/queue_worker.py:127-129`), so this is dead. |
| `/settings/*` (on 7705 too) | `model_service.py:48` | A duplicate mount |

**Background threads in the model service:**

| Thread | Where | What it does |
|---|---|---|
| model-loader | `model_service.py:180-221` | Loads the slots once after the port is bound |
| retry loop | `:190-200` | Every 60 s; per slot, cooldown is 60 s doubling to 600 s (`managers/model_manager.py:13-15,521-529`) |
| stream pump | `:461-511` | One producer thread per active stream |

**GPU auto-select at import.** `gpu_select.select_discrete_gpu()` runs at import time (`model_service.py:36-41`).

## 3. Behaviour

### 3.1 Backends

`ModelManager._create_backend` (`managers/model_manager.py:203-254`) dispatches on `backend_type`:

| Type | Class | jarvisd |
|---|---|---|
| `REST` | `backends/rest_backend.py` `RestClient` | **Kept in spirit.** It becomes the one Go engine client, used for both local llama-server and remote endpoints. |
| `GGUF` | `backends/gguf_backend.py` (llama-cpp-python, in-process) | **Cut.** Replaced by a local llama-server. The MBP dev box runs it today (Qwen3-8B, Metal). |
| `MLX` | `backends/mlx_backend.py`, `mlx_vision_backend.py` | **Cut** (PLAN §7). `mlx_lm.server` stays reachable as a remote endpoint. |
| `VLLM` | `backends/vllm_backend.py`, `vllm_vision_backend.py`, `vllm_availability.py` | **Cut.** vLLM's OpenAI server stays reachable as a remote endpoint. |
| `TRANSFORMERS` | `backends/transformers_backend.py`, `transformers_vision_backend.py` | **Cut** |
| `MOCK` | `backends/mock_backend.py` | **Cut.** The Go tests use an `httptest` fake engine instead. |
| GGUF vision | `backends/gguf_vision_backend.py` | **Cut.** Vision is llama-server `--mmproj`. |

**What `RestClient` does today:**

- **Construction** (`rest_backend.py:69-160`). It reads these settings, which are global rather than per slot:

  | Setting | Notes |
  |---|---|
  | `rest.provider` | |
  | `rest.request_format` | |
  | `rest.auth_type` | |
  | `rest.auth_header_name` | |
  | `rest.auth_token` | |
  | `rest.timeout_seconds` | Prod: 240 |
  | `model.<slot>.reasoning_budget` | The per-slot default |

  The model name is `model.background.rest_model_name` (background) or `model.main.rest_model_name` (live), falling back to the slot's `name` (`:82-98`).
- **Endpoint by provider** (`:211-223`):

  | Provider | Endpoint |
  |---|---|
  | `openai`, `lmstudio`, `generic` | `/v1/chat/completions` |
  | `anthropic` | `/v1/messages` |
  | `ollama` | `/api/chat` |

  The URL is built with `urljoin(base_url, endpoint)`. An absolute path replaces any path in `base_url`, which is why CI's `JARVIS_REST_MODEL_URL=https://api.openai.com/v1/chat/completions` still works.
- **Three generation paths:**

  | Path | Lines | Behaviour |
  |---|---|---|
  | text | `chat_with_temperature`, `:381-464` | Flattened text messages (04 §3.4), temperature, max_tokens, top_p, seed and thinking (`_apply_reasoning`). Content is `.strip()`ped, and **finish_reason is hard-coded `"stop"`** (`:675`). |
  | tools | `_chat_completion_with_tools`, `:292-343` | Same, plus `tools`/`tool_choice`. It returns the structured `tool_calls` and the real `finish_reason`. |
  | vision | `generate_vision_chat`, `:717-812` | Structured parts with images as data URLs. Temperature `or 0.7`, **no thinking control**. |
- **Streaming** (`generate_text_chat_stream`, `:492-631`):
  - It parses upstream OpenAI SSE and skips the `[DONE]` line and malformed frames.
  - It yields `{"delta"}` per content piece.
  - It merges `tool_calls` deltas by `index` (`:19-45`).
  - The final frame is `{"done", content, usage, tool_calls, finish_reason}`.
  - Usage is taken from any chunk carrying `usage`. llama-server sends usage on the last chunk only when asked (`stream_options.include_usage`), and the client never asks, so **usage may be `{}`** (see 02 §8).
- **Sync/async bridge.** A dedicated asyncio loop per backend instance (`:678-715`) is a Python artefact and disappears in Go.
- **Upstream errors** become `BackendHTTPError(status, body)`. `chat_runner.py:740-749` maps that status through (#86/#87).

### 3.2 Slot lifecycle (state machine)

`ModelManager` (`model_manager.py:34-817`) is a singleton, with per-slot state `{status, error, attempts, last_attempt_monotonic, loaded_at_epoch}` (`:114-122`).

```
not_loaded ──mark_all_loading──▶ loading ──_attempt_slot_load ok──▶ ready
                                    │                                 │
                                    └── exception ──▶ failed ◀────────┘ (swap_* error with no backend)
failed ──retry_failed_loads (cooldown 60s·2^(n-1) ≤ 600s; or force) ──▶ loading ──▶ ready|failed
```

**The rules:**

- **Config is resolved once, then frozen** (`_resolve_slot_configs`, `:290-428`).
  - Live reads `model.live.*`, falling back to `model.main.*`, then to env.
  - Background reads `model.background.*`, falling back to the live values.
  - Retries replay the frozen config. They re-resolve only when the frozen config can never load (`:451-465,558-573`).
- **Sharing.** If background has the same `backend_type` **and** `model_path` as live, it mirrors live's instance and state, so there is no second load (`:390-394,513-519`).
  - **BUG:** `rest_url` is not part of the comparison. Two REST slots pointing at different servers but carrying the same model label would silently both go to the **live** server.
  - Prod escapes this only because its labels differ: `Qwen3.8-27B-UD-Q4_K_M.gguf` vs `.models/Qwen3.8-27B-UD-Q4_K_M.gguf`.
- **Registry.** It is filled from the frozen configs only (`_populate_registry`, `:735-783`):
  - The model id is the configured path or label.
  - `context_length` is the configured `context_window`, which is not what the engine actually runs. Prod's live slot says 8192 while llama-server runs `-c 13312`.
  - `supports_images` is the setting.
- **Unavailable slot.** A request to a slot with no backend gets **503 `model_not_loaded`** and fires a non-blocking retry (`model_service.py:240-267`).
- **Reload** (`model_service.py:316-363`):
  1. Retire the old manager (`begin_shutdown`, which waits for in-flight loads) and unload it.
  2. Construct a fresh singleton and `load_all`.
  3. Return 200 only if live is `ready`, else 503 `degraded`.
- **Unload.** Pauses retries (`pause_loads`) so the vision job or training can have the GPU.
- **Shape of a REST slot.** For a REST slot, "load" is just constructing an HTTP client. The real model-load state lives in the llama-server sidecar, which **the proxy never health-checks**. A dead sidecar shows as 500s, not 503s, and the proxy `/health` stays `healthy`.
- **Unused methods.**
  - `swap_live_model` and `swap_background_model` (`:609-700`) have **no route and no caller**: the `/api/v1/model-swap` route in CLAUDE.md does not exist. **Cut** (PLAN §7: "unused `swap_*`").
  - `_load_date_key_adapter` (`:592-603`) is LoRA. **Cut.**

### 3.3 Serialisation and concurrency

**Today:**

- **Sync backends** are serialised per backend instance by `_backend_call_lock` (`services/chat_runner.py:405-427`).
  - `RestClient.generate_text_chat` is sync, so **every non-stream request to one slot is serialised in the proxy**, one at a time. This holds even though prod's live llama-server has 4 parallel slots (`n_slots = 4`, unified KV).
  - Streams take a different route, `generate_text_chat_stream` run in a producer thread, and do **not** take that lock. A stream can therefore run alongside a non-stream request.
  - Vision is async and also skips the lock.
- **Queue jobs.** The single RQ worker processes one job at a time (`scripts/queue_worker.py:158-163`).

**jarvisd:**

- No proxy-side serialisation for engines that batch. llama-server queues on its own `-np` slots.
- Concurrency is bounded per slot by a semaphore equal to `llm.<slot>.parallel`. This is backpressure, not correctness.
- Background work is capped by the queue's per-type concurrency (03).

### 3.4 GPU selection (`gpu_select.py`)

`gpu_select.py` (401 lines, stdlib only) does two things.

**1. Discrete-GPU pin at process start** (`select_discrete_gpu`, `:376-401`). This matters on AMD boxes where an iGPU enumerates first.

1. **Operator override.** If any of `GGML_VK_VISIBLE_DEVICES`, `HIP_VISIBLE_DEVICES`, `ROCR_VISIBLE_DEVICES` or `CUDA_VISIBLE_DEVICES` is set, it does nothing (`:37-42,379-383`).
2. **Detect the backend from tooling, never from a loadable library** (`:358-373`).
   - `JARVIS_GPU_BACKEND=none|cpu|rocm|vulkan` decides directly.
   - Otherwise `/opt/rocm` or `rocminfo` means ROCm, and `vulkaninfo` means Vulkan.
   - Gating on a loadable library would be wrong: Mesa llvmpipe's `libvulkan.so.1` SIGSEGVs when probed.
3. **ROCm.** It finds the first device with `hipDeviceProp_t.integrated == 0`, via a ctypes struct mirror (`:150-207`). The fallback is `rocminfo` with an APU gfx denylist (`:48-51,210-231`). It then sets `HIP_VISIBLE_DEVICES`, deliberately not ROCR, to avoid a double remap (`:388`).
4. **Vulkan.** It finds `PHYSICAL_DEVICE_TYPE_DISCRETE_GPU` in `vulkaninfo --summary`, or through a ctypes `vkGetPhysicalDeviceProperties` (`:57-128`). `JARVIS_GPU_PREFER` picks among several by name substring (`:131-144`). It then sets `GGML_VK_VISIBLE_DEVICES`.
5. **CUDA and Metal:** no-op.

**2. GGUF split-mode auto** (`auto_gguf_split_mode`, `:315-352`). This is used by the cut GGUF backend (`gguf_backend.py:88-96`).

- It returns layer split (1) only when there are 2 or more CUDA GPUs (`nvidia-smi`, honouring `CUDA_VISIBLE_DEVICES` up to the first `-1`, `:244-282`) **and** either:
  - the GPU names are identical, or
  - the smallest card has at least 8192 MiB (`:291,344-351`).
- Otherwise it returns single-GPU (0).

**Prod doesn't use either half.** It pins each sidecar by Docker `device_ids` (live → GPU 1, bg → GPU 0). Whisper and TTS are also on GPU 1.

**jarvisd port** (PLAN §3.3: auto-detect, always overridable, multi-GPU):

- **Detection** (`internal/modules/llm/gpu`):
  - The primary source is **`llama-server --list-devices`** run against the downloaded engine build. It reports exactly the devices that build can use, with names and free/total memory, for CUDA, ROCm, Vulkan and Metal alike, with no ctypes or purego probing.
  - `nvidia-smi --query-gpu=index,name,memory.total,memory.used --format=csv` and `rocminfo` / `vulkaninfo --summary` (when present) add vendor detail and drive the flavour choice before any engine has been downloaded.
  - Port the iGPU rules as data: the APU gfx list, plus "integrated" or "llvmpipe" device names excluded.
- **Proposal.**

  | Hardware | Proposed assignment |
  |---|---|
  | 1 GPU | Both slots on it. Background is `shared` (one engine) unless both models fit. |
  | 2 or more | live → the largest card; background → the next. Tensor split is offered only when a chosen model exceeds the largest single card. |
  | Apple | `metal`, with a single device |
  | None | `cpu`, plus a "consider a remote endpoint" note |
- **Effective config.** A setting beats detection, and a visibility env var the operator set beats both. `engines.Spec.environ` already preserves an operator-set variable.
- **Mapping to the engine:**
  - `llm.<slot>.gpu_devices` → `engines.GPU{Backend, VisibleDevices}`, which sets `CUDA_VISIBLE_DEVICES`, `HIP_VISIBLE_DEVICES` or `GGML_VK_VISIBLE_DEVICES` (`engines.go:56-88`).
  - Multiple devices add `--split-mode layer|row` and `--tensor-split a,b`.
  - `gpu_layers` → `-ngl`.
  - CPU → `-ngl 0` (or a CPU build).
- **Reporting.** `jarvisd doctor` and admin show detection, proposal and effective assignment.

### 3.5 Metal (macOS)

**Today:**

- The MBP runs llm-proxy natively under launchd. The model service uses the in-process GGUF backend with `LLAMA_METAL=true` (MBP `.env`) and Qwen3-8B shared across both slots.
- The proxy reports `/v1/engine` `{inference_engine: "llama_cpp", allows_caching: true}`.
- Admin excludes GPU services from Docker on darwin and installs them natively (`jarvis-admin/server/src/routes/native-services.ts:183-254`).

**jarvisd:**

- darwin/arm64 downloads the upstream macOS release, which is Metal by default. Metal is the default backend.
- `cpu` is used only when the user picks it (PLAN §3.3).
- There is no visibility variable. `-ngl 999` offloads everything.
- Unified memory means the "VRAM" for model fitting is roughly 70% of RAM.
- **Verify on the MBP:** `llama-server --list-devices` shows `Metal`, and tokens/s is at least today's in-process figure for Qwen3-8B Q4_K_M.

### 3.6 Engine binaries

- Prod pins `ghcr.io/ggml-org/llama.cpp@sha256:4d27e401…`, build **b10499**.
- jarvisd pins one llama.cpp release tag. It downloads the matching asset to `~/.jarvis/engines/llama-server/<tag>-<flavour>/`:

  | Platform | Flavours |
  |---|---|
  | linux | `cuda` (12.x), `vulkan`, `cpu` |
  | linux | `rocm`, if upstream ships it; otherwise Vulkan for AMD |
  | macOS | `metal` (arm64) |
  | windows | `cuda`, `vulkan`, `cpu` |

- The asset is verified against a sha256 embedded in the binary, extracted, and marked executable.
- The download is the same queue job type as model downloads (05 §11).
- The flavour is chosen from detection (§3.4), and is overridable through `llm.<slot>.gpu_backend`.

### 3.7 Embeddings

**Today:**

- `POST /v1/embeddings` runs **in the API process**, not the model service. The model is sentence-transformers `all-MiniLM-L6-v2`, 384-d, L2-normalised (`managers/embedding_manager.py:172-244`, `api/embedding_routes.py:59-94`).
- **Warm-up.** It is pre-warmed in a thread at startup (`main.py:103-127`).
- **The request's `model` field is ignored.** The response reports the loaded model's name.
- **Token usage is approximated** as `max(1, chars//4)`.
- **It blocks the event loop.** `encode` is sync and runs inside an async handler.
- **Prod.** It runs on GPU 0 (about 360 MiB, uninvited). It had 0 calls in 3 days of logs.
- **CC's callers:** memory remember/recall, the memories API, agent context, and memory extraction.

**jarvisd:**

- An `llama-server --embedding --pooling mean` engine with `all-MiniLM-L6-v2` GGUF (F16, about 45 MB), CPU by default (`-ngl 0`). It is a third supervised engine, named `llm-embed`.
- **Vectors.** They are L2-normalised in Go so the output matches today's `normalize_embeddings=True`.
- **Re-embedding.** Existing vectors are re-embedded by CC's sweep (PLAN §3.3, schema import notes), because GGUF conversion is not bit-identical.
- **The sherpa-onnx alternative.** PLAN lists sherpa-onnx as an option, but sherpa has no general text-embedding API. **Decided: llama-server.**
- **CC calls it in-process:** `llm.Embed(ctx, texts)`.

### 3.8 Vision (`--mmproj`)

**Today:**

- A request with any image part goes to `generate_vision_chat` (`chat_runner.py:620-638`).
- It is rejected with 400 if the slot's `supports_images` is false (`:563-567`). The non-stream path only (02 §8).
- **Prod.** Live has `--mmproj mmproj-F16.gguf` and `supports_images=true`. Background has neither.
- **OCR's LLM-vision engine asks for `background`** (`internal/modules/ocr/engines.go:398-404`). The legacy OCR does the same (`jarvis-ocr-service/app/providers/llm_proxy_provider.py:308,349`). **In prod that 400s:** the bg slot has no mmproj. The only working vision slot is live, which OCR never asks for. See QUESTIONS LQ4.
- The `vision_inference` queue job (`queues/tasks.py:261-327`, `services/vision_inference.py`) swaps in an in-process transformers vision model. It **has no enqueuer anywhere.** **Cut.**

**jarvisd:**

- `llm.<slot>.mmproj` adds `--mmproj <path>`.
- `supports_images` is derived from it for local engines, and is a setting for remote ones.
- The image check runs on stream and non-stream alike.

### 3.9 Health

**Today:**

| Layer | Lines | Behaviour |
|---|---|---|
| API `/health` (7704) | `api/health_routes.py:59-198` | Aggregates 7705 `/health` into a real status. 200 `healthy` or `initializing` (15-minute grace) or `busy` (probe timeout); 200 `degraded` with no model service URL; 503 on refused, failed live slot or past grace. Adds `version`. |
| Model service `/health` (7705) | | Always 200 |
| llama-server | | Docker healthcheck only (`curl -f :8080/health`, prod compose), which the proxy never consults |

**jarvisd:**

- `engines.Supervisor` health-checks each llama-server `/health`: 503 while loading counts against `StartTimeout`, then the failure threshold applies, then a drain-restart (`engines.go:94-110,489-547`).
- The llm module derives slot readiness from the supervisor `State`:

  | Supervisor state | Slot readiness |
  |---|---|
  | Healthy | ready |
  | Starting / Restarting | loading |
  | Failed | failed |
  | Draining / Unhealthy | ready but degraded |

- For remote engines, a periodic `GET /v1/models` with the API key gives readiness. It is cached for 30 s.
- The `/health` body shape is in 02 §3.7.

## 4. Data

- **No tables.** All state is in memory: slot states, the frozen config, `_active_streams` (02).
- **jarvisd:** in memory too. The engines `Manager` holds the supervisors, and the llm module holds `map[slot]*slotState{cfg, engine *engines.Supervisor | remote client, sem}`.

## 5. Settings

See 05 §5 for the full disposition. The engine-shaping keys are:

| Key | Notes |
|---|---|
| `llm.<slot>.engine` | `local` \| `remote` \| `shared` |
| `model` | |
| `mmproj` | |
| `context` | |
| `parallel` | |
| `gpu_backend` | |
| `gpu_devices` | |
| `split_mode` | |
| `tensor_split` | |
| `gpu_layers` | |
| `kv_cache_type` | |
| `flash_attn` | |
| `extra_args` | |
| `remote_*` | |

**Prod mapping on import:**

| Setting | live | bg |
|---|---|---|
| model | 27B | 27B |
| mmproj | yes | – |
| context | 13312 (taken from the server, not the stale 8192 setting) | 131072 |
| parallel | 4 | 1 |
| devices | `1` | `0` |
| kv | f16 | q8_0 |
| flash_attn | default | on |
| extra_args | `--chat-template-kwargs {"enable_thinking":false} --reasoning-budget 0` | – |

The live slot's thinking flags become redundant once jarvisd sends the kwarg per request (04 §3.3). They are kept anyway, to be safe.

## 6. Dependencies

- `internal/platform/engines` provides Supervisor, Manager, GPU env, health, drain and restart (already built).
- The model and engine downloaders (05).
- The settings platform (requires-reload means restart the engine).
- llama-server features relied on:
  - `--jinja` (tools, `chat_template_kwargs`)
  - `--mmproj`
  - `--embedding`
  - `--list-devices`
  - `/health`, `/v1/chat/completions` (stream + `stream_options.include_usage`), `/v1/embeddings`, `/props`

## 7. Invariants

1. **Two slot names only:** `live` and `background`. Any other `model` value on the external API is forced to `live` (`api/chat_routes.py:272-274`, frozen by `TestLLMChatCompletion`).
2. **An unready slot answers 503 `model_not_loaded`** with the nested OpenAI error, plus `slot` and `state` (`model_service.py:256-267`). This is never a 500.
3. **Background may share live's engine.** When it does, that is explicit (`engine=shared`), never inferred from labels (the §3.2 bug).
4. **A config change restarts only the affected engine.** The live slot stays up when background is reconfigured, and vice versa. Today the admin restarts the whole proxy container.
5. **Operator visibility variables always win** (gpu_select contract, `engines.Spec.environ`).
6. **Never probe a GPU library in-process.** Probing is a native-crash risk, which the gpu_select comments record (`:364-368`). Detection is subprocess-only.

## 8. Oddities and bugs

1. **BUG:** slot sharing ignores `rest_url` (§3.2).
2. **The proxy never health-checks the sidecars.** A dead llama-server means 500 `internal_server_error` while `/health` says `healthy`.
3. **Non-stream requests are serialised per slot in the proxy**, wasting llama-server's 4 parallel slots (§3.3).
4. **Context drift.** The settings say live 8192, the server runs 13312, and the env says `JARVIS_MODEL_CONTEXT_WINDOW=32768`. `/v1/models` reports 8192.
5. **`/v1/engine` in prod reports `backend_type: "GGUF"`.** It reads the `model.main.backend` setting or the env, not the slot (`model_service.py:779-781`). The `description` is "Unknown engine" for `rest`. CC reads only `allows_caching`.
6. **`provider=anthropic` is broken.**
   - It posts OpenAI-shaped `messages` (including `system` role entries) without the required `max_tokens` and `anthropic-version` header to `/v1/messages`.
   - The tools path always parses OpenAI `choices`.
   - **`provider=ollama` is broken too.** `/api/chat` without `stream:false` streams NDJSON, so `response.json()` fails.
   - Neither has a test or a user. PLAN §2 lists "OpenAI, Anthropic, Ollama, LM Studio". **Decided:** jarvisd's remote client speaks the OpenAI-compatible API only. That covers OpenAI, LM Studio, vLLM, MLX, llama-server and Ollama's `/v1`. The Anthropic native API is deferred unless the user wants it (LQ2).
7. **Embeddings take GPU memory in the API process** and block the event loop (§3.7).
8. **The vision job is dead,** and the vision-swap unload/reload pattern exists only for it (§3.8).
9. **`/internal/model/*` is bound on `0.0.0.0:7705` inside the container.** It is published only on loopback by the repo's compose; prod doesn't publish it. Not ported.

## 9. Tests and golden fixtures

**Existing tests:**

| Area | Tests |
|---|---|
| Slot resilience | `tests/test_model_manager_resilience.py`, `test_model_service_health.py`, `test_api_health.py` |
| Streaming | `test_stream_abort.py`, `test_chat_stream_proxy.py`, `test_rest_backend_streaming.py` |
| Error propagation | `test_upstream_error_propagation.py` |
| GPU selection | `test_gpu_select.py`, `test_gguf_split_mode.py` |
| REST event loop | `test_rest_backend_loop_pinning.py` (Python-only concern) |

**Golden fixtures and tests for jarvisd:**

- **G8, request translation** (shared with 04): the upstream payload per CC shape A–K, OCR and recipes.
- **G9, engine argv:** slot settings to the llama-server argv and env, as a table. It includes prod's two slots and the 1-GPU, 2-GPU split, Metal and CPU cases.
- **GPU detection:** fixtures of captured `nvidia-smi`, `rocminfo`, `vulkaninfo --summary` and `llama-server --list-devices` outputs (prod, MBP, an AMD iGPU box), parsed to the proposal.
- **Supervisor integration:** a fake engine binary (a Go test helper that serves `/health` and `/v1/chat/completions`) for start, 503-while-loading, kill → restart, and config change → restart of that slot only.

## 10. Questions for the user

Asked in QUESTIONS.md: LQ1 (topology defaults), LQ2 (remote providers), LQ4 (vision slot). Everything else here is decided above.

## 11. Go port notes

**Packages:**

```
internal/modules/llm/
  engine/      Client interface {Chat(ctx, Req) (Resp, error); Stream(ctx, Req) (<-chan Frame, error); Embed(...); Ready()}
               openai.go  — one OpenAI-compatible HTTP client (local llama-server and remote)
  slots/       slot config → engines.Spec (local) or engine.Client (remote); readiness; per-slot semaphore
  gpu/         detection (subprocess), proposal, effective assignment
```

**Local slot spec.**

```go
engines.Spec{Name: "llm-live", Path: <llama-server>, Args: ["-m", model, "--jinja", "-c", ctx, "-np", par, "-ngl", layers, "--host", "127.0.0.1", "--port", port, ...mmproj, ...split, ...extra], GPU: {Backend, VisibleDevices}, Health: &HealthCheck{URL: "http://127.0.0.1:<port>/health", DrainGrace: 30s}, StartTimeout: 10m}
```

- The port comes from `engines.FreePort()`. The engine binds **127.0.0.1 only**: jarvisd is the only client.

**Remote slot.**

- Base URL, model name and API key are read from settings.
- A `--remote` LLM satellite (PLAN Phase 6) is just another jarvisd whose llm module is reached as a remote engine via its `/v1/chat/completions` with app credentials. That keeps today's "remote-llm" admin option working.

**What goes away.**

- the 7705 hop
- the `X-Internal-Token`
- the singleton manager swap dance
- the Python event-loop bridging
- the per-backend lock

**Restarts.** These are `Supervisor.Restart` with the new Spec: build a new Spec, stop the old supervisor, start the new one. The engines package keeps one Spec per Supervisor, so replace the supervisor in the Manager. That needs a small `Manager.Replace(spec)` addition.
