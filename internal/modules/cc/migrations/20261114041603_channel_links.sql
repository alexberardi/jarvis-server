-- Channels (docs/CHANNELS.md, K2): which account on a chat service speaks as which Jarvis user,
-- and which services have called in. A provider is a name a package picks ("discord"); jarvisd
-- lists none. One row per (household, provider, account); a user has at most one link per
-- provider in a household. Links go with the user (PurgeUser).
-- +goose Up
CREATE TABLE cc_channel_links (
    household_id     TEXT    NOT NULL,
    provider         TEXT    NOT NULL,
    external_user_id TEXT    NOT NULL,
    user_id          INTEGER NOT NULL,
    node_id          TEXT    NOT NULL,
    voice_replies    TEXT    NOT NULL DEFAULT 'auto',
    proactive        INTEGER NOT NULL DEFAULT 0,
    created_at       TEXT    NOT NULL,
    PRIMARY KEY (household_id, provider, external_user_id),
    UNIQUE (household_id, provider, user_id)
);
CREATE INDEX cc_channel_links_user ON cc_channel_links (user_id);

-- A provider's daemon calls in from one node of the household; last_seen_at is its sign of life.
CREATE TABLE cc_channel_providers (
    household_id TEXT NOT NULL,
    provider     TEXT NOT NULL,
    node_id      TEXT NOT NULL,
    last_seen_at TEXT NOT NULL,
    PRIMARY KEY (household_id, provider)
);

-- +goose Down
DROP TABLE cc_channel_providers;
DROP TABLE cc_channel_links;
