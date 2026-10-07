-- Model manager (LD3, docs/llm/06): installed model files and install jobs. Owned by
-- internal/modules/llm/models. Kinds: llm, mmproj, embedding, stt (whisper GGML) run in
-- engines; tts (Kokoro, a directory) and speaker (ERes2Net) run in-binary via sherpa-onnx.

-- +goose Up
CREATE TABLE llm_models (
    id              TEXT    PRIMARY KEY,           -- catalog id, or a slug for a pasted repo
    kind            TEXT    NOT NULL,              -- llm | mmproj | embedding | stt
    display         TEXT    NOT NULL DEFAULT '',
    catalog_id      TEXT,
    repo            TEXT,                          -- Hugging Face repo; NULL when registered in place
    revision        TEXT,                          -- pinned commit
    source_url      TEXT,                          -- direct download URL (non-Hugging Face models)
    archive         TEXT,                          -- tar.gz | tar.bz2 | zip: extracted into a directory
    files           TEXT    NOT NULL DEFAULT '[]', -- JSON [{name, size, sha256}]; several for split GGUFs
    path            TEXT    NOT NULL,              -- what gets loaded: the (first) file, or the extracted directory
    size            INTEGER NOT NULL DEFAULT 0,    -- bytes, all files
    mmproj_id       TEXT,                          -- llm: its vision projector
    context_default INTEGER NOT NULL DEFAULT 0,
    prompt_provider TEXT,
    state           TEXT    NOT NULL,              -- downloading | ready | failed
    bytes_done      INTEGER NOT NULL DEFAULT 0,
    error           TEXT,
    external        INTEGER NOT NULL DEFAULT 0,    -- registered in place: delete never removes the file
    created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE llm_installs (
    id             INTEGER PRIMARY KEY,
    model_id       TEXT    NOT NULL,
    mmproj_id      TEXT,
    engine_kind    TEXT,
    engine_flavour TEXT,
    assign         TEXT    NOT NULL DEFAULT '[]', -- labels to point at the model when done
    state          TEXT    NOT NULL,              -- queued | running | done | failed | cancelled
    phase          TEXT    NOT NULL DEFAULT '',   -- engine | mmproj | model | done
    bytes_total    INTEGER NOT NULL DEFAULT 0,
    bytes_done     INTEGER NOT NULL DEFAULT 0,
    error          TEXT,
    note           TEXT,
    job_id         INTEGER,
    created_at     TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at     TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE INDEX llm_installs_model ON llm_installs (model_id);

-- +goose Down
DROP TABLE llm_installs;
DROP TABLE llm_models;
