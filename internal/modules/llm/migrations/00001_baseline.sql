-- Baseline: jarvis-llm-proxy-api at alembic head g7h8i9j0k1l2, from the live dev DB on the MacBook Pro.
-- Legacy tables dropped: training_jobs (LoRA training, cut by PLAN §7) and service_configs (jarvis-config-client's discovery cache; discovery is in-process).
-- The settings table is created by internal/platform/settings (llm_settings), not here.
-- This migration is intentionally empty: it anchors the module's goose version table so later
-- migrations number from 00002. See docs/schema/llm.md.

-- +goose Up
SELECT 1;

-- +goose Down
SELECT 1;
