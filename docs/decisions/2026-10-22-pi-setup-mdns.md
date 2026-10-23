# A Pi in LAN setup mode advertises _jarvis-setup._tcp

- **Date:** 2026-10-22 (the user's ask in #508)

A Pi in setup mode whose wlan0 is already on a network (after a factory reset, #173; or installed
without enrolling, [#423](2026-10-21-setup-mode-keeps-wifi.md)) serves the provisioning API on its
LAN address but starts no hotspot. Nothing announced it, and the app's Add node flow only looks
for a hotspot, so the node looked dead unless the user typed its IP under developer options.

The agent now advertises the API over mDNS for as long as it is in that mode. This is a contract
with jarvis-node-mobile, which browses for it:

- **Service type** `_jarvis-setup._tcp`, domain `local.` (`mdns.SetupServiceType`).
- **Instance name** the node id the API's `/api/v1/info` reports (`jarvis-<last 8 hex of the MAC>`
  on a reset node).
- **Port** the API's listener (8080), on the host's LAN addresses (`mdns.Advertise`'s interface
  pick, as jarvisd's `_jarvis-config._tcp`).
- **TXT** `node_id` and `hardware` (same values as `/api/v1/info`). Clients should still call
  `/api/v1/info` at the resolved address before provisioning.

It is withdrawn as soon as the node is provisioned (the provisioning server's `OnProvisioned`
ends setup mode) or setup mode stops; a failed attempt leaves it up, as the API is. An
advertisement that can't start (no LAN address, port 5353 refused) is logged and setup mode goes
on: the manual IP still works.

**Hotspot mode does not advertise.** A phone on the setup hotspot finds the node by its SSID and
the fixed 192.168.4.1; announcing on the AP network adds nothing, and the ask only covers the LAN
case. Revisit if the app moves to mDNS for both.
