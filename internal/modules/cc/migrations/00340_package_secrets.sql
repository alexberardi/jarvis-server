-- Server-managed package secrets (docs/decisions/2026-10-30-server-managed-package-secrets.md):
-- a household admin sets the secrets Pantry packages need (HA_URL, a Keep token, ...) in the
-- server, which keeps them encrypted at rest (cc-oauth.key) and hands them to the household's
-- nodes over each node's authenticated channel. The app's K2 push still works alongside.
-- node_ids is a JSON array of node ids, or NULL for every node of the household. A NULL
-- value_enc is a tombstone: the secret was deleted, and a node that pulls it clears its copy.
-- +goose Up
CREATE TABLE cc_package_secrets (
    household_id TEXT NOT NULL,
    secret_key   TEXT NOT NULL,
    value_enc    TEXT,
    node_ids     TEXT,
    updated_at   TEXT NOT NULL,
    PRIMARY KEY (household_id, secret_key)
);

-- +goose Down
DROP TABLE cc_package_secrets;
