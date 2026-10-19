-- Computer control (docs/agent/COMPUTER_CONTROL.md, plan P3 piece 5): a Custom Rule can target a
-- site (site:<host>, which also covers its subdomains), an app (app:<id>) or a computeruse risk
-- category (action:<category>); '' is the tool's rule for every target, as before. SQLite can't
-- change a UNIQUE in place, so cc_tool_rules is rebuilt with the same columns and checks (ids are
-- kept). Down drops the rules that have a target: the old table has no room for them.
--
-- cc_computer_approvals holds the single-use approval tokens cc mints for a computer task's risky
-- action: the agent verifies one (POST /api/v0/computer/approvals/{token}/verify) before it acts.
-- A token is bound to the node that runs the task and to the digest of the approved actions;
-- used_at is set by the one verify that succeeds.
-- +goose Up
CREATE TABLE cc_tool_rules_new (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    household_id TEXT    NOT NULL,
    scope_kind   TEXT    NOT NULL DEFAULT 'household' CHECK (scope_kind IN ('household', 'goal', 'watch')),
    scope_id     TEXT    NOT NULL DEFAULT '',
    tool         TEXT    NOT NULL,
    target       TEXT    NOT NULL DEFAULT '',
    effect       TEXT    NOT NULL CHECK (effect IN ('allow', 'ask', 'block')),
    note         TEXT    NOT NULL DEFAULT '',
    updated_by   INTEGER,
    updated_at   TEXT    NOT NULL,
    UNIQUE (household_id, scope_kind, scope_id, tool, target)
);
INSERT INTO cc_tool_rules_new (id, household_id, scope_kind, scope_id, tool, target, effect, note, updated_by, updated_at)
SELECT id, household_id, scope_kind, scope_id, tool, '', effect, note, updated_by, updated_at
FROM cc_tool_rules;
DROP TABLE cc_tool_rules;
ALTER TABLE cc_tool_rules_new RENAME TO cc_tool_rules;

CREATE TABLE cc_computer_approvals (
    token        TEXT PRIMARY KEY,
    household_id TEXT NOT NULL,
    node_id      TEXT NOT NULL,
    run_id       TEXT NOT NULL,
    digest       TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    expires_at   TEXT NOT NULL,
    used_at      TEXT
);

-- +goose Down
DROP TABLE cc_computer_approvals;
CREATE TABLE cc_tool_rules_old (
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
INSERT INTO cc_tool_rules_old (id, household_id, scope_kind, scope_id, tool, effect, note, updated_by, updated_at)
SELECT id, household_id, scope_kind, scope_id, tool, effect, note, updated_by, updated_at
FROM cc_tool_rules WHERE target = '';
DROP TABLE cc_tool_rules;
ALTER TABLE cc_tool_rules_old RENAME TO cc_tool_rules;
