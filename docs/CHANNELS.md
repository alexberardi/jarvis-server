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
above. The route's shape and how the outbox is drained are settled in the `channels-core` plan
below, for the user's review.

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
- [x] **D3 — one host node per daemon.** jarvisd refuses to install a package with a daemon on a
  second node of the household, with a message the app shows.
  - Verify: route tests. This is a new refusal on an existing route, not a new route.
  - Built: a node's install result names the package's daemons; jarvisd records that on the
    node's installed package (`cc_node_package_stores.has_daemon`) and answers 409
    `daemon_host_conflict`, naming the host, to an install of it on another active node of the
    household. An update where it runs, another household, and a move after uninstalling are
    allowed. Not caught: two first installs started together (jarvisd learns of the daemon from
    the first result), and a node on an agent older than this one, whose result names none.

### Risks

| Risk | Mitigation |
|---|---|
| A daemon's process costs memory on a Pi Zero (one Python process each) | Measure in D2 on the lab Pi; daemons are few |
| A desktop sidecar with backends changes what existing desktop packages see | A1 gives only alerts and the command-centre client; the rest stays at the SDK's defaults, pinned by a test |
| The secrets push restarts a daemon mid-reply | The supervisor stops it with the cancellation `run()` already has to honour; a message in flight is retried by the service or lost, stated in the package guide |

## Plan: `channels-core`, the SDK helper and the first packages (awaiting the user's review)

### Shape

- **A provider is a name a package picks** (`discord`, `slack`; `[a-z0-9_]`, at most 32). Core
  never lists them. A daemon makes itself known by calling in, and core remembers which node of
  the household spoke for it and when.
- **Who may speak for a provider:** any enrolled node of the household, as that node. A package a
  household installs on its own node is the household's; core does not police one package against
  another on the same node. Another household's node cannot reach these links.
- **Every call is a POST from the daemon** (the SDK's `cc_post` is POST only), under
  `/api/v0/node/channels/{provider}/`:

  | Route | Body | Answer |
  |---|---|---|
  | `message` | external user, place, text, images | `{reply}`; `{linked: false}` for an account that isn't linked (the package says nothing) |
  | `command` | external user, place, name, args | `{text, private}`; the names are core's: `link`, `new`, `node`, `voice`, `notify`, `status` |
  | `outbox` | ids to acknowledge | reminders waiting for this provider's linked users; the call is also the daemon's sign of life |

  A `message` is held open for the whole chat turn (up to three minutes), so the agent's `cc.post`
  gets a turn-length timeout for these paths; the node client's usual limit is 15 s.
- **The app** (member tier) under `/api/v0/mobile/household/{id}/channels`: the list (each
  provider that called in, whether it did so in the last minute, and my link), and per provider a
  link code, my preferences and unlink. The bot's credentials are the package's settings, entered
  where an agent's are.
- **Data:** `cc_channel_links` (as #649's table, plus `provider`), `cc_channel_providers`
  (household, provider, node, last seen) and `cc_channel_outbox` (rows dropped when acknowledged or
  a day old). Link codes and the conversation of each person and place stay in memory, as in #649:
  a restart starts a fresh conversation.
- **Reminders:** where #649 called Discord from the node push handler, a reminder a node pushes
  becomes an outbox row for each provider the user opted into. The daemon asks every few seconds.
- **Limits** stay in core, as #649 had them: turn timeout, four images of 2 MiB after shrinking,
  reply length is the package's (it knows its service's limit and splits).

### Tasks (in order; each is one PR)

- [x] **K1 — `ChatText`.** A chat turn as a user without the SSE stream, ported from #649
  (`chat_text.go`, the `sseWriter` collector).
  - Verify: its tests from #649; `scripts/sandbox` with the scripted model.
- [x] **K2 — links and the app's routes.** The three tables, the link store and codes from #649
  with the provider added, the member routes, `PurgeUser`.
  - Built (in review): `cc/channels` (links, codes, the providers that called in),
    `cc_channel_links` and `cc_channel_providers`, and the four member routes. A code is for one
    provider and household; a link also goes when its user leaves the household
    (`PurgeUserHousehold`), which #649 did not do. The outbox table comes with K4. The routes
    are jarvisd's own (the mobile app's), not yet in the web app's gateway, so its tier table is
    unchanged.
  - New routes: tier expectations in the same PR; **held for the user's review**.
  - Verify: #649's store and route tests against the generic names; another household gets 404.
- [ ] **K3 — the daemon's routes.** `message` (text and images, #649's rules: linked accounts
  only, a conversation per person and place), `command`, the provider's sign of life, and the
  agent's timeout for these paths.
  - New node routes: **held for the user's review**.
  - Verify: #649's bridge tests moved to the route; a turn through a real sidecar daemon and a
    sandbox jarvisd with the scripted model.
- [ ] **K4 — reminders.** The outbox, filled from the node push handler for users who opted in
  (off by default), following the push as #649 did (a withheld reminder is not sent).
  - Verify: route tests; nothing queued without opt-in.
- [ ] **S1 — the SDK's channel helper** (`jarvis_command_sdk.channel`, 0.18.0): async calls for
  the three routes that run the blocking `cc_post` in a thread, and the dataclasses a package
  maps its service's events to. Then the Pantry is redeployed (`--no-cache`) so the Forge has it.
  - Verify: the SDK's tests with a fake backend.
- [ ] **P1 — `jarvis-channel-discord`.** A daemon on `discord.py`: direct messages and one
  channel (threads), slash commands mapped to core's, photos, the typing indicator, replies split
  at Discord's limit. Its token and channel are its settings. Submitted to the Pantry.
  - Verify: against the user's test bot, from a desktop node and the lab Pi.
- [ ] **P2 — `jarvis-channel-slack`.** The same over Socket Mode.
  - Acceptance: **no change to jarvisd, the agent or the SDK was needed.** If one is, the
    contract was wrong and is fixed before Slack is called done.
  - Verify: against the user's Slack workspace.
- [ ] **M1 — the mobile card** (jarvis-node-mobile, batched into one PR): the channels list with
  link code, preferences and unlink (the member half of #158), and a `daemon` settings entry
  worded as a daemon, not an agent with an interval.
- [ ] **K5 — voice clips.** `internal/audio/opus` (libopus by purego, #649's `opusnative` and Ogg
  code) on all four release targets; `message` takes a clip and can answer with one, by the
  user's voice-reply preference.
  - Verify: the conversions' tests; a real clip from Discord and from Slack.
- [ ] **Then:** close #649 and jarvis-node-mobile#158 with an explanation and credit; live voice
  as a follow-up inside the Discord package.

### Checkpoints

- After K3: a message typed into a test daemon gets Jarvis's answer, with nothing service-named
  in jarvisd. The user tries it before the packages are written.
- After P2: the acceptance above. This is the point of the whole design.

### Risks

| Risk | Mitigation |
|---|---|
| The contract fits Discord and not Slack | P2 is the test; K3's shapes are written against both services' events before any package |
| A held-open request per turn ties up a daemon thread and a server connection for minutes | Turns are few and bounded by the timeout; a stream replaces it only if live voice needs one |
| A package on a household's node can speak as any linked user of any provider | Stated above as the trust boundary; the Pantry's review is what stands between a package and a node |
| libopus is missing on an install | K5 is last and separate; without it only clips say so |
