# A computer may be the primary node, labelled and sorted after Pis

- **Date:** 2026-10-15 (user)

A household's primary node (`smart_home.primary_node_id`) handles smart-home device discovery,
device listing and Bluetooth jobs (`internal/modules/cc/smarthome.go`), and runs routines that
have no node of their own (`routines_run.go`). The desktop agent has no smart-home device code,
and a computer only runs routines while it is awake.

The user chose to keep a computer (`cc_nodes.node_kind = "desktop"`) **allowed** as the primary
node rather than refuse it, but the primary-node pickers must sort Pis first and show under each
computer: "Can't find smart-home devices · runs routines only while it's on".

To make that possible, `GET /households/{hh}/smart-home/config` adds `node_kind` to each of its
`nodes` options (additive; see [07-smart-home.md](../cc/07-smart-home.md)). A client talking to
an older server, which omits the field, shows no label and keeps its old order. The admin SPA has
no primary-node picker (its chat node picker only reads the primary), so only the mobile app
changes.
