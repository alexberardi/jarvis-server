# Spotify Connect on a node

A node can appear in the Spotify app as a speaker. The agent runs [go-librespot](https://github.com/devgianlu/go-librespot)
as a supervised child process, so choosing the node in Spotify plays through that node's speaker. This works on
nodes with a touch display and on nodes with no screen.

## Requirements and limits

- Spotify Premium on the account that plays to the node.
- go-librespot on the node's `PATH`. It is GPL-3.0, so the agent runs it as its own program and never links it.
- This is an unofficial client. Spotify can change its service without notice, and its terms may not permit
  third-party clients. A failing receiver never stops voice or the display.
- The phone or computer playing must find the node by multicast DNS (mDNS). See [Discovery](#discovery).

## Which mode runs

| Node                        | How the receiver starts                                                | What it shows                            |
| --------------------------- | ---------------------------------------------------------------------- | ---------------------------------------- |
| Touch display (`--display`) | Automatically, unless `spotify_connect` is off in the display settings | A full-screen now-playing view           |
| No screen                   | Add `--spotify-connect` to the agent's `run` command                   | Nothing; the speaker plays and is ducked |

A display node ignores `--spotify-connect` because its own engine already runs the receiver. See
[touch-display.md](touch-display.md) for the now-playing view and its settings.

## Install go-librespot

Download the release that matches the node from the
[go-librespot releases](https://github.com/devgianlu/go-librespot/releases) and put the binary on `PATH`.

| Node                          | Release asset                         |
| ----------------------------- | ------------------------------------- |
| Raspberry Pi 3 and 4 (64-bit) | `go-librespot_linux_arm64.tar.gz`     |
| Raspberry Pi Zero, 1 and 2    | `go-librespot_linux_armv6_rpi.tar.gz` |
| Linux PC (x86-64)             | `go-librespot_linux_x86_64.tar.gz`    |

```bash
tar xzf go-librespot_linux_arm64.tar.gz
sudo install -m 0755 go-librespot /usr/local/bin/go-librespot
```

Check that `go-librespot --help` runs as the user the agent runs as.

## Turn it on for a node with no screen

Add the flags to the agent's command line, for example in its systemd unit:

```ini
ExecStart=/usr/local/bin/jarvis-agent run --voice --spotify-connect --spotify-name "Kitchen"
```

| Flag                    | Meaning                                                                    |
| ----------------------- | -------------------------------------------------------------------------- |
| `--spotify-connect`     | Run the receiver. Off by default.                                          |
| `--spotify-name <name>` | The speaker's name in the Spotify app. The default is the computer's name. |

The agent writes `config.yml` and keeps the remembered pairing in a `spotify` folder beside the agent's
credentials, with mode `0700`. The receiver's local API listens on `127.0.0.1:3678` only. If go-librespot exits,
the agent restarts it after 2 seconds, doubling the wait up to a minute while it keeps failing. If go-librespot is not
installed the agent logs one warning and carries on.

## Pair once

1. Open the Spotify app on a phone or computer signed in to the Premium account.
2. Choose the speaker icon, then pick the node's name.
3. The first connection pairs the node. It remembers the account after that.

## Voice

Port 3678 is the port the agent's music handling already uses for go-librespot, so no extra setting is needed:

- The agent pauses Spotify while you talk to Jarvis and resumes it afterwards, unless you had paused it.
- A music command that starts other playback pauses Spotify first.

## Say "play X on Spotify"

Install the [`spotify_play`](https://github.com/opticon454/jarvis-cmd-spotify) Pantry package and enter your own
Spotify developer app's client ID and secret in its settings. It finds the music with that app and plays it on this
node's receiver.

| You say                                  | Plays                      |
| ---------------------------------------- | -------------------------- |
| "Play Hysteria by Muse on Spotify"       | The song                   |
| "Play the album Rumours from Spotify"    | The album                  |
| "Play Taylor Swift on Spotify"           | The artist (an exact name) |
| "Play my Chill Hits playlist on Spotify" | The playlist               |
| "Spotify, play Discover Weekly"          | Best match                 |

The phrase needs the word "Spotify", so a bare "play some jazz" still goes to the model and to any other music
command. The package declares these as fast paths, which the agent runs with no model (see
[Package fast paths](#package-fast-paths)); each can be switched off in the app's Inspect fast-paths screen. The
package expects the receiver on port 3678, which a display node now prefers.

### Package fast paths

Any Pantry package may declare `fast_path_patterns` (the SDK's `FastPathPattern`) and a `pre_route` handler. The
agent asks the installed packages, after its own built-in fast paths, whether one claims what was said. A claim that
fails or has nothing to say falls through to the model. Switched-off patterns and commands that are off are skipped.

## Discovery

The node advertises `_spotify-connect._tcp` on UDP port 5353 of its own network only. mDNS does not cross subnets or
VLANs. If the Spotify app is on a different subnet, the speaker does not appear. Either join the phone to the node's
network for the first pairing, or enable an mDNS repeater or reflector between the networks (in UniFi Network this is
the multicast DNS setting). To check from a Linux machine on the node's network:

```bash
avahi-browse -rt _spotify-connect._tcp
```

## Troubleshooting

| Symptom                                 | Check                                                                            |
| --------------------------------------- | -------------------------------------------------------------------------------- |
| The node never appears in Spotify       | The subnet, as above, and that go-librespot is running (`pgrep go-librespot`).   |
| One warning about go-librespot at start | It is not on `PATH` for the agent's user. Install it as above.                   |
| It appears but will not play            | The account needs Premium. Check the agent's log for the receiver's exit reason. |
| No sound                                | The receiver uses PulseAudio. Confirm the node's default sink with `pactl info`. |

## Tests

`internal/agent/spotifyconnect` tests the config file, the restart loop and the missing-binary case.
`internal/agent/display` tests the display receiver. Neither starts the real go-librespot or talks to Spotify; a
live Spotify session and a Pi are exercised by hand.
