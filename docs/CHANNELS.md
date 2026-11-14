# Channels: talk to Jarvis from a chat service, through a package

Status: **spec in progress, 2026-11-13.** Nothing here is built. Questions are asked one at a time;
the queue is at the end. It replaces the shape of contributor PR #649 (a Discord bridge inside
`cc`, by opticon454), whose rules and tests are the starting point for the core side.

## Goal (user, 2026-11-13)

People reach Jarvis from a chat service they already use, from anywhere, with no VPN. "I don't want
discord specific code in the core product. What we need to do is generalize this entire approach so
it could hopefully work across multiple services like slack, telegram, discord, whatever."

## Intent (confirmed by the user, 2026-11-13)

- **Outcome:** household members message Jarvis from a chat service, starting with Discord, at
  parity with #649: direct messages and one channel, commands, voice clips, photos, opt-in
  reminders. A household installs the service's package, enters the bot's credentials in the app,
  and each member links their account.
- **Who:** other people's households more than the user's own (his install is public).
- **Why now:** a contributor built it as Discord code inside `cc`; that shape will not be merged.
- **Success:** Discord and Slack both work as packages against the user's test bot and Slack
  workspace, on a desktop node and a Pi, and adding Slack needed no change to jarvisd or the Go
  agent.
- **Constraint:** no service's name, client library or limits in core. Generic building blocks
  are fine when useful on their own: `IJarvisDaemon`, agents on desktop nodes, an Opus package,
  the channels contract ([decision](decisions/2026-11-13-channels-are-packages.md)).
- **Who builds it:** this project, all of it, using parts of #649 and jarvis-node-mobile#158 with
  credit to their author. Both PRs are closed with an explanation once the replacement exists.
- **Out of scope:** live voice in the first release (a follow-up inside the Discord package);
  failover between nodes; a household with no node; Go adapters compiled into jarvisd.

## What exists (survey, origin/main @ f412ad54)

- **`IJarvisAgent`** (the SDK repo's `jarvis_command_sdk/agent.py`): `run()` on an interval. The Go agent owns the
  schedule (`internal/agent/pkgagents`, a check every 10 s) and the sidecar runs one
  (`agent_run`). The sidecar answers one request at a time, so a run blocks every other package
  call on that node. Nothing can hold a connection.
- **Pi only.** `cmd/jarvis-agent/main.go` runs a desktop's scheduler with `TestsOnly: true`, and
  the sidecar gets `--backends` (the SDK's `cc`, inbox, alerts, llm backends) only on the Pi
  profile. On a desktop an agent's `cc_post` does nothing.
- **Secrets** a package declares (`required_secrets`) are entered in the app and read through
  storage on both profiles.
- **PR #649:** `cc/discord/bridge.go` imports no Discord library; its rules (linked accounts only,
  reply where asked, a conversation per person and place, opt-in reminders) are service-neutral.
  `cc.Module.ChatText` runs a chat turn without the SSE stream. Discord-only: the gateway, slash
  command registration, the attachment hosts, live voice (`dave-go`). Its Opus and Ogg code
  (`opusnative`, `clip`) is generic.

## Decided

- **A channel is a package, not a core module** (user, 2026-11-13). Go adapters compiled into
  jarvisd were rejected: the service's code and dependencies would still ship in the core binary.
  [Decision](decisions/2026-11-13-channels-are-packages.md).
- **`IJarvisDaemon`, a new SDK kind** (user, 2026-11-13): started once, kept running, supervised by
  the Go agent. Discord and Slack deliver messages only over a connection the bot holds open.
- **Desktop nodes run agents and daemons too** (user, 2026-11-13), bundled with this work.
- **One host node per daemon** (user, 2026-11-13): the node the package was installed on. A second
  install of the same daemon package in the household is refused. No failover in this version.
- **Core decodes voice clips** (user, 2026-11-13): libopus is acceptable in core as a generic,
  built-in audio package (`internal/audio/`, loaded with purego, naming no service), not as part of
  a channel. #649's `opusnative` and Ogg code are the starting point. A package hands core the
  clip's bytes and content type. It must load on every install target, not only the container
  image; where it is missing, only clips say so.
- **Live voice is a follow-up** (user, 2026-11-13), inside the Discord package. The contract leaves
  room: a daemon will need "transcribe this audio" and "speak this text" from core.
- **The inbound turn is one blocking request** (chosen here): the daemon posts a message and gets
  the reply. The package shows the service's own typing indicator while it waits. A stream is added
  only if live voice or partial replies need it.

## Capability map

| Module | What | Repo | Needs |
|---|---|---|---|
| `desktop-agents` | A desktop node runs its installed packages' agents; its sidecar has the backends they post through. | jarvis-server (agent) | — |
| `daemon` | `IJarvisDaemon` in the SDK; the Go agent runs each in its own sidecar process and restarts it. Both profiles. | jarvis-command-sdk, jarvis-server (agent) | `desktop-agents` (the backends) |
| `channels-core` | Links, link codes, the inbound turn, the reminder outbox, the app's routes. No service named. | jarvis-server (cc) | — |
| `channel-sdk` | A helper over `cc_post` so a package is its client library plus message mapping. | jarvis-command-sdk | `channels-core` |
| `channel-discord` | The Discord package: a daemon using the helper. | new `jarvis-channel-discord` | `daemon`, `channel-sdk` |
| `channel-slack` | The second service, to prove the contract (Socket Mode, a held connection like Discord's). | new `jarvis-channel-slack` | same |
| `mobile` | One channels card: status, link code, my preferences, per installed channel (the member half of #158; its bot half becomes the package's secrets). | jarvis-node-mobile | `channels-core` |

`desktop-agents` and `daemon` are useful without channels and land first.

## Spec: `desktop-agents`

- The desktop profile's sidecar starts with the backends an agent needs: `cc`, inbox and alerts
  (storage is there already). Backends are constructible on any OS (`internal/agent/CLAUDE.md`).
- The desktop scheduler drops `TestsOnly`; `AgentHealth` reports a failing package as on a Pi.
- Success: a package agent installed on a desktop node runs on its schedule and its inbox item
  arrives; a Forge test install still works. Verify with the sidecar test packages (`watcher`) on
  the desktop profile and a live install on this box.

## Spec: `daemon`

```python
class IJarvisDaemon(ABC):
    name: str
    description: str
    required_secrets: List[IJarvisSecret]

    async def run(self) -> None:
        """Runs until cancelled. Returning or raising means it died."""
```

- A package lists daemons in its manifest as it lists agents (`daemons/<name>/daemon.py`).
- **Own process.** The Go agent starts one sidecar process per daemon, with the same backends, so
  a daemon never blocks the node's package calls.
- **Supervised.** Restarted with backoff when `run()` returns, raises or the process exits; after
  repeated failures its package is reported failing, as an agent's is.
- **Lifecycle.** Started at agent start and on install, stopped on uninstall and agent stop,
  restarted when its secrets change.
- **One host.** jarvisd refuses to install a package with a daemon on a second node of the
  household.
- Not in this version: health checks, schedules, context data, failover.
- Success: a test daemon package stays up, is restarted after a crash, stops on uninstall, and
  picks up a changed secret, on a Pi and a desktop. Clock and backoff are injected in tests.

## Spec: `channels-core`

Ported from #649 with the service names removed.

- **Links:** `cc_channel_links (household_id, provider, external_user_id, user_id, node_id,
  voice_replies, proactive, created_at)`. Link codes as in #649: ten minutes, single use, five
  wrong guesses lock that external account out for ten minutes.
- **Inbound turn** (node-authenticated, from the daemon's host node): provider, external user,
  place, text, attachments. Core resolves the link, runs `ChatText` as that user and returns the
  reply. An unlinked account gets nothing. A conversation is kept per person and place.
- **Commands** core answers for every provider: link, new conversation, node, voice replies,
  reminders on or off, status. How a person types them is the package's.
- **Outbox:** a reminder a node pushes is queued for each channel the user opted into (off by
  default); the daemon drains it. Jarvis starts no other conversation.
- **App routes:** `/api/v0/mobile/household/{id}/channels` and `/channels/{provider}/…` for
  status, link code, my preferences and unlink. The bot's credentials are the package's secrets,
  not household settings.
- **Limits** stay in core: turn timeout, image count and size after shrinking, clip length.
- Success: the route and store tests of #649 pass against the generic names; a second provider
  needs no core change.

## Boundaries

- Always: only linked accounts are answered; replies never mention anyone; attachments are size
  capped; a channel's credentials are write-only in the app.
- Ask first: anything that lets Jarvis start a conversation beyond opted-in reminders; any new
  dependency in jarvisd.
- Never: a service's name, client library or limits in jarvisd or the Go agent; a new inbound
  port or public endpoint.

## Questions

None open. Voice clips, the inbound request, the two PRs and node-less households are answered
above. `channels-core`'s own questions (the route's exact shape, how the outbox is drained) are
asked when its plan is written.

## Plan: `desktop-agents` and `daemon`

Each task is one PR. `channels-core` and the packages are planned after D3.

- [x] **A1 — a desktop node has the SDK backends.** The desktop profile builds an `sdkhost.Host`
  with what it has (alerts and the command-centre client; no timers or audio, whose calls answer
  with the SDK's safe default as a nil part does today) and its sidecar starts with `--backends`.
  - Acceptance: on a desktop, a package's `cc_post` and inbox post reach jarvisd as the node.
  - Verify: `internal/agent/sidecar` tests on the desktop profile; `scripts/sandbox node-sim`.
  - Built: the desktop serves the families that go to command-center (`llm`, `cc`, `inbox`,
    `signals`) and `alerts`; the sidecar registers only the families its node serves
    (`--backends=…`), so timers and volume keep the SDK's defaults there.
- [x] **A2 — a desktop node runs its packages' agents.** The scheduler drops `TestsOnly`;
  `AgentHealth` is wired as on a Pi.
  - Acceptance: the `watcher` test package's agent runs on its schedule on a desktop and a Forge
    test install still runs.
  - Verify: `pkgagents` tests (injected clock); a live install on the dev box.
  - Built: every node's sidecar loads agents (`packages.NodeTypes`) and one scheduler runs them;
    `TestsOnly` is gone. A package with only an agent now installs on a computer. Tested with a
    real sidecar and a fake command-center on both backend sets (the agent's inbox post arrives
    as the node). Not yet run from an installed desktop agent against a real jarvisd.
- [x] **D1 — `IJarvisDaemon` in the SDK** (jarvis-command-sdk): the class, its Forge hints, the
  manifest's `daemons/<name>/daemon.py` convention, an SDK release.
  - Verify: the SDK's own tests.
  - Built: SDK 0.17.0 (jarvis-command-sdk#21). The component types are also listed in the
    Pantry and its web app, which nothing keeps in step: `daemon` was added there
    (jarvis-pantry#40 and #41, jarvis-pantry-web#37), and the SDK's and the Pantry's `CLAUDE.md`
    name every place. **The Pantry and its web app still have to be deployed** (`fly deploy`,
    and the cached test base image rebuilt) before a daemon package can be published.
- [x] **D2 — the Go agent runs daemons.** `packages/manifest.go` knows the `daemon` component; a
  supervisor (`internal/agent/pkgdaemons`) starts one sidecar process per daemon with the node's
  backends, restarts it with backoff, stops it on uninstall and agent stop, restarts it on a
  secrets push, and reports its package failing after repeated crashes.
  - Acceptance: the `daemon` success criteria above, on both profiles.
  - Verify: a fake child and an injected clock (no wall-clock waits); test packages for a daemon
    that stays up, one that crashes and one that returns; a live run on the dev box and the lab Pi.
  - Built: `sidecar.StartDaemon` runs one daemon in a Python process of its own; its backend
    requests overlap (a chat turn takes minutes while its connection still needs serving), so a
    daemon calls the SDK's blocking helpers from a thread. Closing its input cancels `run()`; it
    is killed after 5 s. `pkgdaemons.Supervisor` restarts an ended daemon after 5 s, doubling to
    5 min, reports its package failing after three ends in a row, restarts it on new code, and
    stops it when its package goes. The commands sidecar describes a daemon's settings, so the
    app lists them with the agents' (marked `daemon`; the app still words it as an agent with an
    interval, to fix with the mobile card).
  - Not built: daemons in a Forge test install. Shortcut: any secrets push restarts every
    daemon, not only the one whose secret changed.
  - Not verified: on a Pi, on Windows or macOS, or from an installed agent with a real package;
    the memory a daemon's process costs on a Pi Zero.
- [ ] **D3 — one host node per daemon.** jarvisd refuses to install a package with a daemon on a
  second node of the household, with a message the app shows.
  - Verify: route tests. This is a new refusal on an existing route, not a new route.

### Risks

| Risk | Mitigation |
|---|---|
| A daemon's process costs memory on a Pi Zero (one Python process each) | Measure in D2 on the lab Pi; daemons are few |
| A desktop sidecar with backends changes what existing desktop packages see | A1 gives only alerts and the command-centre client; the rest stays at the SDK's defaults, pinned by a test |
| The secrets push restarts a daemon mid-reply | The supervisor stops it with the cancellation `run()` already has to honour; a message in flight is retried by the service or lost, stated in the package guide |
