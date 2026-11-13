-- Discord bridge (docs/discord-bridge.md): which Discord account speaks as which Jarvis user.
-- One row per (household, Discord account); a user has at most one link per household. Rows go
-- with the user (PurgeUser) and with the household.
-- +goose Up
CREATE TABLE cc_discord_links (
    household_id    TEXT    NOT NULL,
    discord_user_id TEXT    NOT NULL,
    user_id         INTEGER NOT NULL,
    node_id         TEXT    NOT NULL,
    voice_replies   TEXT    NOT NULL DEFAULT 'auto',
    proactive       INTEGER NOT NULL DEFAULT 0,
    created_at      TEXT    NOT NULL,
    PRIMARY KEY (household_id, discord_user_id),
    UNIQUE (household_id, user_id)
);
CREATE INDEX cc_discord_links_user ON cc_discord_links (user_id);

-- +goose Down
DROP TABLE cc_discord_links;
