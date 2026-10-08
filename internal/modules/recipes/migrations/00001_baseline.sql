-- Baseline: jarvis-recipes-server at alembic head e1f2a3b4c5d6, translated from the live dev DB
-- on the MacBook Pro (restored from 887601b; see docs/schema/recipes.md), with the port's
-- changes (docs/recipes/00-inventory.md §5.1, decisions RD4/RD5):
--   * recipes_recipes gains prep_time_minutes and cook_time_minutes (RD5; legacy dropped them, B1).
--   * recipes, meal plans and staples have no FK to recipes_users: they belong to the household and
--     outlive their author (RD4), so deleting the author's shadow row must not cascade to them.
--     The author-only tables (ingestions, stage recipes) keep the cascading FK.
--   * recipes_recipe_tags is a rowid table: a recipe's tags come back in insertion order (legacy).
--   * Tag and stock names are unique case-insensitively (legacy matched them with lower()/ILIKE).
--   * The SKU map scope index treats a NULL household as one scope (legacy let NULLs duplicate).
--   * recipes_recipe_parse_jobs gains queue_job_id (the jarvisd queue row) and a list index.
--   * recipes_recipe_ingestions drops the OCR fan-out/join and dead tier columns; it and
--     recipes_stage_recipes gain household_id, so the deletion hooks can scope them.
--   * recipes_mailbox_messages is gone (write-only, §5.4).
--   * recipes_meta holds the stock-data version hash.
-- The settings table is created by internal/platform/settings (recipes_settings), not here.
-- Timestamps are ISO-8601 UTC TEXT; `date` columns TEXT 'YYYY-MM-DD'; json columns TEXT;
-- user_id is the auth user id as a string (legacy convention, kept).

-- +goose Up

-- Shadow of auth users that own recipe data (legacy `users`, one column).
CREATE TABLE recipes_users (
    user_id TEXT PRIMARY KEY
);

CREATE TABLE recipes_recipes (
    id                 INTEGER PRIMARY KEY,
    user_id            TEXT    NOT NULL,
    household_id       TEXT,
    title              TEXT    NOT NULL,
    description        TEXT,
    image_url          TEXT,
    source_type        TEXT    NOT NULL DEFAULT 'manual' CHECK (source_type IN ('manual', 'image', 'url')),
    source_url         TEXT,
    servings           INTEGER,
    prep_time_minutes  INTEGER,
    cook_time_minutes  INTEGER,
    total_time_minutes INTEGER,
    created_at         TEXT             DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at         TEXT             DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE INDEX recipes_recipes_user_id ON recipes_recipes (user_id);
CREATE INDEX recipes_recipes_household_id ON recipes_recipes (household_id);

CREATE TABLE recipes_ingredients (
    id               INTEGER PRIMARY KEY,
    recipe_id        INTEGER NOT NULL REFERENCES recipes_recipes (id) ON DELETE CASCADE,
    text             TEXT    NOT NULL,
    quantity_display TEXT,
    quantity_value   REAL,   -- numeric(10,4): stored rounded to 4 decimals
    unit             TEXT
);
CREATE INDEX recipes_ingredients_recipe_id ON recipes_ingredients (recipe_id);

CREATE TABLE recipes_steps (
    id          INTEGER PRIMARY KEY,
    recipe_id   INTEGER NOT NULL REFERENCES recipes_recipes (id) ON DELETE CASCADE,
    step_number INTEGER NOT NULL,
    text        TEXT    NOT NULL,
    UNIQUE (recipe_id, step_number)
);

CREATE TABLE recipes_tags (
    id   INTEGER PRIMARY KEY,
    name TEXT    NOT NULL
);
CREATE UNIQUE INDEX recipes_tags_name ON recipes_tags (name COLLATE NOCASE);

CREATE TABLE recipes_recipe_tags (
    recipe_id INTEGER NOT NULL REFERENCES recipes_recipes (id) ON DELETE CASCADE,
    tag_id    INTEGER NOT NULL REFERENCES recipes_tags (id) ON DELETE CASCADE,
    PRIMARY KEY (recipe_id, tag_id)
);
CREATE INDEX recipes_recipe_tags_tag_id ON recipes_recipe_tags (tag_id);

CREATE TABLE recipes_meal_plans (
    id           INTEGER PRIMARY KEY,
    user_id      TEXT    NOT NULL,
    household_id TEXT,
    name         TEXT,
    start_date   TEXT    NOT NULL, -- YYYY-MM-DD
    created_at   TEXT             DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE INDEX recipes_meal_plans_user_id ON recipes_meal_plans (user_id);
CREATE INDEX recipes_meal_plans_household_id ON recipes_meal_plans (household_id);

CREATE TABLE recipes_meal_plan_items (
    id           INTEGER PRIMARY KEY,
    meal_plan_id INTEGER NOT NULL REFERENCES recipes_meal_plans (id) ON DELETE CASCADE,
    recipe_id    INTEGER NOT NULL REFERENCES recipes_recipes (id) ON DELETE CASCADE,
    date         TEXT    NOT NULL, -- YYYY-MM-DD
    meal_type    TEXT    NOT NULL
);
CREATE INDEX recipes_meal_plan_items_meal_plan_id ON recipes_meal_plan_items (meal_plan_id);
CREATE INDEX recipes_meal_plan_items_recipe_id ON recipes_meal_plan_items (recipe_id);

CREATE TABLE recipes_grocery_sku_map (
    id              INTEGER PRIMARY KEY,
    user_id         TEXT    NOT NULL,
    household_id    TEXT,
    retailer        TEXT    NOT NULL DEFAULT 'walmart',
    ingredient_name TEXT    NOT NULL,
    sku             TEXT    NOT NULL,
    product_name    TEXT,
    unit_size       TEXT,
    source          TEXT    NOT NULL DEFAULT 'manual',
    created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at      TEXT
);
CREATE UNIQUE INDEX recipes_grocery_sku_map_scope
    ON recipes_grocery_sku_map (COALESCE(household_id, ''), user_id, retailer, ingredient_name);
CREATE INDEX recipes_grocery_sku_map_user_id ON recipes_grocery_sku_map (user_id);
CREATE INDEX recipes_grocery_sku_map_household_id ON recipes_grocery_sku_map (household_id);

CREATE TABLE recipes_recipe_parse_jobs (
    id               TEXT    PRIMARY KEY, -- UUID, generated by the app
    user_id          TEXT    NOT NULL,
    household_id     TEXT,
    job_type         TEXT    NOT NULL, -- ingestion | image | meal_plan_generate | grocery_match
    url              TEXT,
    use_llm_fallback INTEGER NOT NULL DEFAULT 1,
    status           TEXT    NOT NULL DEFAULT 'PENDING',
    result_json      TEXT,
    job_data         TEXT,
    error_code       TEXT,
    error_message    TEXT,
    attempts         INTEGER NOT NULL DEFAULT 0,
    queue_job_id     INTEGER, -- platform_jobs row, for cancel and inspection
    started_at       TEXT,
    completed_at     TEXT,
    committed_at     TEXT,
    abandoned_at     TEXT,
    canceled_at      TEXT,
    created_at       TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at       TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE INDEX recipes_recipe_parse_jobs_user_status ON recipes_recipe_parse_jobs (user_id, status);
CREATE INDEX recipes_recipe_parse_jobs_list ON recipes_recipe_parse_jobs (user_id, job_type, completed_at);
CREATE INDEX recipes_recipe_parse_jobs_household_id ON recipes_recipe_parse_jobs (household_id);
CREATE INDEX recipes_recipe_parse_jobs_completed_at ON recipes_recipe_parse_jobs (completed_at);

CREATE TABLE recipes_recipe_ingestions (
    id            TEXT    PRIMARY KEY, -- UUID, generated by the app
    user_id       TEXT    NOT NULL REFERENCES recipes_users (user_id) ON DELETE CASCADE,
    household_id  TEXT,
    status        TEXT    NOT NULL DEFAULT 'PENDING',
    image_s3_keys TEXT    NOT NULL, -- JSON array of blob keys
    tier_max      INTEGER,
    pipeline_json TEXT,             -- JSON
    title_hint    TEXT,
    recipe_id     INTEGER REFERENCES recipes_recipes (id) ON DELETE SET NULL,
    ocr_readings  TEXT,             -- JSON
    created_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE INDEX recipes_recipe_ingestions_user_id ON recipes_recipe_ingestions (user_id);
CREATE INDEX recipes_recipe_ingestions_household_id ON recipes_recipe_ingestions (household_id);
CREATE INDEX recipes_recipe_ingestions_recipe_id ON recipes_recipe_ingestions (recipe_id);

CREATE TABLE recipes_stage_recipes (
    id                INTEGER PRIMARY KEY,
    user_id           TEXT    NOT NULL REFERENCES recipes_users (user_id) ON DELETE CASCADE,
    household_id      TEXT,
    title             TEXT    NOT NULL,
    description       TEXT,
    yield_text        TEXT,
    prep_time_minutes INTEGER DEFAULT 0,
    cook_time_minutes INTEGER DEFAULT 0,
    ingredients       TEXT    NOT NULL, -- JSON
    steps             TEXT    NOT NULL, -- JSON
    tags              TEXT    NOT NULL DEFAULT '[]', -- JSON
    notes             TEXT    NOT NULL DEFAULT '[]', -- JSON
    request_id        TEXT,
    created_at        TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    expires_at        TEXT    NOT NULL
);
CREATE INDEX recipes_stage_recipes_user_id ON recipes_stage_recipes (user_id);
CREATE INDEX recipes_stage_recipes_household_id ON recipes_stage_recipes (household_id);
CREATE INDEX recipes_stage_recipes_request_id ON recipes_stage_recipes (request_id);

CREATE TABLE recipes_staples (
    id           INTEGER PRIMARY KEY,
    user_id      TEXT    NOT NULL,
    household_id TEXT,
    name         TEXT    NOT NULL,
    created_at   TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (user_id, name)
);
CREATE INDEX recipes_staples_household_id ON recipes_staples (household_id);

-- Reference data, upserted at module start from the embedded static files (§5.3).
CREATE TABLE recipes_stock_ingredients (
    id         INTEGER PRIMARY KEY,
    name       TEXT    NOT NULL,
    category   TEXT,
    synonyms   TEXT,
    created_at TEXT,
    updated_at TEXT
);
CREATE UNIQUE INDEX recipes_stock_ingredients_name ON recipes_stock_ingredients (name COLLATE NOCASE);

CREATE TABLE recipes_stock_units_of_measure (
    id           INTEGER PRIMARY KEY,
    name         TEXT    NOT NULL,
    abbreviation TEXT             UNIQUE,
    created_at   TEXT,
    updated_at   TEXT
);
CREATE UNIQUE INDEX recipes_stock_units_of_measure_name ON recipes_stock_units_of_measure (name COLLATE NOCASE);

-- Module bookkeeping (e.g. the stock-data version hash).
CREATE TABLE recipes_meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- +goose Down
DROP TABLE recipes_meta;
DROP TABLE recipes_stock_units_of_measure;
DROP TABLE recipes_stock_ingredients;
DROP TABLE recipes_staples;
DROP TABLE recipes_stage_recipes;
DROP TABLE recipes_recipe_ingestions;
DROP TABLE recipes_recipe_parse_jobs;
DROP TABLE recipes_grocery_sku_map;
DROP TABLE recipes_meal_plan_items;
DROP TABLE recipes_meal_plans;
DROP TABLE recipes_recipe_tags;
DROP TABLE recipes_tags;
DROP TABLE recipes_steps;
DROP TABLE recipes_ingredients;
DROP TABLE recipes_recipes;
DROP TABLE recipes_users;
