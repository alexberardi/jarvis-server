# Re-enrolling a computer replaces its node

- **Date:** 2026-10-15 (user approved the fix; the design choices below are the implementer's)

Purging the desktop agent (`uninstall --purge`, the setup page's Remove) and setting it up again
used to give jarvisd a brand-new node every time, and the old one stayed forever: a dev household
collected five dead "desktop" nodes in every node picker. Now **the same computer, enrolled again
by the same member in the same household, replaces its previous node.**

- **Identity.** The agent sends `device_id` when it registers (`POST /nodes/register`, with
  `node_kind: "desktop"`) and in every heartbeat. It is an HMAC-SHA256, keyed with a fixed
  app-specific salt, of the OS machine id (`/etc/machine-id`, macOS `IOPlatformUUID`, Windows
  `MachineGuid`) and the OS user id (uid, or the Windows SID): one value per computer and desktop
  account, the raw machine id never leaves the computer, and nothing is stored, so it survives
  `--purge`. `internal/agent/deviceid`. An OS that doesn't say gives "" and the old behaviour.
- **Replacement.** When a node registers saying it is a desktop with a device id, jarvisd removes
  every other node with the same `household_id`, the same `device_id` **and the same
  `owner_user_id`** (the member who minted the token: the pairing approver, or who signed in), and
  that is itself a desktop node. Removal is the Sign out / Delete path (`removeNode`: revoked in
  auth, rows hard-deleted, `signed-out` published, never a factory reset). It runs after the new
  node is committed and is best effort: a failure leaves the old node, as before.
- **Why the owner scope.** `device_id` is client-supplied, so it must not be a capability. A
  member can only retire nodes they paired themselves, which they could already sign out. A Pi,
  an admin-key node (no owner), another member's computer (even when an admin pairs it), another
  household's node, and the same computer in another of the member's households are never
  touched. The id is never returned by any API.
- **Older agents** send no `device_id`: every enrolment still adds a node. An upgraded agent
  reports its id in heartbeats, so its node is on file and the next enrolment replaces it. Nodes
  left by older agents (the five above) must be signed out by hand once.
- **A running agent whose node is replaced** (enrolled again without stopping it) gets the
  `signed-out` nudge. It deletes `node.json` only if the file still holds its own node; when it
  holds the new one it keeps it and exits with an ordinary failure, so the supervisor restarts it
  on the new credentials.
- **Known limit.** Cloned disk images that share a machine id (a VM template without
  `systemd-machine-id-setup`) look like one computer: if the same member enrols two of them in the
  same household, the second replaces the first.

Changes [agent/DESIGN.md §13](../agent/DESIGN.md) ("The node stays on the server" after Remove).
Builds on [2026-10-14-device-sign-out.md](2026-10-14-device-sign-out.md) (`owner_user_id`).
