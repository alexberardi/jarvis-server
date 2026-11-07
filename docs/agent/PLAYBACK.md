# Playback: one now-playing state per node, any provider (plan)

Status: **planned, 2026-11-07.** Nothing here is built yet. The decisions below were made with the user on
2026-11-07; each open question carries a recommendation, to be settled one at a time before the pieces start.

Paths are relative to the repo root. "The agent" is `cmd/jarvis-agent` and `internal/agent/`; "the music
module" is `internal/agent/music`; "the SDK" is `jarvis-command-sdk`.

## Goal (user's words, 2026-11-07)

> If we can signal from the command or the node that a generic music is playing with payload, we could also
> show a music playing card in the mobile app with controls.

> My concern is keeping this in sync. There's a lot of entry points for stopping and starting music over
> voice, chat, via Bluetooth, etc. We'd need a centralized place and built in SDK function that handles the
> playback and makes it all seamless.

> I want the core to be provider/package agnostic though. I don't want Spotify specific code in a core module
> where a user may only have Apple Music or not want music at all.

What started this: contributor PR #591 added a Spotify Connect receiver and a Spotify now-playing view to the
display, in core and on by default. The need it serves is real; the shape isn't. Its now-playing view is the
starting point for the display piece below, rebuilt over the generic state.

## Today

Music starts, stops and changes from many places, and nothing holds one answer to "what is playing here":

| Entry point | What happens now |
|---|---|
| Voice or chat: a music command (Spotify, Music Assistant, Pandora, Audacy) | The package's command drives its own player. Each package implements pause, skip and volume itself. |
| The music module | Before a music command's "play", `BeforeCall` stops the other players (takeover). While the user talks, it ducks every known player by process name (`sigstopPlayers`, `muteOnlyPlayers`). It knows players by binary, not by what they play. |
| Command-to-player map | Declared by the command's `takes_over_playback_binary` (read in `internal/agent/sidecar`), or `legacyTakeover` in `music.go`, which hardcodes `"spotify": "go-librespot"` and three others. |
| The touch display | `display_media` plays video in its own mpv, with its own status (`GetPlaybackStatus`) and controls (`ControlMedia`). Remote destinations are household media devices. Playercast and Cast receivers feed the same mpv. |
| Outside Jarvis | The phone's Spotify app controls go-librespot directly. A phone paired over Bluetooth streams into the node (`internal/agent/bluetooth`, loopback into the default sink). Neither tells Jarvis anything. |
| Apps | No now-playing anywhere. The device catalogue's `media_player` type has `state`, `volume`, `media_title`, `media_artist`, `source`, `position` and `duration`. The mobile app maps it to a media control but has no media control screen yet. |

## Decisions (2026-11-07, user)

| # | Decision |
|---|---|
| P1 | **Core is provider-agnostic.** No service's name, API or program in jarvisd, the agent or the apps. A node with no music package has no now-playing view, no setting and no background process. Open protocols that are not a service (Bluetooth audio, the display's own mpv) may be built-in sources. |
| P2 | **One owner per node: the agent's music module.** It already sees every music command (takeover) and every player (ducking). It becomes the node's playback hub: the registry of sources, the one current state, and the only path for controls. |
| P3 | **The player is the source of truth.** The hub never records what a command meant to do. It reads state back from the source, so a change from any entry point, inside Jarvis or not, shows up the same way. |
| P4 | **An SDK contract for sources.** A package reports what is playing and which actions it supports, and handles actions sent back to it. Its shape is Q4. |
| P5 | **Generic transport commands in core.** "Pause the music", "next", "turn it down" and "what's playing" are built-in commands that go to the active source through the hub. Packages keep only what is theirs: search and starting playback. |
| P6 | **jarvisd keeps the latest state per node and relays controls.** The display, the mobile card and the web UI read that one state, and their buttons go back through the hub's control path. |
| P7 | **Field names follow the catalogue's `media_player` type**, so one mobile media card can serve both Jarvis nodes and imported speakers. |
| P8 | **Spotify stays a package.** `jarvis-cmd-spotify` works as a speaker with no setup (jarvis-cmd-spotify#2: Client ID optional) and implements the contract. #591 is closed with a pointer here. |
| P9 | **Ducking and takeover run on the hub's registry.** The hardcoded player lists and `legacyTakeover` go once every music package declares itself. |

## Design

### The state

One record per node, `null` when nothing plays:

| Field | Notes |
|---|---|
| `source` | The package or built-in source: its id and display name ("Spotify", "Bluetooth: Alex's phone"). |
| `state` | `playing`, `paused` or `idle` (the catalogue's values). |
| `media_title`, `media_artist`, `media_album` | Strings; `media_album` is the one new field next to the catalogue's. |
| `position`, `duration` | Seconds, with `updated_at`, so a viewer advances the position on its own clock between readings. |
| `volume` | 0 to 100. |
| `artwork` | A reference the node serves, never a third-party URL handed to clients (Q5). |
| `actions` | What this source supports, from a fixed list: `play`, `pause`, `next`, `previous`, `seek`, `volume`, `shuffle`, `repeat`, plus `like` for a source with a library. Clients show only these. |
| `shuffle`, `repeat`, `liked`, `context` | Optional; present only when the source reports them. `context` is "playing from" (a playlist or album name). |

### SDK

A package that plays audio declares itself a playback source and implements two calls: `playback_status()`
returns the record above, and `playback_control(action, **args)` performs one action and returns the new
record. An optional `report()` helper pushes a change as it happens (a track change, the phone pausing), so
the hub doesn't have to wait for its next poll. Declaring a source replaces `takes_over_playback_binary`: the
hub knows which binary to duck and stop from the same declaration.

### Node (the hub)

- **Registry:** sources come from the sidecar's package scan (declared commands) and from built-ins (the
  display's mpv; a phone streaming over Bluetooth, Q7).
- **Current state:** while something plays, the hub polls the active source (Q9) and takes `report()` pushes.
  The active source is the one that most recently started playing (Q6). Takeover already stops the others
  when a command plays, so one source plays at a time.
- **Controls:** every control, from voice, the display, the mobile card or the web UI, goes through
  `hub.Control(action)`, which calls the active source and returns the state read back afterwards.
- **Ducking and takeover:** they use the registry's declared binaries instead of the hardcoded lists.
- **Reporting:** state changes go to jarvisd promptly (Q3).
- **Built-in commands:** the hub registers the generic transport commands (P5). A music package's own pause
  and skip actions keep working through the migration; once the built-ins ship, they can go.

### Server

jarvisd keeps the latest record per node in memory (it is live state, not history) and serves it to members
of that node's household. A control request is checked against the household and relayed to the node as a
command verb (the same channel `node.Runner.Verbs` already carries). New `/api` routes need tier expectations
in the same PR and get the user's review (AGENTS.md merge policy).

### Clients

- **Display:** a generic now-playing view (cover, title and artist, seek bar, the source's actions, volume),
  full screen while playing, built from #591's view with the Spotify specifics removed.
- **Mobile:** a now-playing card with controls (Q1 for its scope, Q2 for how it stays current).
- **Web UI:** the same card, later; not in the first pieces.

## Open questions (recommendations; settle one at a time)

| # | Question | Recommendation |
|---|---|---|
| Q1 | Does the mobile card show one node's music, or everything playing in the household? | The whole household, grouped by node, with the user's current node first. |
| Q2 | How does the mobile app stay current? It has no live channel today except chat's stream. | Poll the household's playback while the card is on screen (every 2 s), and take the state a control returns immediately. A live stream can come later without changing the record. |
| Q3 | How does the node report changes? The heartbeat runs once a minute. | Publish over the node's existing MQTT connection on each change, throttled to one message a second; the heartbeat carries the same record so jarvisd recovers after a restart. |
| Q4 | Contract shape: optional methods on the existing command class, or a new `player` component type? | Optional methods on the command class, found by the sidecar's scan the way `takes_over_playback_binary` is, plus the `report()` helper. No new component type to install or list. |
| Q5 | How does cover art reach the clients? | The source returns the image bytes through the sidecar; the node caches them per track and serves them to the display and to jarvisd. No client or core code fetches a service's image host. |
| Q6 | Which source is active when two report playing? | The one that most recently started; takeover keeps it to one in practice. |
| Q7 | Is a phone streaming over Bluetooth a source in the first version? | Yes, as "Bluetooth: <phone name>", with play, pause, next and previous over the phone's Bluetooth remote-control profile. Track details (title and artist from the phone) follow in a later piece; that is new work in `internal/agent/bluetooth`. |
| Q8 | Is the display's own mpv (`display_media`, Cast, Playercast) a source? | Yes, built in: its status and controls already exist (`GetPlaybackStatus`, `ControlMedia`). |
| Q9 | Poll cadence while something plays? | Every 2 s for a source that doesn't push, never while idle; a source that pushes is polled every 10 s as a check. |
| Q10 | The Python node (`jarvis-node-setup`)? | Out of scope: it keeps working without a now-playing card. Go agent nodes only. |

## Pieces (in order)

1. **SDK:** the contract, the `report()` helper and their tests (jarvis-command-sdk).
2. **Agent hub:** the registry, current state, `Control`, the built-in transport commands, ducking and
   takeover on the registry, and the display's mpv as a built-in source.
3. **Server:** the per-node record, the household-scoped read and control routes with tier expectations
   (held for review), and the relay to the node.
4. **Display:** the generic now-playing view.
5. **Spotify package:** implements the contract from go-librespot's status (the first real source).
6. **Mobile:** the now-playing card. Mobile merges ship to testers, so this lands as one batch.
7. **Bluetooth source**, then track details from the phone.
8. **Clean-up:** remove `legacyTakeover` and the hardcoded player lists once the music packages declare
   themselves, and the packages' own pause and skip actions once the built-ins cover them.

## Verification

Each piece proves itself in the product flow, not only in unit tests: a sandbox with a scripted model for
voice and chat paths, the display shell in the sandbox's browser, and a real node with a real source for the
Spotify and Bluetooth pieces. The end-to-end check is one session that changes playback from each entry
point in turn (voice, chat, the display, the mobile card, the phone's Spotify app, Bluetooth) and confirms
the display and the mobile card agree after each one.

## Not in scope

- Playing to other rooms or grouping speakers (multi-room).
- Queues and browsing a library from the apps.
- Lyrics.
- Any service-specific feature in core (P1).
