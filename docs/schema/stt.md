# stt schema baseline

Migration: `internal/modules/stt/migrations/00001_baseline.sql`. Source: jarvis-whisper-api.

## Source and head

**Live dev DB** (MacBook Pro, `jarvis_whisper`) at `005`, equal to the repo head.

## Tables (0)

The only legacy table was `settings`. The baseline is an intentional no-op (`SELECT 1`).
Voice profiles never lived in this DB, and voice is greenfield anyway (PLAN §5: users re-enroll).

## Dropped

`alembic_version`; `settings` (platform-owned).

## Setting keys

From `jarvis-whisper-api/app/services/settings_definitions.py` (input for the module's settings `Definitions`; not implemented yet). `(reload)` = `requires_reload`.

| key | type | default | env fallback | description |
|---|---|---|---|---|
| `whisper.use_gpu` | bool | `False` | WHISPER_USE_GPU | Offload whisper.cpp inference to the GPU. Only meaningful on the CUDA image (docker-compose.gpu.yaml / Dockerfile.gpu), which compiles whisper.cpp with -DGGML_CUDA=ON; on the CPU image this is a no-op. The legacy WHISPER_ENABLE_CUDA env var only ever documented this intent -- nothing read it, and the GPU image ignores it, so offload stayed off even with CUDA compiled in. (reload) |
| `whisper.model_path` | string | `'~/whisper.cpp/models/ggml-base.en.bin'` | WHISPER_MODEL | Path to the Whisper GGML model file (reload) |
| `whisper.allow_model_autodownload` | bool | `False` | WHISPER_ALLOW_MODEL_AUTODOWNLOAD | Allow whisper.cpp / pywhispercpp to auto-download a model from huggingface.co when no local file exists at whisper.model_path. Default false keeps the service fully offline: outbound internet is opt-in only. When false and no local model is present, the engine fails closed with an actionable error instead of egressing. Supply your own model via whisper.model_path / WHISPER_MODEL and leave this off, or set it true to permit the download. (reload) |
| `whisper.default_temperature` | float | `0.0` | WHISPER_DEFAULT_TEMPERATURE | Default initial temperature for sampling (0.0-1.0) |
| `whisper.default_temperature_inc` | float | `0.2` | WHISPER_DEFAULT_TEMPERATURE_INC | Default temperature increment on decode failure (0.0-1.0) |
| `whisper.default_beam_size` | int | `2` | WHISPER_DEFAULT_BEAM_SIZE | Default beam size for beam search (1-16), used when the caller doesn't pass beam_size explicitly. Decode time scales with beam size and STT sits on the voice hot path, so this defaults low. |
| `whisper.language` | string | `'en'` | WHISPER_LANGUAGE | Default language for transcription |
| `voice.recognition_enabled` | bool | `False` |  | Enable speaker identification |
| `voice.encoder` | string | `'ecapa'` |  | Speaker recognition encoder. 'ecapa' (SpeechBrain ECAPA-TDNN) is the modern default, better on short utterances. 'resemblyzer' is the legacy GE2E encoder, kept as a rollback option. |
| `voice.similarity_threshold` | float | `0.5` |  | Cosine similarity threshold for speaker matching on normal-length utterances (between voice.short_cutoff_seconds and voice.long_cutoff_seconds). Optimal value depends on the encoder — ECAPA typically wants ~0.50, resemblyzer ~0.75. Tune empirically using scripts/benchmark_speaker_encoders.py. |
| `voice.threshold_short` | float | `0.65` |  | Stricter threshold applied to short clips (duration < voice.short_cutoff_seconds). Defaults assume ECAPA; tune via the benchmark script. |
| `voice.threshold_long` | float | `0.4` |  | Relaxed threshold applied to long clips (duration > voice.long_cutoff_seconds). Defaults assume ECAPA. |
| `voice.min_speaker_margin` | float | `0.05` |  | Open-set/ambiguity gate. With 2+ enrolled voices, a recognition is REJECTED (returned as unknown speaker) when the winner's cosine beats the runner-up household member by less than this margin — the clip is too close to another member to safely commit an identity. Guards against misattribution when far-field live audio collapses cosines toward each other. 0 disables the gate. Calibrate from the 'margin=' values in the 'Speaker match' log line on real traffic; raise if misID persists, lower if it abstains on genuine matches. |
| `voice.short_cutoff_seconds` | float | `1.0` |  | Clips shorter than this use voice.threshold_short. |
| `voice.embed_preprocess_enabled` | bool | `False` |  | Apply symmetric preprocessing (DC removal + silence trim + RMS normalization) to BOTH enrollment and recognition audio before ECAPA embedding. Default OFF: an offline leave-one-out benchmark on real profiles showed it is score-neutral (ECAPA already mean-var-normalizes input), so it ships dormant pending evaluation against per-trial telemetry rather than changing production matching today. Flipping this re-embeds cached profiles on the next request. ECAPA only — resemblyzer already normalizes internally. |
| `voice.embed_target_rms_db` | float | `-23.0` |  | Target RMS level (dBFS) that recognition + enrollment audio is normalized to before ECAPA embedding when voice.embed_preprocess_enabled is true. Affects scoring consistency only, not the loudness of any audio output. |
| `voice.embed_trim_silence_db` | float | `-40.0` |  | Silence threshold (dBFS) for trimming leading/trailing silence before ECAPA embedding when voice.embed_preprocess_enabled is true. Conservative by default so speech is never removed; an all-silence clip falls back to the untrimmed signal. |
| `voice.long_cutoff_seconds` | float | `3.0` |  | Clips longer than this use voice.threshold_long. |
| `voice.emotion_enabled` | bool | `False` |  | Analyze the acoustic affect (arousal/energy) of the command audio and return it as an `affect` block on the transcribe response. Default OFF: when off, the analysis never runs (the response still carries `affect: null`). Sensitive inference — kept local, per-household opt-in, and not persisted. |
| `voice.emotion_min_confidence` | float | `0.45` |  | Minimum affect confidence (0-1) before the read is surfaced on the response. Below this, `affect` is null — a shaky read is worse than none, so we bias toward silence. Every read is still logged for threshold tuning regardless of this gate. |
| `server.port` | int | `7706` | PORT | API server port (reload) |
| `server.log_console_level` | string | `'INFO'` | JARVIS_LOG_CONSOLE_LEVEL | Console logging level |
| `server.log_remote_level` | string | `'DEBUG'` | JARVIS_LOG_REMOTE_LEVEL | Remote logging level |
| `auth.cache_ttl_seconds` | int | `60` | NODE_AUTH_CACHE_TTL | Auth validation cache TTL in seconds |

- **Cut/changed keys:** `voice.encoder` (`ecapa` is the cut torch stack; sherpa-onnx encoder replaces it), the `voice.*` thresholds need recalibration for the new encoder (PLAN §3.3) so imported values should not be trusted; `whisper.*` keys depend on D7 (whisper.cpp engine vs sherpa ASR).
- Dev DB seeds 13 of the 24 keys.

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
