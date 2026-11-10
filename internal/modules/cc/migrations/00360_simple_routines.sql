-- +goose Up
ALTER TABLE cc_routines ADD COLUMN mode TEXT NOT NULL DEFAULT 'advanced';
ALTER TABLE cc_routines ADD COLUMN utterance TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE cc_routines DROP COLUMN utterance;
ALTER TABLE cc_routines DROP COLUMN mode;
