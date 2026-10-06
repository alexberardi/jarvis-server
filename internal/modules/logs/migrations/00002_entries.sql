-- The log store that replaces Loki (PLAN §3.2). One row per entry; context is JSON text.
-- +goose Up
CREATE TABLE logs_entries (
    id      INTEGER PRIMARY KEY,
    ts      INTEGER NOT NULL,          -- unix nanoseconds, UTC (Loki's resolution)
    service TEXT    NOT NULL,
    level   TEXT    NOT NULL CHECK (level IN ('DEBUG', 'INFO', 'WARNING', 'ERROR', 'CRITICAL')),
    message TEXT    NOT NULL,
    context TEXT                       -- JSON object, or NULL
);
CREATE INDEX logs_entries_ts ON logs_entries (ts);
CREATE INDEX logs_entries_service_ts ON logs_entries (service, ts);

-- +goose Down
DROP TABLE logs_entries;
