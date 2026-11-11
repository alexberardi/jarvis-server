-- A direct device's credentials and LAN address belong to the node that discovered/paired it.
-- Keep the ID after node removal so requests fail explicitly instead of reaching another LAN.
-- +goose Up
ALTER TABLE cc_devices ADD COLUMN node_id TEXT;
-- +goose Down
ALTER TABLE cc_devices DROP COLUMN node_id;
