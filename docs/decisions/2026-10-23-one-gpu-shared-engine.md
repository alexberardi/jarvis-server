# One GPU, two labels on one model: share the engine, refuse what doesn't fit, don't retry an OOM

- **Date:** 2026-10-23 (agent, loop #562; the user asked for the three behaviours and left the
  rules to the agent)

On the devbox (one 12 GiB RTX), live and background named the same Qwen3.5-9B with settings that
differed in one load field, so their instance keys differed (LD1) and jarvisd started two full
~7.5 GiB copies at once. One won the race; the other exited 1 out of GPU memory, burnt its five
supervisor restarts and three resolver retries, and its label answered every turn with
`503 model_not_loaded … press Retry`, a Retry that could never succeed. The admin's overcommit
warning stayed quiet because `Manager.Residents` merged labels on fewer fields than the resolver.

**1. A second engine is weighed before it starts.** `Resolver.Fits` (set to
`models.Manager.PlacementFit`) is asked, before a new GPU engine is started, whether it fits on
its card next to the GPU engines other labels already hold (their need from the same estimate as
the Models page's fit and overcommit warning). It says no only when the newcomer **would fit the
card alone** but not next to them: a model too big for the card on its own is left to llama.cpp to
offload as it can, as before. Engines with only some layers on the GPU (`gpu_layers` other than
999 or negative) aren't judged, and neither are CPU engines: their need is unknown or not the
card's. When the card holds both, nothing changes: background with its own context still gets its
own engine (`TestResolveSharesOneEngine`), because a separate engine means background work never
waits on live's slots.

**2. When it doesn't fit, share if the engine can serve the label; refuse otherwise.** If an
engine already runs the same model with the same build, placement (layers, devices, split),
projector, template and extra args, and differs only in context, slots, KV cache type or flash
attention, the label is bound to that engine and gets *its* context and slots (the endpoint
reports them, so requests are shaped for the engine that really serves them). Those four fields
change memory and throughput, not what the model answers. Status carries a note naming the engine
and the reason. We don't restart the running engine with the larger context or the sum of the
slots: that would interrupt the label already serving, and the operator can make the settings
equal if they want that. Any other difference can change the answers (projector, template, extra
args) or the placement, so the label is refused instead: state `does_not_fit`, with a reason that
names the card, its size, both needs and what to change (`engine=shared` for background, matching
settings, a smaller model, lower context, another card or the CPU). Nothing is started, so there
is no Retry loop. The label is weighed again on every reconcile and starts by itself once the
other engine goes away.

**3. An engine that ran out of GPU memory is not restarted or retried.** The supervisor gets a
`Fatal` pattern (`engines.Spec.Fatal`). The resolver sets it to llama.cpp's and torch's
out-of-memory lines (`cudaMalloc failed`, `out of memory`, `unable to allocate … buffer`,
`ErrorOutOfDeviceMemory`). When an exit follows such a line, the engine fails at once, with no
backoff restarts, and the resolver gives up without its timed retries. The label's reason says
"out of GPU memory (<line>) … free GPU memory, lower the context or pick a smaller model, then
press Retry". Retry stays: once something has freed the card, starting again can work, and a
settings change still starts a fresh engine.

**Overcommit warning.** `Residents` now groups labels by the engine they run on when they're
running (`Endpoint.Engine`), and otherwise by `engine.EngineIdentity` (the instance key without
the build), so it counts the engines the resolver would start.
