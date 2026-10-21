-- Path prefix per registry row (single-port mode): jarvisd's mounted listeners are served on
-- the config port under their prefix (pconfig.Paths), e.g. jarvis-command-center at
-- localhost:7700 + /cc. syncSelf writes it; the URL answers base URL + path. NULL = no path
-- (a listener on its own port, the MQTT broker, external and operator rows).

-- +goose Up
ALTER TABLE config_services ADD COLUMN path TEXT;

-- +goose Down
ALTER TABLE config_services DROP COLUMN path;
