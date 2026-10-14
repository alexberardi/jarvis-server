# Desktop voice (phase 3) for the Go agent, Linux and macOS together

- **Date:** 2026-10-10
- **Moved from** the STATUS.md decision log on 2026-10-14.

**Desktop voice (phase 3) for the Go agent, Linux and macOS together** ([agent/go-pi-node.md §3.7](../agent/go-pi-node.md)). `run --voice` (off by default) / `listen`: ALSA and AudioQueue over purego (`internal/audio/device`), the Python node's wake/endpointing/upload behaviour with its numbers (golden-tested), 48 kHz uploads (§8 Q1), the §1.1 rows 7–14 turn. Wake models are fetched once into `<user cache>/jarvis-agent/openwakeword`, pinned by SHA-256 (`--wake-models DIR` to supply them). Deliberate differences: a synthesised chime instead of the greeting/error phrases, and a 202 `complete` with no message stays silent (no "Task completed."). Windows: "not supported yet", no onnxruntime linked.
