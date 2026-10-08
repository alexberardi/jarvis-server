# ocr schema baseline

Migration: `internal/modules/ocr/migrations/00001_baseline.sql`. Source: jarvis-ocr-service.

## Source and head

- **Live dev DB** (MacBook Pro, `jarvis_ocr`) at `003`.
- Git head is `002`. `003_fix_seed_drift.py` is **uncommitted on the MBP checkout**; it only
  rewrites/inserts settings rows (`ocr.enable_rapidocr`, `ocr.enabled_tiers`,
  `ocr.validation_model`), no DDL. The schema is identical at both.

## Tables (0)

The only legacy table was `settings`. The baseline is an intentional no-op (`SELECT 1`). The OCR
job queue was Redis/RQ; it becomes the platform queue, not a module table.

## Type notes

The legacy `settings` table here was `timestamptz` (other services used naive `timestamp`).

## Dropped

`alembic_version`; `settings` (platform-owned).

## Setting keys

From `jarvis-ocr-service/app/services/settings_definitions.py` (input for the module's settings `Definitions`; not implemented yet). `(reload)` = `requires_reload`.

| key | type | default | env fallback | description |
|---|---|---|---|---|
| `ocr.enable_easyocr` | bool | `False` | OCR_ENABLE_EASYOCR | Enable EasyOCR backend (reload) |
| `ocr.enable_paddleocr` | bool | `False` | OCR_ENABLE_PADDLEOCR | Enable PaddleOCR backend (reload) |
| `ocr.enable_rapidocr` | bool | `False` | OCR_ENABLE_RAPIDOCR | Enable RapidOCR backend (ONNX Runtime-based) (reload) |
| `ocr.enable_apple_vision` | bool | `False` | OCR_ENABLE_APPLE_VISION | Enable Apple Vision backend (macOS only) (reload) |
| `ocr.enable_llm_proxy_vision` | bool | `False` | OCR_ENABLE_LLM_PROXY_VISION | Enable LLM Proxy vision mode for OCR (reload) |
| `ocr.enable_llm_proxy_cloud` | bool | `False` | OCR_ENABLE_LLM_PROXY_CLOUD | Enable LLM Proxy cloud mode for OCR (reload) |
| `ocr.max_text_bytes` | int | `51200` | OCR_MAX_TEXT_BYTES | Maximum output text size in bytes (truncates if exceeded) |
| `ocr.min_valid_chars` | int | `3` | OCR_MIN_VALID_CHARS | Minimum characters for valid OCR output |
| `ocr.language_default` | string | `'en'` | OCR_LANGUAGE_DEFAULT | Default language hint for OCR |
| `ocr.max_attempts` | int | `3` | OCR_MAX_ATTEMPTS | Maximum retry attempts for failed OCR jobs |
| `ocr.enabled_tiers` | string | `'tesseract,easyocr,paddleocr,rapidocr,apple_vision,llm_local,llm_cloud'` | OCR_ENABLED_TIERS | Comma-separated list of enabled provider tiers for fallback |
| `ocr.validation_model` | string | `'live'` | OCR_VALIDATION_MODEL | LLM model used for output validation |
| `server.log_level` | string | `'INFO'` | OCR_LOG_LEVEL | Logging level |
| `auth.cache_ttl_seconds` | int | `60` | JARVIS_APP_AUTH_CACHE_TTL_SECONDS | Auth validation cache TTL in seconds |

- **Cut keys** (engines cut by PLAN §7): `ocr.enable_easyocr`, `ocr.enable_paddleocr`, `ocr.enable_rapidocr`; drop `easyocr`, `paddleocr`, `rapidocr` from `ocr.enabled_tiers` values on import.

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

## Go module notes (2026-10-06)

- **Still no table.** Job state for `POST /v1/ocr` and `POST /v1/ocr/jobs` lives in the blob store
  under `ocr/jobs/<job_id>/` (`job.json` plus `image-<i>`, images deleted when the job ends). The
  hourly `ocr.purge` job drops a job 24 h after its last write, which is the legacy Redis TTL.
  The work runs as platform queue jobs: `ocr.job` (concurrency `Module.Concurrency`, default 1,
  retries up to `ocr.max_attempts` for infrastructure failures only), `ocr.callback`, `ocr.purge`.
- **Settings ported** (read live, no env fallback, M3): `ocr.enable_apple_vision`,
  `ocr.enable_llm_proxy_vision`, `ocr.max_text_bytes`, `ocr.min_valid_chars`,
  `ocr.language_default`, `ocr.max_attempts`, `ocr.enabled_tiers` (default now
  `tesseract,apple_vision,llm_local`; the legacy `remote_ocr` tier name is accepted as an alias
  for `apple_vision`), `ocr.validation_model`. `ocr.enable_apple_vision` defaults **on on macOS**
  (Vision is built in there, ID13) and off elsewhere. **New:** `ocr.llm_vision_timeout_seconds`
  (int, default 180, read live): LLM vision's per-image timeout, legacy 60 s (A10e M4).
  **Dropped:** the three cut-engine flags, `ocr.enable_llm_proxy_cloud` (a second LLM tier on the
  same `background` model), `server.log_level` (jarvisd logs), `auth.cache_ttl_seconds` (auth is
  in-process). Import skips their rows. The two `enable_*` flags are no longer `requires_reload`.
- **Engines.** tesseract via exec when on PATH or in the Homebrew/MacPorts dirs
  (`Module.TesseractPath`, `-` disables). **Apple Vision** (one engine, `apple_vision`):
  - **macOS: in process** (ID13, `vision_darwin.go`): `VNRecognizeTextRequest` through purego's
    Objective-C runtime, no cgo, no helper. Foundation + Vision are `dlopen`ed from
    `/System/Library/Frameworks`; the image bytes go to `VNImageRequestHandler
    initWithData:options:` as an `NSData` (Vision decodes JPEG/PNG/HEIC and honours EXIF
    orientation, as jarvis-osx-api did), recognition level **Accurate (0)**, language correction
    on, recognition languages from the hints (`en` → `en-US` …; retried with Vision's defaults if
    a language is refused), then `results` → `topCandidates:1` → `string`, `confidence`,
    `boundingBox`. Mapping (`mapVision`, pure Go): one observation per line joined with `\n`; boxes
    flipped to a top-left origin and scaled to pixels when Go can size the image (else left
    normalised, like the HTTP route); block confidence 0–1 (Recognize reports the mean on 0–100).
    Crash safety: every class is looked up and every selector checked with
    `respondsToSelector:`/`instancesRespondToSelector:` at load and on each returned object
    before it is messaged (an ObjC exception would abort jarvisd); Go panics recovered; anything
    missing → not built, a start-up WARN with the reason. Memory: one locked OS thread and one
    `NSAutoreleasePool` per recognition, the alloc'd handler and request released. One recognition
    at a time; a cancelled context sends the request `cancel`. No entitlement and no TCC grant:
    Vision is an Apple system framework (the notarized binary keeps only
    `disable-library-validation`; verified under an ad-hoc hardened-runtime signature with that
    entitlement on the MBP).
  - **Through jarvis-osx-api** (`POST /v1/ocr`, Bearer an `ocr:read` key) when
    `JARVIS_OSX_API_URL`/`JARVIS_OSX_API_KEY` are set: the fallback behind the native reader on
    macOS (used when it is unavailable or fails), the only route on other OSes. The legacy
    in-process PyObjC provider and the separate `remote_ocr` name are folded into `apple_vision`.
  LLM vision and text validation through an OpenAI-compatible URL (`Module.LLMURL`, the legacy
  llm-proxy during the strangler phase).
- **Where tiers apply.** As before, `POST /v1/ocr/batch` with `auto` walks the fixed engine order
  (tesseract → apple_vision → llm_proxy_vision) and ignores `ocr.enabled_tiers`; the queued paths
  use the tier chain (enabled tiers, normalize, LLM-validate, truncate to `ocr.max_text_bytes`).

## Recipes handoff (replaces the Redis queue-flow)

**Legacy.** `jarvis-recipes-server` `api/routes/from_image.py` uploaded the images to its own
S3/MinIO, then `queue_service.enqueue_ocr_request` LPUSHed a queue-flow v1 envelope
(`job_type: ocr.extract_text.requested`, `payload.image_refs: [{kind:"s3", value:"s3://…", index}]`,
`reply_to: jarvis.recipes.jobs`) onto every queue in `OCR_QUEUES` (default `jarvis.ocr.jobs`).
The OCR worker BRPOPped it, resolved the S3 refs, ran the tier chain and **RQ-enqueued** an
`ocr.completed` envelope onto `jarvis.recipes.jobs` (function
`jarvis_recipes.app.services.queue_worker.process_job`, arg = the JSON string), which recipes'
parse worker routes by `workflow_id`. The synchronous `/v1/ocr/batch` path
(`image_ingest_pipeline`) is only reached by recipes' deprecated legacy image-job handler.

**jarvisd** owns the queue; recipes talks HTTP (decision 2026-10-06). No Redis/RQ in jarvisd.

`POST /v1/ocr/jobs` (app auth) → **202** `{"job_id", "status": "pending", "created_at"}`.
Body, either:

- `multipart/form-data` (what recipes should send, it already holds the bytes): repeated
  `images` file parts (1-8, in index order; the part's Content-Type, else sniffed), and optional
  fields `options` (JSON, `{"language": "en"}`) or `language`, `callback_url`, `workflow_id`,
  `parent_job_id`, `request_id`, `source`.
- JSON: `{"images": [{"content_type", "base64"}], "options": {"language"}, "callback_url",
  "workflow_id", "parent_job_id", "request_id", "source"}`.

422s: no images / more than 8 (`["body","images"]`), a non-http(s) `callback_url`
(`["body","callback_url"]`). No URL/S3 fetching on purpose: jarvisd never needs recipes'
storage credentials, and no SSRF surface.

`GET /v1/ocr/jobs/{job_id}` → the legacy status envelope `{job_id, status
(pending|processing|completed|failed), created_at, updated_at, result, error}` where, for these
jobs, `result` is the legacy **ocr.completed payload**: `{status: "success"|"failed", results:
[{index, ocr_text, truncated, meta: {language, confidence, text_len, is_valid, tier,
validation_reason}, error: null|{code, message}}], artifact_ref: null, error: {code, message}}`
(`status` is `success` when any image is valid; PDFs are `unsupported_media` per image). A job
whose processing itself fails after retries is `status: "failed"` with a payload
`{status: "failed", results: [], error: {code: "internal_error", …}}`.

**Callback.** With `callback_url`, on completion jarvisd POSTs the full legacy `ocr.completed`
envelope (`schema_version: 1, job_id (fresh uuid, stable across retries), workflow_id,
job_type: "ocr.completed", source: "jarvis-ocr-service", target (= request source), created_at,
attempt: 1, reply_to: null, payload, trace: {request_id, parent_job_id}`, plus `ocr_job_id`) with
jarvisd's app credentials (`X-Jarvis-App-Id/Key`, `Module.AppID/AppKey`), as an `ocr.callback`
queue job retried with exponential backoff until a 2xx (12 attempts over about 25 minutes).
`workflow_id`/`parent_job_id` default to the OCR job id.

**Recipes-side change (done separately, Python):**

1. `from_image.py`: keep the S3 upload (recipes still stores the images). Replace
   `queue_service.enqueue_ocr_request(...)` with one HTTP call per OCR host (today: one, jarvisd):
   `POST {ocr_url}/v1/ocr/jobs`, multipart, the same resized bytes as `images` parts in index
   order, `options={"language":"en"}`, `workflow_id=job.id`, `parent_job_id=job.id`,
   `source="jarvis-recipes-server"`, `callback_url={recipes_public_url}/internal/ocr/callback`,
   app headers. `sent` (→ `ingestion.ocr_expected`) = the number of 202s.
2. New route `POST /internal/ocr/callback` (app auth: accept jarvisd's app id): validate the body
   has `job_type == "ocr.completed"`, then
   `Queue("jarvis.recipes.jobs").enqueue("jarvis_recipes.app.services.queue_worker.process_job",
   json.dumps(body), job_id=body["job_id"], job_timeout="10m")` and answer 204. A repeat delivery
   carries the same `job_id`, so RQ dedups it. Everything downstream (`_process_ocr_completed`,
   the ensemble join) is unchanged.
3. Optional: a poller fallback on `GET /v1/ocr/jobs/{id}` is not needed (callbacks retry), but
   the result stays readable there for 24 h.
