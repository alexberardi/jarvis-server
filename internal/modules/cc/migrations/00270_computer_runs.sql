-- Computer control (docs/agent/COMPUTER_CONTROL.md, plan P3 piece 10): cc_computer_runs is one
-- computer task's loop (cc/computer): the errand step it belongs to, its goal and target, its state
-- and round count, and its history (the actions and their results, as text: never an image). A
-- confirmation parks the errand and leaves the run 'waiting' with the held batch in pending, so the
-- approval resumes the same task. stop_requested is the durable stop the loop checks before each
-- round.
-- +goose Up
CREATE TABLE cc_computer_runs (
    id             TEXT    PRIMARY KEY,               -- cr_<hex>
    household_id   TEXT    NOT NULL,
    user_id        INTEGER,
    node_id        TEXT    NOT NULL,
    workflow_id    TEXT    NOT NULL DEFAULT '',
    step           INTEGER NOT NULL DEFAULT 0,
    goal           TEXT    NOT NULL,
    target         TEXT    NOT NULL CHECK (target IN ('browser', 'desktop')),
    state          TEXT    NOT NULL DEFAULT 'running'
                   CHECK (state IN ('running', 'waiting', 'paused', 'done', 'failed', 'stopped')),
    stop_requested INTEGER NOT NULL DEFAULT 0,
    rounds         INTEGER NOT NULL DEFAULT 0,
    history        TEXT    NOT NULL DEFAULT '[]',     -- JSON array, text only
    pending        TEXT,                              -- JSON: the batch held for a confirmation
    model          TEXT    NOT NULL DEFAULT '',
    error          TEXT    NOT NULL DEFAULT '',
    created_at     TEXT    NOT NULL,
    updated_at     TEXT    NOT NULL
);
CREATE INDEX cc_computer_runs_step ON cc_computer_runs (workflow_id, step);

-- +goose Down
DROP TABLE cc_computer_runs;
