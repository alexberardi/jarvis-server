-- The node core a heartbeat reports (version_info.runtime, docs/agent/go-pi-node.md §7): "go" for
-- the Go node core (jarvis-agent, a desktop agent or a Pi), NULL for the Python node, which never
-- sends it. A Go node follows this server's release; the Python node follows jarvis-node-setup's.
-- +goose Up
ALTER TABLE cc_nodes ADD COLUMN runtime TEXT;

-- +goose Down
ALTER TABLE cc_nodes DROP COLUMN runtime;
