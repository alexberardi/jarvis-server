-- A device imported through a Forge test install's device protocol goes when that test ends
-- (user 2026-11-10: without its protocol nothing can control it; re-add it after a real install).
-- The test's node and package; NULL for every other device, and once re-imported through a protocol
-- no running test provides. docs/decisions/2026-11-10-forge-test-agents.md.
-- +goose Up
ALTER TABLE cc_devices ADD COLUMN test_node_id TEXT;
ALTER TABLE cc_devices ADD COLUMN test_package TEXT;

-- +goose Down
ALTER TABLE cc_devices DROP COLUMN test_package;
ALTER TABLE cc_devices DROP COLUMN test_node_id;
