# Computers are labelled by their name, Pis by their room

- **Date:** 2026-10-15 (user)

Every desktop agent's room is `desktop`, so node pickers that label by room (the mobile Store's
install picker, the primary-node picker, the web chat picker) showed several computers as the same
"desktop". The user chose: a computer (`node_kind = "desktop"`) is labelled by its **name** (the
computer name the agent enrolled with, e.g. its hostname), falling back to its room and then a
short node id. A Pi keeps its room label, even when it has a registered name.

The name already reached the server at enrolment (the provisioning token's or pairing code's
`name`, stored as `auth_node_registrations.name`); it was never returned. `NodeResponse` (cc's
node list, get and PATCH) and the smart-home config's `nodes` options now carry `name`
(additive), null when the node was registered without one (auth then stores the node id). See
[05-nodes.md](../cc/05-nodes.md) and [07-smart-home.md](../cc/07-smart-home.md). A client on an
older server gets no `name` and keeps the room label.

Not covered: renaming. The name is the one given at enrolment; the agent's heartbeat does not
carry it, so changing `name` in the agent's `node.json` later does not reach the server.
