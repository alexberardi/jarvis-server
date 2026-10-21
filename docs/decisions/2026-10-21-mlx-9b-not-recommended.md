# The MLX Qwen 3.5 9B 4-bit is not recommended; a 16 GB Mac gets the GGUF

- **Date:** 2026-10-21 (build agent, item #371; changes [2026-10-20-model-folders](2026-10-20-model-folders.md))

Measured on the Mac mini (M4, 16 GB, test4 build with the prompt diet), `scripts/toolbench` on the
80-utterance bench corpus, `--fold`, the captured post-diet voice prompt (31 tools, temperature
0.4, `enable_thinking: false`):

| build | engine | tool accuracy |
|---|---|---|
| `mlx-community/Qwen3.5-9B-4bit` (affine 4-bit, group 64) | mlx_lm.server 0.32.2 | 88.8% (71/80) |
| `unsloth/Qwen3.5-9B-GGUF` Q4_K_M | llama-server b11457 | 100% (80/80) |

The MLX build answered nine cases itself instead of calling the tool: eight arithmetic or unit
conversions ("what's 15 times 12" → "That's 180.", 100/4, 9×7, 250/5, 45+55, 200−75, 144/12,
"convert 3 pounds to ounces") and "add apples to the grocery list" ("Got it, I'll add apples…").

**It is not the chat template.** The MLX repo's `chat_template.jinja` is the upstream Qwen 3.5
template (the same file as llama.cpp's `models/templates/Qwen3.5-4B.jinja`); the catalog's pinned
`qwen3.5.jinja` is the unsloth GGUF's embedded template. They differ only in the thinking default
when `enable_thinking` is unset and in how they read a tool call's `arguments` in history. With
`enable_thinking: false` and no tool-call history, the bench's request renders byte-identically
through both (mlx_lm.server's `apply_chat_template` path), and every one of the 80 cases had the
same prompt token count on both engines. mlx_lm.server 0.32.2 honours `chat_template_kwargs`, the
temperature and the tools; it ignores `tool_choice` (we send `auto`). Pinning the template on
MLX would change nothing.

What differs is the weights (MLX affine 4-bit vs Q4_K_M, which keeps some tensors at 6-bit) and the
sampler: jarvisd sends only `temperature`, so llama-server applies its defaults (top-k 40, top-p
0.95, min-p 0.05) while mlx_lm.server samples the full distribution (top-k 0, top-p 1, min-p 0).
Neither was measured separately: the agent had no Metal device and no Hugging Face access to try
a 6-bit, 8-bit or DWQ build.

**Decision:** the catalog entry gets `no_recommend`, so `Recommend` skips it and a 16 GB Mac is
recommended the 9B GGUF on llama.cpp (Metal). The MLX 9B stays in the catalog and installable,
and its notes say why it isn't the pick. The 27B and 35B-A3B MLX builds (32 GB Macs) are
unmeasured and stay recommended there. Revisit with a bench of `Qwen3.5-9B-6bit`, a DWQ 4-bit,
or MLX with llama-server's sampler defaults passed through: an MLX 9B entry that reaches ≥98% on
the same bench can drop `no_recommend`.
