# The Go Pi node's devices: families in the sidecar, MQTT and the registry in Go

- **Date:** 2026-10-19 (build agent, loop #176; for review)

Choices made porting device scan, list and state, `invalidate_device_cache`, `device_removed`
and `jarvis/auth/+/ready` to the Go Pi node ([agent/go-pi-node.md §3.17](../agent/go-pi-node.md)):

- **Device families stay Python, in the package sidecar.** They are Pantry packages
  (`device_families/<name>/protocol.py`, an SDK `IJarvisDeviceProtocol`; managers in
  `device_managers/<name>/manager.py`), so the sidecar loads them next to the packages' commands
  and answers `device_scan`, `device_list`, `device_state`, `device_removed`, `store_auth` and
  `device_families`. Go owns MQTT, the uploads to jarvisd and the registry
  (`internal/agent/devices`), as Bluetooth does. Only a Pi lists the families for the sidecar;
  a desktop still skips them (and refuses a package with nothing else).
- **N:'s domain handlers are ported into the sidecar**, not imported. jarvis-node-setup's
  pyproject doesn't package `device_families/` (the light/climate/cover/... state normalisation
  and UI hints) or `device_managers/`, and those modules import the Python node's logger and
  config. The handlers are pure functions (~150 lines), copied with their Python rounding, and
  the sidecar's tests pin their output. The alternative, a jarvis-node-setup PR that packages them
  and moves the pin, is cross-repo; if it lands, the sidecar can import them instead.
  `jarvis_direct` (N:'s built-in manager: the families' devices as manager devices) is the
  sidecar's too.
- **Same shapes and flow as N:,** with these differences:
  - A scan gives a second device with an `entity_id` already taken a `_<protocol>` suffix (N:'s
    `DeviceScannerService` does; its scan handler doesn't, so an import kept only one of them).
  - A family's or manager's secrets are checked on every request, not once at discovery: a token
    set from the app or by OAuth takes effect without a restart.
  - A Home Assistant device (`source` not `direct`) has no state on this node: N: asks HA
    through `ha_shared`, which the Go node doesn't have. jarvisd gets N:'s "Failed to query device
    state", as from a Python node without HA.
  - The registry (`GET /api/v0/node/devices`, direct devices only, at start and every 5 minutes,
    dropped on `invalidate_device_cache` and after `device_removed`) is replaced on each fetch, so
    a deleted device leaves it; N:'s merged. It is for device control (#177); the Go node has no
    background-agent context yet, so N:'s `device_agent` context isn't sent.
  - OAuth's `store_auth` tries the commands first and then the families, as N: does, so a Pi
    command with OAuth (a Pantry package's) also gets its tokens.
- **The heartbeat carries `protocols` on a Pi** (the configured families, `[]` for none, so a
  node that ran the Python node stops claiming its families), and beats again when the sidecar
  comes up and after OAuth stores a family's token, so jarvisd's `pickNode` finds it at once.
- **The settings snapshot** lists the families and managers in N:'s shape; their secrets' values
  are the agent's store's (a manager's sensitive values never, as N:).
- **One sidecar request at a time.** A scan waits for every family's discover (10 s each, in
  parallel), and a command call in the meantime waits behind it, as any sidecar request does.
  The topics run off the MQTT goroutine and a redelivered request runs once.
