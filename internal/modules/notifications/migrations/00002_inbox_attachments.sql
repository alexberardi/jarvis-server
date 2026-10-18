-- Inbox attachments (docs/decisions/2026-10-17-inbox-attachments.md): images and video on inbox
-- items. The bytes are in the blob store under inbox/sha256/<ab>/<sha256>, shared by content;
-- a row is one file on one item. item_id is NULL from upload until an item claims it (the
-- cleanup sweep drops rows unclaimed after an hour). owner is the uploader ("app:<id>" or
-- "node:<id>"): only it may claim the upload. household_id is the node's household from the
-- upload, or the item's from the claim.

-- +goose Up
CREATE TABLE notifications_inbox_attachments (
    id           TEXT    PRIMARY KEY,
    item_id      TEXT    REFERENCES notifications_inbox_items (id) ON DELETE CASCADE,
    owner        TEXT    NOT NULL,
    household_id TEXT,
    sha256       TEXT    NOT NULL,
    mime         TEXT    NOT NULL,
    size         INTEGER NOT NULL,
    width        INTEGER,
    height       INTEGER,
    duration_ms  INTEGER,
    position     INTEGER NOT NULL DEFAULT 0,
    created_at   TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE INDEX notifications_inbox_attachments_item_id ON notifications_inbox_attachments (item_id);
CREATE INDEX notifications_inbox_attachments_sha256 ON notifications_inbox_attachments (sha256);

-- +goose Down
DROP TABLE notifications_inbox_attachments;
