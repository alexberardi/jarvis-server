-- Public base URL per registry row (cutover Q1: prod keeps its Cloudflare tunnel). The operator
-- enters e.g. https://command-center.example.io or wss://mqtt.example.io on the admin Connections
-- page; it is stored as external_scheme + the existing external_host/external_port, which
-- syncSelf never writes. Discovery answers it to requests that arrived through a public hostname
-- and to ?style=external. NULL external_scheme = no public URL (legacy external_* semantics).

-- +goose Up
ALTER TABLE config_services ADD COLUMN external_scheme TEXT;

-- +goose Down
ALTER TABLE config_services DROP COLUMN external_scheme;
