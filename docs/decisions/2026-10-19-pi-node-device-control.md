# The Go node's action, callback and toggle_command verbs

- **Date:** 2026-10-19 (build agent, loop #177; for review)

Choices made porting N:'s `handle_action`, `handle_callback` and `handle_toggle_command` to the
Go node ([agent/go-pi-node.md §3.18](../agent/go-pi-node.md)):

- **Every profile, not only the Pi.** jarvisd sends `action` to computers too (a desktop
  command's buttons, `S:commands.go`, behind `canUseDesktop`) and `callback` to whichever node
  owns an inbox item's command; a computer's Pantry packages have `handle_action` and `@callback`
  like a Pi's. Until now a computer ignored both and the app timed out. With no device families,
  a computer answers `control_device` with "Device protocol '…' not found" at once instead of
  timing out. `toggle_command` writes the same switch as the `command_registry` push, which every
  profile already honours.
- **Always verify an `action`.** jarvisd never sends `trusted` (D4/D7, D48), so the Go node has no
  unverified path; N:'s `trusted` skip is not ported. A `callback`'s verification is its payload
  fetch (`GET /callbacks/{job}`), as on N:.
- **The sidecar does what N: does in Python**, with N:'s quirks kept so Pantry families work
  unchanged: `control()` gets the `DiscoveredDevice` where the SDK's signature says `ip`, and the
  whole action context as `data` (both of N:'s control paths do this). A family is looked up
  configured or not (N: falls back to the snapshot's families). Control runs on one persistent
  event loop per sidecar, as N:'s `_device_protocol_loop`, so a pairing session (pyatv's)
  survives `pair_start` → `pair_finish`. Scans and state still use a fresh loop per request.
  A new sidecar (a package install) starts a new loop, so a pairing in progress across an
  install is lost, as across an N: restart.
- **A command turned off is absent** for `action` and `callback` (N:'s discovery leaves it out).
  The switch is by name, as on N:: `toggle_command` with `enabled: false` (jarvisd sends it when
  `use_external_devices` is turned on) hides any command named `control_device`, and the app's
  control then goes to the family path. The Go sidecar doesn't load N:'s built-in
  `control_device` (§7 5.5), so on a Go node a `control_device` command can only come from a
  Pantry package.
- **Differences from N::** a callback's `RequestInformation` has no `home_context` (the sidecar's
  tool calls don't pass one either); a sidecar that is down or crashed answers `success: false`
  with its error rather than nothing.
- **Not in this item:** voice control of devices (the `control_device` tool that resolves a name
  through the registry of §3.17) and the `routine` verb, which is how jarvisd runs a routine's
  device steps on a node.
