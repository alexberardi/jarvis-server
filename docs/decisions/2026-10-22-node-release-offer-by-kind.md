# The release offered to a node follows its kind, capped at jarvisd's version

- **Date:** 2026-10-22 (item #487, from the user's report that the app offered Go Pi nodes 0.3.x)

`GET /api/v0/releases/latest?node_id=<id>` answers for that node's kind. A node that follows this
server (a Go Pi node or a desktop agent, `followsServer()`) gets `kind: "go"` and the newest
jarvis-server release by semver precedence, on the server's channel (prereleases only when jarvisd
runs one, `update.Newest`), **capped at jarvisd's own version**: "Update" moves a Go node to
jarvisd's version (`requestServerVersionUpdate`, the heartbeat's `server_version`), so offering a
newer release than the server runs would offer something the button can't deliver. A Python node
gets jarvis-node-setup's latest (`kind: "python"`). A release not newer than the node's
`last_seen_version` is `null`, so even an app with a broken comparison shows no update.

With a `node_id` the route needs a member of the node's household (`requireNodeAccess`), since a
node's kind and version are household data. Without one it stays open and answers as before
(jarvis-node-setup's latest), so the shipped app keeps working until it sends `node_id` with its
bearer. See [05-nodes](../cc/05-nodes.md).
