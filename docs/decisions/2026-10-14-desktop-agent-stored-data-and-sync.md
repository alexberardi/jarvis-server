# Desktop agents answer the app's Stored data and secret Sync as a Pi does

- **Date:** 2026-10-14 (user, through the coordinator; the two differences below are design
  choices made while building it)

The mobile app's "Stored data" tab and its "Sync to other nodes" work for a desktop agent
unchanged. The agent answers the app's requests over the same MQTT topics, payloads and K2
crypto as a Pi. Golden tests built from the Python node's own functions pin the answers.

Where a byte-for-byte copy of the Pi would copy a bug, the agent deliberately differs in two
places:

- An edit from the app keeps a record's expiry. The Pi drops it.
- A secrets push without a user keeps the key's integration scope. On a Pi, the same push fails
  forever once that key also exists as some user's secret.

The agent's built-in Go tools keep no `JarvisStorage` records, so the browser lists only the
sidecar's packages.

Detail: [agent/DESIGN.md §16](../agent/DESIGN.md). Extends
[2026-10-14-desktop-agent-node-settings-k2](2026-10-14-desktop-agent-node-settings-k2.md),
which listed the browser as not built.
