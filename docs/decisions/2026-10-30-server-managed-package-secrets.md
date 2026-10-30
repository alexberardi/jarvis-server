# Package secrets can be set on the server, not only from the mobile app

- **Date:** 2026-10-30 (user: package secrets, such as a Home Assistant token, should be settable
  without the mobile app)

A Pantry package declares the secrets it needs (`HA_URL`, a Keep token, ...). Until now the only
way to give a node one was the mobile app's K2-encrypted config push: only the app holds K2, so
the admin UI, scripts and AI or MCP tooling had no way to set them.

A household admin can now set, list and delete them on the server:

- `PUT /api/v0/households/{id}/package-secrets/{KEY}` with `{"value": "...", "node_ids": [...]}`
  (`node_ids` omitted: every node of the household), `GET` for the names (never a value) and
  `DELETE`. Household admin (or the admin key) only.
- The server keeps the value encrypted at rest with the same dedicated key as the smart-home
  OAuth tokens (`cc-oauth.key`), and returns it only to the nodes it is meant for.
- Delivery is nudge-and-pull, like package installs: jarvisd publishes `package-secrets` (no
  secret in the message), and the node pulls `GET /api/v0/nodes/{id}/package-secrets` with its own
  credentials. A node also pulls on every MQTT connect, so one that was away catches up.
- A deleted secret leaves a tombstone for 30 days; the node pulls it as `""` and clears its copy.
- The agent writes into the same node secret store the K2 push does, so either path can set or
  change a secret, and the app keeps working as before. A key the node holds as a user's own
  secret is not touched. It needs no K2 on the node.

**Trade-off, said plainly.** The K2 relay is blind by design: jarvisd never sees the plaintext.
This path means jarvisd holds the secret (encrypted at rest) and sends it to the node over the
node's authenticated channel, which is plain HTTP on a LAN install, as every other node pull is.
A household that does not want jarvisd to hold a secret keeps using the app. Nothing is
required: with no server-managed secrets set, behaviour is unchanged.

Python nodes do not pull; this is for the Go agents. See `docs/cc/12-packages-and-command-data.md`
§2.4 and §4.
