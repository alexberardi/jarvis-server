# whisper-server builds come from our own CI

- **Date:** 2026-10-06
- **Moved from** the STATUS.md decision log on 2026-10-14.

**whisper-server builds come from our own CI** (user: "a for sure"): jarvis-server CI builds whisper.cpp's whisper-server per flavour (Metal on darwin/arm64, CUDA/Vulkan/ROCm/CPU on linux, Windows) and publishes them as release assets with checksums; the engine manager fetches them like llama-server builds, so STT stays one-click everywhere.
