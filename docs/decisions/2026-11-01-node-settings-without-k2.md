# Ordinary node settings without K2

- **Date:** 2026-11-01, user approved implementation and explicitly prohibited moving K2 into admin.

The admin panel can edit ordinary voice and supported Pi hardware preferences.
Desktop Settings also offers local desktop preferences while jarvisd is offline
or the agent is stopped. Neither interface needs K2. This is a narrow exception
to the blanket "no web settings view" statement in [agent design §16](../agent/DESIGN.md).
Encrypted command/package settings and their mobile workflows remain unchanged.

Use a scalar allowlist on both server and agent. The server constructs the schema
and filters the device's reply; it never returns an encrypted snapshot, package
secrets, credentials, broker settings or service URLs. Remote edits use existing
household membership checks and verified MQTT commands. The local page retains
its loopback listener, per-launch URL token and same-origin write protection.

Updated Go agents acknowledge persistence and report their current values.
Older Python Pi nodes can receive explicitly labeled, unconfirmed updates through
their existing command; the UI leaves unknown values blank. It does not invent
readback support or defaults. No Python runtime changes are required for this
compatibility mode.

Save only changed keys, serialize settings writes across processes, and apply
voice tuning between turns. Explicit command-line overrides are read-only.
Startup and local stopped edits share a short-lived lock so a successful save
cannot fall between startup's settings load and control-socket availability.
Local desktop options that need a restart are marked and offer an explicit restart.

The new admin route registrations trigger the repository's user-review merge
hold. See the [implementation specification](../agent/NODE-SETTINGS.md).
