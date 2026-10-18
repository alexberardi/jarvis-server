# The Pi node keeps onnxruntime embedded in jarvis-agent

- **Date:** 2026-10-18 (loop #162, building the Pi install's release; the user approves the merge)

The Raspberry Pi node installs the same `jarvis-agent-<v>-linux-arm64.tar.gz` the release already
builds for Linux desktops, with onnxruntime **embedded** in the binary (extracted once under the
node user's cache, `jarvis-agent/native/`), and `install-pi.sh` ships beside it. This supersedes
[go-pi-node.md §8 Q3](../agent/go-pi-node.md)
("separate file in the Pi tarball"), unless measurements on a Pi say otherwise.

Why, from the numbers already measured (go-pi-node.md §3.11, §4):

- **Memory is the same either way.** The library is mapped from a file in both layouts: of the
  34.6 MB `libonnxruntime.so` about 12 MB is touched. The agent on the Pi 4 and the Zero 2 W is
  ~23 MB private (anonymous) of an 85–87 MB RSS that is mostly clean file-backed pages (the 51 MB
  binary and the extracted library), which the kernel drops under pressure. A separate file saves
  no RAM on a 512 MB Zero 2 W.
- **One file to update.** The agent's self-update (stage, rename, health gate, rollback; #161)
  swaps one binary in `/opt/jarvis-agent`, so onnxruntime can never be out of step with the code
  that loads it, and a rollback restores both. A separate library would need its own staging and
  rollback, and a version check between the two.
- **One build.** The Pi uses the desktop's linux-arm64 archive and its CI checks (`verify` starts
  it and loads the embedded voice libraries, `install-agent` on arm64); a Pi-only tarball would be
  another artifact to build, sign and test.

What it costs: ~35 MB of SD card for the extracted copy beside the binary, and a ~51 MB download
per update instead of a smaller one when only the agent changed. Neither matters at these sizes.

Revisit if a Pi shows memory pressure from the extracted copy, or if a minimal onnxruntime build
(§4's note: a few MB) makes the split worth having.
