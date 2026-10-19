-- Computer control (docs/agent/COMPUTER_CONTROL.md, plan P3 piece 26): taught workflows and the
-- recordings they are learnt from are household data with an owner. A workflow is visible to its
-- owner, and to every member of its household when shared; only the owner or a household admin
-- edits or deletes it. trigger_phrases, params, steps and success are JSON (cc/workflows.go);
-- recording_id is the recording it was learnt from (NULL once that recording is gone).
--
-- A recording is the trace an agent uploaded (cc/recordings.go). It is kept until expires_at
-- (created_at + computer.recording_retention_days); a daily sweep deletes it then, and asks the
-- agent that made it (node_id) to delete its local copy.
-- +goose Up
CREATE TABLE cc_taught_workflows (
    id              TEXT    PRIMARY KEY,           -- tw_<16 hex>
    household_id    TEXT    NOT NULL,
    owner_id        INTEGER NOT NULL,
    name            TEXT    NOT NULL,
    slug            TEXT    NOT NULL,
    trigger_phrases TEXT    NOT NULL DEFAULT '[]', -- JSON []string
    params          TEXT    NOT NULL DEFAULT '[]', -- JSON []Param
    steps           TEXT    NOT NULL DEFAULT '[]', -- JSON []WStep
    success         TEXT    NOT NULL DEFAULT '{}', -- JSON computer.SuccessCheck
    shared          INTEGER NOT NULL DEFAULT 0,
    recording_id    TEXT,
    version         INTEGER NOT NULL DEFAULT 1,
    created_at      TEXT    NOT NULL,
    updated_at      TEXT    NOT NULL,
    UNIQUE (household_id, slug)
);
CREATE INDEX cc_taught_workflows_owner ON cc_taught_workflows (household_id, owner_id);

CREATE TABLE cc_recordings (
    id           TEXT    PRIMARY KEY,           -- rec_<16 hex>
    household_id TEXT    NOT NULL,
    owner_id     INTEGER NOT NULL,
    node_id      TEXT,                          -- the agent that recorded it (holds the local copy)
    mode         TEXT    NOT NULL CHECK (mode IN ('browser', 'desktop')),
    trace        TEXT    NOT NULL DEFAULT '[]', -- JSON, the agent's events
    narration    TEXT    NOT NULL DEFAULT '',
    keyframes    INTEGER NOT NULL DEFAULT 0,
    created_at   TEXT    NOT NULL,
    expires_at   TEXT    NOT NULL
);
CREATE INDEX cc_recordings_owner ON cc_recordings (household_id, owner_id);
CREATE INDEX cc_recordings_expires ON cc_recordings (expires_at);

-- +goose Down
DROP INDEX cc_recordings_expires;
DROP INDEX cc_recordings_owner;
DROP TABLE cc_recordings;
DROP INDEX cc_taught_workflows_owner;
DROP TABLE cc_taught_workflows;
