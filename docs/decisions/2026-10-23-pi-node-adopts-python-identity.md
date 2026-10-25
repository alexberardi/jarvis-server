# The Go Pi node adopts the Python node's identity (migrate, don't re-enroll)

- **Date:** 2026-10-23, user (built 2026-10-24 in loop #586).

[go-pi-node.md](../agent/go-pi-node.md)'s Retirement proposal step 2 left a choice: build a
migration, or swap by "enroll a new node, move the room, delete the old node". The user chose
**migrate**. On a Pi whose Python node (jarvis-node-setup) is provisioned and that has no
`~/.config/jarvis-agent/node.json`, the Go node takes over that node's id and key, config-service
URL and room, so jarvisd keeps the same node with its room, settings, devices, history and app
pairing; nothing mints a second node.

- **Where:** `jarvis-agent adopt-python`, which `install-pi.sh --activate` runs before it swaps
  the nodes; the agent also tries it at start on a Pi with no node.json. An existing node.json
  always wins. A failed adoption refuses `--activate` rather than fall back to setup mode.
- **Local state:** carried over once, from `jarvis_node.db` (SQLCipher) and the secret dir:
  command secrets, disabled commands, command data (reminders), timers and K2. Go has no SQLCipher
  driver and a cgo one would break the `CGO_ENABLED=0` cross builds, so the DB is read by a small
  exporter (`internal/agent/pinode/pyexport.py`) run with the Python node's own venv, which has
  sqlcipher3 and cryptography, and the agent imports its JSON.
- **Leave the Python node intact:** its `.provisioned` marker, secret dir, `config.json` and DB
  are only read, never moved or deleted, so `install-pi.sh --keep-python` (an alias of
  `--deactivate`) brings it back unchanged.
- **MQTT credentials** are not copied: the Go node fetches them from jarvisd at run time.
