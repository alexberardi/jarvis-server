# The Go desktop node moved in as cmd/jarvis-agent

- **Date:** 2026-10-10
- **Moved from** the STATUS.md decision log on 2026-10-14.

**The Go desktop node moved in as `cmd/jarvis-agent`** (phase 2 of [agent/go-pi-node.md §7](../agent/go-pi-node.md); user decisions in its §8). Code under `internal/agent/{host,desktop,apps,macax,node,sidecar,deploy}`; it imports nothing from jarvisd's modules. The wake-word stack is a shared library beside sherpa: `internal/voice/ort` loads the *same* embedded `libonnxruntime` (`sherpa.LibDir`), `internal/voice/oww` is openWakeWord, `audio.Decimator` resamples 48→16 kHz. The desktop agent keeps heartbeating `install_mode: "desktop-agent"` (jarvisd only offers remote updates to `"tarball"`, which the Pi Go node will use) and adds `version_info.runtime: "go"`. `internal/update` is not reused yet: it hard-codes the jarvisd binary/asset names and the DB snapshot.
