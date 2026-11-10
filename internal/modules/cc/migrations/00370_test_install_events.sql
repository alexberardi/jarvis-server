-- Send to Forge: what a test install did on the node besides chat tool calls (its log lines,
-- device scans and controls), as the node's sidecar recorded them. Sent by the node during the
-- test window; swept with the request row (ON DELETE CASCADE).
-- +goose Up
CREATE TABLE cc_test_install_events (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    request_id TEXT    NOT NULL REFERENCES cc_test_install_requests (id) ON DELETE CASCADE,
    kind       TEXT    NOT NULL,
    event      TEXT    NOT NULL,
    created_at TEXT    NOT NULL
);
CREATE INDEX cc_test_install_events_request_id ON cc_test_install_events (request_id);
-- +goose Down
DROP TABLE cc_test_install_events;
