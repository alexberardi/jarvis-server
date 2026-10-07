-- 04 memory (Phase 5c): embeddings follow the engine (docs/llm LD6/LD7).
--
-- The baseline pinned cc_user_memories.embedding to 384 float32 (all-MiniLM-L6-v2). LD6
-- makes the embedding model a setting: every vector is tagged with the model that produced
-- it, recall compares only vectors of the current model, and the embedding sweep re-embeds
-- every row whose tag differs. So the column takes any whole number of float32s (still
-- little-endian and L2-normalised), and embedding_model holds the tag. Rows carried over
-- from the baseline keep their vector but get no tag, so the sweep re-embeds them.
--
-- SQLite can't drop a CHECK in place: the table is rebuilt with the same columns, defaults
-- and indexes (ids are kept, so AUTOINCREMENT never reuses one mobile has seen).

-- +goose Up
CREATE TABLE cc_user_memories_new (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id         INTEGER,                                -- NULL = household memory
    household_id    TEXT    NOT NULL,
    category        TEXT    NOT NULL DEFAULT 'general',
    key             TEXT,
    content         TEXT    NOT NULL,
    source          TEXT    NOT NULL DEFAULT 'voice',
    is_active       INTEGER NOT NULL DEFAULT 1,
    is_pinned       INTEGER NOT NULL DEFAULT 0,
    embedding       BLOB    CHECK (embedding IS NULL OR (length(embedding) > 0 AND length(embedding) % 4 = 0)),
    embedding_model TEXT,                                   -- LD6: the model that produced embedding
    created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    expires_at      TEXT
);
INSERT INTO cc_user_memories_new (id, user_id, household_id, category, key, content, source, is_active,
    is_pinned, embedding, embedding_model, created_at, updated_at, expires_at)
SELECT id, user_id, household_id, category, key, content, source, is_active,
    is_pinned, embedding, NULL, created_at, updated_at, expires_at
FROM cc_user_memories;
DROP TABLE cc_user_memories;
ALTER TABLE cc_user_memories_new RENAME TO cc_user_memories;
CREATE INDEX cc_user_memories_category ON cc_user_memories (category);
CREATE INDEX cc_user_memories_lookup ON cc_user_memories (user_id, household_id, is_active);
CREATE INDEX cc_user_memories_household ON cc_user_memories (household_id, is_active);
CREATE INDEX cc_user_memories_pinned ON cc_user_memories (user_id, household_id)
    WHERE is_active = 1 AND is_pinned = 1;
CREATE INDEX cc_user_memories_expires_at ON cc_user_memories (expires_at) WHERE expires_at IS NOT NULL;

-- Extraction claims transcripts per job (extraction_job_id); the stale release looks them up.
CREATE INDEX cc_conversation_transcripts_extraction_job_id ON cc_conversation_transcripts (extraction_job_id)
    WHERE extraction_job_id IS NOT NULL;

-- +goose Down
DROP INDEX cc_conversation_transcripts_extraction_job_id;
CREATE TABLE cc_user_memories_old (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id      INTEGER,
    household_id TEXT    NOT NULL,
    category     TEXT    NOT NULL DEFAULT 'general',
    key          TEXT,
    content      TEXT    NOT NULL,
    source       TEXT    NOT NULL DEFAULT 'voice',
    is_active    INTEGER NOT NULL DEFAULT 1,
    is_pinned    INTEGER NOT NULL DEFAULT 0,
    embedding    BLOB    CHECK (embedding IS NULL OR length(embedding) = 1536),
    created_at   TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at   TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    expires_at   TEXT
);
INSERT INTO cc_user_memories_old (id, user_id, household_id, category, key, content, source, is_active,
    is_pinned, embedding, created_at, updated_at, expires_at)
SELECT id, user_id, household_id, category, key, content, source, is_active,
    is_pinned, CASE WHEN length(embedding) = 1536 THEN embedding END, created_at, updated_at, expires_at
FROM cc_user_memories;
DROP TABLE cc_user_memories;
ALTER TABLE cc_user_memories_old RENAME TO cc_user_memories;
CREATE INDEX cc_user_memories_category ON cc_user_memories (category);
CREATE INDEX cc_user_memories_lookup ON cc_user_memories (user_id, household_id, is_active);
CREATE INDEX cc_user_memories_household ON cc_user_memories (household_id, is_active);
CREATE INDEX cc_user_memories_pinned ON cc_user_memories (user_id, household_id)
    WHERE is_active = 1 AND is_pinned = 1;
CREATE INDEX cc_user_memories_expires_at ON cc_user_memories (expires_at) WHERE expires_at IS NOT NULL;
