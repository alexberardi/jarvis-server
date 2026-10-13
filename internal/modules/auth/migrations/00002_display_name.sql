-- An optional, human-friendly name for a user (what the mobile app and admin show for household
-- members instead of the username). NULL = unset. Trimmed, at most 64 characters, no control
-- characters; validated by the API, not the schema.

-- +goose Up
ALTER TABLE auth_users ADD COLUMN display_name TEXT;

-- +goose Down
ALTER TABLE auth_users DROP COLUMN display_name;
