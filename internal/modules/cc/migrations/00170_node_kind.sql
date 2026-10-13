-- Install plan item 1.8 (docs/agent/DESIGN.md §9 Q1): a node says what it is in its heartbeat.
-- node_kind is "desktop" for the desktop agent, NULL for a node that never said (a Pi).
-- capabilities is a JSON object the node reports, e.g. {"browser_extension":"connected"}.
-- +goose Up
ALTER TABLE cc_nodes ADD COLUMN node_kind TEXT;
ALTER TABLE cc_nodes ADD COLUMN capabilities TEXT;

-- +goose Down
ALTER TABLE cc_nodes DROP COLUMN capabilities;
ALTER TABLE cc_nodes DROP COLUMN node_kind;
