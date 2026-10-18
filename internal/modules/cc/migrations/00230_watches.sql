-- Watches (docs/autonomy/PLAN.md §2): a source that observes a value (a web page, a search or a
-- signal kind), a deterministic condition over it and a fire policy. Built on the signal bus
-- (A3); any identified member creates them (A8, user_id is the owner, NULL = the household); web
-- fetches honour web_scraping.allow_external (A9). source_kind is validated in Go, not by a
-- CHECK, so later kinds need no migration. The user's watches are deleted on account deletion.
-- +goose Up
CREATE TABLE cc_watches (
    id               TEXT    PRIMARY KEY,           -- wt_<16 hex>
    household_id     TEXT    NOT NULL,
    user_id          INTEGER,                       -- owner; NULL = the household
    title            TEXT    NOT NULL,
    source_kind      TEXT    NOT NULL,              -- web | signal (validated in Go: P4 adds kinds)
    source_spec      TEXT    NOT NULL DEFAULT '{}', -- JSON watches.Source
    condition        TEXT    NOT NULL DEFAULT '{}', -- JSON watches.Condition
    cadence_seconds  INTEGER NOT NULL DEFAULT 0,    -- web only
    state            TEXT    NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'paused', 'done')),
    fire_policy      TEXT    NOT NULL DEFAULT 'once' CHECK (fire_policy IN ('once', 'every_change', 'cooldown')),
    cooldown_seconds INTEGER NOT NULL DEFAULT 0,
    expires_at       TEXT,
    last_value       TEXT,
    last_checked_at  TEXT,
    last_fired_at    TEXT,
    last_error       TEXT,
    created_at       TEXT    NOT NULL,
    updated_at       TEXT    NOT NULL
);
CREATE INDEX cc_watches_household ON cc_watches (household_id, state);
CREATE INDEX cc_watches_signal ON cc_watches (source_kind, state);

-- +goose Down
DROP INDEX cc_watches_signal;
DROP INDEX cc_watches_household;
DROP TABLE cc_watches;
