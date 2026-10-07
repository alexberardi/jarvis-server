-- Voiceprints (docs/cc/06 §11 "Storage"; D34, D36). One row per enrollment take, per
-- (household, user): a voiceprint is only ever scored inside the household it was enrolled in.
-- D34: no audio is kept. `embedding` is the take's L2-normalised speaker embedding as
-- little-endian float32 × dim, tagged with the model that produced it; rows from any other model
-- are ignored (a model change means re-enrollment). Raw user ids: the DB is local, unlike the
-- legacy sha256-named directories. Legacy WAV profiles are not imported (PLAN §5).

-- +goose Up
CREATE TABLE stt_voiceprints (
    household_id TEXT    NOT NULL,
    user_id      INTEGER NOT NULL,
    sample_index INTEGER NOT NULL CHECK (sample_index BETWEEN 0 AND 999),
    model_id     TEXT    NOT NULL,
    dim          INTEGER NOT NULL,
    embedding    BLOB    NOT NULL,
    speech_ms    INTEGER NOT NULL DEFAULT 0, -- D37: VAD speech in the take, for diagnostics
    created_at   TEXT    NOT NULL,
    PRIMARY KEY (household_id, user_id, sample_index)
);
CREATE INDEX stt_voiceprints_user ON stt_voiceprints (user_id);

-- +goose Down
DROP TABLE stt_voiceprints;
