-- The device managers a node runs (its heartbeat's "managers", a JSON array of names), so a
-- manager's device (cc_devices.source names the manager) is read and driven on a node that has
-- it, as protocols do for direct devices. NULL: the node never reported any (a Python node).
-- +goose Up
ALTER TABLE cc_nodes ADD COLUMN managers TEXT;

-- +goose Down
ALTER TABLE cc_nodes DROP COLUMN managers;
