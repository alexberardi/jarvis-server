# The follow-up window is 3 s, and never longer

- **Date:** 2026-10-19 (user, #296, after testing the Go Pi node: "8 seconds is way too long. It
  should be settable in the mobile app with a maximum of like 3 seconds.")

The Go node's follow-up window is 3 s on every iteration (`voice.DefaultFollowUp`; it was 8 s,
then 6, then 5) and at most 3 s however it is set: agent.json `follow_up`, `--follow-up` and the
app's `follow_up_listen_seconds` (update_node_config) are clamped to
`voice.MaxFollowUpListenSecs` with a log line, not refused, so an old agent.json with 8 still
starts the agent. 0 still turns follow-up off. The old 5 s floor (`follow_up_min_timeout_secs`)
is gone from the defaults and can no longer raise a window above the one set, which made any
setting under 5 s (the app's 4) a 5 s window.

This narrows the window chosen in
[2026-10-11-follow-ups-heard-from-the-room.md](2026-10-11-follow-ups-heard-from-the-room.md)
(go-pi-node.md §3.10). The app's slider (jarvis-node-mobile) and the Python node
(jarvis-node-setup) get the same 0–3 s range in their own repos.
