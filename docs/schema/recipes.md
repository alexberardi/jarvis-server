# recipes schema baseline

Migration: `internal/modules/recipes/migrations/00001_baseline.sql`. Source: jarvis-recipes-server.

## Source and head

**Live dev DB** (MacBook Pro, `jarvis_recipes`) at `e1f2a3b4c5d6` (`add_staples`), equal to the
repo head. The dev DB holds no recipe data (all tables empty), so value-level checks (enum
spelling etc.) were done against the models, not data.

## Tables (16)

| SQLite table | Legacy table | Notes |
|---|---|---|
| `recipes_users` | `users` | one column, `user_id` TEXT PK (auth user id as string) |
| `recipes_recipes` | `recipes` | `source_type` CHECK |
| `recipes_ingredients` | `ingredients` | CASCADE from recipe |
| `recipes_steps` | `steps` | UNIQUE(recipe_id, step_number) |
| `recipes_tags` | `tags` | UNIQUE name |
| `recipes_recipe_tags` | `recipe_tags` | composite PK, `WITHOUT ROWID` |
| `recipes_meal_plans` | `meal_plans` | |
| `recipes_meal_plan_items` | `meal_plan_items` | CASCADE from plan and recipe |
| `recipes_grocery_sku_map` | `grocery_sku_map` | unique scope index (nullable household_id) |
| `recipes_recipe_parse_jobs` | `recipe_parse_jobs` | UUID text id |
| `recipes_recipe_ingestions` | `recipe_ingestions` | UUID text id; `recipe_id` SET NULL |
| `recipes_mailbox_messages` | `mailbox_messages` | UUID text id |
| `recipes_stage_recipes` | `stage_recipes` | integer id (since `f6a7b8c9d0e1`) |
| `recipes_staples` | `staples` | UNIQUE(user_id, name) |
| `recipes_stock_ingredients` | `stock_ingredients` | reference data |
| `recipes_stock_units_of_measure` | `stock_units_of_measure` | reference data |

All FKs and ON DELETE actions kept. `grocery_sku_map`, `recipe_parse_jobs` never had a FK to
`users`; none added.

## Type notes

- All timestamps were naive `timestamp` (UTC: `datetime.utcnow` / `now()` on a UTC server).
  Stored ISO-8601 UTC text.
- `date` (`meal_plans.start_date`, `meal_plan_items.date`) → TEXT `YYYY-MM-DD`.
- `json` columns → TEXT: `recipe_ingestions.image_s3_keys` (JSON **array** of blob keys),
  `tier3_raw_response`, `pipeline_json`, `ocr_readings`; `recipe_parse_jobs.result_json`,
  `job_data`; `mailbox_messages.payload`; `stage_recipes.ingredients`, `steps`, `tags`, `notes`.
- `ingredients.quantity_value` `numeric(10,4)` → REAL. Values are 4-decimal quantities; a float64
  round-trip is exact enough, but golden fixtures should compare with a tolerance.
- `source_type` was a non-native enum (`varchar(6)`): now `TEXT CHECK (source_type IN
  ('manual','image','url'))`, default `'manual'`.
- `status` columns (`PENDING`, …), `job_type` (`url|ocr|social`), `meal_type`, `retailer`, `source`
  stay free TEXT as in legacy.
- **ORM-side defaults promoted to DB defaults:** `recipes.created_at/updated_at`,
  `meal_plans.created_at`, `source_type`, `use_llm_fallback` 1, `status` 'PENDING', `attempts` 0,
  `prep/cook_time_minutes` 0, `stage_recipes.tags/notes` `'[]'`.
- `updated_at` columns with ORM `onupdate`: no trigger, the app sets them.

## Dropped

- `alembic_version`; `settings` (platform-owned).
- Redundant indexes: every `ix_<table>_id` on a PK, `ix_users_user_id`,
  `ix_steps_recipe_id` (covered by the UNIQUE), `ix_staples_user_id` (covered by the UNIQUE),
  `ix_recipe_parse_jobs_user_id` (covered by `user_status`), `ix_grocery_sku_map_household_id`
  (leading column of the scope index).
- Added: indexes on `recipe_tags.tag_id` and `recipe_ingestions.recipe_id` (FK child columns).

## Legacy-import notes

- **Enum spelling:** SQLAlchemy non-native enums store the member *name*, so legacy
  `recipes.source_type` holds `MANUAL`/`IMAGE`/`URL`. Import must `lower()` it (verify against a
  prod snapshot; the dev DB is empty).
- Preserve integer ids (meal plans, ingestions and mobile clients reference recipe ids).
- Insert order: users → recipes → ingredients/steps/tags/recipe_tags → meal_plans → items →
  everything else.
- **Blobs:** `recipe_ingestions.image_s3_keys` and possibly `recipes.image_url` point into
  S3/SeaweedFS. Copy the objects into the blob store under the same keys; rewrite `image_url`
  values that are absolute S3/MinIO URLs.
- `stock_ingredients` / `stock_units_of_measure` are seeded at runtime from static files
  (`static_seed_service.py`), not by migrations; seed them in Go rather than importing.
- `stage_recipes` rows are short-lived (`expires_at`); importing them is optional.
- Settings: all 7 defined keys are seeded on dev.

## Setting keys

From `jarvis-recipes-server/jarvis_recipes/app/services/settings_service.py` (input for the module's settings `Definitions`; not implemented yet). `(reload)` = `requires_reload`.

| key | type | default | env fallback | description |
|---|---|---|---|---|
| `llm.full_model_name` | string | `'live'` | JARVIS_FULL_MODEL_NAME | Model name used for full recipe extraction and meal planning |
| `llm.lightweight_model_name` | string | `'live'` | JARVIS_LIGHTWEIGHT_MODEL_NAME | Model name used for cheap OCR-text structuring passes |
| `llm.background_model_name` | string | `'background'` | JARVIS_BACKGROUND_MODEL_NAME | Model name used for slow background passes (grocery SKU matching) |
| `queue.max_retries` | int | `3` | LLM_RECIPE_QUEUE_MAX_RETRIES | Maximum retries for a failed recipe parse job |
| `parse_job.abandon_minutes` | int | `4320` | RECIPE_PARSE_JOB_ABANDON_MINUTES | Minutes before an in-progress parse job is considered abandoned |
| `image.max_bytes` | int | `10485760` (10 MiB) | RECIPE_IMAGE_MAX_BYTES | Maximum accepted size of a single uploaded recipe image, in bytes |
| `scraper.user_agent` | string | `'Mozilla/5.0 (Macintosh; Intel Mac OS X 13_6) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36'` | SCRAPER_USER_AGENT | User-Agent header sent when fetching recipe pages |

- `llm.*_model_name` keep their meaning (`live`/`background` slot names).

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
