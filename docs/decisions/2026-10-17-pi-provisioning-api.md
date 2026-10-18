# The Go Pi node's provisioning API

- **Date:** 2026-10-17 (loop #169, agent; no user answer needed)

`internal/agent/provisioning` ports the Python node's setup API (`N:provisioning/api.py`,
`state_machine.py`, `registration.py`) and `jarvis-agent provision` serves it on `:8080`. The bodies
are golden-tested against `models.py` (`tools/golden/export_provisioning.py`), the routes, messages,
state machine, the one-attempt lock (a second provision answers 200 `success: false` "Provisioning
already in progress", which the app matches), the rollback + hotspot restore and the registration
retry policy (connect errors 8 tries, 5xx 3, 4xx none) are the Python node's. Where it differs on purpose:

- **Wi-Fi is an interface** (`provisioning.WiFi`). Only `--wifi simulated` exists; the
  NetworkManager D-Bus backend ([go-pi-node.md §8 Q7](../agent/go-pi-node.md)) is a separate item.
  `save_wifi_credentials` (the Python node's encrypted copy for its recovery watcher) is not ported:
  the NM profile that `CommitConnection` keeps holds them.
- **Where things land:** the credentials go to the agent's `node.json` (what `run` reads), K2 to the
  node settings store beside it under the CC-assigned node id the app sends (the same id the node
  then registers as, so `run` keeps it). There is no `.provisioned` marker or `config.json`: the node
  counts as provisioned once `node.json` loads, which also refuses K2 and sets
  `previously_provisioned`, as the marker does in Python. No config-write step is wired (the
  `SaveConfig` hook is there).
- **config_service_url absent:** `node.json` needs the config URL, so the node assumes the command
  center's host on port 7700. The app sends it whenever it knows one.
- **hardware:** a 64-bit Pi reports `pi-zero-w`/`raspberry-pi` (Python's `"arm" in machine`
  misses `aarch64` and says `linux-aarch64`); other machines say `linux-x86_64` and so on, as Python.
- **firmware_version** is the agent's build version, not Python's fixed `"1.0.0"`.
- **422 bodies** keep FastAPI's shape (`{"detail": [{"loc": ["body", field], "msg": …}]}`) but not
  every pydantic key; a refusal's detail is CC's `detail` field only.
- **factory-reset** answers its shape and calls an injected reset; the command doesn't wire one yet
  (go-pi-node §7 5.7 owns the reset itself), so it answers `success: false`.
- **Port 80** (the phones' connectivity checks) is opt-in, `--captive-listen :80`, since the agent
  runs as a user without that port.

The API is unauthenticated, as in Python; it only runs while `jarvis-agent provision` does, which a
Pi in setup mode starts.
