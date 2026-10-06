-- Baseline: jarvis-tts at alembic head 004, from the live dev DB on the MacBook Pro.
-- The only legacy table was settings.
-- The settings table is created by internal/platform/settings (tts_settings), not here.
-- This migration is intentionally empty: it anchors the module's goose version table so later
-- migrations number from 00002. See docs/schema/tts.md.

-- +goose Up
SELECT 1;

-- +goose Down
SELECT 1;
