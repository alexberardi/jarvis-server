# Pi node: restart the music players when the Bluetooth output moves

- **Date:** 2026-10-22 (loop #478, build agent; the merge waits for the user: it adds a root wrapper op)

On jarvis-pi4, shairport-sync (Music Assistant's, pa backend, a system unit running as the node's
user) logged "Pulseaudio stream failed!" when the agent made the Px7 S2's sink the default, and
played nothing after that until `systemctl restart shairport-sync`. So after the node moves the
default sink to or from a speaker (pair, its connection event, the reconnect loop, disconnect,
forget, and headphones that power off and take the output with them), it restarts the music
players that are running: shairport-sync, spotifyd and go-librespot (`packages.MusicPlayers`).
Only when the default actually changed; best effort, logged, never fatal. The restarts run in
the background, so a pair's reply doesn't wait on them (up to three units, each with its own
timeout); switches made while one runs restart the players once more after it, so the last switch
always gets a restart of its own. One switch of the default sink runs at a time.

**How it gets the right to restart a system unit:** a new `restart-service` op in the
`jarvis-post-install` root wrapper (`--package <pkg>`, payload `{"service": …}`), not new sudoers
lines. The wrapper only `try-restart`s a unit whose `jarvis.conf` drop-in carries that package's
`managed-by` marker, so a package can restart no more than its `configure_systemd_service` op
already restarts, and a stopped unit stays stopped. sudoers is unchanged (the wrapper is already
allowed). The agent finds the owning package by reading the drop-in (0644), and checks
`systemctl is-active` first. A unit no package configured is restarted only if it is the node
user's own (`systemctl --user`); a system unit installed by hand is not the node's to restart.
