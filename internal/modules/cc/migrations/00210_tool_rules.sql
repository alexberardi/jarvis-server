-- The household's Custom Rules (docs/autonomy/PLAN.md §1, A1, A7): per household, per scope
-- (household, goal or watch) and per tool, whether a background action runs without a tap, asks
-- or is blocked. A table rather than a setting: household-setting writes only accept bool, int
-- and string. updated_by is the user who last changed the rule; it is de-identified, not
-- deleted, on account deletion (D20).
-- +goose Up
CREATE TABLE cc_tool_rules (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    household_id TEXT    NOT NULL,
    scope_kind   TEXT    NOT NULL DEFAULT 'household' CHECK (scope_kind IN ('household', 'goal', 'watch')),
    scope_id     TEXT    NOT NULL DEFAULT '',
    tool         TEXT    NOT NULL,
    effect       TEXT    NOT NULL CHECK (effect IN ('allow', 'ask', 'block')),
    note         TEXT    NOT NULL DEFAULT '',
    updated_by   INTEGER,
    updated_at   TEXT    NOT NULL,
    UNIQUE (household_id, scope_kind, scope_id, tool)
);

-- +goose Down
DROP TABLE cc_tool_rules;
