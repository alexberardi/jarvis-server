-- Several pantries, slice 2 (docs/decisions/2026-11-11-several-pantries.md, decisions 4, 5, 8, 11):
-- a request records the pantry.stores id it was made against, and a node's installed package keeps
-- its store's id for as long as it is installed. Request rows are swept after 30 days, so where a
-- package came from lives in its own table, not on them. NULL / no row: from before this change,
-- which falls back to the household's default store.
-- +goose Up
ALTER TABLE cc_package_install_requests ADD COLUMN pantry_store_id TEXT;
ALTER TABLE cc_test_install_requests ADD COLUMN pantry_store_id TEXT;
CREATE TABLE cc_node_package_stores (
    node_id         TEXT NOT NULL REFERENCES cc_nodes (node_id) ON DELETE CASCADE,
    command_name    TEXT NOT NULL,
    pantry_store_id TEXT NOT NULL,
    installed_at    TEXT NOT NULL,
    PRIMARY KEY (node_id, command_name)
);

-- +goose Down
DROP TABLE cc_node_package_stores;
ALTER TABLE cc_test_install_requests DROP COLUMN pantry_store_id;
ALTER TABLE cc_package_install_requests DROP COLUMN pantry_store_id;
