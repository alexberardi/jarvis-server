-- +goose Up
-- D44: the node default routines (Good morning, …) are seeded as cc_routines rows once per
-- household. This marks a household as seeded, so a default the user deletes stays deleted.
CREATE TABLE cc_routine_seeds (
    household_id TEXT PRIMARY KEY,
    seeded_at    TEXT NOT NULL
);

-- +goose Down
DROP TABLE cc_routine_seeds;
