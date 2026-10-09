-- Forge test install, ported after all (user 2026-10-08, reversing D5's drop): legacy
-- test_install_requests (models.py:518-542, migration m3h4i5j6k7l8) plus verified_at for the
-- D39 pickup/verify deadlines the package requests use. No `restarting` status: the node
-- reloads a test command in process.
-- +goose Up
CREATE TABLE cc_test_install_requests (
    id            TEXT    PRIMARY KEY,
    node_id       TEXT    NOT NULL REFERENCES cc_nodes (node_id) ON DELETE CASCADE,
    household_id  TEXT    NOT NULL,                    -- '' if the node has no household
    share_code    TEXT    NOT NULL,
    package_name  TEXT    NOT NULL,
    status        TEXT    NOT NULL DEFAULT 'pending'
                  CHECK (status IN ('pending', 'completed', 'failed', 'expired')),
    results_json  TEXT,
    error_message TEXT,
    created_at    TEXT    NOT NULL,
    verified_at   TEXT,
    expires_at    TEXT    NOT NULL,
    completed_at  TEXT
);
CREATE INDEX cc_test_install_requests_household_id ON cc_test_install_requests (household_id);
CREATE INDEX cc_test_install_requests_node_id ON cc_test_install_requests (node_id);

-- +goose Down
DROP TABLE cc_test_install_requests;
