-- The cutover import's bookkeeping (docs/recipes/00-inventory.md §13, R11): one row per legacy
-- row `jarvisd import-recipes` wrote (or matched to an existing row), so a re-run skips it, plus
-- the user and household mappings a run settled, so later runs keep them.
--   legacy_kind: user | household | recipe | meal_plan | meal_plan_item | staple | sku_map
--   legacy_id:   the id in the legacy bundle (as text)
--   new_id:      the jarvisd id it became (as text)

-- +goose Up
CREATE TABLE recipes_import_log (
    legacy_kind TEXT NOT NULL,
    legacy_id   TEXT NOT NULL,
    new_id      TEXT NOT NULL,
    imported_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    PRIMARY KEY (legacy_kind, legacy_id)
);
CREATE INDEX recipes_import_log_new_id ON recipes_import_log (legacy_kind, new_id);

-- +goose Down
DROP TABLE recipes_import_log;
