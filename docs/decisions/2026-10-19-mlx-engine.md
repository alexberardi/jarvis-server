# MLX on Apple Silicon is a supervised jarvisd engine (the Python engine layer)

- **Date:** 2026-10-19 (user; built by loop #299)

jarvisd runs `mlx_lm.server` on Apple Silicon as a supervised local engine, the same way it runs
llama-server: fetched on demand, started on a loopback port, health-checked, restarted on a crash,
stopped with jarvisd, and reached through the same OpenAI-compatible client. Two labels on the
same MLX model share one server (LD1).

The jarvisd binary stays pure Go: the no-Python rule of [PLAN §3](../PLAN.md) holds for the
server itself. Python appears only as a downloaded engine, on Macs that choose an MLX model. The
install is self-contained under `<home>/engines/mlx-lm/<version>-metal/`: uv (a pinned,
sha256-checked release, like llama-server's) installs a uv-managed CPython there and a venv with
mlx-lm's whole package set, hash-locked (`--require-hashes`, wheels only). No system Python, no
Homebrew. The directory counts as installed only after an import check passes.

This reverses the 2026-10-06 cut of MLX as an in-process backend ([PLAN §7](../PLAN.md),
[llm/00-overview.md](../llm/00-overview.md), [llm/01-backends-and-engines.md](../llm/01-backends-and-engines.md)),
which was never put to the user. It is recorded as LD9 in [llm/QUESTIONS.md](../llm/QUESTIONS.md)
and supersedes LD7's "llama-server is the only local engine" for MLX. The engine layer is built
generically (a kind with a pinned package set, a platform gate, a uv env, a launch command,
supervision, an OpenAI-compatible endpoint) because the user wants vLLM (#300) and Transformers
(#301) back as engines on the same layer; those are separate items.

How it is built: [llm/01-backends-and-engines.md §3.11](../llm/01-backends-and-engines.md).
