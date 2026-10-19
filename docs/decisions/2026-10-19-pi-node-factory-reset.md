# The Go Pi node's factory reset, routines sync and context requests

- **Date:** 2026-10-19 (build agent, loop #173; for review)

Choices made porting `factory-reset`, `routines/sync` and `context/{op}` to the Go Pi node
([agent/go-pi-node.md §7](../agent/go-pi-node.md) parity 7):

- **Same flow as N:, same anti-spoof check.** The tracked task reports `in_progress` with the
  `X-Reset-Token` header before anything is wiped; jarvisd refusing it (a token it never issued,
  an expired one, a task id it doesn't match) or not answering aborts the reset. No task id: the
  legacy `verify-reset` must answer 200 first. The status calls send the token only, not the
  node's key, as N: does. A message for another node, or without `request_id`/`node_id`, is
  ignored, and a redelivery while a reset runs is dropped.
- **What is wiped** (`reset.Wipe`, over the directory of node.json): `k2.json`,
  `node-settings.json` (command secrets, command data, the household's on/off switches: N:'s node
  DB and its key), `timers.json`, `bluetooth.json`, the Pantry packages (`packages/pkgs`, with the
  versions revert would restore, and `packages/commands.json`), and `node.json` last: it is the
  node's credentials and its provisioning marker, so a wipe that fails part-way leaves a node that
  still knows it is enrolled. **Kept:** the binary and unit, `agent.json` (the node's settings, as
  N: keeps config.json), `volume.json`, the self-update records, and the packages' venv with the
  built-in commands (the node's runtime; reinstalling it would need the network at first boot).
  N:'s Wi-Fi credentials file has no Go counterpart: the Wi-Fi profile lives in NetworkManager, and
  N: doesn't remove that either.
- **Nothing writes back after the wipe.** Once jarvisd confirms the reset, and before the wipe,
  the agent seals its settings store and its timers (`Store.Seal`, `timers.Service.Seal`): a
  config push, a command saving data or a timer change before the reboot then fails without
  writing, so `node-settings.json`, `k2.json` and `timers.json` stay gone. A wipe that fails
  leaves them sealed until the agent restarts.
- **The provisioning API's `POST /api/v1/factory-reset` runs the same wipe** (it answered "not
  available on this node yet" before). It is reachable only while `jarvis-agent provision` serves
  setup mode, unauthenticated as N:'s is.
- **Reboot, then quit if that fails.** `sudo -n systemctl reboot` (allowed by install-pi.sh's
  sudoers); if it fails the agent ends its run and systemd starts it again, N:'s fallback.
  **Gap:** a Go node without credentials doesn't enter setup mode at boot yet (4.4 "starting setup
  mode at boot", after #170's NetworkManager hotspot), so until that lands the reset leaves the
  node wiped but not serving the provisioning hotspot by itself.
- **Routines** pull on the nudge and on every MQTT connect (N: pulls at boot; a reconnect also
  catches a nudge sent while the node was away). The four default slugs (N:'s
  `RoutineCommand._default_routines`) and the routine components of installed packages are never
  pruned. The routine command itself is still skipped (4.5), so the records are a cache for now.
- **Context requests** are answered from the sidecar's commands: `describe` now lists each
  command's `context_operations`, and a `context_query` round trip runs one as the requesting user.
  Checks that need no Python (operation and params shape, provider lookup, required params) run in
  Go with N:'s messages and `no_provider` code. A command the household turned off is not a
  provider. Answers over 128 KiB become "context result too large".
- **Open for the user (access, not changed here):** jarvisd's broker ACL (`CanPublish`,
  `internal/platform/mqtt/auth.go`) lets a node publish `context/query/response/{cid}` but not
  `context/operations/response/{cid}`, so the node's `operations` answers are dropped. Nothing in
  jarvisd sends either request yet. Widening the ACL is an access change and waits for the user.
