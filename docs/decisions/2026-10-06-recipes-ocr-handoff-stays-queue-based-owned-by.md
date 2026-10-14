# Recipes ↔ OCR handoff stays queue-based, owned by jarvisd

- **Date:** 2026-10-06
- **Moved from** the STATUS.md decision log on 2026-10-14.

**Recipes ↔ OCR handoff stays queue-based, owned by jarvisd** (user: OCR is long-running, it needs a queue). jarvisd OCR exposes an async HTTP job API (`POST /v1/ocr/jobs` → 202 job_id, `GET /v1/ocr/jobs/{id}`, optional `callback_url` POSTed with app creds on completion, retried) on the durable queue. Recipes changes `from_image.py` to submit over HTTP and adds a callback route that enqueues onto its own RQ. No Redis between them, no pickled jobs. Legacy sync `/v1/ocr/batch` kept.
