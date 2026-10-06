-- +goose Up
CREATE TABLE platform_jobs (
    id           INTEGER PRIMARY KEY,
    type         TEXT    NOT NULL,
    payload      BLOB    NOT NULL DEFAULT '',
    dedup_key    TEXT,
    state        TEXT    NOT NULL DEFAULT 'queued', -- queued | running | done | failed | cancelled
    priority     INTEGER NOT NULL DEFAULT 0,        -- higher runs first
    attempts     INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 3,
    run_at       INTEGER NOT NULL,                  -- unix ms; not before
    lease_until  INTEGER,                           -- unix ms; running jobs only
    last_error   TEXT,
    result       BLOB,
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
);

-- A dedup key is unique only among live jobs, so the same key can run again once finished.
CREATE UNIQUE INDEX platform_jobs_dedup ON platform_jobs (dedup_key)
    WHERE dedup_key IS NOT NULL AND state IN ('queued', 'running');

CREATE INDEX platform_jobs_ready ON platform_jobs (type, state, priority DESC, run_at, id);
CREATE INDEX platform_jobs_lease ON platform_jobs (state, lease_until);

-- +goose Down
DROP TABLE platform_jobs;
