-- Channels (docs/CHANNELS.md, K4): what jarvisd wants said unasked on a chat service, which is
-- only a reminder for a user who opted in. The provider's daemon asks for its rows and names
-- the ones it sent; a row not taken within a day is dropped.
-- +goose Up
CREATE TABLE cc_channel_outbox (
    id               TEXT NOT NULL PRIMARY KEY,
    household_id     TEXT NOT NULL,
    provider         TEXT NOT NULL,
    external_user_id TEXT NOT NULL,
    text             TEXT NOT NULL,
    created_at       TEXT NOT NULL
);
CREATE INDEX cc_channel_outbox_provider ON cc_channel_outbox (household_id, provider, created_at);

-- +goose Down
DROP TABLE cc_channel_outbox;
