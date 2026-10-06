-- Baseline: jarvis-config-service at alembic head 006, translated from the live dev DB on the
-- MacBook Pro. See docs/schema/config.md. The settings table is created by
-- internal/platform/settings (config_settings), not here.
-- Legacy timestamps here were naive `timestamp` holding UTC; stored as ISO-8601 UTC TEXT.

-- +goose Up

-- The service registry. Kept although jarvisd's own modules are discovered in-process: it
-- still names services outside jarvisd (jarvis-web, jarvis-osx-api, jarvis-node-setup, the
-- MQTT broker), Python services during the strangler phase, and remote GPU satellites.
CREATE TABLE config_services (
    id            INTEGER PRIMARY KEY,
    name          TEXT    NOT NULL,
    host          TEXT    NOT NULL,
    port          INTEGER NOT NULL,
    scheme        TEXT    NOT NULL DEFAULT 'http',
    health_path   TEXT             DEFAULT '/health',
    description   TEXT,
    external_host TEXT,
    external_port INTEGER,
    created_at    TEXT             DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at    TEXT             DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE UNIQUE INDEX config_services_name ON config_services (name);

-- +goose Down
DROP TABLE config_services;
