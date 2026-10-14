-- Device sign-out (docs/decisions/2026-10-14-device-sign-out.md): owner_user_id is the member
-- who paired the node, i.e. who minted the provisioning token it redeemed (the approver of a
-- pairing code, or the member who signed in on the computer). NULL: created with the admin key,
-- or the token was already pruned. Backfilled from tokens still on file.
-- "This computer" (install plan §3.2 Q2 (a)): last_addr is the TCP peer address of the node's
-- latest heartbeat (never a forwarded header). NULL until the next heartbeat.
-- +goose Up
ALTER TABLE cc_nodes ADD COLUMN owner_user_id INTEGER;
ALTER TABLE cc_nodes ADD COLUMN last_addr TEXT;
UPDATE cc_nodes SET owner_user_id = (
    SELECT t.created_by_user_id FROM cc_provisioning_tokens t
    WHERE t.node_id = cc_nodes.node_id AND t.consumed_at IS NOT NULL AND t.created_by_user_id IS NOT NULL
    ORDER BY t.consumed_at DESC LIMIT 1);

-- +goose Down
ALTER TABLE cc_nodes DROP COLUMN last_addr;
ALTER TABLE cc_nodes DROP COLUMN owner_user_id;
