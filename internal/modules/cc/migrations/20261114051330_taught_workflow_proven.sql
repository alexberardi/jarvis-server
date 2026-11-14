-- +goose Up
-- proven: the workflow has had a run that finished with its result verified (WORKFLOW-SKILLS.md,
-- verified-replay Q3). Workflows from before this column have been running already and stay as
-- they were: 1. A newly learnt one is inserted with 0.
ALTER TABLE cc_taught_workflows ADD COLUMN proven INTEGER NOT NULL DEFAULT 1;

-- +goose Down
ALTER TABLE cc_taught_workflows DROP COLUMN proven;
