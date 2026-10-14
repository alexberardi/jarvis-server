# Voice turns use the agent's client tools

- **Date:** 2026-10-10
- **Moved from** the STATUS.md decision log on 2026-10-14.

**Voice turns use the agent's client tools** ([agent/go-pi-node.md §3.8](../agent/go-pi-node.md)). One `node.Multi` (desktop + sidecar) serves both MQTT `tool_call` and voice; offered at `/conversation/start`, run locally on a 202 `tool_calls`, results posted with the **blocking** `/voice/command/continue` (not Python's stream-first: jarvisd's stream continue can't chain, and window tools need list → act), ≤ 10 rounds, Python's repeat guard and "every tool failed → speak the error". No confirmation step for voice, same as the MQTT path and Python (only `desktop_close_window` is risky, and it's a graceful close).
