# Recipes moves INTO jarvisd

- **Date:** 2026-10-08
- **Moved from** the STATUS.md decision log on 2026-10-14.

**Recipes moves INTO jarvisd** (user: "remove architectural complexity and just move recipes within the main jarvis server"). Reverses 2026-10-06 "recipes stays out". A `recipes` module on 7030 with wire-compatible routes for the mobile app; SQLite, jarvisd's queue and blob store (no Postgres/Redis/MinIO); OCR/LLM in process. Cutover imports prod's recipes data (48 recipes, 2 meal plans, photos) mapped to new accounts by email. recipes PR #39 (add-on on jarvisd) not merged; jarvisd's own app client from that work is kept.
