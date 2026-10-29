-- +goose Up
CREATE TABLE cc_display_config (scope TEXT PRIMARY KEY, overrides TEXT NOT NULL DEFAULT '{}', verifier TEXT NOT NULL DEFAULT '');
CREATE TABLE cc_display_ack (node_id TEXT PRIMARY KEY REFERENCES cc_nodes(node_id) ON DELETE CASCADE, revision TEXT NOT NULL, error TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL);
CREATE TABLE cc_display_assets (id TEXT PRIMARY KEY, kind TEXT NOT NULL, name TEXT NOT NULL, content BLOB NOT NULL);

-- +goose Down
DROP TABLE cc_display_ack;
DROP TABLE cc_display_config;
DROP TABLE cc_display_assets;
