# Playback is provider-agnostic, with one owner per node

- **Date:** 2026-11-07 (user, reviewing contributor PR #591)

Core (jarvisd, the agent, the display and the apps) holds no service-specific music code: no service's
name, API or program. The user's reason: "a user may only have Apple Music or not want music at all." A
node with no music package has no now-playing view, no setting and no background process. Open protocols
that are not a service (Bluetooth audio, the display's own mpv) may be built-in sources.

Now-playing state has one owner per node, the agent's music module. It reads state back from the player
(the source of truth), and every control from voice, chat, the display or the apps goes through it, so the
entry points can't drift apart. Packages implement a generic SDK contract (report what is playing, handle
actions). Generic transport commands (pause, next, volume, what's playing) live in core and go to the
active source.

This is why #591 (a Spotify Connect receiver and a Spotify now-playing view in the display, on by default)
is not merged as written. Spotify stays in `jarvis-cmd-spotify`, which now works as a speaker with no setup
(jarvis-cmd-spotify#2).

The design, with its decisions and open questions, is in [the playback plan](../agent/PLAYBACK.md).
