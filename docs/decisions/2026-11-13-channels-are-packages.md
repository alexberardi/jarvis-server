# A chat-service channel is a package run as a daemon, not a core module

- **Date:** 2026-11-13 (user; [CHANNELS.md](../CHANNELS.md), reviewing contributor PR #649)

PR #649 put a Discord bridge inside `internal/modules/cc`: a `discord` sub-package, `discord.*`
household settings, a `cc_discord_links` table, `/discord` app routes, a call from the node push
handler, and `disgo`, `dave-go` and libopus in the build. The user: "I don't want discord specific
code in the core product", as with the Spotify receiver in #591.

- **Core holds a generic channels contract** (links, link codes, the inbound turn, the reminder
  outbox, app routes), naming no service. A service is a Pantry package.
- **Go adapters compiled into jarvisd behind an interface were rejected** (user: "I'd really rather
  not do this"): the service's code and dependencies would still ship in the core binary.
- **`IJarvisDaemon` is a new SDK kind**, started once and supervised by the Go agent in its own
  sidecar process. `IJarvisAgent` runs on an interval and blocks the sidecar while it runs; Discord
  and Slack deliver messages only over a connection the bot keeps open.
- **Desktop nodes run package agents and daemons**, not only a Pi. Bundled with this work.
- **One host node per daemon:** the node it was installed on; a second install in the household is
  refused. No failover in this version.
- **Core decodes voice clips; libopus is allowed in core as a generic audio package** (user: "I'm
  okay with libopus if it's a generic package that will be useful in the future to have built in").
  It lives under `internal/audio/`, loaded with purego, and names no service. A package hands core
  the clip's bytes.
- **Live voice in a voice channel is a follow-up**, inside the Discord package (user). It is
  Discord-only and is what needed `dave-go` (young, unaudited).
- **This project builds all of it**, using parts of #649 and jarvis-node-mobile#158 with credit to
  their author; both are closed with an explanation once the replacement exists. Slack is the
  second service that proves the contract. A household with no node is out of scope.
