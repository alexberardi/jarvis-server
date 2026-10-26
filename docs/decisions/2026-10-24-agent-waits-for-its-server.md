# The desktop agent says it is waiting for its server, and picks up a local one in seconds

- **Date:** 2026-10-24 (user, QA on the MBP, 2026-10-23)

The user started the agent with its own local server (127.0.0.1:7700) off. The agent logged
"jarvisd not reachable yet; retrying" with waits doubling to a minute, the status icon said
"Running", and turning "Listen for Hey Jarvis" on twice from the menu did nothing (the voice
pipeline starts on the connection, in the runner's `OnConnected`). Starting the server changed
nothing visible, so the user restarted the agent — after which listening came up at once. The
verdict: "the agent doesn't work unless you start the server … if you start the server after
agent, it never recovers and needs a restart."

The user asked for three things, and that is what was built ([agent/DESIGN.md §15](../agent/DESIGN.md)):

1. **Say it.** While the server hasn't answered, the menu's header is "Waiting for the Jarvis
   server at `<config URL>`" instead of "Running", and the island says it in a line when the
   wait starts and says "Connected to the Jarvis server" when the server answers. The island
   says it once, not with every try: the menu is what says it for as long as it lasts, so a
   server that stays away (a laptop away from home) doesn't keep a pill on the screen. With
   listening on, a line under the toggle says listening starts once the server answers, and
   turning it on while waiting says so rather than appearing to do nothing.
2. **Retry quickly for a server on this computer.** The wait between discovery tries still
   doubles from a second, but its cap is five seconds when the config URL's host is loopback
   (127.0.0.1, localhost, ::1 — the bundled server app), and a minute for a server on the
   network, where a minute of silence costs nothing and a LAN server is not what the user just
   started. Watching the port or the mDNS announcement was considered and dropped: the five
   second cap gets the same result ("picked up within seconds") with none of the machinery.
   A server reached through the machine's own hostname or LAN address still gets the long cap;
   "Retry now" covers it.
3. **Retry now** in the menu (`node.Runner.RetryNow`): the waiting run tries again at once
   instead of after its backoff.

Only the discovery loop at the start of `Runner.Run` is affected. Once the node has reached
jarvisd, an outage is MQTT's to re-connect (paho, auto-reconnect) and heartbeats'; the node
does not go back through this loop, so nothing here changes what happens after a connection.
