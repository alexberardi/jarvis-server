# The Pi node's Wi-Fi: NetworkManager over D-Bus, and how it differs from N:'s nmcli

- **Date:** 2026-10-18 (agent, loop #170; implements the user's §8 Q7 answer)

`provisioning.NetworkManager` implements the provisioning `WiFi` interface over the system
D-Bus (godbus, no cgo), following [go-pi-node.md §8 Q7](../agent/go-pi-node.md). It ports
N:provisioning/wifi_manager.py's NetworkManager path; where it differs, on purpose:

- **The PSK travels in the D-Bus settings map** of `AddAndActivateConnection`, not through
  nmcli. N: had to keep it off every argv (sudo's journal line, `/proc`) with a 0600
  passwd-file; D-Bus messages have neither leak, so the file dance is gone. NM stores the secret
  system-owned (`psk-flags` 0) in its root-only keyfile, as before.
- **The commit re-sends the profile it added** (with the PSK) through `Update`, because `Update`
  replaces the whole profile and `GetSettings` returns no secrets.
- **The hotspot is open, on 192.168.4.1, channel 6 (bg)**, as N:'s hostapd backend (the one Pis
  ran), not N:'s nmcli `hotspot` (WPA2 password `jarvis-setup`, NM's 10.42.0.1): the app and the
  captive DNS expect the hostapd setup. It is NM's AP mode with `ipv4.method shared` and a
  **volatile** profile (`jarvis-setup-ap`), so NM forgets it when it goes down. The captive DNS
  (`address=/#/192.168.4.1`) moves from N:'s own dnsmasq config to
  `/etc/NetworkManager/dnsmasq-shared.d/jarvis-captive.conf`, written by `install-pi.sh`.
- **Waiting is polling**, not signals: the active connection's `State` every 250 ms until
  ACTIVATED, or DEACTIVATED / gone (a failure, with the device's `StateReason` in the error, e.g.
  "secrets required (wrong password?)"), or 45 s (N:'s `nmcli up` timeout). Simpler to fake
  and to reason about than a signal subscription, for a few calls per provisioning.
- **Security strings** follow nmcli's SECURITY column, which N: passed through (`WPA2`,
  `WPA2 WPA3`, `WPA1 WPA2`, …), except that an open network says `OPEN` (nmcli printed nothing).
  Signal is N:'s percentage conversion (-90 + 0.6 × %). Duplicates keep the strongest.
- **Permissions:** the node runs as its user, not root, and sudoers can't help a D-Bus call, so
  `install-pi.sh` adds a polkit rule giving that user NetworkManager's actions
  (`/etc/polkit-1/rules.d/50-jarvis-agent-networkmanager.rules`). This replaces the sudoers
  `nmcli` entry N: needed. It lets the node's user change any NetworkManager setting; narrowing it
  to the Wi-Fi actions can follow once the on-device test shows which ones are used.
- **The net watchdog leaves the hotspot alone.** The watchdog (N:'s byte for byte until now)
  counts no gateway as a LAN failure and, from the third minute, forces the preferred profile up,
  which would tear NM's hotspot down long before the recovery cycle's 10 minutes (with hostapd,
  NM was stopped and those calls failed). It now exits early while `jarvis-setup-ap` is active.

The recovery cycle (`provisioning.Recovery`) is N:recovery_watcher.py's, with the same knobs,
defaults and floors; "a pairing attempt is in progress" is the state machine's `Busy`
(CONNECTING or REGISTERING), as in N:. It needs `StopAPMode` and `CurrentSSID`, now part of the
`WiFi` interface. hostapd stays unbuilt: per Q7 it comes only if NM's AP mode fails on a chip.
