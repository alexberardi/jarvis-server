-- The IANA zone each node last reported in its conversation context. The household timezone
-- (attention quiet hours and journal, errand times, D18) is the most recently seen node's.
-- +goose Up
ALTER TABLE cc_nodes ADD COLUMN timezone TEXT;

-- +goose Down
ALTER TABLE cc_nodes DROP COLUMN timezone;
