-- Baseline: jarvis-whisper-api at alembic head 005, from the live dev DB on the MacBook Pro.
-- The only legacy table was settings. Voice profiles are greenfield (PLAN §5: not imported).
-- The settings table is created by internal/platform/settings (stt_settings), not here.
-- This migration is intentionally empty: it anchors the module's goose version table so later
-- migrations number from 00002. See docs/schema/stt.md.

-- +goose Up
SELECT 1;

-- +goose Down
SELECT 1;
