-- Re-enrolling a computer replaces its node (docs/decisions/2026-10-15-reenroll-replaces-node.md).
-- device_id is the desktop agent's stable identity for one computer and OS user (an HMAC of the
-- OS machine id and user id; the raw machine id is never sent). It comes with /nodes/register
-- and every heartbeat. NULL: a Pi, or an agent that predates it.
-- +goose Up
ALTER TABLE cc_nodes ADD COLUMN device_id TEXT;
CREATE INDEX idx_cc_nodes_device_id ON cc_nodes (household_id, device_id) WHERE device_id IS NOT NULL;

-- +goose Down
DROP INDEX idx_cc_nodes_device_id;
ALTER TABLE cc_nodes DROP COLUMN device_id;
