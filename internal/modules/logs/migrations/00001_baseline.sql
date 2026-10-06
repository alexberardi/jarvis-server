-- Baseline: jarvis-logs at alembic head 002, from the live dev DB on the MacBook Pro.
-- Legacy logs lived in Loki; the only Postgres table was settings. The jarvisd log table is new design (PLAN §3.2) and arrives with the logs module in Phase 1.
-- The settings table is created by internal/platform/settings (logs_settings), not here.
-- This migration is intentionally empty: it anchors the module's goose version table so later
-- migrations number from 00002. See docs/schema/logs.md.

-- +goose Up
SELECT 1;

-- +goose Down
SELECT 1;
