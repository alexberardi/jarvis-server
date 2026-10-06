# tts schema baseline

Migration: `internal/modules/tts/migrations/00001_baseline.sql`. Source: jarvis-tts.

## Source and head

**Live dev DB** (MacBook Pro, `jarvis_tts`) at `004`, equal to the repo head.

## Tables (0)

The only legacy table was `settings`. The baseline is an intentional no-op (`SELECT 1`).

## Dropped

`alembic_version`; `settings` (platform-owned).

## Setting keys

From `jarvis-tts/app/services/settings_definitions.py` (input for the module's settings `Definitions`; not implemented yet). `(reload)` = `requires_reload`.

| key | type | default | env fallback | description |
|---|---|---|---|---|
| `tts.llm_proxy_version` | int | `1` | JARVIS_LLM_PROXY_API_VERSION | LLM Proxy API version for wake responses |
| `tts.provider` | string | `'piper'` | TTS_PROVIDER | Active TTS provider backend |
| `tts.default_voice` | string | `'en_GB-alan-low'` | TTS_DEFAULT_VOICE | Piper voice model name (looks up app/models/<name>.onnx) |
| `tts.kokoro_voice` | string | `'bm_george'` | TTS_KOKORO_VOICE | Kokoro voice ID (e.g., bm_george, bm_fable, af_heart) |
| `tts.kokoro_speed` | float | `1.25` | TTS_KOKORO_SPEED | Kokoro speech speed multiplier |
| `tts.kokoro_device` | string | `'cpu'` | TTS_KOKORO_DEVICE | PyTorch device for Kokoro inference (cpu, cuda, mps) |
| `tts.kokoro_gain` | float | `2.0` | TTS_KOKORO_GAIN | Output amplitude multiplier for Kokoro. Compensates for its quiet output (~0.3-0.5 peak) vs Piper (~0.7-0.9). 1.0 = unchanged, 2.0 ≈ +6 dB (default — peaks ~0.7, no clipping), 3.0 ≈ +9.5 dB (peaks near full scale, mild saturation on loud chunks). |
| `tts.wake_system_prompt` | string | `"You are Jarvis, a voice assistant butler. The user has just called you for help. Please keep the greeting gender neutral. Please keep the greeting to one or two short sentences, but make it charming. The entire response should be less than 10 words if possible. Generate a short greeting like 'At your service', 'How may I help you?', etc."` | TTS_WAKE_SYSTEM_PROMPT | System prompt for generating wake responses |
| `server.port` | int | `7707` | TTS_PORT | API server port (reload) |
| `server.log_console_level` | string | `'INFO'` | JARVIS_LOG_CONSOLE_LEVEL | Console logging level |
| `server.log_remote_level` | string | `'DEBUG'` | JARVIS_LOG_REMOTE_LEVEL | Remote logging level |
| `auth.cache_ttl_seconds` | int | `60` | NODE_AUTH_CACHE_TTL | Auth validation cache TTL in seconds |

- **Cut keys:** `tts.default_voice` (Piper, cut), `tts.kokoro_device` (PyTorch device; sherpa Kokoro runs on CPU in-binary). `tts.provider` default becomes `kokoro` (legacy default `piper`; prod is already Kokoro). `tts.wake_system_prompt` and `tts.llm_proxy_version` belong to the deprecated `/generate-wake-response` route, which is cut.

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
