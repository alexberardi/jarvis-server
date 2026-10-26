# Package agents on the Go Pi node: scheduled in Go, run in the sidecar

- **Date:** 2026-10-23 (agent, loop #583; the gap from the user's QA the same day)

A Pantry package's background agents (`components: type: agent`, `agents/<name>/agent.py`, an
IJarvisAgent: comfyui's `comfyui_jobs`) run on a Pi node's Go agent the way the reminder agent
already did: the sidecar loads them next to the package's commands and runs one on request
(`agent_run`), and Go owns the schedule (`internal/agent/pkgagents`) and the alert queue. A
desktop agent still skips them. See [go-pi-node.md §3.19](../agent/go-pi-node.md), gap (f).

Choices a later reader would otherwise rediscover:

- **Schedule.** Checked every 10 s, as `N:services/agent_scheduler_service.py`. A failed run
  counts as a run and is retried at the agent's interval; the Python node retried on every
  10 s tick, which only hammers a server that is down.
- **Health.** No auto-disable and no per-agent switch (the Python node's `agent_registry`):
  the Go node has no place for the app to turn one back on. An agent whose last 3 runs failed
  marks its package `failed` in `installed_packages` until a run succeeds; an agent that
  doesn't load fails its package as a command does (the install is undone).
- **One request at a time.** A run holds the sidecar, so a voice command waits for it (comfyui's
  tick with no jobs makes no request; a download is seconds on the LAN). Running agents in a
  second sidecar is the fix if one proves slow.
- **Inbox attachments.** The sidecar's inbox backend now takes `attachments` (SDK 0.13.0). The
  file crosses to Go as a path (the package's own, or a temp file the sidecar removes after the
  post), never as bytes on the pipe; Go uploads each to `/node/inbox-attachment` (2-minute
  limit) and posts the item with their ids, with `N:services/inbox_backend.py`'s tags for 413,
  415 and 422.
- **Not ported:** the agents' context data in voice requests (`get_context_data`) and the
  household home context the Python node seeds before a run; nothing in the ask needed them.
