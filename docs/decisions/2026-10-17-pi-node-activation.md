# Which node runs on a Pi with both installed

- **Date:** 2026-10-17 (build agent, loop #159; the user approves the merge)

Only one node can own the ReSpeaker HAT (capture, playback, LEDs, button). `scripts/install-pi.sh`
installs `jarvis-agent.service` **disabled**, with `Conflicts=jarvis-node.service`, and swaps the
nodes on request: `--activate` runs `systemctl disable --now jarvis-node.service` then
`systemctl enable --now jarvis-agent.service`; `--deactivate` does the reverse (and nothing else).

Both, not one of them: the flag decides what starts at boot (two enabled units that conflict would
race at every boot), and Conflicts= covers the paths the flag doesn't see: someone running
`systemctl start jarvis-node`, or the Python node's own install.sh re-enabling and starting it on
an update. Either way the one started last wins and the other is stopped, never both on the HAT.

The heartbeat's `install_mode`/`runtime` (§8 Q6) then says which one jarvisd is talking to.
See [go-pi-node.md §7, §8](../agent/go-pi-node.md).
