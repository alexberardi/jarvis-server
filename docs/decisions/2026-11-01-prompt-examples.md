# Tool examples in the prompt, on for small models only

- **Date:** 2026-11-01 (user: "put examples behind a setting so we don't blow up the prompt size for larger
  models. Default it to on from the quick select for the 1.7 model, maybe 4b too")

The built-in prompts get `{{examples}}`: each offered tool's primary example (the `examples` a node already
sends with its tools), written as the `<tool_call>` the model should make. `llm.prompt_examples` (`auto` |
`on` | `off`) turns it on; `auto` follows the live model's catalog entry, whose `prompt_examples` is true for
`qwen3-1.7b` and `qwen3-4b` only. Off renders nothing, so the built-in prompts are byte-identical for every
other model (the golden fixtures are unchanged). The admin's Models page shows it with the other LLM modes.

Measured with `scripts/toolbench` on Qwen3 1.7B Q8_0 (CPU, 3 runs, the Qwen3_8B_Compressed prompt with native
tools as a local install runs it). Every score is held out: a case whose utterance the prompt shows is not
scored.

| Held-out set | No examples | Examples as `<tool_call>` |
| --- | --- | --- |
| Legacy voice corpus (75) | 90.7% | 97.3% |
| Go node's examples, right tool (74) | 70.3% | 88.7% |
| Go node's examples, tool + arguments | 55.4% | 69.8% |

The prompt grows by about 700 tokens (median 4,710 → 5,435).

**The format is the decision.** Shown as bare `"said" → tool {args}` lines, the same examples dropped the corpus
to 61%: the model wrote `get_current_time {"location":"Tokyo"}` as its reply instead of calling the tool.
Described in prose they gained little (92.5% on the corpus). As `<tool_call>` blocks, a copied example is a real
call. That is the Qwen format; another model family that wants examples needs its own rendering.

Only the primary example per tool: all of them (about 1,700 tokens) scored no better on the corpus.

Supersedes the earlier idea of a 1.7B-specific prompt: a draft scored the same as Qwen3_8B_Compressed (71.3% vs
71.7%) and was dropped.
