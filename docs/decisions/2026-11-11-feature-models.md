# A model slot per built-in feature

- **Date:** 2026-11-11 (user asked for it; interview in [docs/intent/feature-models.md](../intent/feature-models.md))

The user (2026-11-11): "instead of us hardcoding certain features to specific models like live,
background, cloud, etc. we're going to leverage the existing Feature Models section in the admin
panel and break features out in there and allow the user to specify while setting the default to
the current behavior."

The driver was debt, not a routing need: "it's just some code debt I think we have and where we
should abstract my opinion out and allow the user to customize." Nothing was on the wrong model.

**Thirteen features, one pattern.** [2026-10-22-cloud-model-slot.md](2026-10-22-cloud-model-slot.md)
built the mechanism — `llm.SlotSetting` (`auto` | `live` | `background` | `cloud`) and the admin
Models page's "Feature models" card — and applied it to `computer.model` alone. Every other
built-in feature now has one too: `conversation.model`, `memory.model`, `signals.model`,
`watches.model`, `workflows.model`, `research.model`, `errands.model`, `phone_calls.model`,
`phone_calls.planning_model`, `ocr.model`, `recipes.import.model`, `recipes.grocery.model`. Keys
follow each feature's existing prefix, as `computer.model` does. All are superuser and
system-wide; a per-household choice remains an access change nobody has asked for.

**`auto` is kept even where it is a static alias.** `Memory: Background` reads better than
`Auto — background`, but `auto` is the only way to distinguish "the user chose background" from
"we chose background". Since the current values are explicitly the maintainer's opinion, some will
be revised; `auto` is what makes that possible without overriding a deliberate choice. The card
shows `Auto — background`, naming the slot it resolves to.

**Three slots, permanently.** The user ruled out a fourth: "running 2 models requires pretty heavy
duty hardware. Needing a slot for a third seems kinda crazy when you theoretically could just have
one really large model for handling any critical paths and a fast model for voice and stuff."
`cloud` covers the specialist case at no hardware cost.

**Alphabetical, not grouped.** The card first grouped rows by what they default to (someone is
waiting / runs in the background / needs to see images). That was wrong and was reverted: the
default slot is the thing the card edits, so moving Conversation to background left it under
"Someone is waiting" with nothing re-sorting. Sorting by title is stable under every change.

**The legacy recipes keys were renamed.** `llm.full_model_name` → `recipes.import.model`,
`llm.background_model_name` → `recipes.grocery.model`, and `llm.lightweight_model_name` folded
into the import slot (a cheap OCR-text structuring pass inside an import, not a feature a user
would name). `settings.Service.Rename` and `Forget` carry stored values over at startup; an
install that pointed the lightweight pass at a different slot is warned before its value is
dropped. Nothing outside this repo reads these keys and the `JARVIS_*_MODEL_NAME` env fallbacks
were cut at the port, so this is a settings rename, not a wire change.

**Compaction follows the conversation.** It measured the *live* slot's context window to decide
when to compact; it now measures the slot `conversation.model` resolves to, or a conversation
moved off live compacts at the wrong threshold.

**What stays hardcoded, on purpose.** The public async-job API (`llm/jobs.go`, `llm/validate.go`)
is pinned to `background` by contract, and the node's `/node/llm/chat` `model` argument is
normalized so no node or API caller can reach the cloud slot. `TestNoFeatureHardcodesItsModel`
allows exactly those and fails any other chat request pinned to a slot in code.

**Two behavior changes, both on a misconfigured install only.** The settings validator now rejects
a free-text model name where the old recipes string settings accepted one and silently resolved it
to live; and a value that does get past it takes the feature's own default rather than live.
