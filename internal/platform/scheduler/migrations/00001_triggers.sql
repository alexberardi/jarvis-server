-- +goose Up
CREATE TABLE platform_triggers (
    name         TEXT    PRIMARY KEY,
    kind         TEXT    NOT NULL,   -- interval | cron | once
    spec         TEXT    NOT NULL,   -- JSON, per kind
    job_type     TEXT    NOT NULL,
    payload      BLOB    NOT NULL DEFAULT '',
    next_fire_at INTEGER,            -- unix ms; NULL = finished (a fired one-shot, or a cron that never matches)
    last_fired_at INTEGER,           -- unix ms
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
);
CREATE INDEX platform_triggers_due ON platform_triggers (next_fire_at);

-- +goose Down
DROP TABLE platform_triggers;
