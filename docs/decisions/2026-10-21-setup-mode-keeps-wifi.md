# Setup mode keeps a working Wi-Fi; --activate refuses an unenrolled node over Wi-Fi SSH

- **Date:** 2026-10-21 (the user's ask in #423)

A Pi installed with `install-pi.sh --activate` before it was enrolled went into setup mode
(#292, [go-pi-node.md §7 4.4, D6](../agent/go-pi-node.md)), brought up its hotspot on wlan0 and
left the home network. Over a Wi-Fi SSH session that strands the node: the AP↔STA recovery only
runs for a node provisioned before, and a reboot comes back to setup mode.

Two changes:

- **Setup mode starts the hotspot only when wlan0 is on no network.** When `CurrentSSID` names a
  network, the agent serves the provisioning API (:8080, and :80) on its LAN address, with the
  HAT's setup pattern, and leaves the radio alone. When the question fails, it starts the hotspot
  as before. A failed provisioning attempt still brings the hotspot back (unchanged), so the app
  can retry from it. This narrows D6, where the hotspot was always part of setup mode.
- **`install-pi.sh --activate` refuses on a node with no credentials
  (`~<user>/.config/jarvis-agent/node.json`) when the SSH session's route goes out wlan0**, and
  prints the enroll command; the install itself is done and the unit left disabled.
  `--setup-mode` activates anyway. Over ethernet or a local console it warns and activates.

Open: whether jarvis-node-mobile can find and provision a node in setup mode on the LAN rather
than over its hotspot; until it can, such a node is enrolled with `jarvis-agent enroll`.
