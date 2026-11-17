# Pantry packages get the Python node's modules on the Go agent

**Date:** 2026-11-16 · **Status:** decided (user: "we need full parity with the previous python
module. that was built intentionally and worked for users")

## Context

Pantry packages were written for the Python node and import its modules: 25 of them across
the 29 published packages (secrets, auth flags, the agent scheduler's context, Config, the
REST client, the domain handlers, timezone utils, direct devices, ...). The Go agent's
sidecar had none, so packages silently fell back or failed: Home Assistant on a desktop
listed devices but could control none (`No module named 'device_families'`), saw no
entities for voice, and lost its re-auth flag.

## Decision

The sidecar provides those modules under their node names (`internal/agent/sidecar/nodecompat/`):

- The node's pure code is copied verbatim from jarvis-node-setup (pinned commit); the node is
  frozen, so drift is not a concern.
- The node's I/O modules are reimplemented over the Go agent, registered by name so they also
  win over a Pi's installed jarvis-node-setup (which reads the Python node's database, not the
  agent's store).
- **The node key stays in Go** (DESIGN.md: "the child never sees the node key"). `RestClient`
  calls go through a new host call, `cc.request`: GET/POST/PUT to command-center's `/api/v0/`,
  signed by the agent. This is wider than `cc.post` (`/api/v0/node/` only) because the node's
  RestClient reached any route a node key may use; jarvisd still decides what that is.
  `Config.get_str("api_key")` reads as unset.
- jarvisd gets `GET /api/v0/node/info` (node auth): the node's id, room, household and the
  household's `home_context`, which the node kept in config.json.
- Every venv also gets `pytz`, which the node had and its time utilities use.

## Consequences

Packages behave as on the node without changes. A package that builds its own request with
the node key (Home Assistant's room hierarchy for floor/area commands) gets no key and loses
that call until it uses `RestClient`. New node imports in packages need a module here and a
case in `TestNodeModulesForPackages`.
