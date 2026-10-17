# A node's heartbeat renames its own registration

- **Date:** 2026-10-16 (build agent, item #62; held for the user's review because it writes
  auth's node registrations)

The heartbeat (`POST /admin/nodes/heartbeat`) takes an optional `name`. jarvisd trims it and,
when it is 1–100 characters, sets `auth_node_registrations.name` for the node the key
authenticated (auth's in-process `RenameNode`: active row only, no write when unchanged). A node
can only rename itself: the handler never takes a node id from the body. An empty or overlong
name is ignored with a 200 rather than a 422, like `device_id` and `node_kind`, so a bad
node.json or an old agent can never stop a node heartbeating.

The bound applies to the heartbeat only. Registration (`POST /internal/nodes/register`,
`POST /admin/nodes`, provisioning tokens) still takes any name, as legacy did; bounding those
would change their contract (a 422 where Python answered 200) and is a separate decision.

The desktop agent sends `name` from node.json (`Creds.Name`, set at enrolment from `--name`,
which defaults to the hostname). It never falls back to `os.Hostname()`: a node enrolled
without a name (pairing, a provisioning token) has its name from the app, and a hostname
fallback would overwrite it. So a hostname change alone does not relabel a computer; editing
`name` in node.json (read at start, so after a restart) or re-enrolling does. See
[../cc/05-nodes.md](../cc/05-nodes.md) §3.8 and [../agent/DESIGN.md](../agent/DESIGN.md).
