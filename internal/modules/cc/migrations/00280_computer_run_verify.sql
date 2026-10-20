-- Verify before done (docs/agent/COMPUTER_CONTROL.md C4, plan P3 piece 12): cc_computer_runs.verify
-- is a run's result checks so far, as JSON: whether a done without a check was sent back once, and
-- since which round a failed check is being fixed (with its reason), so a run that parks on a
-- confirmation or a question in between keeps its extra budget.
-- +goose Up
ALTER TABLE cc_computer_runs ADD COLUMN verify TEXT;

-- +goose Down
ALTER TABLE cc_computer_runs DROP COLUMN verify;
