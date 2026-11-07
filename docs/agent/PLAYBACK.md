# Playback: one now-playing state per node, any provider (plan)

Status: **planned, 2026-11-07; every open question settled the same day (P10–P19).** Nothing here is built
yet. The next step is piece 1, the SDK.

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
| P4 | **An SDK contract for sources.** A package reports what is playing and which actions it supports, and handles actions sent back to it, as an `IJarvisMusicCommand` (P13). |
| P5 | **Generic transport commands in core.** "Pause the music", "next", "turn it down" and "what's playing" are built-in commands that go to the active source through the hub. Packages keep only what is theirs: search and starting playback. |
| P6 | **jarvisd keeps the latest state per node and relays controls.** The display, the mobile card and the web UI read that one state, and their buttons go back through the hub's control path. |
| P7 | **Field names follow the catalogue's `media_player` type**, so one mobile media card can serve both Jarvis nodes and imported speakers. |
| P8 | **Spotify stays a package.** `jarvis-cmd-spotify` works as a speaker with no setup (jarvis-cmd-spotify#2: Client ID optional) and implements the contract. #591 stays open until the user has verified and tested the replacement; it is then closed with a pointer here. |
| P9 | **Ducking and takeover run on the hub's registry.** The hardcoded player lists and `legacyTakeover` go once every music package declares itself. |
| P10 | **The mobile card shows the whole household (Q1, 2026-11-07).** Several nodes can play at once, and the card lists every node that is playing, each with its own controls. The first version is whatever is simplest beyond that: a plain list, no ordering by the user's own node and no grouping beyond one entry per node. The user's words: "whatever's easiest on the first pass, assuming you can play multiple nodes and view them in the household." |
| P11 | **The mobile app checks playback once, at the root (Q2, 2026-11-07).** One React Query query beside `PresenceManager` in `App.tsx`; no screen subscribes. It checks every 2 s while any node plays and every 15 s when none does, only while signed in and in the foreground (wire React Query's `focusManager` to `AppState`, which the app doesn't do yet). A control shows the state jarvisd returns at once. A mini-player bar, drawn once in the tab navigator's tab bar, appears when something plays and opens the full list. It shows only on each tab's first screen: drill-in screens (a routine's edit screen, say) and the root stack's screens (Settings, Inbox) never show it. The tabs hold stack navigators that keep the tab bar on drill-ins, so "wherever the tab bar shows" is not the rule. |
| P12 | **The node reports changes with an HTTP request (Q3, 2026-11-07).** The user was fine with MQTT or an API request. The node already sends everything to jarvisd over HTTP with its own credentials (the once-a-minute status message is `CC.Beat`, an HTTP call), and uses MQTT only for jarvisd's requests to it and its replies. So it sends each playback change to a node route, at most one a second, with the latest state winning; the status message carries the same record, so jarvisd catches up after a restart. The new route needs tier expectations and the user's review. Controls from jarvisd to the node use the existing MQTT request channel (P6). |
| P13 | **Sources are an `IJarvisMusicCommand` in the SDK (Q4, 2026-11-07).** The user's idea: "an IMusicCommand that exposes these commands and inherits from IJarvisCommand so it's compatible as a regular command but also gets further customization." Named `IJarvisMusicCommand` after the SDK's `IJarvis*` convention. It adds `playback_status()`, `playback_control()` and `playback_binary` as abstract members, and `report()` as a helper. No new manifest component type. |
| P14 | **No stale now-playing (Q6, 2026-11-07).** The most recently started source that is still playing is the one shown. The user's concern, which this decision is built around: "I'm just afraid we're going to have stale data showing" (first stated as "auto kill the old one's status", then: "maybe overcorrecting it"). So nothing is stopped automatically; instead every layer expires what it can no longer confirm. The node polls every source that has reported playing and drops one after three failed checks (6 s). While playing it resends the state every 10 s and sends an empty state on stop. jarvisd drops a node's record after 30 s without a refresh. A control returns the state read back from the player, never the state asked for. The end-to-end check covers each case: stop from outside Jarvis, a second source, the node going offline. |
| P15 | **A phone streaming over Bluetooth is a source in the first version, if it's easy (Q7, 2026-11-07).** BlueZ exposes the phone's remote-control profile as `org.bluez.MediaPlayer1` over D-Bus, which `internal/agent/bluetooth` already speaks (godbus): play, pause, next, previous, and the track's title, artist, album, duration and position. If that holds on real phones, it ships with the other sources; if it turns out hard, it moves after them and the rest doesn't wait. A phone that doesn't offer the profile shows as "Bluetooth: <phone name>" with no controls. |
| P16 | **The display's own mpv is a built-in source (Q8, 2026-11-07),** over its existing status and controls (`GetPlaybackStatus`, `ControlMedia`); Cast and Playercast feed that mpv. |
| P17 | **The hub polls each source that is playing every 2 s (Q9, 2026-11-07),** and checks each idle source every 10 s. Amended while building the hub (2026-11-07): the plan first said none while idle, but `report()` can't reach the agent unasked (the agent reads the sidecar's output only while it waits for a reply), so it only queues in the sidecar and the hub collects the queue each tick. Without the idle check, music started from outside Jarvis (the phone's Spotify app) would never be noticed. The checks are local calls. |
| P18 | **Go agent nodes only (Q10, 2026-11-07).** The user: the Python node is "totally deprecated." `jarvis-node-setup` gets none of this. |
| P19 | **Cover art is the image itself (Q5, 2026-11-07; the user took the recommendation).** The source returns the image bytes through the sidecar; the node keeps one per track and serves it to the display and to jarvisd, which serves it to the apps. No core code or app fetches from a service's image host (P1), and an image that needs sign-in works. |

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
| `artwork` | A reference to the image the node serves, never a third-party URL handed to clients (P19). |
| `actions` | What this source supports, from a fixed list: `play`, `pause`, `next`, `previous`, `seek`, `volume`, `shuffle`, `repeat`, plus `like` for a source with a library. Clients show only these. |
| `shuffle`, `repeat`, `liked`, `context` | Optional; present only when the source reports them. `context` is "playing from" (a playlist or album name). |

### SDK

A package that plays audio subclasses `IJarvisMusicCommand` (P13), which inherits from `IJarvisCommand`: it
is still a regular command, with its own parameters and `run()`, and the sidecar's scan finds it as one
(`issubclass(cls, IJarvisCommand)`). On top, it implements:

- `playback_status()`: the record above, or `None` when it isn't playing.
- `playback_control(action, **args)`: performs one action and returns the record read back afterwards.
- `playback_binary`: the player process it runs, for ducking and takeover. It replaces
  `takes_over_playback_binary`, which the sidecar keeps reading for packages that haven't moved.

The base class gives it `report()`, a helper that pushes a change as it happens (a track change, the phone
pausing), so the hub doesn't have to wait for its next poll. The sidecar recognises a source by
`issubclass(cls, IJarvisMusicCommand)`; a package built against an older SDK stays a plain command.

### Node (the hub)

- **Registry:** sources come from the sidecar's package scan (declared commands) and from built-ins (the
  display's mpv, P16; a phone streaming over Bluetooth, P15).
- **Current state:** while something plays, the hub polls each playing source every 2 s (P17) and takes
  `report()` pushes. The node shows the most recently started source that is still playing (P14). It polls every
  source that has reported playing, not only the shown one, so when the newer one stops, an older one
  still playing shows again. A source that fails three checks in a row (6 s) counts as stopped and its
  state is dropped. The hub stops no player on its own; takeover still stops the others when a music
  command plays, as today.
- **Controls:** every control, from voice, the display, the mobile card or the web UI, goes through
  `hub.Control(action)`, which calls the active source and returns the state read back afterwards.
- **Ducking and takeover:** they use the registry's declared binaries instead of the hardcoded lists.
- **Reporting:** state changes go to jarvisd as an HTTP request, at most one a second (P12). While
  something plays the node resends the state every 10 s even with no change, and sends an empty state
  when playback stops (P14).
- **Built-in commands:** the hub registers the generic transport commands (P5). A music package's own pause
  and skip actions keep working through the migration; once the built-ins ship, they can go.

### Server

jarvisd keeps the latest record per node in memory (it is live state, not history), and drops a record
that hasn't been refreshed for 30 s (a node that crashed or went offline) so the apps never show it (P14). It serves the records to members
of that node's household. A control request is checked against the household and relayed to the node as a
command verb (the same channel `node.Runner.Verbs` already carries). New `/api` routes need tier expectations
in the same PR and get the user's review (AGENTS.md merge policy).

### Clients

- **Display:** a generic now-playing view (cover, title and artist, seek bar, the source's actions, volume),
  full screen while playing, built from #591's view with the Spotify specifics removed.
- **Mobile:** a now-playing card listing every playing node in the household, each with its controls (P10), behind a mini-player on the tabs' first screens (P11).
- **Web UI:** the same card, later; not in the first pieces.

## Open questions

None: Q1–Q10 were settled with the user on 2026-11-07 (P10–P19).

## Pieces (in order)

1. **SDK:** `IJarvisMusicCommand`, the `report()` helper and their tests (jarvis-command-sdk).
2. **Agent hub:** the registry, current state, `Control`, the built-in transport commands, ducking and
   takeover on the registry, and the display's mpv as a built-in source.
3. **Server:** the per-node record, the household-scoped read and control routes with tier expectations
   (held for review), and the relay to the node.
4. **Display:** the generic now-playing view.
5. **Spotify package:** implements the contract from go-librespot's status (the first real source).
6. **Mobile:** the now-playing card. Mobile merges ship to testers, so this lands as one batch.
7. **Bluetooth source** (P15).
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
