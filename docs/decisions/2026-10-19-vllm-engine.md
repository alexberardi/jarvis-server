# vLLM is a supervised jarvisd engine again (Linux + NVIDIA)

- **Date:** 2026-10-19 (user; built by loop #300)

jarvisd runs vLLM's OpenAI-compatible server as a supervised local engine on linux/amd64 hosts
with an NVIDIA GPU, the same way it runs llama-server and (on Apple Silicon) mlx-lm: fetched on
demand, started on a loopback port with its own API key, health-checked, restarted on a crash,
stopped with jarvisd, refused with the reason when the GPU is lost or its driver is broken, and
reached through the same OpenAI-compatible client. Two labels on the same vLLM model share one
server (LD1). A Hugging Face directory model on linux/amd64 runs on vLLM; GGUF files stay on
llama-server.

It is built on the Python engine layer of [2026-10-19-mlx-engine](2026-10-19-mlx-engine.md): the
jarvisd binary stays pure Go, and Python appears only as this downloaded engine, installed
self-contained under `<home>/engines/vllm/<version>-cuda/` by the pinned uv with a hash-locked
package set (vllm, torch and its CUDA 13 libraries). The install is large (about 4 GB of wheels,
8 GB installed, 17 GB free needed while it runs), so the admin shows the sizes before the
download, and an install without the disk for it doesn't start.

vLLM preallocates a fixed share of the card (upstream default 0.9). On one GPU next to a
llama-server label that default would take the card, so jarvisd always passes the share: the
label's `gpu_memory_utilization`, or one sized to the model at its context. The fit check counts
that share as the engine's need.

This reverses LD7 (2026-10-06, "vLLM support is dropped") and the vLLM part of the PLAN §7 cut of
the in-process backends. It is recorded as LD10 in [llm/QUESTIONS.md](../llm/QUESTIONS.md).

How it is built, and what it does and doesn't do for cc (tool calls, JSON mode, thinking,
vision): [llm/01-backends-and-engines.md §3.12](../llm/01-backends-and-engines.md).
