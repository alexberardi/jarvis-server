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
