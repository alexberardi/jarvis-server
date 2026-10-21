# One API port; the legacy ports behind `server.legacy_ports`

- **Date:** 2026-10-20 (user, approving plan P4 "jarvisd on Unraid"; built in item #362)

Every module is served on the config port (7700) under its own path, and a fresh install
listens only on 7700 (API), 7710 (admin) and 1884 (MQTT). This changes
[PLAN §3.1](../PLAN.md), which kept
every legacy port open.

**The prefixes.** Each module keeps its own mux, mounted on 7700 with its prefix stripped, so
no route moves:

| listener | registry name | prefix on 7700 |
|---|---|---|
| auth | jarvis-auth | `/auth` |
| logs | jarvis-logs | `/logs` |
| command-center | jarvis-command-center | `/cc` |
| llm | jarvis-llm-proxy-api | `/llm` |
| whisper | jarvis-whisper-api | `/stt` |
| tts | jarvis-tts | `/tts` |
| notifications | jarvis-notifications | `/notifications` |
| recipes | jarvis-recipes-server | `/recipes` |
| ocr | jarvis-ocr-service | `/ocr` |

The mounts are there either way; the switch only decides whether the old ports open too.
`/info`, `/health`, `/services` and `/settings` stay at the root of 7700. The admin (7710) and
the MQTT broker (1884) keep their own ports.

**The switch.** The config module's `server.legacy_ports` setting (Bool, restart to apply),
overridden by `JARVIS_LEGACY_PORTS` (`1`/`true`, `0`/`false`; any other value fails startup).
jarvisd reads it once, after migrations and before modules register. On: every legacy port
binds as before. Off: only 7700, 7710 and 1884 (plus the engines' loopback ports).

**The upgrade rule.** When the setting has no row yet, jarvisd decides once and stores the
answer: **on** when `config_services` already holds rows an earlier jarvisd synced
(`description = 'served by jarvisd'`), i.e. an upgrade whose clients know the old ports, and
**off** on a fresh database. An upgraded install changes nothing until its admin turns the
setting off.

**9883 follows the switch.** The MQTT WebSocket isn't used by LAN clients, but prod publishes
the broker as `wss://mqtt.jarvisautomation.io` through its tunnel and the dockerized demo node
connects over it. So it opens with legacy ports on (prod is an upgrade) and stays closed on
fresh installs. `JARVIS_MQTT_WS_ADDR` still forces it either way (`""` disables it).

**`/services` follows the switch.** Off: jarvisd's own rows are `localhost:7700` plus the path,
and each entry's `url` carries it (`http://<host>:7700/cc`, no trailing slash, plus a `path`
field). On: exactly the old rows, so an upgraded install answers what its clients already
know. Every client builds requests by concatenating onto `url`, so a path survives.

`jarvisd doctor` reads the stored decision without migrating (no database or no row reads as
off) and checks only the ports that bind. The harness (`scripts/sandbox`) keeps the legacy
ports unless `--single-port` is given.
