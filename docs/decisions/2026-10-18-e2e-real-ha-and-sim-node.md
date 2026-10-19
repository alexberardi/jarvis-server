# E2E: a simulated node for pairing, and a real Home Assistant (split out)

- **Date:** 2026-10-18 (user asked for real outside systems in the E2E regression, 2026-10-17;
  design by agent, loop item #154, piece 7/12)

The E2E suites should use the real thing wherever it can run on the dev box, not a fake
(`../dev/e2e.md`). This piece covers two outside systems: a node pairing with the server, and
Home Assistant.

## Pairing: the real agent, headless

`scripts/sandbox node-sim` runs the real `jarvis-agent` (built from this checkout) as a Pi node:
`enroll --code --profile pi`, then `run --voice=false --no-auto-update`. No new "fake node" is
written: the enrollment, the join question, registration, MQTT and heartbeats are the shipped
code paths.

- The Pi profile, because it is headless (no tray, island or browser setup) and is never retired
  by a desktop's re-enrolment (`device_id` replacement only touches desktop nodes).
- The code comes from the agent's stdout, the only place it exists (cc never lists codes, loop
  #100). The script parses it and prints it; it also writes it to a file for Maestro.
- The join question is answered "y" on stdin; the agent reads it only after the approval.
- The agent gets its own HOME and XDG dirs and no display or session bus, so it never touches
  the user's files or desktop agent. It stops with the sandbox (`scripts/sandbox down`).
- The web suite approves on the superuser's Nodes page (`e2e/web/tests/pairing.spec.ts`); the
  mobile app's approval is a Maestro flow in jarvis-node-mobile once its runner exists (#144).

## Home Assistant: its own item

Not built in this piece, for two reasons:

1. The build sandbox can't reach Docker (`/var/run/docker.sock`: permission denied), so the
   container, its pinned image and the onboarding script can't be run or checked.
2. On the server, device scan, device list and control go through a node over MQTT
   (`internal/modules/cc/smarthome_control.go`), and the Go node can't talk to Home Assistant
   yet (`../agent/go-pi-node.md`: device-list, scan and state are "later"; #176/#177). Only the
   Python node (jarvis-node-setup) does today. So "control a device and assert Home Assistant's
   state changed" would need that Python node in the sandbox at a pinned ref (a cross-repo
   dependency), or has to wait for the Go node.

The user's call (2026-10-18): ship the pairing half here and build Home Assistant as its own
item, with the Docker socket allowed for that build and no Python node: the control test is
skipped, pointing at #176/#177, and turned on once the Go node handles Home Assistant. The plan
for it: `scripts/sandbox up --ha` runs the official image pinned by
digest on `8123 + offset`, data in `.sandbox/<name>/ha/`, with `demo:` and template entities
(lights, a lock, a garage door, a thermostat, a sensor) under fixed entity ids; onboarding by
script (user, auth code exchange, a long-lived token over the websocket); `SBX_HA_URL` (the
host's LAN address: cc refuses loopback for local providers) and `SBX_HA_TOKEN` in
`sandbox.env`. That item adds its own section here when it lands.
