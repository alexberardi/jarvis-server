# Smart-home Devices belong in the member web app

- **Date:** 2026-11-10 (user requested implementation)

Add a Devices tab for all users in the Chat/Recordings/Workflows section of the web
navigation. It lets household members discover devices on a node's network and pair/add
them, using the node-side capabilities already used by mobile. Pantry's device-protocol
support makes this useful from a browser as well.

This supersedes the Devices exclusion in
[the original member web app plan](../webapp/01-member-web-app-plan.md). It does not move
Jarvis node enrollment or administrative node management into the Devices page.

The [specification](../webapp/02-devices-spec.md) limits the first version to protocols
already installed and configured. External-manager households get a read-only list;
Pantry installation, provider OAuth and credentials stay in their existing surfaces.

Import accepts an optional household `node_id`, persisted on direct devices. Server
control/state/removal use that node without fallback; accepted pairing starts also pin
older unbound direct devices. Re-import cannot move a binding. An older client may
re-import identical connection data, but replacing a bound connection requires the
matching node ID. A bounded five-minute in-memory session store checks the household,
device, user and follow-up action and consumes each answer once. Restart requires a
new pairing. No PIN is stored. Control data cannot override the registered identity.

This is a guarantee for server-routed operations (including web/mobile pairing), not a
change to nodes' existing local voice device caches. Household-wide device visibility
remains necessary for display media selection. Binding-aware local voice routing across
nodes needs a separate Go/Python client change; do not filter that shared registry.

Removal deletes the registration even when its node is unavailable; cleanup/unpair is
best effort on its assigned node only. It does not guarantee physical unpair or reset.
New member gateway routes retain CC authorization and trigger the server's human
access-review hold before merge.
