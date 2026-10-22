-- Inbound phone calls (loop #446, docs/cc/phone-sip.md §1 and §4 step 3): calls the PBX routes
-- to Jarvis's SIP extension, answered by the receptionist/screener, which takes a message or
-- hangs up. Their own table rather than cc_phone_call_sessions, whose states, confirm card,
-- goal NOT NULL, caps and do-not-call all assume we dial out.
--
-- caller_number / caller_id_name are what the PBX said (P-Asserted-Identity, then From):
-- a hint, never authentication. caller_name, reason and message are what the caller said
-- (take_message). ended_at NULL is a call in progress (or one jarvisd stopped during; the next
-- start closes those as failed / interrupted). outcome: message (one was taken), no_message
-- (the caller left without one), hung_up (the receptionist ended it: spam, a robocall),
-- failed. No transcript is kept (docs/cc/phone-sip.md §7).
-- +goose Up
CREATE TABLE cc_phone_inbound_calls (
    id               TEXT    PRIMARY KEY,
    household_id     TEXT    NOT NULL,
    provider         TEXT    NOT NULL DEFAULT 'sip',
    call_id          TEXT,
    caller_number    TEXT,
    caller_id_name   TEXT,
    caller_id_source TEXT,
    caller_name      TEXT,
    reason           TEXT,
    message          TEXT,
    summary          TEXT,
    outcome          TEXT    CHECK (outcome IS NULL OR outcome IN ('message', 'no_message', 'hung_up', 'failed')),
    end_reason       TEXT,
    audio_object_key TEXT,
    turns            INTEGER NOT NULL DEFAULT 0,
    started_at       TEXT    NOT NULL,
    ended_at         TEXT,
    duration_seconds INTEGER
);
CREATE INDEX cc_phone_inbound_calls_household_started ON cc_phone_inbound_calls (household_id, started_at);

-- +goose Down
DROP INDEX cc_phone_inbound_calls_household_started;
DROP TABLE cc_phone_inbound_calls;
