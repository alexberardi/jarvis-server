# Remote labels can speak the Anthropic Messages API

- **Date:** 2026-10-12
- **Moved from** the STATUS.md decision log on 2026-10-14.

**Remote labels can speak the Anthropic Messages API** (`llm.<label>.remote_api`: `auto` = anthropic for `api.anthropic.com`, `openai`, `anthropic`; [llm/06 "Anthropic remote"](../llm/06-model-manager-api.md)). Reverses LD2's OpenAI-only scope for Anthropic. A translation layer (`internal/modules/llm/anthropic.go`) ported from jarvis-llm-proxy-api#75 (@davidalee), plus its one usage bug fixed (a `message_delta` no longer drops the cache counts from the prompt total). Chat, tools and streaming; embeddings refused as misconfigured.
