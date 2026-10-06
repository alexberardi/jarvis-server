# llm 05: models, downloads and settings

This doc covers which model files exist, how they reach the disk, who configures what, and which settings keys survive. Engine binaries (llama-server builds) are in [01](01-backends-and-engines.md) §3.6.

Sources:
- `/home/alex/jarvis/jarvis-llm-proxy-api`
- `/home/alex/jarvis/jarvis-admin` (`src/…` is the React app, `server/…` the Fastify backend)
- `/home/alex/jarvis/jarvis-installer`
- the umbrella `jarvis` CLI
- prod `~/.jarvis/compose`, read on 2026-10-06

## 1. Purpose

Today a model is a file under `.models/` plus a set of DB settings that point at it. There is no first-class model catalog in the server. Three separate client-side catalogs exist (admin, CLI, quick-sets), all out of date (§3.1).

Downloads happen through an exec into the llm-proxy container or a host fetch, with no progress reporting. llama-server sidecars are configured by `.env` variables, which the admin UI mostly doesn't write; prod was hand-tuned (§3.4).

jarvisd should own all of this: one catalog, downloads with progress and resume, and per-slot engine settings in the settings DB, editable in admin.

## 2. Entry points (today)

**Admin backend (Fastify)**

| Caller | Route / action | Effect | Cite |
|---|---|---|---|
| Admin first-boot wizard "Models" step | `POST /api/models/download` | 1. Docker exec `hf_hub_download` into `/app/.models`. 2. Fallback: Node `fetch` of `https://huggingface.co/<repo>/resolve/main/<file>`, 3 attempts, 30 min timeout, partial file deleted on failure. 3. Snapshots (vLLM) use the native venv's Python. | `server/src/routes/models.ts:196-265,39-74,382-411` |
| Admin `/llm-setup` | `POST /api/llm-setup/download` | Docker exec only, blocking, no progress | `server/src/routes/llm-setup.ts:215-285` |
| Admin wizard / `/llm-setup` | `POST /api/llm-setup/configure` | 1. `PUT <llm>/settings/` (bulk) with an allow-list of keys. 2. `llm.interface` written to CC via config-service. 3. Restart every `llm-proxy` container. | `llm-setup.ts:95-210` |
| Admin quick-sets | `POST /api/quick-sets/apply` | Writes `model.{live,background}.{name,chat_format,backend,context_window}` + `llm.interface`, then restarts | `server/src/routes/quick-sets.ts:138-266` |
| Admin reconcile (bg sidecar toggle) | `POST /api/install/reconcile` | 1. Writes `.env` `BG_MODEL_ENABLED`/`BG_MODEL_FILE`. 2. After `compose up`, writes `model.background.{backend=REST,name,rest_url=http://llama-server-bg:8080,context_window=CTX/NP,reasoning_budget=-1}`, `model_service.timeout_seconds=240`, `rest.timeout_seconds=240`. | `server/src/routes/install.ts:1033-1036,1168-1212`, `server/src/services/bg-model-config.ts:18-57` |
| Admin | `GET /api/models/installed`, delete | Lists and deletes `.gguf`/`.bin` files in the models dir | `models.ts:154-193,319-360` |
| Admin | `GET /api/install/hardware` | GPU detection (§3.5) | `install.ts:387-575` |

**Other configurators**

| Caller | Route / action | Effect | Cite |
|---|---|---|---|
| `jarvis` CLI wizard | `PUT :7704/settings/` | Writes `model.main.*`, `inference.general.engine`, `inference.gguf.{n_gpu_layers,n_threads}`, `inference.transformers.device`, `inference.vllm.*` | `jarvis:1879-1904,1915-1920` |
| `deploy/llm-proxy/setup.sh` (remote GPU box) | `POST /settings/sync-from-env` | Copies env into the DB | `deploy/llm-proxy/setup.sh:336-363` |
| jarvis-installer | compose only | 1. `LLM_INTERFACE_SEED`. 2. GPU type chosen by hand. 3. No model download and no llama-server. | `jarvis-installer/src/lib/compose-generator.ts:437-438`, `src/components/wizard/ConfigurationStep.tsx:218-291` |

On the llm-proxy side, `settings_service.discover_installed_model_paths()` feeds the dropdown options of `model.*.name` from `.models/` (`services/settings_service.py:17-51`).

## 3. Behaviour

### 3.1 Catalogs (all stale against D11/D12)

- **Admin `src/data/models.ts:1-110`.** Six entries: qwen3-4b, qwen3-8b, qwen3-14b, qwen25-7b, llama-3.1-8b and hermes-3-8b.
  - Fields: `hfRepoGguf`, `ggufFilename`, `hfRepoVllm`, `chatFormat`, `contextWindow`, sizes, `vramMb`, `gated`, `promptProvider`.
  - There is **no mmproj/vision field, no reasoning flag and no MLX repo**.
  - D12 already decided the cleanup:
    - drop qwen25-7b, llama-3.1-8b and hermes-3-8b
    - remap qwen3-14b to `Qwen3_14B_Compressed`
    - add Qwen3.5-9B and prod's 27B
  - Bug: qwen3-4b maps to `Qwen3_8B_Compressed`.
- **CLI `jarvis:188-199`.** Six different entries, using bartowski GGUF repos plus MLX repos. Chat formats are `qwen3`/`llama3`/`mistral`, and there is no `promptProvider`.
- **Quick-sets `server/src/data/quick-sets.json:1-104`.** Ten metadata-only presets with no files: Gemma 2/3/4, Llama, and Qwen 2.5/3.
- **Remote-deploy presets** (`deploy/llm-proxy/setup.sh:56-88`). vLLM and Mixtral presets.
- **Admin `llmInterfaceOptions`** (`server/src/data/service-registry.json:166-183`). Lists three dead providers.

### 3.2 What prod actually runs (2026-10-06)

`~/.jarvis/compose/.models/` holds 150 GB.

- **In use:**
  - `Qwen3.8-27B-UD-Q4_K_M.gguf` (16.5 GB), used by both slots
  - `mmproj-F16.gguf` (0.93 GB), live slot only
- **Present but unused** (about 130 GB of experiments):
  - Q3_K_XL 27B, Qwen3-Coder-30B ×2, Qwen3-30B-A3B, Devstral-24B, Qwen3-14B, Ministral-14B, Qwen3.5-9B
  - Gemma-4-31B and Qwen2.5-32B directories
  - an HF `.cache/`

The `.env.bak.*` files record a swap history: 14b → devstral → coder30b → abliterated → vision → 27b-bg → live-13k.

**Prod was hand-tuned, not wizard-configured:**
- `model.live.backend=REST` and `model.live.rest_url=http://llama-server:8080` are not written by any admin code (admin's compose comment `compose-generator.ts:237` says "set in the DB").
- The live server's `--jinja --mmproj … --chat-template-kwargs … --reasoning-budget` flags appear in prod's compose but **not** in admin's generator. That generator emits `--chat-template chatml -c 24576 -ctxcp -cms` with no `--jinja` (`compose-generator.ts:592-631`).

### 3.3 Storage layout today

**Paths**

| Deployment | Path |
|---|---|
| Docker | the host `${MODELS_DIR:-./.models}`, mounted at `/app/.models` (proxy, read-write) and `/models` (sidecars, read-only) |
| Native (Mac) | `~/.jarvis/native/jarvis-llm-proxy-api/.models` or `~/jarvis/jarvis-llm-proxy-api/.models` |
| Admin search order | `$MODELS_DIR`, native, checkout, `~/.jarvis/compose/.models`, `~/.jarvis/.models` (`models.ts:129-148`) |

**How the model is named.** The settings value is a **relative** path (`.models/<file>`), or just a label on REST. On REST it is only a label, and is sent as the OpenAI `model` field unless `rest_model_name` overrides it. llama-server ignores it.

**Embeddings.** sentence-transformers downloads `all-MiniLM-L6-v2` itself into the HF cache inside the container on first start (`managers/embedding_manager.py:183,221`). The download is unauthenticated, and the model ID can be overridden by the env var `JARVIS_EMBEDDING_MODEL`. It is not configurable in the DB.

### 3.4 How the llama-server sidecars are configured today

**Admin's generator**

| Sidecar | When emitted | Flags | GPU |
|---|---|---|---|
| live | `SERVING_TYPE=llama-server`, non-darwin (`compose-generator.ts:238-241`) | `-m /models/${LIVE_MODEL_FILE} --chat-template chatml -ngl 99 -c 24576 -ctxcp 32 -cms 256` | `device_ids ['${LIVE_MODEL_GPU_DEVICE:-1}']` (`:626`) |
| bg | `BG_MODEL_ENABLED` (`:248-251`) | `--jinja -ngl 99 -c ${BG_MODEL_CTX:-32768} -np ${BG_MODEL_NP:-2}` | `device_ids ['${BG_MODEL_GPU_DEVICE:-0}']` (`:675`) |

- The image is a pinned upstream digest `ghcr.io/ggml-org/llama.cpp@sha256:4d27e401…` (`:565-573`). Prod is build b10499.
- There is no healthcheck and no `depends_on`.
- Nothing in the admin UI sets `SERVING_TYPE`. It is only reconstructed from `.env` (`server/src/services/upgrade/state-reconstructor.ts:82-89`) or set by install-e2e.
- The bg default `-np 2` halves per-request context: `-c` is total. Admin compensates by writing `context_window = CTX/NP` (`bg-model-config.ts:53-57`).

**Prod's hand-edited compose:**
- **Live:** `--jinja -ngl 99 -c 13312 --mmproj --chat-template-kwargs '{"enable_thinking": false}' --reasoning-budget 0` on GPU 1. llama-server defaulted to 4 slots with a unified KV.
- **Bg:** `-c 131072 -np 1 -fa on --cache-type-k q8_0 --cache-type-v q8_0` on GPU 0.

### 3.5 GPU detection and recommendation (admin)

**Detection by platform** (`install.ts:387-575`):

| Platform | Method | VRAM figure |
|---|---|---|
| darwin | `system_profiler SPDisplaysDataType -json` | total RAM, since memory is unified |
| linux/win32 | `nvidia-smi --query-gpu=name,memory.total` | **summed** across GPUs |
| AMD fallback | `lspci` + `/sys/class/drm/card*/device/mem_info_vram_total` | read from sysfs |

**Recommendation:**
- The only rule is the filter `model.vramMb <= gpuVramMb` (`src/components/wizard/LlmStep.tsx:153-157`). Summed VRAM over-offers models that can't fit on one card.
- There is no tier table.
- Manual GPU override list: `src/components/wizard/HardwareStep.tsx:16-50`.
- The sidecar GPU pins 0 and 1 are fixed defaults, not detected.

### 3.6 Remote / cloud providers

**No wizard offers OpenAI, Anthropic, Ollama, LM Studio, vLLM or MLX endpoints, or API keys.** The only remote options are:

- **"remote-llm" in the wizard.** This is another *jarvis-llm-proxy* URL, which becomes CC's `JARVIS_LLM_PROXY_URL` (`src/components/wizard/HardwareStep.tsx:257-296`, `compose-generator.ts:776-783`).
  - Prod CC also has the setting `llm.proxy.url=https://llm-proxy.jarvisautomation.io`.
- **CI's OpenAI REST override**, set in `install-e2e/docker-compose.ci-override.yaml:17-35` through env vars: `JARVIS_MODEL_BACKEND=REST`, provider `openai`, bearer `OPENAI_API_KEY`, `JARVIS_REST_MODEL_URL=https://api.openai.com/v1/chat/completions`, model `gpt-4.1-nano`.

**`rest.*` settings are global, not per slot** (`backends/rest_backend.py:104-127`). Live and background can't use different providers or keys.

## 4. Data

No tables today: settings plus files on disk.

**Proposed jarvisd layout** under the data dir (`~/.jarvis/`, `internal/platform/config`):

```
~/.jarvis/
  models/
    <catalog-id>/<file>.gguf          # e.g. qwen3.8-27b/Qwen3.8-27B-UD-Q4_K_M.gguf
    <catalog-id>/mmproj-F16.gguf
    custom/<file>.gguf                # user-supplied (imported or "use existing path")
    .partial/<sha>.part               # in-progress downloads (resumable via HTTP Range)
  engines/llama-server/<build>-<flavour>/...   # 01 §3.6
```

A small `llm_models` table records each file: catalog id, file, sha256, size, source URL, state (`downloading|ready|failed`), bytes done, and `added_at`. This makes downloads resumable across restarts and lets admin list them.

**Legacy import:**
- Don't copy weights. Register existing files in place: the user points admin at `~/.jarvis/compose/.models`, and jarvisd records absolute paths.
- Prod's 150 GB must not be duplicated.

## 5. Settings

### 5.1 Legacy keys: disposition

Keys come from `services/settings_service.py`. A key not listed here is cut: its only readers are LoRA, training, vLLM, transformers, GGUF-in-process or `cache/` (see the full reader map in docs/schema/llm.md and below).

| Legacy key | Live reader (non-test) | Prod value | jarvisd |
|---|---|---|---|
| `model.{live,background}.name` | `managers/model_manager.py:302,357` | 27B file / `.models/…27B…` | **Keep** as `llm.<slot>.model` (a catalog id, or an absolute path for custom files) |
| `model.{live,background}.backend` | `model_manager.py:293,349` | REST / REST | **Replace** with `llm.<slot>.engine` = `local` \| `remote` (01 §3.1) |
| `model.{live,background}.rest_url` | `model_manager.py:334,381` | `http://llama-server(-bg):8080` | **Keep** as `llm.<slot>.remote_url` (only when engine = remote) |
| `model.background.rest_model_name`, `model.main.rest_model_name` | `rest_backend.py:83-93` | '' | **Keep** as `llm.<slot>.remote_model` (OpenAI needs it) |
| `model.{live,background}.context_window` | `model_manager.py:326,375`, reported in `/v1/models` | 8192 (but the server runs 13312) / 131072 | **Keep** as `llm.<slot>.context`. It now **drives** `-c`, so there is no drift |
| `model.{live,background}.supports_images` | `model_manager.py:344,386` | true / false | **Derive** from "mmproj configured" for local engines; keep the setting for remote |
| `model.{live,background}.reasoning_budget` | `settings_helpers.py:145`, `rest_backend.py:135` | 0 / -1 | **Keep** as `llm.<slot>.reasoning_budget` |
| `model.{live,background}.chat_format`, `.stop_tokens` | GGUF in-process only | chatml | **Drop**: llama-server uses `--jinja` and the GGUF's own template |
| `model.main.*` | fallback chain in `model_manager.py:296-340`, `model_service.py:779` (`/v1/engine` backend_type) | GGUF 14B (unloaded) | **Drop**. Import maps `model.main.*` to `llm.live.*` only when `model.live.*` is blank |
| `rest.provider`, `rest.request_format` | `rest_backend.py:115-122` | openai / (default openai) | **Keep** `provider` per slot (`openai\|anthropic\|ollama\|lmstudio\|generic`). **Drop** `request_format` (§8.4) |
| `rest.auth_type`, `rest.auth_header_name`, `rest.auth_token` (secret) | `rest_backend.py:104-112` | none / – / '' | **Keep** per slot as `llm.<slot>.remote_api_key` (secret) plus `remote_auth_header` |
| `rest.timeout_seconds` | `rest_backend.py:125` | 240 | **Keep** as `llm.request_timeout_seconds` |
| `model_service.url`, `model_service.timeout_seconds` | `chat_routes.py:277,290`, `tasks.py:131,138`, `health_routes.py` | localhost:7705 / 240 | **Drop**: no model service. The timeout folds into `llm.request_timeout_seconds` |
| `queue.name`, `queue.per_attempt_timeout_seconds` | `queue_routes.py:141`, `tasks.py:72-78` (computed, **never used**) | default | **Drop** |
| `queue.callback_timeout_seconds` | `tasks.py:413` | default 10 | **Drop**: the platform queue's callback retry policy (03) |
| `inference.general.engine` | `model_service.py:776` (`/v1/engine`), cut backends | llama_cpp | **Drop**: `/v1/engine` is derived from the slot engine (02 §3.6) |
| `inference.general.{max_tokens,top_p,top_k,repeat_penalty}` | cut backends only | 7000/0.95/40/1.1 | **Drop** (inert on REST today; 04 §3.5) |
| `inference.gguf.n_gpu_layers` | cut GGUF backend | -1 | **Becomes** the engine flag `llm.<slot>.gpu_layers` (default 999) |
| `inference.gguf.split_mode`, `main_gpu`, `tensor_split` | cut GGUF backend | split_mode=1 | **Becomes** per-engine `llm.<slot>.split_mode` / `tensor_split` (01 §3.4) |
| `inference.gguf.flash_attn`, `n_batch`, `n_ubatch`, `n_threads` | cut GGUF backend | – | **Becomes** an optional `llm.<slot>.extra_args` (free text), not individual keys |
| `logging.console_level`, `logging.remote_level` | `config/logging_config.py` | WARNING/DEBUG | **Drop**: the platform logging setting |
| all `training.*`, `adapter*`, `storage.*`, `inference.vllm.*`, `inference.transformers.*`, `inference.gguf.{enable_context_cache,max_cache_size,rope_scaling_type,seed,verbose,mirostat_*}`, `cache.*`, `date_keys.*`, `debug.*` | cut code only, or nothing (`storage.s3_force_path_style`, `training.output_dir/dataset_path/params_path/base_model_id`, `debug.dump_gbnf_path` have **no reader at all**) | – | **Drop** (D11) |

### 5.2 New jarvisd keys (`llm` module definitions)

**Per slot** (`<slot>` = `live` or `background`):

| Key | Values / meaning |
|---|---|
| `llm.<slot>.engine` | `local` \| `remote` \| `shared` (background only: reuse the live engine) |
| `llm.<slot>.model` | catalog id or absolute path |
| `llm.<slot>.mmproj` | optional |
| `llm.<slot>.context` | context size |
| `llm.<slot>.parallel` | `-np`, default 1 |
| `llm.<slot>.gpu_backend` | `auto` \| `cuda` \| `rocm` \| `vulkan` \| `metal` \| `cpu` |
| `llm.<slot>.gpu_devices` | e.g. `"1"` or `"0,1"`; empty = auto |
| `llm.<slot>.split_mode` | `none` \| `layer` \| `row` |
| `llm.<slot>.tensor_split` | e.g. `"1,1"` |
| `llm.<slot>.gpu_layers` | default 999 |
| `llm.<slot>.kv_cache_type` | `f16` \| `q8_0` |
| `llm.<slot>.flash_attn` | `auto` \| `on` \| `off` |
| `llm.<slot>.reasoning_budget` | thinking default |
| `llm.<slot>.extra_args` | free-text engine flags |
| `llm.<slot>.remote_url` | remote engine only |
| `llm.<slot>.remote_provider` | remote engine only |
| `llm.<slot>.remote_model` | remote engine only |
| `llm.<slot>.remote_api_key` | remote engine only; secret |

**Global:**

| Key | Values / meaning |
|---|---|
| `llm.request_timeout_seconds` | default 240, prod's value |
| `llm.embeddings.model` | catalog id; default MiniLM GGUF (01 §3.7) |
| `llm.embeddings.engine` | `local` \| `remote` \| `off` |
| `llm.hf_token` | secret |
| `llm.engine_build` | pinned llama.cpp build, read-only display |

All `requires_reload`. A change restarts the affected engine through the supervisor (01 §3.2), not the whole process.

## 6. Dependencies

- **Hugging Face Hub** for weights. Gated repos need `llm.hf_token`.
- **GitHub releases** of `ggml-org/llama.cpp` for engine binaries (01 §3.6).
- **CC** reads `llm.prompt_provider`, the D11 rename of `llm.interface`. The catalog's `promptProvider` drives it.
- **Admin** (Phase 6) gets a jarvisd API for the catalog, downloads, slot config and GPU detection (§11).

## 7. Invariants

1. **A slot's configured model, context and `supports_images` describe what the engine actually loaded.** Today they drift: the live context is 8192 in settings vs `-c 13312`, and the bg label was the stale Coder-30B until a reload. In jarvisd the settings *generate* the engine command line, so they can't drift.
2. **Downloads never leave a half-file where the engine looks.** Write to `.partial/`, verify the size (and sha256 when the catalog has one), then rename atomically.
3. **The user can always point a slot at an arbitrary local GGUF or a remote URL.** The catalog is a convenience, not a gate (D5 spirit).

## 8. Oddities

1. **Four catalogs that disagree** (§3.1). Two map models to dropped prompt providers (D11/D12).
2. **Admin's live-sidecar generator would not produce prod's working config.**
   - It omits `--jinja`, so there are no tools and no `chat_template_kwargs`.
   - It omits `--mmproj`.
   - It never flips `model.live.backend=REST` (`compose-generator.ts:237,592-631`).
   
   Prod works only because it was hand-edited.
3. **The CLI writes the wrong env fallbacks**, which the proxy ignores (`jarvis:1172-1201` vs `settings_service.py:345-404`):

   | CLI writes | Proxy reads |
   |---|---|
   | `JARVIS_CHAT_FORMAT` | `JARVIS_MODEL_CHAT_FORMAT` |
   | `JARVIS_CONTEXT_WINDOW` | `JARVIS_MODEL_CONTEXT_WINDOW` |
   | `JARVIS_GGUF_N_GPU_LAYERS` | `JARVIS_N_GPU_LAYERS` |
   | `JARVIS_GGUF_N_THREADS` | `JARVIS_N_THREADS` |
4. **`rest.request_format` values `ollama` and `chatml`, and `provider=ollama|anthropic`, are untested and likely broken** (01 §8). The 2026-09-29 prod compose comment records that ollama was abandoned (it ignored `reasoning_budget` and forced 1 slot).
5. **Summed multi-GPU VRAM** in model filtering (`install.ts:461-487`) offers a 40 GB model on 2×24 GB as if it fit one card. That is only true with tensor split.
6. **Embeddings are not configurable in the DB.** Prod's sentence-transformers grabbed about 360 MiB of GPU 0 inside the API process, uninvited (prod `nvidia-smi`).
7. **130 GB of dead experiments in prod's `.models`.** jarvisd's model list should show disk usage per file and offer delete.

## 9. Tests

- **Existing:** admin has none for downloads. llm-proxy `tests/test_settings_service.py` and `test_settings_routes.py` cover definitions, and `test_model_supports_images.py` covers the flag.
- **Go:**
  - **Download manager:** unit tests against an `httptest` server for Range resume, size/sha mismatch, cancel, and concurrent requests for the same file (dedup).
  - **Catalog:** a lint test that every catalog entry's `promptProvider` is a kept provider (D11 hard error).
  - **Slot config → engine argv:** table tests (01 §9 G9).
- **Contract:** `TestLLMModels` (02) checks `context_length` and `supports_images` against the running slot.

## 10. Questions for the user

Asked from QUESTIONS.md (LQ3, LQ5, LQ6). Resolved here without a question:

- the settings rename to `llm.<slot>.*`
- per-slot remote providers and keys
- the partial-file layout
- registering prod's existing files in place instead of copying

## 11. Go port notes

**Catalog**

- `internal/modules/llm/catalog`, embedded as JSON. Each entry has:
  - `id`, `display`, `hf_repo`, `file`, `sha256`, `size`
  - optional `mmproj {repo, file, sha256, size}`
  - `context_default`, `min_vram_mb` (per card, not summed)
  - `prompt_provider`, `thinking` (bool)
  - `tags` (live/background suitability)
- Seed it per D12: Qwen3-8B, Qwen3-14B, Qwen3.5-9B, prod's 27B with its mmproj, and Qwen3-4B with its provider fixed.
- The admin catalog in `models.ts` is replaced by `GET /api/v0/llm/catalog` (Phase 6).

**Download manager** (`internal/modules/llm/download`)

- It is a durable queue job type `llm.download`, with concurrency 1 and dedup key = target path. That gives restart-resume for free.
- It uses HTTP Range against `huggingface.co/<repo>/resolve/<rev>/<file>`.
  - It pins `rev` to a commit SHA from the catalog, so the file can't drift.
  - It sends a Bearer token when set.
  - It reports progress in the `llm_models` row, polled by admin.
- It respects `HF_ENDPOINT` for mirrors.

**Installer and admin flow** (for Phase 6, recorded here)

1. Hardware detection runs: per-card VRAM and backend (01 §3.4).
2. The catalog recommends a model per slot from the largest single card. A `background = shared` default applies when only one card fits.
3. The user picks a model; jarvisd downloads the weights and the right engine flavour.
4. The settings write generates engine argv and restarts only that engine.

Cloud and remote are first-class options in the same screen (01 §3.1): for example OpenAI with an API key for a CPU-only box. That replaces today's CI-only env override.
