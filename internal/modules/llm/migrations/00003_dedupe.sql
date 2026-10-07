-- llm_dedupe: the queue enqueue dedupe window (docs/llm/03 §11). A repeat of
-- (job_id, idempotency_key) within ttl_seconds of the first enqueue is answered deduped:true,
-- even after the job finished (the legacy Redis SET NX EX semantics, frozen by
-- TestLLMQueueEnqueue). The platform queue's own dedup key only guards live jobs.
-- Version 00002 is left to the model registry (docs/llm/05 §4).

-- +goose Up
CREATE TABLE llm_dedupe (
    key        TEXT    PRIMARY KEY,
    expires_at INTEGER NOT NULL
);
CREATE INDEX llm_dedupe_expires ON llm_dedupe (expires_at);

-- +goose Down
DROP TABLE llm_dedupe;
