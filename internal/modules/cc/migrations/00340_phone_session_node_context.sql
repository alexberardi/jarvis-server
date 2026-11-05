-- Call plans gather context from the household's commands at plan time
-- (docs/decisions/2026-11-05-call-plan-node-context.md). The give-if-asked fields among them
-- stay with the session for the spoken-output guard, since nothing is fetched mid-call.
-- NULL: none (every session before this, and plans with no node context).
-- +goose Up
ALTER TABLE cc_phone_call_sessions ADD COLUMN node_context_json TEXT;
-- +goose Down
ALTER TABLE cc_phone_call_sessions DROP COLUMN node_context_json;
