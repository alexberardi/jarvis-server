# Pi node files

`respeaker-2mic-v2_0-overlay.dts`: Seeed's device-tree overlay for the ReSpeaker 2-mics Pi HAT
v2.0 (TLV320AIC3104), vendored unchanged from jarvis-node-setup `setup/respeaker-2mic-v2_0-overlay.dts`
(commit dcf1c6e, MIT, © 2024 Seeed Studio). Raspberry Pi OS ships no overlay for the v2.0 board.

`scripts/install-pi.sh` carries a copy of it (so the script installs from a single file) and
compiles it with `dtc` on the Pi; `scripts/install-pi-check.sh` fails when the two differ.

`jarvis-apt-install`, `jarvis-apt-source`, `jarvis-post-install`: the root wrappers a Pantry
package's system setup runs through sudoers (apt packages, apt sources, post_install ops; parity
5.6), vendored unchanged from jarvis-node-setup `scripts/` (commit 73b3660). The agent calls them
with `sudo -n` (`internal/agent/packages/system.go`). `install-pi.sh` carries a copy of each and
puts it in `/usr/local/sbin` (root's, 0755) with one NOPASSWD line each; `scripts/install-pi-check.sh`
fails when a copy differs. `jarvis-post-install` needs python3. It has one op the original
doesn't: `restart-service` (#478), which try-restarts a unit only when the calling package's
drop-in configures it.
