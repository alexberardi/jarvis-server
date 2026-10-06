-- command-center (module cc) schema baseline.
--
-- Source: jarvis_command_center on the MBP dev stack at alembic head `sb01signals`
-- (pg_dump --schema-only, 2026-10-06), with the cuts and additions decided in
-- docs/cc/QUESTIONS.md. docs/schema/cc.md maps every table, drop and addition.
--
-- Conventions:
--   * Timestamps are TEXT, ISO-8601 UTC with a trailing Z ('2026-10-06T12:00:00.000Z').
--     Every legacy column was `timestamp without time zone` holding naive UTC.
--   * Booleans are INTEGER 0/1. JSON stays TEXT. Enum-like strings get a CHECK.
--   * No FKs to other modules (auth users/households): modules migrate independently.
--   * The settings table (cc_settings) is created by internal/platform/settings, not here.

-- +goose Up

-- ---------------------------------------------------------------- 07 smart home: rooms
CREATE TABLE cc_rooms (
    id              TEXT    PRIMARY KEY,
    household_id    TEXT    NOT NULL,
    name            TEXT    NOT NULL,
    normalized_name TEXT    NOT NULL,
    icon            TEXT,
    ha_area_id      TEXT,
    parent_room_id  TEXT    REFERENCES cc_rooms (id) ON DELETE SET NULL,
    created_at      TEXT,
    updated_at      TEXT,
    CONSTRAINT cc_rooms_household_name UNIQUE (household_id, normalized_name)
);
CREATE INDEX cc_rooms_household_id ON cc_rooms (household_id);
CREATE INDEX cc_rooms_parent_room_id ON cc_rooms (parent_room_id);

-- ---------------------------------------------------------------- 05 nodes
-- Dropped: api_key (plaintext legacy key; node auth is the auth module's, D40 05.Q10),
-- adapter_hash (LoRA cut, PLAN §7 / D9).
CREATE TABLE cc_nodes (
    node_id           TEXT    PRIMARY KEY,
    room              TEXT    NOT NULL,
    "user"            TEXT,
    voice_mode        TEXT,
    last_seen         TEXT,
    room_id           TEXT    REFERENCES cc_rooms (id) ON DELETE SET NULL,
    household_id      TEXT,
    last_seen_version TEXT,
    install_mode      TEXT,                              -- tarball | docker | dev
    is_busy           INTEGER NOT NULL DEFAULT 0,
    git_sha           TEXT,
    is_active         INTEGER NOT NULL DEFAULT 1,
    protocols         TEXT,                              -- JSON array
    needs_k2          INTEGER NOT NULL DEFAULT 1
);
CREATE INDEX cc_nodes_household_id ON cc_nodes (household_id);
CREATE INDEX cc_nodes_room_id ON cc_nodes (room_id);

-- reset_token / reset_token_consumed_at: D10 (the factory-reset token is persisted with the
-- task instead of the in-memory 300 s dict). Stored raw: it must be re-published to the node.
CREATE TABLE cc_node_tasks (
    id                      TEXT    PRIMARY KEY,
    node_id                 TEXT    NOT NULL REFERENCES cc_nodes (node_id) ON DELETE CASCADE,
    kind                    TEXT    NOT NULL CHECK (kind IN ('update', 'factory_reset')),
    target_version          TEXT,
    state                   TEXT    NOT NULL DEFAULT 'pending'
                            CHECK (state IN ('pending', 'dispatched', 'in_progress', 'success', 'failed')),
    error_message           TEXT,
    reset_token             TEXT,
    reset_token_consumed_at TEXT,
    created_at              TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at              TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    finished_at             TEXT
);
CREATE INDEX cc_node_tasks_node_id ON cc_node_tasks (node_id);
CREATE INDEX cc_node_tasks_node_state ON cc_node_tasks (node_id, state);
CREATE UNIQUE INDEX cc_node_tasks_reset_token ON cc_node_tasks (reset_token) WHERE reset_token IS NOT NULL;

-- No FK on node_id: the token is minted before the node exists.
CREATE TABLE cc_provisioning_tokens (
    id                 TEXT    PRIMARY KEY,
    token_hash         TEXT    NOT NULL UNIQUE,          -- sha256 hex
    node_id            TEXT    NOT NULL,
    household_id       TEXT    NOT NULL,
    room               TEXT,
    name               TEXT,
    created_by_user_id INTEGER,
    expires_at         TEXT    NOT NULL,
    created_at         TEXT    NOT NULL,
    consumed_at        TEXT
);
CREATE INDEX cc_provisioning_tokens_node_id ON cc_provisioning_tokens (node_id);

-- include_values / user_id: D40 05.Q8 (were MQTT-only; persisted so the reconnect backstop
-- can honour secret sync).
CREATE TABLE cc_settings_requests (
    request_id     TEXT    PRIMARY KEY,
    node_id        TEXT    NOT NULL REFERENCES cc_nodes (node_id) ON DELETE CASCADE,
    status         TEXT    NOT NULL CHECK (status IN ('pending', 'fulfilled', 'expired')),
    include_values INTEGER NOT NULL DEFAULT 0,
    user_id        INTEGER,
    created_at     TEXT    NOT NULL,
    expires_at     TEXT    NOT NULL
);
CREATE INDEX cc_settings_requests_node_id ON cc_settings_requests (node_id);
CREATE INDEX cc_settings_requests_created_at ON cc_settings_requests (created_at);

CREATE TABLE cc_settings_snapshots (
    snapshot_id                 TEXT    PRIMARY KEY,
    node_id                     TEXT    NOT NULL REFERENCES cc_nodes (node_id) ON DELETE CASCADE,
    request_id                  TEXT    NOT NULL REFERENCES cc_settings_requests (request_id) ON DELETE CASCADE,
    ciphertext                  TEXT    NOT NULL,        -- base64url
    nonce                       TEXT    NOT NULL,
    tag                         TEXT    NOT NULL,
    aad_node_id                 TEXT    NOT NULL,
    aad_schema_version          INTEGER NOT NULL,
    aad_commands_schema_version INTEGER NOT NULL,
    aad_revision                INTEGER NOT NULL,
    aad_request_id              TEXT    NOT NULL,
    created_at                  TEXT    NOT NULL
);
CREATE INDEX cc_settings_snapshots_node_id ON cc_settings_snapshots (node_id);
CREATE INDEX cc_settings_snapshots_request_id ON cc_settings_snapshots (request_id);

-- user_id: added so account deletion can hard-delete a user's traces (D20, 04.Q3).
CREATE TABLE cc_request_traces (
    id                TEXT    PRIMARY KEY,
    conversation_id   TEXT    NOT NULL,
    request_type      TEXT    NOT NULL,                  -- stt | warmup | voice_command | voice_command_stream | ...
    source            TEXT    NOT NULL,                  -- node | mobile
    node_id           TEXT    REFERENCES cc_nodes (node_id) ON DELETE SET NULL,
    household_id      TEXT,
    user_id           INTEGER,
    user_command      TEXT,
    assistant_message TEXT,
    status            TEXT    NOT NULL DEFAULT 'ok',     -- ok | error
    error_message     TEXT,
    total_duration_ms REAL    NOT NULL,
    spans_json        TEXT    NOT NULL,
    created_at        TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE INDEX cc_request_traces_conversation_id ON cc_request_traces (conversation_id);
CREATE INDEX cc_request_traces_created_at ON cc_request_traces (created_at);
CREATE INDEX cc_request_traces_household_id ON cc_request_traces (household_id);
CREATE INDEX cc_request_traces_node_id ON cc_request_traces (node_id);
CREATE INDEX cc_request_traces_user_id ON cc_request_traces (user_id);

-- ---------------------------------------------------------------- 07 smart home
CREATE TABLE cc_devices (
    id              TEXT    PRIMARY KEY,
    household_id    TEXT    NOT NULL,
    room_id         TEXT    REFERENCES cc_rooms (id) ON DELETE SET NULL,
    entity_id       TEXT    NOT NULL,
    name            TEXT    NOT NULL,
    domain          TEXT    NOT NULL,
    device_class    TEXT,
    manufacturer    TEXT,
    model           TEXT,
    source          TEXT    DEFAULT 'home_assistant',
    ha_device_id    TEXT,
    is_controllable INTEGER DEFAULT 1,
    is_active       INTEGER DEFAULT 1,
    protocol        TEXT,
    local_ip        TEXT,
    mac_address     TEXT,
    cloud_id        TEXT,
    created_at      TEXT,
    updated_at      TEXT,
    CONSTRAINT cc_devices_household_entity UNIQUE (household_id, entity_id)
);
CREATE INDEX cc_devices_household_id ON cc_devices (household_id);
CREATE INDEX cc_devices_room_id ON cc_devices (room_id);

CREATE TABLE cc_device_scan_requests (
    id            TEXT    PRIMARY KEY,
    node_id       TEXT    NOT NULL REFERENCES cc_nodes (node_id) ON DELETE CASCADE,
    household_id  TEXT    NOT NULL,
    status        TEXT    NOT NULL DEFAULT 'pending'
                  CHECK (status IN ('pending', 'completed', 'failed', 'expired')),
    results_json  TEXT,
    device_count  INTEGER,
    error_message TEXT,
    created_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    expires_at    TEXT    NOT NULL,
    completed_at  TEXT
);
CREATE INDEX cc_device_scan_requests_household_id ON cc_device_scan_requests (household_id);
CREATE INDEX cc_device_scan_requests_node_id ON cc_device_scan_requests (node_id);

CREATE TABLE cc_device_list_requests (
    id               TEXT    PRIMARY KEY,
    node_id          TEXT    NOT NULL REFERENCES cc_nodes (node_id) ON DELETE CASCADE,
    household_id     TEXT    NOT NULL,
    status           TEXT    NOT NULL DEFAULT 'pending'
                     CHECK (status IN ('pending', 'completed', 'failed', 'expired')),
    manager_name     TEXT,
    can_edit_devices INTEGER,
    results_json     TEXT,
    device_count     INTEGER,
    error_message    TEXT,
    created_at       TEXT    NOT NULL,
    expires_at       TEXT    NOT NULL,
    completed_at     TEXT
);
CREATE INDEX cc_device_list_requests_household_id ON cc_device_list_requests (household_id);
CREATE INDEX cc_device_list_requests_node_id ON cc_device_list_requests (node_id);

-- K2 relay: CC stores only ciphertext (AES-GCM by mobile with the node's K2).
CREATE TABLE cc_config_pushes (
    id          TEXT    PRIMARY KEY,
    node_id     TEXT    NOT NULL REFERENCES cc_nodes (node_id) ON DELETE CASCADE,
    config_type TEXT    NOT NULL,
    ciphertext  TEXT    NOT NULL,
    nonce       TEXT    NOT NULL,
    tag         TEXT    NOT NULL,
    status      TEXT    NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'consumed')),
    created_at  TEXT    NOT NULL,
    consumed_at TEXT,
    expires_at  TEXT
);
CREATE INDEX cc_config_pushes_node_status ON cc_config_pushes (node_id, status);

-- Provider OAuth sessions. *_enc columns are AES-256-GCM b64url(nonce||ct) under the
-- dedicated at-rest key (D40 07.Q9).
CREATE TABLE cc_auth_sessions (
    id                TEXT    PRIMARY KEY,
    provider          TEXT    NOT NULL,
    node_id           TEXT    NOT NULL REFERENCES cc_nodes (node_id) ON DELETE CASCADE,
    user_id           INTEGER,
    status            TEXT    NOT NULL DEFAULT 'pending'
                      CHECK (status IN ('pending', 'active', 'consumed', 'expired')),
    state             TEXT    NOT NULL UNIQUE,           -- CSRF token
    code_verifier     TEXT,
    provider_base_url TEXT,
    authorize_url     TEXT,
    exchange_url      TEXT,
    redirect_uri      TEXT,
    client_id         TEXT    NOT NULL,
    client_secret_enc TEXT,
    access_token_enc  TEXT,
    refresh_token_enc TEXT,
    token_data_enc    TEXT,
    created_at        TEXT    NOT NULL,
    expires_at        TEXT    NOT NULL,
    completed_at      TEXT
);
CREATE INDEX cc_auth_sessions_node_id ON cc_auth_sessions (node_id);
CREATE INDEX cc_auth_sessions_provider ON cc_auth_sessions (provider);
CREATE INDEX cc_auth_sessions_status ON cc_auth_sessions (status);

CREATE TABLE cc_bluetooth_scan_requests (
    id            TEXT    PRIMARY KEY,
    node_id       TEXT    NOT NULL REFERENCES cc_nodes (node_id) ON DELETE CASCADE,
    household_id  TEXT    NOT NULL,
    status        TEXT    NOT NULL DEFAULT 'pending'
                  CHECK (status IN ('pending', 'completed', 'failed', 'expired')),
    role          TEXT    NOT NULL DEFAULT 'source',
    results_json  TEXT,
    device_count  INTEGER,
    error_message TEXT,
    source        TEXT    NOT NULL DEFAULT 'mobile',
    user_id       INTEGER,
    created_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    expires_at    TEXT    NOT NULL,
    completed_at  TEXT
);
CREATE INDEX cc_bluetooth_scan_requests_household_id ON cc_bluetooth_scan_requests (household_id);
CREATE INDEX cc_bluetooth_scan_requests_node_id ON cc_bluetooth_scan_requests (node_id);

CREATE TABLE cc_bluetooth_pair_requests (
    id            TEXT    PRIMARY KEY,
    node_id       TEXT    NOT NULL REFERENCES cc_nodes (node_id) ON DELETE CASCADE,
    household_id  TEXT    NOT NULL,
    mac_address   TEXT    NOT NULL,
    role          TEXT    NOT NULL DEFAULT 'source',
    status        TEXT    NOT NULL DEFAULT 'pending'
                  CHECK (status IN ('pending', 'completed', 'failed', 'expired')),
    device_name   TEXT,
    error_message TEXT,
    created_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    expires_at    TEXT    NOT NULL,
    completed_at  TEXT
);
CREATE INDEX cc_bluetooth_pair_requests_household_id ON cc_bluetooth_pair_requests (household_id);
CREATE INDEX cc_bluetooth_pair_requests_node_id ON cc_bluetooth_pair_requests (node_id);

-- ---------------------------------------------------------------- 04 memory and knowledge
-- AUTOINCREMENT on the integer-id tables keeps Postgres serial semantics: ids are exposed to
-- mobile and are never reused after a delete.
--
-- embedding: 384 float32, little-endian (1536 bytes), L2-normalised (all-MiniLM-L6-v2).
-- Searched by brute-force cosine in Go (PLAN §3.2); the pgvector HNSW index is not carried over.
CREATE TABLE cc_user_memories (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id      INTEGER,                                -- NULL = household memory
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
CREATE INDEX cc_user_memories_category ON cc_user_memories (category);
CREATE INDEX cc_user_memories_lookup ON cc_user_memories (user_id, household_id, is_active);
CREATE INDEX cc_user_memories_household ON cc_user_memories (household_id, is_active);
CREATE INDEX cc_user_memories_pinned ON cc_user_memories (user_id, household_id)
    WHERE is_active = 1 AND is_pinned = 1;
CREATE INDEX cc_user_memories_expires_at ON cc_user_memories (expires_at) WHERE expires_at IS NOT NULL;

CREATE TABLE cc_conversation_transcripts (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id           INTEGER NOT NULL,
    household_id      TEXT    NOT NULL,
    conversation_id   TEXT    NOT NULL,
    user_message      TEXT    NOT NULL,
    assistant_message TEXT,
    tool_calls_json   TEXT,
    is_processed      INTEGER NOT NULL DEFAULT 0,
    processed_at      TEXT,
    extraction_job_id TEXT,
    user_rating       INTEGER CHECK (user_rating IS NULL OR user_rating IN (-1, 0, 1)),
    rating_notes      TEXT,
    rated_at          TEXT,
    created_at        TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE INDEX cc_conversation_transcripts_created_at ON cc_conversation_transcripts (created_at);
CREATE INDEX cc_conversation_transcripts_is_processed ON cc_conversation_transcripts (is_processed);
CREATE INDEX cc_conversation_transcripts_user_id ON cc_conversation_transcripts (user_id);

-- Ported dormant (D30).
CREATE TABLE cc_person_characterizations (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id            INTEGER NOT NULL,
    household_id       TEXT    NOT NULL,
    body               TEXT    NOT NULL,                 -- JSON
    rendered           TEXT,
    confidence         REAL,
    version            INTEGER NOT NULL DEFAULT 1,
    last_transcript_at TEXT,
    model              TEXT,
    created_at         TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at         TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    CONSTRAINT cc_person_characterizations_user_household UNIQUE (user_id, household_id)
);

-- ---------------------------------------------------------------- 08 routines and schedules
-- routine_executions is not created (D40 08.Q11).
CREATE TABLE cc_routines (
    id                   TEXT    PRIMARY KEY,
    household_id         TEXT    NOT NULL,
    slug                 TEXT    NOT NULL,
    name                 TEXT    NOT NULL,
    trigger_phrases      TEXT    NOT NULL DEFAULT '[]',  -- JSON array
    steps                TEXT    NOT NULL DEFAULT '[]',  -- JSON [{command, args:[{key,value}], label}]
    response_instruction TEXT    NOT NULL DEFAULT '',
    response_length      TEXT    NOT NULL DEFAULT 'short' CHECK (response_length IN ('short', 'medium', 'long')),
    schedule             TEXT,                           -- JSON (mobile contract) or NULL
    enabled              INTEGER NOT NULL DEFAULT 1,
    created_at           TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at           TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    CONSTRAINT cc_routines_household_slug UNIQUE (household_id, slug)
);
CREATE INDEX cc_routines_household_id ON cc_routines (household_id);

-- Errand schedules. Dropped: title (unused) and the 'paused' state (nothing set it)
-- (D40 08.Q12).
CREATE TABLE cc_schedules (
    id            TEXT    PRIMARY KEY,                   -- sch_<hex32>
    household_id  TEXT    NOT NULL,
    user_id       INTEGER,
    node_id       TEXT,
    intent        TEXT    NOT NULL,
    timezone      TEXT    NOT NULL DEFAULT 'UTC',
    next_fire_at  TEXT    NOT NULL,
    recurrence    TEXT,                                  -- JSON or NULL
    state         TEXT    NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'done', 'cancelled')),
    last_fired_at TEXT,
    created_at    TEXT    NOT NULL,
    updated_at    TEXT    NOT NULL
);
CREATE INDEX cc_schedules_household_id ON cc_schedules (household_id);
CREATE INDEX cc_schedules_next_fire_at ON cc_schedules (next_fire_at);
CREATE INDEX cc_schedules_state ON cc_schedules (state);

-- ---------------------------------------------------------------- 09 errands and workflows
-- Dropped vestigial columns routine_slug, cursor, results_json (D9 / 09.Q8).
-- expires_at is kept: it carries the 24 h draft TTL (D40 09.Q6), which adds the 'expired' state.
CREATE TABLE cc_errand_plans (
    id            TEXT    PRIMARY KEY,                   -- pl_<hex>
    household_id  TEXT    NOT NULL,
    user_id       INTEGER,
    node_id       TEXT,
    goal          TEXT    NOT NULL,
    summary       TEXT,
    steps         TEXT    NOT NULL DEFAULT '[]',         -- JSON
    state         TEXT    NOT NULL DEFAULT 'draft'
                  CHECK (state IN ('draft', 'launched', 'cancelled', 'failed', 'expired')),
    revision      INTEGER NOT NULL DEFAULT 1,
    error         TEXT,
    inbox_item_id TEXT,
    workflow_id   TEXT,                                  -- cc_workflows.id (no FK, as legacy)
    created_at    TEXT    NOT NULL,
    updated_at    TEXT    NOT NULL,
    confirmed_at  TEXT,
    expires_at    TEXT
);
CREATE INDEX cc_errand_plans_household_id ON cc_errand_plans (household_id);
CREATE INDEX cc_errand_plans_state ON cc_errand_plans (state);
CREATE INDEX cc_errand_plans_workflow_id ON cc_errand_plans (workflow_id);

CREATE TABLE cc_workflows (
    id            TEXT    PRIMARY KEY,                   -- wf_<hex>
    kind          TEXT    NOT NULL,
    household_id  TEXT    NOT NULL,
    user_id       INTEGER,
    node_id       TEXT,
    goal          TEXT    NOT NULL DEFAULT '',
    title         TEXT,
    steps         TEXT    NOT NULL DEFAULT '[]',         -- JSON (mutated by replan splices)
    cursor        INTEGER NOT NULL DEFAULT 0,
    results_json  TEXT    NOT NULL DEFAULT '[]',
    revision      INTEGER NOT NULL DEFAULT 1,
    state         TEXT    NOT NULL DEFAULT 'running'
                  CHECK (state IN ('running', 'waiting', 'done', 'partial', 'failed', 'timeout', 'cancelled')),
    waiting_on    TEXT    CHECK (waiting_on IS NULL OR waiting_on IN ('phone_call', 'timer', 'approval')),
    wake_at       TEXT,
    inbox_item_id TEXT,                                  -- delta card
    error         TEXT,
    created_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE INDEX cc_workflows_household_id ON cc_workflows (household_id);
CREATE INDEX cc_workflows_kind ON cc_workflows (kind);
CREATE INDEX cc_workflows_state ON cc_workflows (state);
CREATE INDEX cc_workflows_wake_at ON cc_workflows (wake_at);

-- ---------------------------------------------------------------- 10 signals, attention, proposals
CREATE TABLE cc_signals (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    household_id TEXT    NOT NULL,
    user_id      INTEGER,                                -- NULL = household
    node_id      TEXT,
    room         TEXT,
    kind         TEXT    NOT NULL,
    subject      TEXT,
    source_key   TEXT    NOT NULL,
    summary      TEXT,
    facts        TEXT,                                   -- JSON
    source_agent TEXT,
    cacheable    INTEGER NOT NULL DEFAULT 0,
    salience     REAL,
    observed_at  TEXT,
    expires_at   TEXT,                                   -- NULL never expires
    is_active    INTEGER NOT NULL DEFAULT 1,             -- schema parity; never set false
    created_at   TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at   TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    CONSTRAINT cc_signals_household_source UNIQUE (household_id, source_key)
);
CREATE INDEX cc_signals_expires_at ON cc_signals (expires_at);
CREATE INDEX cc_signals_household_id ON cc_signals (household_id);
CREATE INDEX cc_signals_kind ON cc_signals (kind);
CREATE INDEX cc_signals_user_id ON cc_signals (user_id);

CREATE TABLE cc_proposal_suppressions (
    id           TEXT    PRIMARY KEY,                    -- sup_<hex>
    household_id TEXT    NOT NULL,
    user_id      INTEGER,
    command      TEXT    NOT NULL,
    source_key   TEXT,
    descriptor   TEXT,
    created_at   TEXT    NOT NULL
);
CREATE INDEX cc_proposal_suppressions_household_id ON cc_proposal_suppressions (household_id);
CREATE INDEX cc_proposal_suppressions_user_id ON cc_proposal_suppressions (user_id);

-- Attention journal (D18). attention_source_tiers / _consents / _feedback are not created.
-- Deliveries cascade from events: the TTL cleanup deletes events only (needs foreign_keys=ON).
CREATE TABLE cc_attention_events (
    id             TEXT    PRIMARY KEY,
    household_id   TEXT    NOT NULL,
    source         TEXT    NOT NULL,
    category       TEXT    NOT NULL,
    title          TEXT    NOT NULL,
    summary        TEXT    NOT NULL DEFAULT '',
    dedupe_key     TEXT,
    target_user_id INTEGER,
    origin_node_id TEXT,
    payload_json   TEXT,
    created_at     TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE INDEX cc_attention_events_dedupe_key ON cc_attention_events (dedupe_key);
CREATE INDEX cc_attention_events_household_id ON cc_attention_events (household_id);
CREATE INDEX cc_attention_events_created_at ON cc_attention_events (created_at);

CREATE TABLE cc_attention_deliveries (
    id              TEXT    PRIMARY KEY,
    event_id        TEXT    NOT NULL REFERENCES cc_attention_events (id) ON DELETE CASCADE,
    household_id    TEXT    NOT NULL,
    rung            TEXT    NOT NULL CHECK (rung IN ('journal', 'inbox', 'push')),
    gate_trail_json TEXT    NOT NULL DEFAULT '[]',
    withheld_by     TEXT,
    inbox_item_id   TEXT,
    request_id      TEXT,
    outcome         TEXT,                                -- delivered | redeemed | expired | failed | duplicate
    created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE INDEX cc_attention_deliveries_created_at ON cc_attention_deliveries (created_at);
CREATE INDEX cc_attention_deliveries_event_id ON cc_attention_deliveries (event_id);
CREATE INDEX cc_attention_deliveries_household_id ON cc_attention_deliveries (household_id);
CREATE INDEX cc_attention_deliveries_request_id ON cc_attention_deliveries (request_id);

-- NEW (D7): the action an automation "Confirm" card runs. The card carries only this id;
-- confirm checks node ∈ caller's household and runs exactly this row, once.
CREATE TABLE cc_automation_actions (
    id              TEXT    PRIMARY KEY,
    household_id    TEXT    NOT NULL,
    node_id         TEXT    NOT NULL REFERENCES cc_nodes (node_id) ON DELETE CASCADE,
    command_name    TEXT    NOT NULL,
    arguments_json  TEXT    NOT NULL DEFAULT '{}',
    idempotency_key TEXT,
    inbox_item_id   TEXT,
    created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    consumed_at     TEXT
);
CREATE INDEX cc_automation_actions_household_id ON cc_automation_actions (household_id);
CREATE INDEX cc_automation_actions_node_id ON cc_automation_actions (node_id);

-- NEW (D40 10.Q5): reaction dedup state (leave-by claims, automation last-signature) with a TTL,
-- replacing the in-process dicts. Claimed only on terminal outcomes.
CREATE TABLE cc_reaction_claims (
    household_id TEXT    NOT NULL,
    claim_key    TEXT    NOT NULL,                       -- e.g. leave_by:<event>, automation:<uid>:presence
    value        TEXT,                                   -- last signature / state, for "changed?" checks
    outcome      TEXT,
    claimed_at   TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    expires_at   TEXT    NOT NULL,
    PRIMARY KEY (household_id, claim_key)
) WITHOUT ROWID;
CREATE INDEX cc_reaction_claims_expires_at ON cc_reaction_claims (expires_at);

-- ---------------------------------------------------------------- 11 phone
-- Dropped: overlay_json and source='web' (M12 / D47).
CREATE TABLE cc_phone_contacts (
    id              TEXT    PRIMARY KEY,
    household_id    TEXT    NOT NULL,
    name            TEXT    NOT NULL,
    normalized_name TEXT    NOT NULL,
    number          TEXT    NOT NULL,                    -- E.164
    address         TEXT,
    source          TEXT    NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'call')),
    line_type       TEXT    CHECK (line_type IS NULL OR line_type IN ('mobile', 'landline', 'voip', 'unknown')),
    do_not_call     INTEGER NOT NULL DEFAULT 0,
    notes           TEXT,
    verified_at     TEXT,
    created_at      TEXT    NOT NULL,
    updated_at      TEXT    NOT NULL,
    CONSTRAINT cc_phone_contacts_household_name UNIQUE (household_id, normalized_name)
);
CREATE INDEX cc_phone_contacts_household_id ON cc_phone_contacts (household_id);

-- Dropped: constraints (never written, M12 / D47). Added: in_call_at (D40 11.Q6, the minutes
-- cap is measured from it). errand_id holds a cc_workflows id despite its name (no FK, as legacy).
CREATE TABLE cc_phone_call_sessions (
    id               TEXT    PRIMARY KEY,
    household_id     TEXT    NOT NULL,
    user_id          INTEGER,
    confirmed_by     INTEGER,
    contact_id       TEXT    REFERENCES cc_phone_contacts (id) ON DELETE SET NULL,
    contact_name     TEXT,
    contact_address  TEXT,
    goal             TEXT    NOT NULL,
    details          TEXT,
    resolved_number  TEXT,
    dialed_number    TEXT,
    number_edited    INTEGER NOT NULL DEFAULT 0,
    line_type        TEXT    CHECK (line_type IS NULL OR line_type IN ('mobile', 'landline', 'voip', 'unknown')),
    state            TEXT    NOT NULL DEFAULT 'draft'
                     CHECK (state IN ('draft', 'confirmed', 'dialing', 'in_call', 'wrapup',
                                      'done', 'failed', 'declined', 'expired')),
    error_message    TEXT,
    transcript_json  TEXT,
    outcome_json     TEXT,
    audio_object_key TEXT,
    worker_url       TEXT,
    heartbeat_at     TEXT,
    twilio_call_sid  TEXT,
    duration_seconds INTEGER,
    errand_id        TEXT,
    errand_step      INTEGER,
    created_at       TEXT    NOT NULL,
    confirmed_at     TEXT,
    in_call_at       TEXT,
    expires_at       TEXT,
    ended_at         TEXT
);
CREATE INDEX cc_phone_call_sessions_errand_id ON cc_phone_call_sessions (errand_id);
CREATE INDEX cc_phone_call_sessions_household_id ON cc_phone_call_sessions (household_id);
CREATE INDEX cc_phone_call_sessions_state ON cc_phone_call_sessions (state);
CREATE INDEX cc_phone_call_sessions_contact_id ON cc_phone_call_sessions (contact_id);

-- ---------------------------------------------------------------- 12 packages
-- test_install_requests (D5) and prompt_provider_install_requests (D4 stub, PLAN §7) are not
-- created. verified_at: D39 (5-min pickup deadline, then expires_at = verified_at + 15 min).
-- No `action` column (D48).
CREATE TABLE cc_package_install_requests (
    id              TEXT    PRIMARY KEY,
    node_id         TEXT    NOT NULL REFERENCES cc_nodes (node_id) ON DELETE CASCADE,
    household_id    TEXT    NOT NULL,                    -- '' if the node has no household
    command_name    TEXT    NOT NULL,
    github_repo_url TEXT    NOT NULL,                    -- '' for uninstall / revert
    git_tag         TEXT,
    status          TEXT    NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending', 'restarting', 'completed', 'failed', 'expired')),
    results_json    TEXT,
    error_message   TEXT,
    created_at      TEXT    NOT NULL,
    verified_at     TEXT,
    expires_at      TEXT    NOT NULL,
    completed_at    TEXT
);
CREATE INDEX cc_package_install_requests_household_id ON cc_package_install_requests (household_id);
CREATE INDEX cc_package_install_requests_node_id ON cc_package_install_requests (node_id);

-- ---------------------------------------------------------------- 13 mobile chat, callbacks
-- node_id NULL = server plane.
CREATE TABLE cc_callback_jobs (
    id                       TEXT    PRIMARY KEY,        -- = MQTT request_id
    node_id                  TEXT    REFERENCES cc_nodes (node_id) ON DELETE CASCADE,
    household_id             TEXT    NOT NULL,
    user_id                  INTEGER,
    command_name             TEXT    NOT NULL,
    callback_name            TEXT    NOT NULL,
    data_json                TEXT,
    status                   TEXT    NOT NULL DEFAULT 'pending'
                             CHECK (status IN ('pending', 'completed', 'failed', 'expired')),
    error_message            TEXT,
    result_context_data_json TEXT,
    navigation_type          TEXT    NOT NULL DEFAULT 'new_notification',
    idempotency_key          TEXT,
    created_at               TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    expires_at               TEXT    NOT NULL,
    completed_at             TEXT
);
CREATE INDEX cc_callback_jobs_household_id ON cc_callback_jobs (household_id);
CREATE INDEX cc_callback_jobs_idempotency_key ON cc_callback_jobs (idempotency_key);
CREATE INDEX cc_callback_jobs_node_status ON cc_callback_jobs (node_id, status);

-- +goose Down
DROP TABLE cc_callback_jobs;
DROP TABLE cc_package_install_requests;
DROP TABLE cc_phone_call_sessions;
DROP TABLE cc_phone_contacts;
DROP TABLE cc_reaction_claims;
DROP TABLE cc_automation_actions;
DROP TABLE cc_attention_deliveries;
DROP TABLE cc_attention_events;
DROP TABLE cc_proposal_suppressions;
DROP TABLE cc_signals;
DROP TABLE cc_workflows;
DROP TABLE cc_errand_plans;
DROP TABLE cc_schedules;
DROP TABLE cc_routines;
DROP TABLE cc_person_characterizations;
DROP TABLE cc_conversation_transcripts;
DROP TABLE cc_user_memories;
DROP TABLE cc_bluetooth_pair_requests;
DROP TABLE cc_bluetooth_scan_requests;
DROP TABLE cc_auth_sessions;
DROP TABLE cc_config_pushes;
DROP TABLE cc_device_list_requests;
DROP TABLE cc_device_scan_requests;
DROP TABLE cc_devices;
DROP TABLE cc_request_traces;
DROP TABLE cc_settings_snapshots;
DROP TABLE cc_settings_requests;
DROP TABLE cc_provisioning_tokens;
DROP TABLE cc_node_tasks;
DROP TABLE cc_nodes;
DROP TABLE cc_rooms;
