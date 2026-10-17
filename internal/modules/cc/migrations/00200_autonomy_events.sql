-- The autonomy activity log (docs/autonomy/PLAN.md §0.3): an append-only record of what ran
-- without a tap, what was asked and what was blocked, by which rule, with the source (errand,
-- goal, watch, automation). It is the "inspect what it did" surface; nothing is hidden.
-- +goose Up
CREATE TABLE cc_autonomy_events (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    household_id TEXT    NOT NULL,
    user_id      INTEGER,
    source       TEXT    NOT NULL,                 -- errand_plan | errand_step | automation | watch
    source_id    TEXT    NOT NULL DEFAULT '',      -- plan, workflow, automation kind or watch id
    tool         TEXT    NOT NULL DEFAULT '',
    effect       TEXT    NOT NULL CHECK (effect IN ('allow', 'ask', 'block')),
    outcome      TEXT    NOT NULL,                 -- launched | ran | asked | blocked | failed | stopped | fired
    rule_id      INTEGER,
    reason       TEXT    NOT NULL DEFAULT '',
    detail       TEXT,                             -- JSON object or NULL
    created_at   TEXT    NOT NULL
);
CREATE INDEX cc_autonomy_events_household ON cc_autonomy_events (household_id, created_at);

-- +goose Down
DROP INDEX cc_autonomy_events_household;
DROP TABLE cc_autonomy_events;
