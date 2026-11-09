-- Send to Forge: the runs of a test-installed command during the node's test window (arguments,
-- output), kept with the request so the developer can send them back to the Forge draft.
-- Swept with the request row (cleanupTestInstalls, ON DELETE CASCADE).
-- +goose Up
CREATE TABLE cc_test_install_runs (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    request_id TEXT    NOT NULL REFERENCES cc_test_install_requests (id) ON DELETE CASCADE,
    command    TEXT    NOT NULL,
    arguments  TEXT,
    output     TEXT,
    created_at TEXT    NOT NULL
);
CREATE INDEX cc_test_install_runs_request_id ON cc_test_install_runs (request_id);
-- +goose Down
DROP TABLE cc_test_install_runs;
