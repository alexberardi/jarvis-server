-- +goose Up
-- last_run: the workflow's last run as its page shows it (JSON computer.RunRecord; '' when it
-- has never run). Replaced by every run; for the workflow's owner only.
ALTER TABLE cc_taught_workflows ADD COLUMN last_run TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE cc_taught_workflows DROP COLUMN last_run;
