# llm schema baseline

Migration: `internal/modules/llm/migrations/00001_baseline.sql`. Source: jarvis-llm-proxy-api.

## Source and head

**Live dev DB** (MacBook Pro, `jarvis_llm_proxy`) at `g7h8i9j0k1l2`, equal to the repo head.

## Tables (0)

The baseline is an intentional no-op (`SELECT 1`). Every legacy table is dropped:

| Legacy table | Why dropped |
|---|---|
| `training_jobs` | LoRA adapter training — cut (PLAN §7, Appendix A) |
| `service_configs` | Not an alembic table: jarvis-config-client's local discovery cache (`CREATE TABLE IF NOT EXISTS` in `jarvis_config_client/client.py`). Discovery is in-process in jarvisd. |
| `settings` | platform-owned |
| `alembic_version` | goose replaces it |

The llm-proxy job queue (Redis/RQ, `llmproxy:dedupe`) becomes the platform queue.

## Legacy-import notes

- Nothing to import except settings.
- The `llm.interface` → `llm.prompt_provider` rename (D11, EXTERNAL-CHANGES.md) is a
  **command-center** setting, not one of llm-proxy's; the CC baseline handles it.

## Setting keys

From `jarvis-llm-proxy-api/services/settings_service.py` (input for the module's settings `Definitions`; not implemented yet). `(reload)` = `requires_reload`.

| key | type | default | env fallback | description |
|---|---|---|---|---|
| `model.live.name` | string | `''` | JARVIS_LIVE_MODEL_NAME | Live model path or HuggingFace ID (falls back to model.main.name) (reload) |
| `model.live.backend` | string | `''` | JARVIS_LIVE_MODEL_BACKEND | Live model backend (falls back to model.main.backend) (reload) |
| `model.live.chat_format` | string | `''` | JARVIS_LIVE_MODEL_CHAT_FORMAT | Live model chat format (falls back to model.main.chat_format) (reload) |
| `model.live.context_window` | int | `0` | JARVIS_LIVE_MODEL_CONTEXT_WINDOW | Live model context window (falls back to model.main.context_window) (reload) |
| `model.live.stop_tokens` | string | `''` | JARVIS_LIVE_MODEL_STOP_TOKENS | Live model stop tokens (falls back to model.main.stop_tokens) (reload) |
| `model.live.rest_url` | string | `''` | JARVIS_LIVE_REST_MODEL_URL | REST URL for live model backend (falls back to model.main.rest_url) (reload) |
| `model.live.reasoning_budget` | string | `''` | JARVIS_LIVE_REASONING_BUDGET | Thinking token budget for the LIVE model on a reasoning-capable backend (e.g. Qwen3.5 via llama-server/REST): 0 = off (fast voice), -1 = unrestricted, N = cap. Blank falls back to model.main.reasoning_budget, then the server's own --reasoning-budget. A request may override it per-turn. (reload) |
| `model.live.supports_images` | bool | `False` | JARVIS_LIVE_SUPPORTS_IMAGES | Set true when the LIVE slot's model was loaded with a vision projector (llama-server --mmproj) and can therefore accept image content. When false, chat requests carrying an image are rejected with 400 before any backend call. (reload) |
| `model.background.name` | string | `''` | JARVIS_BACKGROUND_MODEL_NAME | Background model path (empty shares live model) (reload) |
| `model.background.backend` | string | `''` | JARVIS_BACKGROUND_MODEL_BACKEND | Background model backend (falls back to live backend) (reload) |
| `model.background.chat_format` | string | `''` | JARVIS_BACKGROUND_MODEL_CHAT_FORMAT | Background model chat format (falls back to live) (reload) |
| `model.background.context_window` | int | `0` | JARVIS_BACKGROUND_MODEL_CONTEXT_WINDOW | Background model context window (falls back to live) (reload) |
| `model.background.stop_tokens` | string | `''` | JARVIS_BACKGROUND_MODEL_STOP_TOKENS | Background model stop tokens (falls back to live) (reload) |
| `model.background.rest_url` | string | `''` | JARVIS_BACKGROUND_REST_MODEL_URL | REST URL for background model backend (falls back to live) (reload) |
| `model.background.rest_model_name` | string | `''` | JARVIS_REST_BACKGROUND_MODEL_NAME | Model name sent to the background REST backend (overrides model.background.name in requests) (reload) |
| `model.background.reasoning_budget` | string | `''` | JARVIS_BACKGROUND_REASONING_BUDGET | Thinking token budget for the BACKGROUND model on a reasoning-capable backend: 0 = off, -1 = unrestricted, N = cap. Blank falls back to model.main.reasoning_budget, then the server's own --reasoning-budget. A queue job may override it per-job via reasoning_budget. (reload) |
| `model.background.supports_images` | bool | `False` | JARVIS_BACKGROUND_SUPPORTS_IMAGES | Set true when the BACKGROUND slot's model was loaded with a vision projector and can accept image content on queue jobs. (reload) |
| `model.main.name` | string | `'.models/llama-3.1-8b-instruct-jarvis-Q4_K_M.gguf'` | JARVIS_MODEL_NAME | Main model path or HuggingFace ID (legacy, fallback for model.live) (reload) |
| `model.main.backend` | string | `'GGUF'` | JARVIS_MODEL_BACKEND | Main model backend (legacy, fallback for model.live) (reload) |
| `model.main.chat_format` | string | `'llama3'` | JARVIS_MODEL_CHAT_FORMAT | Chat template format (legacy, fallback for model.live) (reload) |
| `model.main.context_window` | int | `8192` | JARVIS_MODEL_CONTEXT_WINDOW | Maximum context window (legacy, fallback for model.live) (reload) |
| `model.main.stop_tokens` | string | `''` | JARVIS_MODEL_STOP_TOKENS | Comma-separated stop tokens (legacy, fallback for model.live) (reload) |
| `inference.vllm.gpu_memory_utilization` | float | `0.9` | JARVIS_VLLM_GPU_MEMORY_UTILIZATION | GPU memory utilization (0.0-1.0) (reload) |
| `inference.vllm.tensor_parallel_size` | int | `1` | JARVIS_VLLM_TENSOR_PARALLEL_SIZE | Number of GPUs for tensor parallelism (reload) |
| `inference.vllm.max_batched_tokens` | int | `8192` | JARVIS_VLLM_MAX_BATCHED_TOKENS | Maximum batched tokens for vLLM (reload) |
| `inference.vllm.max_num_seqs` | int | `256` | JARVIS_VLLM_MAX_NUM_SEQS | Maximum number of sequences (reload) |
| `inference.vllm.quantization` | string | `''` | JARVIS_VLLM_QUANTIZATION | vLLM quantization method (reload) |
| `inference.vllm.max_lora_rank` | int | `64` | JARVIS_VLLM_MAX_LORA_RANK | Maximum LoRA rank for adapters (reload) |
| `inference.vllm.max_loras` | int | `1` | JARVIS_VLLM_MAX_LORAS | Maximum concurrent LoRA adapters (reload) |
| `inference.gguf.n_gpu_layers` | int | `-1` | JARVIS_N_GPU_LAYERS | GPU layers (-1=all, 0=CPU only) (reload) |
| `inference.gguf.split_mode` | int | `-1` | JARVIS_GGUF_SPLIT_MODE | Multi-GPU split: -1=auto (recommended: splits layers across GPUs when 2+ capable NVIDIA GPUs are visible — identical cards, or mixed cards all >=8GB; single-GPU otherwise), 0=single GPU (main_gpu only), 1=split layers across GPUs, 2=split rows. (reload) |
| `inference.gguf.main_gpu` | int | `0` | JARVIS_GGUF_MAIN_GPU | GPU index to use (with split_mode=0) or for scratch/small tensors (when splitting) (reload) |
| `inference.gguf.tensor_split` | string | `''` | JARVIS_GGUF_TENSOR_SPLIT | Comma-separated VRAM proportions per GPU for multi-GPU split (e.g. '0.5,0.5'). Requires split_mode>=1. (reload) |
| `inference.gguf.n_batch` | int | `512` | JARVIS_N_BATCH | Batch size for llama.cpp (reload) |
| `inference.gguf.n_ubatch` | int | `512` | JARVIS_N_UBATCH | Micro-batch size (reload) |
| `inference.gguf.n_threads` | int | `10` | JARVIS_N_THREADS | Number of CPU threads (reload) |
| `inference.gguf.flash_attn` | bool | `True` | JARVIS_FLASH_ATTN | Enable flash attention (reload) |
| `inference.gguf.f16_kv` | bool | `True` | JARVIS_F16_KV | Use FP16 for KV cache (reload) |
| `inference.gguf.mul_mat_q` | bool | `True` | JARVIS_MUL_MAT_Q | Enable quantized matrix multiplication (reload) |
| `inference.transformers.device` | string | `'auto'` | JARVIS_DEVICE | Compute device (reload) |
| `inference.transformers.torch_dtype` | string | `'auto'` | JARVIS_TORCH_DTYPE | Torch dtype (reload) |
| `inference.transformers.use_quantization` | bool | `False` | JARVIS_USE_QUANTIZATION | Enable bitsandbytes quantization (reload) |
| `inference.transformers.quantization_type` | string | `'4bit'` | JARVIS_QUANTIZATION_TYPE | Quantization type (reload) |
| `inference.transformers.device_map` | string | `'auto'` | JARVIS_TRANSFORMERS_DEVICE_MAP | Device map for transformers (reload) |
| `inference.general.engine` | string | `'llama_cpp'` | JARVIS_INFERENCE_ENGINE | Default inference engine (reload) |
| `inference.general.max_tokens` | int | `512` | JARVIS_MAX_TOKENS | Default max generation tokens |
| `inference.general.top_p` | float | `0.95` | JARVIS_TOP_P | Top-P sampling value |
| `inference.general.top_k` | int | `40` | JARVIS_TOP_K | Top-K sampling value |
| `inference.general.repeat_penalty` | float | `1.1` | JARVIS_REPEAT_PENALTY | Repetition penalty |
| `training.adapter_dir` | string | `'/tmp/jarvis-adapters'` | LLM_PROXY_ADAPTER_DIR | Local adapter storage directory |
| `training.batch_size` | int | `1` | JARVIS_ADAPTER_BATCH_SIZE | Training batch size |
| `training.grad_accum` | int | `4` | JARVIS_ADAPTER_GRAD_ACCUM | Gradient accumulation steps |
| `training.epochs` | int | `1` | JARVIS_ADAPTER_EPOCHS | Training epochs |
| `training.learning_rate` | float | `0.0002` | JARVIS_ADAPTER_LEARNING_RATE | Training learning rate |
| `training.lora_r` | int | `16` | JARVIS_ADAPTER_LORA_R | LoRA rank |
| `training.lora_alpha` | int | `32` | JARVIS_ADAPTER_LORA_ALPHA | LoRA alpha scaling |
| `training.lora_dropout` | float | `0.05` | JARVIS_ADAPTER_LORA_DROPOUT | LoRA dropout rate |
| `training.max_seq_len` | int | `2048` | JARVIS_ADAPTER_MAX_SEQ_LEN | Maximum sequence length for training |
| `storage.s3_endpoint_url` | string | `''` | S3_ENDPOINT_URL | S3 endpoint URL (for MinIO) |
| `storage.s3_region` | string | `'us-east-1'` | S3_REGION | S3 region |
| `storage.adapter_bucket` | string | `'jarvis-llm-proxy'` | LLM_PROXY_ADAPTER_BUCKET | S3 bucket for adapters |
| `storage.adapter_prefix` | string | `'adapters'` | LLM_PROXY_ADAPTER_PREFIX | S3 prefix for adapters |
| `storage.s3_force_path_style` | bool | `False` | S3_FORCE_PATH_STYLE | Force S3 path-style addressing (MinIO) |
| `logging.console_level` | string | `'WARNING'` | JARVIS_LOG_CONSOLE_LEVEL | Console log level |
| `logging.remote_level` | string | `'DEBUG'` | JARVIS_LOG_REMOTE_LEVEL | Remote (jarvis-logs) log level |
| `model.main.rest_url` | string | `''` | JARVIS_REST_MODEL_URL | REST URL for main model backend (reload) |
| `model.main.rest_model_name` | string | `''` | JARVIS_REST_MODEL_NAME | REST model name override for main model (reload) |
| `cache.type` | string | `'local'` | JARVIS_CACHE_TYPE | Cache type |
| `cache.session_ttl_seconds` | int | `600` | JARVIS_SESSION_TTL | Session TTL in seconds |
| `cache.cleanup_interval_seconds` | int | `30` | JARVIS_CACHE_CLEANUP_INTERVAL | Cache cleanup interval in seconds |
| `adapter_cache.max_size` | int | `10` | LLM_PROXY_ADAPTER_CACHE_MAX_SIZE | Max adapters tracked in memory |
| `adapter_cache.evict_disk` | bool | `False` | LLM_PROXY_ADAPTER_CACHE_EVICT_DISK | Evict adapter disk cache on LRU eviction |
| `model_service.url` | string | `'http://127.0.0.1:7705'` | MODEL_SERVICE_URL | Internal model service URL |
| `model_service.timeout_seconds` | float | `60.0` | MODEL_SERVICE_TIMEOUT | Timeout in seconds for model service requests |
| `queue.name` | string | `'llm_proxy_jobs'` | LLM_PROXY_QUEUE_NAME | Queue name for async jobs |
| `queue.per_attempt_timeout_seconds` | float | `0.0` | LLM_PROXY_PER_ATTEMPT_TIMEOUT | Per-attempt timeout seconds (0 for none) |
| `queue.callback_timeout_seconds` | float | `10.0` | LLM_PROXY_CALLBACK_TIMEOUT | Callback timeout seconds |
| `inference.gguf.enable_context_cache` | bool | `True` | JARVIS_ENABLE_CONTEXT_CACHE | Enable GGUF context cache |
| `inference.gguf.max_cache_size` | int | `100` | JARVIS_MAX_CACHE_SIZE | Max GGUF context cache size |
| `inference.gguf.rope_scaling_type` | int | `0` | JARVIS_ROPE_SCALING_TYPE | RoPE scaling type for GGUF |
| `inference.gguf.seed` | int | `42` | JARVIS_SEED | Random seed for GGUF backend |
| `inference.gguf.verbose` | bool | `False` | JARVIS_VERBOSE | Enable verbose GGUF logging |
| `inference.gguf.mirostat_mode` | int | `0` | JARVIS_MIROSTAT_MODE | Mirostat mode for GGUF |
| `inference.gguf.mirostat_tau` | float | `5.0` | JARVIS_MIROSTAT_TAU | Mirostat tau for GGUF |
| `inference.gguf.mirostat_eta` | float | `0.1` | JARVIS_MIROSTAT_ETA | Mirostat eta for GGUF |
| `inference.transformers.do_sample` | bool | `True` | JARVIS_DO_SAMPLE | Enable sampling for transformers backend |
| `inference.transformers.use_cache` | bool | `True` | JARVIS_USE_CACHE | Use model cache for transformers backend |
| `inference.transformers.trust_remote_code` | bool | `False` | JARVIS_TRUST_REMOTE_CODE | Trust remote code for transformers backend |
| `rest.provider` | string | `'generic'` | JARVIS_REST_PROVIDER | REST provider name |
| `rest.request_format` | string | `'openai'` | JARVIS_REST_REQUEST_FORMAT | REST request format |
| `rest.timeout_seconds` | int | `60` | JARVIS_REST_TIMEOUT | REST request timeout seconds |
| `rest.auth_type` | string | `'none'` | JARVIS_REST_AUTH_TYPE | REST auth type |
| `rest.auth_header_name` | string | `'Authorization'` | JARVIS_REST_AUTH_HEADER | REST auth header name |
| `rest.auth_token` | string | `''` | JARVIS_REST_AUTH_TOKEN | Auth token / API key for the REST backend (e.g. OpenAI API key) **secret** (reload) |
| `training.backend` | string | `'auto'` | JARVIS_ADAPTER_TRAIN_BACKEND | Training backend (auto detects platform) |
| `training.train_cmd` | string | `'python3 scripts/train_adapter.py'` | JARVIS_ADAPTER_TRAIN_CMD | Adapter training command (overrides backend auto-detection if changed from default) |
| `training.public_url_prefix` | string | `''` | JARVIS_ADAPTER_PUBLIC_URL_PREFIX | Public URL prefix for training artifacts |
| `training.train_timeout_seconds` | int | `0` | JARVIS_ADAPTER_TRAIN_TIMEOUT_SECONDS | Training timeout seconds (0 for default) |
| `training.adapter_hf_base_model_id` | string | `''` | JARVIS_ADAPTER_HF_BASE_MODEL_ID | HF base model ID for GGUF training |
| `training.adapter_gguf_convert_cmd` | string | `''` | JARVIS_ADAPTER_GGUF_CONVERT_CMD | GGUF conversion command override |
| `training.adapter_train_dtype` | string | `'auto'` | JARVIS_ADAPTER_TRAIN_DTYPE | Training dtype |
| `training.adapter_train_load_in_4bit` | bool | `False` | JARVIS_ADAPTER_TRAIN_LOAD_IN_4BIT | Use 4bit training load |
| `training.adapter_train_load_in_8bit` | bool | `False` | JARVIS_ADAPTER_TRAIN_LOAD_IN_8BIT | Use 8bit training load |
| `training.adapter_train_device_map` | string | `''` | JARVIS_ADAPTER_TRAIN_DEVICE_MAP | Training device map override |
| `training.date_adapter_train_load_in_4bit` | bool | `True` | JARVIS_DATE_ADAPTER_TRAIN_LOAD_IN_4BIT | Date adapter training 4bit load |
| `training.output_dir` | string | `''` | JARVIS_TRAIN_OUTPUT_DIR | Training output directory |
| `training.dataset_path` | string | `''` | JARVIS_TRAIN_DATASET_PATH | Training dataset path |
| `training.params_path` | string | `''` | JARVIS_TRAIN_PARAMS_PATH | Training params path |
| `training.base_model_id` | string | `''` | JARVIS_TRAIN_BASE_MODEL_ID | Training base model ID override |
| `adapter.pause_serving_during_training` | bool | `True` | ADAPTER_PAUSE_SERVING_DURING_TRAINING | Unload the live model before adapter training and reload after. Required on single-GPU hosts where serving + training can't coexist (e.g. 12GB Ubuntu boxes). Voice commands will 5xx briefly during the pause window. Safe to disable on hosts with enough headroom or on MLX (Apple) setups where training is lightweight and model_service.url is typically unset. |
| `date_keys.disable_llm` | bool | `False` | JARVIS_DISABLE_DATE_KEY_LLM | Disable LLM date key extraction |
| `date_keys.device_map` | string | `'cpu'` | JARVIS_DATE_KEY_DEVICE_MAP | Device map for date key LLM |
| `debug.enabled` | bool | `False` | DEBUG | Enable debug mode |
| `debug.port` | int | `5678` | DEBUG_PORT | Debug port for debugpy |
| `debug.dump_gbnf_path` | string | `''` | JARVIS_DUMP_GBNF_PATH | Path to dump GBNF grammar |

- **Cut keys** (LoRA/adapters and the in-process TRANSFORMERS/vLLM/MLX backends, PLAN §7): every `training.*`, `adapter.*`, `adapter_cache.*`, `storage.adapter_bucket`, `storage.adapter_prefix`, `inference.vllm.*`, `inference.transformers.*`.
- **Probably cut, decide in Phase 3** (kept in the list): `cache.*` (legacy `cache/` is cut), `model_service.*` (the separate model-service process disappears), `date_keys.device_map` (torch), `debug.*` (debugpy), `storage.s3_*` (only adapter storage used them), `inference.gguf.*` and `model.main.*` (llama-cpp-python in-process; `llama-server` flags may map onto some), `queue.name`.
- Dev DB seeds only 39 of the 115 keys.

## Settings import (all modules with a settings table)

The legacy `settings` table is **not** in this baseline: `internal/platform/settings` creates
`<module>_settings` with the same columns (`key, value, value_type, category, description,
requires_reload, is_secret, env_fallback, household_id, node_id, user_id, created_at,
updated_at`). Import copies rows 1:1, with these transforms:

- booleans → 0/1; timestamps → ISO-8601 UTC text.
- The SQLite table has `UNIQUE(key, COALESCE(household_id,''), COALESCE(node_id,''), COALESCE(user_id,0))`.
  Postgres' `uq_setting_scope` let duplicate system-scope rows through (NULLs never collide), so
  dedupe on import, keeping the most recently updated row.
- `value` is already JSON/text-encoded by the settings client; copy it verbatim.
- Rows whose key is no longer defined (cut keys, listed above) are skipped.
- Keys defined but never seeded are fine: the definition default applies.
