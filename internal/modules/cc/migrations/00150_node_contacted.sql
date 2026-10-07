-- A10 F19: registration stamps last_seen (legacy parity), so a node that never connected
-- counted as online for 15 minutes and every round trip to it waited out its timeout.
-- contacted is 0 from registration until the node's first authenticated request or MQTT
-- reply; rows that already exist were seen before, so they keep 1.
-- +goose Up
ALTER TABLE cc_nodes ADD COLUMN contacted INTEGER NOT NULL DEFAULT 1;

-- +goose Down
ALTER TABLE cc_nodes DROP COLUMN contacted;
