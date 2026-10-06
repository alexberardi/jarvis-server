# llm (Phase 3) questions for the user

These are asked **one at a time**, most consequential first. Record each answer here as a decision (LD1, LD2, …), then fold it into the owning doc. Technical questions have already been resolved in the docs, marked **Decided**.

**Already decided upstream, and not re-asked here:**
- PLAN §3.3: llama-server per slot, Metal on macOS, GPU auto-detected and overridable, multi-GPU.
- PLAN §7: the cut list.
- D8: bugs are fixed by default. The llm D8 fixes are listed in 00 §8.
- D9 / D40: date keys are regex only, once per turn.
- D11 / D12: settings hygiene and the catalog cleanup.

## Decisions

| # | Date | Question | Decision |
|---|---|---|---|
| LD1 | 2026-10-06 | LQ1 | **Slots are labels; memory sharing follows the model path.** `live` and `background` are labels, each configured with a model; background work ALWAYS uses the background label's model (it is never silently routed to the live model). Engine instances are keyed by (model path, load settings, device placement): two labels with the same key share one loaded llama-server instance (its parallel slots), the jarvisd equivalent of the legacy in-process llama.cpp backend sharing one `Llama` object when both labels use the same path. On limited hardware the user configures both labels with the same model. Different path or placement → separate engines. Fixes the legacy bug where background mirrored live whenever both named the same model even with different server URLs. No cgo, so llama.cpp in-process becomes a supervised llama-server for both legacy backends. |
| LD2 | 2026-10-06 | LQ2 | **Remote providers in v1: OpenAI-compatible only** (URL + model name + API key per slot label), offered in admin as "Use a remote or cloud model", off by default, with a privacy note. Covers OpenAI, OpenRouter, vLLM, MLX server, LM Studio, Ollama `/v1`, remote llama-server/jarvisd. The broken native Anthropic/Ollama paths are cut; native Anthropic is possible later. CI uses the same settings. |
| LD3 | 2026-10-06 | LQ3 | **No automatic model download; a first-class model manager instead** (user: "a really simple way to install one via Hugging Face"). jarvisd starts without a model (`/health`: LLM "not configured") and owns a model-management API: hardware-aware recommendations from the catalog, **install from Hugging Face** by picking a catalog entry or pasting a repo (and choosing a GGUF file/quant, with size and fit-for-VRAM shown), resumable downloads with progress and checksum verification on the durable queue, the matching llama-server engine fetched in the same job, list/delete installed models, and assigning models to the `live`/`background` labels (LD1). The UI is a first-class screen in admin (setup flow + a Models page), built on that API. |
| LD4 | 2026-10-06 | LQ4 | **Image requests follow the slot label strictly** (consistent with LD1): OCR vision is background work → background label; images in voice/chat turns → live. A slot whose model has no vision projector answers a clear error (no silent fallback to the other slot). Admin shows which labels can see images and offers adding the mmproj when installing a model (LD3). Prod fix at cutover: give the background model its projector. User expects to rework vision routing later. |

## Queue

### LQ1 [scope] One GPU (or none): should background share the live model?

**Context.**

Prod has two RTX 3090s and runs two engines: live on GPU 1, background on GPU 0. Both load the same 27B, but with different context and KV settings (01 §3.4, 05 §3.4). Most installs will have one GPU, or a Mac, and can't hold two copies of a model.

Today the legacy code shares one instance only when both slots name the same file. On the MBP that means one Qwen3-8B serves both slots.

**Options.**

- **(a) Shared engine.** On a single-card or Mac box, background uses the live engine.
  - jarvisd caps background work at one call at a time.
  - Voice has priority: background calls wait while a live call is running. This is a per-slot semaphore in the engine, plus llama-server's parallel slots.
- **(b) A second, smaller model** for background, on the same card, chosen from the catalog by the leftover VRAM.
- **(c) Always ask in the setup wizard.**

**Recommendation: (a) by default, with (b) and separate cards as options in admin.**
- Two engines are proposed automatically only when two cards are detected, or when one card fits both models.
- Prod keeps its two-engine layout on import.

### LQ2 [scope] Which remote and cloud LLM providers should jarvisd support in v1?

**Context.**

PLAN §2 says the REST backend is ported for OpenAI, Anthropic, Ollama and LM Studio. In practice:
- Only an OpenAI-compatible endpoint works today.
- The Anthropic and native Ollama code paths are broken and unused (01 §8.6).
- No admin screen offers cloud providers. Only CI uses OpenAI (`gpt-4.1-nano`), and that is set through env vars (05 §3.6).

An OpenAI-compatible client covers a lot: OpenAI, OpenRouter, vLLM, MLX's server, LM Studio, Ollama's `/v1`, llama-server, and another jarvisd.

Core principle 1 is "no cloud dependencies by default".

**Options.**

- **(a) OpenAI-compatible only.** Configure a URL, a model name and an API key per slot. It is offered in admin as "Use a remote or cloud model", off by default, with a privacy note.
- **(b) (a) plus native Anthropic.** That is an extra translator for messages, tools and streaming.
- **(c) (a), but hidden in admin.** Settings and env only, for CI and power users.

**Recommendation: (a).** Native Anthropic can come later if wanted. A CPU-only box then has a real path to a usable assistant. CI keeps working through the same settings.

### LQ3 [behaviour] First run: should jarvisd download a model by itself?

**Context.**

Models are 5–17 GB. The llama-server engine is another roughly 50–500 MB, depending on the GPU flavour.

Today nothing is automatic: the admin wizard downloads the model the user picks (05 §2). The PLAN goal is "download one file and run it".

**Options.**

- **(a) Wait for the user.** jarvisd starts with no model. `/health` reports the LLM as "not configured", and admin's setup screen recommends a model for the detected hardware. One click downloads it, with progress shown.
- **(b) Download the recommended model and engine automatically** on first start, on a background job.
- **(c) Download the engine automatically,** but wait for the user to choose the model.

**Recommendation: (a).** A multi-GB download should be a choice the user sees, and the recommendation does most of the work. Engines are fetched with the model, in the same job.

### LQ4 [behaviour] Which slot should image (vision) requests use?

**Context.**

- OCR's LLM-vision engine always asks for `background`. That is true in both the Python service and the Go OCR module (`internal/modules/ocr/engines.go:398-404`).
- Prod loads the vision projector on **live** only, so an image sent to background is refused with 400.
- LLM vision for OCR is off by default (`ocr.enable_llm_proxy_vision`).
- Recipes sends no images.

**Options.**

- **(a) Use whichever slot can see images,** preferring background so voice isn't slowed, and falling back to live.
- **(b) Background only.** The background model must have a projector for OCR vision to work. Admin warns when it doesn't.
- **(c) Keep today's behaviour** (background only, silent 400).

**Recommendation: (a).** OCR vision then works on prod's current layout. Background stays preferred once it has a projector.

### LQ5 [behaviour] Prod cutover: what happens to its hand-tuned model setup?

**Context.**

Prod's working setup is hand-edited compose plus `.env` (05 §3.2, §3.4):
- live: 27B with vision, 13,312-token context split across 4 parallel requests, thinking off
- background: 27B, 131,072-token context, one request at a time, flash attention, q8 KV cache

`.models/` holds 150 GB, of which about 130 GB is unused experiments.

**Options.**

- **(a) Translate the running config 1:1** into jarvisd settings (01 §5 has the mapping). Register the existing model files in place, with no copy. Admin shows the unused files with a delete button.
- **(b) Start clean** from the catalog defaults, and re-download only the 27B and its projector.

**Recommendation: (a).** No 17 GB re-download and no behaviour change at cutover. Clean up through admin afterwards.

### LQ6 [behaviour] Embeddings: keep MiniLM, or upgrade while memories are re-embedded anyway?

**Context.**

CC's memory uses 384-dimension `all-MiniLM-L6-v2` vectors today. Moving to llama-server changes the numbers slightly, so the import re-embeds every memory either way (PLAN §3.3, 01 §3.7).

A stronger small model, such as bge-small or nomic-embed (about 100–300 MB), would likely improve recall. It would also mean recalibrating CC's similarity thresholds, `memory.*`.

**Options.**

- **(a) Keep MiniLM.** Same dimensions, same thresholds, the least risk.
- **(b) Switch to a better model now,** and recalibrate the thresholds on prod's memories.

**Recommendation: (a) for the port.** Make the embedding model a setting, so (b) can be tried after the cutover against a recall eval.

### LQ7 [behaviour] Should background jobs be allowed to "think" without a limit?

**Context.**

Prod's background slot defaults to unrestricted thinking (`reasoning_budget=-1`). Callers that don't ask otherwise get long reasoning before their answer:
- deep-research summaries
- characterization
- the errand planner (6,000-token budget)
- recipes' grocery matching

At about 43 tokens/s, a long think block can take minutes. Memory extraction, the situation matcher, signals and phone drafts already turn thinking off per request (04 §3.3).

**Options.**

- **(a) Keep today's behaviour:** unrestricted unless the caller says otherwise. Admin can change the per-slot default.
- **(b) Cap it,** for example at 2,048 thinking tokens. llama-server can't enforce a cap on Qwen3.5-family models (04 §3.3), so in practice this means "off".
- **(c) Off by default** for background.

**Recommendation: (a).** Quality on slow jobs is the point of the background slot. The setting is already per slot and visible in admin.
