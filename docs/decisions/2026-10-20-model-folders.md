# Hugging Face model folders: install, prompt provider and fold inference

- **Date:** 2026-10-20 (build agent, item #351; follows [2026-10-19-mlx-engine](2026-10-19-mlx-engine.md))

A Hugging Face model folder (config.json + `*.safetensors`, with its tokenizer and chat template)
installs as **one model whose path is the folder** (`<models>/<owner>--<name>`), through the same
durable, sliced, resumable per-file download as a split GGUF. Only the top-level files an engine
reads are fetched (weights, `*.json`, `*.jinja`, tokenizer files); READMEs, images, GGUFs,
subfolders and remote code are skipped. The repo's Browse lists the folder as one choice next to
its GGUFs, with the engine that runs it here (MLX on Apple Silicon; vLLM or Transformers
elsewhere) or why none can. A repo whose name says MLX (`mlx-community/…`) only runs on MLX, whatever
the label's `dir_engine` says, and is refused off Apple Silicon.

A catalog GGUF gets its prompt provider and fold flag from its entry; a folder gets them from
its config.json (`model_type`, `architectures`, `text_config`) and name (`InferDirModel`):

- Qwen 3.5 / 3.8: fold on (their strict template rejects a later system message and needs a user
  turn, as their GGUF twins say); `Qwen3_5_9B_Compressed` up to 12B, `Qwen3_14B_Compressed` above,
  matching the GGUF catalog's 27B.
- Qwen 3: `Qwen3_8B_Compressed` up to 10B, `Qwen3_14B_Compressed` above; fold off.
- The parameter count comes from the name (`9B`), else an estimate from the config's sizes.
- Anything else: no provider. The install (and register) is then refused until the request names
  one (`prompt_provider`), and the page asks for it, so a label never runs a model cc can't prompt
  (`unknown prompt provider ""`). A label set to a folder path by hand gets the same inference.

Python engines are text-only: a folder label drops its projector and says so in a note on the
label, not as a problem.

The catalog lists MLX entries beside the GGUFs (`directory: true`, listed at install time, each
file checked against the listing's sha256). `min_memory_mb` marks an entry too big below that
much memory, so the 27B and 35B-A3B builds need a 32 GB Mac and the recommendation for a 16 GB
Apple Silicon machine is `mlx-community/Qwen3.5-9B-4bit` (about 5.1 GB). On Metal, the
recommendation prefers a fitting MLX entry over a GGUF.
