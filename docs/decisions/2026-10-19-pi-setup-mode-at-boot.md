# A Pi with no credentials boots into setup mode

- **Date:** 2026-10-19 (agent, loop #292; the user's report from the lab Pi 4)

`jarvis-agent run --profile pi` with no `node.json` (after a factory reset, or installed without
enrolling by code) no longer exits 78 until systemd gives up. Run by systemd (not typed in a
terminal), it enters setup mode in the same process, as N:run_provisioning.py does at boot:

- it scans, starts the `jarvis-<id8>` hotspot on wlan0 (NetworkManager) and serves the
  provisioning API on :8080, the same code as `jarvis-agent provision --wifi networkmanager`;
- it answers the phones' connectivity checks on :80, so they open the setup sheet. The unit
  grants `AmbientCapabilities=CAP_NET_BIND_SERVICE` for that; a Pi installed before this has
  no capability until `install-pi.sh` runs again, and setup mode then logs it and goes on
  without the captive listener (the app reaches :8080 either way);
- with the HAT, the LEDs show `setup`, a slow blue breath (3 s) no other state uses. N:'s LED
  service has no provisioning pattern, so this one is the Go node's own; it is also in
  `jarvis-agent hat cycle`. Without the HAT there is nothing to show;
- once the app has provisioned it, the HAT is handed back and `run` goes on as the node. An
  un-enrolled Pi stays in setup mode however long it takes (no AP↔STA cycle: it has no network
  to go back to).

The factory reset still keeps NetworkManager's station profiles, as decided in
[2026-10-19-pi-node-factory-reset.md](2026-10-19-pi-node-factory-reset.md) (N: keeps them too).
The Pi may rejoin the home Wi-Fi at boot; setup mode then scans and takes wlan0 for the hotspot,
and NetworkManager doesn't autoconnect over an active connection, and the net watchdog leaves
wlan0 alone while `jarvis-setup-ap` is up.
