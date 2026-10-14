# Home state reaches watches as node signals

- **Date:** 2026-10-14 (user)

Home-state watches are event-driven: nodes publish device-state changes as `device.state` signals (`source_key='device:<node>:<entity>'`, the facts `{state, attributes}`, a TTL). There is no server-side polling of node tools or Home Assistant. This needs changes in Python node-setup, the Go node and the device-manager packages (EXTERNAL-CHANGES when P4 lands). Plan: [autonomy/PLAN.md](../autonomy/PLAN.md) §2.
