-- Central package management (docs/decisions/2026-10-30-central-package-assignments.md): a
-- household says which Pantry package should be on which nodes, and the server installs it
-- there with the ordinary install request (cc_package_install_requests). One assignment per
-- household and package; all_nodes covers every active node of the household, including ones
-- paired later, otherwise only the nodes with a row here.
-- request_id is the latest install request made for the node (shown as installed, installing
-- or failed from that request's status); attempts and last_attempt_at limit automatic retries;
-- note says why a node was skipped (a computer the Pantry won't vouch for, say).
-- +goose Up
CREATE TABLE cc_package_assignments (
    id              TEXT    PRIMARY KEY,
    household_id    TEXT    NOT NULL,
    command_name    TEXT    NOT NULL,
    github_repo_url TEXT    NOT NULL,
    git_tag         TEXT,
    all_nodes       INTEGER NOT NULL DEFAULT 0 CHECK (all_nodes IN (0, 1)),
    created_at      TEXT    NOT NULL,
    updated_at      TEXT    NOT NULL,
    UNIQUE (household_id, command_name)
);

CREATE TABLE cc_package_assignment_nodes (
    assignment_id   TEXT    NOT NULL REFERENCES cc_package_assignments (id) ON DELETE CASCADE,
    node_id         TEXT    NOT NULL REFERENCES cc_nodes (node_id) ON DELETE CASCADE,
    request_id      TEXT,
    attempts        INTEGER NOT NULL DEFAULT 0,
    last_attempt_at TEXT,
    note            TEXT,
    PRIMARY KEY (assignment_id, node_id)
);
CREATE INDEX cc_package_assignment_nodes_node_id ON cc_package_assignment_nodes (node_id);

-- +goose Down
DROP TABLE cc_package_assignment_nodes;
DROP TABLE cc_package_assignments;
