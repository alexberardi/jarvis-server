# Windows desktop voice over winmm, not WASAPI

- **Date:** 2026-10-30 (user: "we need to implement that immediately", after a Windows user
  found no "Listen for Hey Jarvis" in the agent's menu)

The Windows agent gets voice through winmm's waveIn/waveOut (`internal/audio/device/winmm_windows.go`),
not WASAPI as [go-pi-node.md](../agent/go-pi-node.md) §8 row 10 planned. winmm is a plain C API
(no COM vtables), and its default device, `WAVE_MAPPER`, converts from the hardware format to
the rate and channels the pipeline asks for, as AudioQueue does on macOS, so no resampler is
needed. Completion is signalled through an event, so no Windows thread calls into Go. The cost:
the default input and output only (like macOS), and winmm's extra latency (a few tens of ms),
which a wake word and a spoken reply don't notice. WASAPI stays the path if device choice or
echo cancellation on Windows ever needs it.

The wake word itself needed nothing new: onnxruntime is already embedded for windows-amd64
(`internal/voice/sherpa`) and `internal/voice/ort` already loads it there.
