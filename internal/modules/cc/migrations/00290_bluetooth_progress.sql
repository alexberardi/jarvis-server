-- Bluetooth progress (#433): what the node is doing on a pending scan or pair ("scanning",
-- "pairing", "connecting", "audio"), posted to …/progress while it works, so the app can show it;
-- a pending scan's results_json is then the devices found so far.
-- +goose Up
ALTER TABLE cc_bluetooth_scan_requests ADD COLUMN progress TEXT;
ALTER TABLE cc_bluetooth_pair_requests ADD COLUMN progress TEXT;

-- +goose Down
ALTER TABLE cc_bluetooth_pair_requests DROP COLUMN progress;
ALTER TABLE cc_bluetooth_scan_requests DROP COLUMN progress;
