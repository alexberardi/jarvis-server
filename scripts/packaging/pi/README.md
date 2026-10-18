# Pi node files

`respeaker-2mic-v2_0-overlay.dts`: Seeed's device-tree overlay for the ReSpeaker 2-mics Pi HAT
v2.0 (TLV320AIC3104), vendored unchanged from jarvis-node-setup `setup/respeaker-2mic-v2_0-overlay.dts`
(commit dcf1c6e, MIT, © 2024 Seeed Studio). Raspberry Pi OS ships no overlay for the v2.0 board.

`scripts/install-pi.sh` carries a copy of it (so the script installs from a single file) and
compiles it with `dtc` on the Pi; `scripts/install-pi-check.sh` fails when the two differ.
