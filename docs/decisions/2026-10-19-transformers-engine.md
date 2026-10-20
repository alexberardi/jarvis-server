# Transformers is a supervised jarvisd engine

- **Date:** 2026-10-19 (user; built by loop #301). Scope of the first PR: 2026-10-20 (user).

jarvisd runs Hugging Face's own OpenAI-compatible server, `transformers serve` (pinned
transformers 5.20.2 with torch 2.14.1), as a supervised local engine for any Hugging Face
directory model (safetensors), the same way it runs llama-server, mlx-lm and vLLM: fetched on
demand, started on a loopback port, health-checked, restarted on a crash, stopped with jarvisd,
and reached through the same OpenAI-compatible client. Two labels on the same model and
settings share one server (LD1).

It is built on the Python engine layer of [2026-10-19-mlx-engine](2026-10-19-mlx-engine.md): the
jarvisd binary stays pure Go, and Python appears only as this downloaded engine, installed
self-contained under `<home>/engines/transformers/<version>-<flavour>/` by the pinned uv with a
hash-locked package set. The accelerator is torch's: CUDA on NVIDIA, MPS on Apple Silicon, the
CPU otherwise, each with its own package set and its size shown before the download. dtype and
bitsandbytes quantization (CUDA only) are label settings, and `dir_engine` chooses Transformers
for a directory model where MLX or vLLM would be the default.

`transformers serve` was checked against what cc needs before building on it (it streams
`/v1/chat/completions` for real), so no server script of our own ships with the engine. What it
does with tool calls, JSON mode, thinking and vision, and the gaps:
[llm/01-backends-and-engines.md §3.13](../llm/01-backends-and-engines.md).

**First PR (user, 2026-10-20):** the macOS arm64 (MPS and CPU) and Windows x64 (CPU) sets. The
Linux CPU set and the Linux and Windows CUDA sets (pinned to PyTorch's cu128 build, driver 570 or
later, with bitsandbytes) need PyTorch's wheel index, which the build sandbox couldn't reach; they
follow as their own item once it can.

This reverses the TRANSFORMERS part of the PLAN §7 cut of the in-process backends, which was made
without being put to the user. It is recorded as LD11 in [llm/QUESTIONS.md](../llm/QUESTIONS.md).
