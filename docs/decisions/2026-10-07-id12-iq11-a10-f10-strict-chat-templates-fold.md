# ID12 (IQ11, A10 F10): strict chat templates, fold + pin

- **Date:** 2026-10-07
- **Moved from** the STATUS.md decision log on 2026-10-14.

**ID12 (IQ11, A10 F10): strict chat templates, fold + pin** (user). Endpoints flagged `fold_system_messages` (catalog: Qwen 3.5 9B and Qwen 3.8 27B; per-label `auto/on/off`, remote only when on) get later system messages folded into the adjacent user turn as a `<system>` block, plus a minimal user turn when none exists, in `llm.Service` just before the engine; history, prompts and goldens unchanged. Every catalog LLM pins its chat template (embedded, sha256-checked, `--chat-template-file`). Details: `docs/llm/04-prompt-shaping.md` §3.7, `docs/llm/06-model-manager-api.md`.
