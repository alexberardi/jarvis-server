# Desktop agents update to their server's version, not GitHub's latest

- **Date:** 2026-10-14 (Claude, building install plan item 1.7; the user preferred this when
  asking for it)

The desktop agent's self-update installs the jarvis-server release whose tag is the version of
the jarvisd it is signed in to. cc's heartbeat answer to a desktop node carries that version
(`server_version`), and an update a member requests for a desktop node ("latest") means the same
version. The agent never asks GitHub which release is newest.

**Why.** A household's agents then always match its server: the pair a release was tested as,
with the same wire contract (tool calls, voice routes, node settings). An admin who holds jarvisd
back, or rolls it back, holds the agents back with it, and an agent can't run ahead of a server
that doesn't know its new routes. It is also how Pi nodes are updated (the heartbeat is the
dispatch channel), so the app's per-node update request works for computers too. The cost: a
fix that only touches the agent reaches users with the next jarvisd release, which ships both
anyway (one release, one `SHA256SUMS`).

The alternative, checking GitHub releases directly, was rejected for the mismatch it allows
(newer agents against an older server) and because it would need its own prerelease policy.

A development jarvisd has no release to follow, and a development agent never updates itself
unless forced (`jarvis-agent update --force --version vX`). See
[agent/DESIGN.md §13 "Self-update"](../agent/DESIGN.md) and
[install/01-no-cli-plan.md](../install/01-no-cli-plan.md) item 1.7.
