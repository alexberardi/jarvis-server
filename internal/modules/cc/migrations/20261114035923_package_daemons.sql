-- Channels D3 (docs/CHANNELS.md): a package's daemon holds one connection (a chat bot's), so a
-- household runs it on one node. A node's installed package records whether it has a daemon,
-- as the node's install result says, and an install of it on another node is refused.
-- +goose Up
ALTER TABLE cc_node_package_stores ADD COLUMN has_daemon INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE cc_node_package_stores DROP COLUMN has_daemon;
