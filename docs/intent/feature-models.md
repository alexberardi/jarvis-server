# Intent: feature models

Confirmed by the user on 2026-11-11, via `interview-me`. The spec this produced is
[docs/llm/SPEC-feature-models.md](../llm/SPEC-feature-models.md).

## Statement of intent

- **Outcome:** the hardcoded model choices at ~30 call sites become per-feature settings, picked in
  the Feature models card that already exists on the admin Models page.
- **User:** self-hosters whose hardware or priorities differ from the maintainer's — and the
  maintainer later, when revising a default without stomping someone's deliberate choice.
- **Why now:** code debt, not a routing need. Nothing is on the wrong model today. In the user's
  words: *"it's just some code debt I think we have and where we should abstract my opinion out and
  allow the user to customize."*
- **Success:** 13 rows, each `Auto — {label}` plus live/background/cloud; every default reproduces
  today's behavior, proven by test before any call site changes.
- **Constraint:** three slots, permanently. *"Running 2 models requires pretty heavy duty hardware.
  Needing a slot for a third seems kinda crazy when you theoretically could just have one really
  large model for handling any critical paths and a fast model for voice and stuff."* `cloud`
  covers the specialist case at no hardware cost.
- **Out of scope:** new or user-defined slots, per-feature model ids, per-household choice, any
  API-selectable slot.

## What the interview changed

The first draft of the spec was wrong in four ways. Recording them so the reasoning is not lost:

1. **No forcing function.** The opening hypothesis was that a specific model needed a home (the
   Qwen3-1.7B tools fine-tune). It did not. That matters: with no concrete routing need, the risk
   is not mis-routing, it is shipping pickers nobody touches — which is why the row count and the
   UI cost were argued rather than assumed.
2. **`auto` is for the maintainer, not the user.** `Memory: Background` reads better than
   `Memory: Auto (background)`. `auto` is kept anyway, because it is the only way to tell "the user
   chose background" from "we chose background" — and since the current values are explicitly the
   maintainer's opinion, some will be revised once the pickers exist and people report what they
   run. Displayed as `Auto — {label}`.
3. **The legacy recipes keys get renamed, not kept.** The first draft kept
   `llm.full_model_name`/`llm.background_model_name` to avoid a migration. But surfacing
   `llm.background_model_name` in the UI as "Grocery matching" is the exact debt this change is
   about, and nothing outside jarvis-server reads them. `llm.lightweight_model_name` is folded into
   Recipe import rather than given a row: 14 rows became 13.
4. **The test plan was heavier than it needed to be.** The proposal was a mock that replies "I'm
   the background model". The thing under test is `ChatRequest.Label`, so a reply is not needed —
   and every affected package already has a `fakeLLM` recording the request, with `req.Label`
   assertions already in place at several of these call sites. No new fake; existing assertions
   count as the default-equivalence proof.

## Decided, so it is not re-litigated

| Question | Decision |
|---|---|
| Granularity | 13 rows, as the spec's table; not ~6 coarse groups, not one row per call site |
| A fourth or user-defined slot | No. Hardware ceiling is two local models; `cloud` is the third |
| `auto` vs. a literal default | Keep `auto`, display `Auto — {label}` |
| Legacy recipes keys | Rename with a goose migration; fold the lightweight pass away |
| A slot with no vision projector | Disabled in the picker with the reason shown |
| Setting scope | Superuser, system-wide. Household is an access change and needs its own ask |
| Delivery | A stack of four PRs; isolated worktree (`feature-models`) |
