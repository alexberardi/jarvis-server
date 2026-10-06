-- Baseline: jarvis-ocr-service at alembic head 003 (002 in git; 003_fix_seed_drift is uncommitted on the MBP and only rewrites settings rows), from the live dev DB on the MacBook Pro.
-- The only legacy table was settings.
-- The settings table is created by internal/platform/settings (ocr_settings), not here.
-- This migration is intentionally empty: it anchors the module's goose version table so later
-- migrations number from 00002. See docs/schema/ocr.md.

-- +goose Up
SELECT 1;

-- +goose Down
SELECT 1;
