# Custom agents' context: through the memory path, not the system prompt (proposed)

- **Date:** 2026-10-21 (proposed by the build agent on item #405; **awaiting the user's answer**,
  nothing is built yet)

The SDK's `IJarvisAgent.get_context_data()` docstring and the extending/agents tutorial
(jarvis-docs#202, #398) promise that an agent's context is given to the LLM. On jarvisd that is
true only for the smart-home agent: the node's agent payload reaches the prompt as `ctx.Agents`
(`turn.go`, `warmup.go`), and its only readers are `deviceAgentData`
(`internal/modules/cc/prompts/sections.go`) and the `get_ha_entities` server tool
(`internal/modules/cc/servertools/haentities.go`), which read the `home_assistant` key (else
`device_agent`). Every other key, i.e. every custom agent, is stored and never read.

A second path exists: `agentContextHint` (`internal/modules/cc/memory_agent.go`, docs/cc/04
§3.3 C) appends the household's agent-injected memory rows (`POST /memories/inject`, TTL'd,
deduplicated per key) that match the utterance to the user message, after the turn hints. It is
bounded by `memory.agent_context_max_results` (5) and `memory.agent_context_max_chars` (500
chars, ~125 tokens) and adds nothing when nothing matches. `memory.agent_context_enabled`
defaults on; `model.advanced_context` (the per-household switch) defaults **off**. The Python
node reportedly never calls `inject_memories` (jarvis-node-setup `clients/rest_client.py:190` has
no callers; not checked here, that repo is not in this worktree).

## Options

- **A. Render the other agents' keys into the system prompt**, capped per agent and in total
  (a generic `<agent_context>` block, one line of compact JSON per agent).
- **B. Use the memory path.** The node turns each custom agent's `get_context_data()` into
  inject rows (key per agent and fact, a TTL from the agent's refresh interval) and calls
  `/memories/inject`; jarvisd already retrieves only what matches the utterance.
- **C. Drop the promise** from the SDK docstring and the tutorial: `get_context_data()` feeds the
  node's own commands only.

## What A costs, measured

`scripts/toolbench` on this branch's captured prompts (sandbox `loop-405`, the installed jarvisd's
live model, qwen3.5-9b), the 69 standard cases (desktop toolset examples plus first turns of the
pipeline scenarios), 3 repeats each (207 calls per side). "After" is the captured prompt plus one
system message, before the user turn, carrying three realistic custom agents (weather with a
3-day forecast, a calendar with 5 events, 2 package shipments): 1,163 chars, under a plausible
600-per-agent / 1,200-total cap.

| | before | after (option A) |
|---|---|---|
| prompt tokens, median per turn | 4,753 | 5,211 (+458, +9.6 %) |
| tool accuracy | 88.9 % | 82.6 % |
| tool + args accuracy | 77.8 % | 72.0 % |
| median latency (warm) | 0.67 s | 0.64 s |

The +458 tokens is the same on every turn (the block does not depend on the utterance), so it
undoes about a tenth of the prompt diet (`2026-10-20-prompt-diet.md`) and is paid cold on every
uncached turn, on the slow Macs most of all. The accuracy drop held across the single run and the
3-repeat run (a single run: 89.9 → 81.2 % tool), mostly desktop and browser commands that have
nothing to do with the data: unrelated context distracts the 9B. One model, one payload; the 4B
was not measured.

## Recommendation: B, and fix the docs now

- **B** keeps the prompt diet: zero tokens when the utterance does not touch an agent's data, at
  most ~125 tokens when it does, and the rows sit next to the user turn where recency helps. The
  retrieval, TTL, dedup, caps and per-household switch are already built and tested on jarvisd.
  What it needs: node work (call `inject_memories` from the agent scheduler with each agent's
  context flattened to short facts; possibly an optional SDK hook such as
  `get_context_facts() -> list[str]` so an agent author writes the sentences rather than the node
  guessing from a dict), and a decision on `model.advanced_context`'s default, which gates it
  today.
- **A** is rejected on the numbers above. A cap small enough to protect the prompt is too small
  to be useful for a calendar or forecast.
- **C now, regardless**: until B ships, the SDK docstring and the tutorial (jarvis-docs#398)
  should say that `get_context_data()` is read by the node's commands, and that only the
  smart-home agent's data reaches the model, so nobody builds on the promise.

## Open question for the user

1. Go with B (node injects custom agents' context as memory rows; jarvisd retrieves per turn)
   and C for the docs in the meantime? Or C alone, dropping the promise for good?
2. If B: should `model.advanced_context` default to on (it gates agent context per household),
   or should custom agent context get its own default-on switch, leaving `advanced_context` off?
3. If B: an optional SDK method that returns the agent's facts as sentences, or the node
   flattening `get_context_data()`'s dict itself?

Follow-up items (the node and SDK change, the docs fix) come after the answer. Links:
`../cc/prompt-contract.md` (the `agents` section), `../cc/04-memory-and-knowledge.md` §3.3,
`2026-10-20-prompt-diet.md`.
