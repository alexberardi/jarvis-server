# The computer loop's reply shape is enforced by the engine, not only repaired after

- **Date:** 2026-11-17 (user)

A local model's answer in a computer task should be held to its shape by the engine's grammar,
so a malformed answer can't be produced, instead of being repaired after the fact. The user
called this "an important facet" and asked that it not be lost behind the model-catalogue work.

**Why.** Every answer a small model lost on 2026-11-17 was a shape failure, not a wrong
decision: Gemma 4 12B wrote its thought and its answer as two objects, or went on after the
answer with a code fence; a `type` action came with no text; Qwen3.5-9B wrote an intent over
the 200-character limit after 20 good rounds. Each got its own repair in
`internal/modules/cc/computer/parse.go`, and the next model will find another.

**What exists today.** `response_format: json_object` in the LLM module is the legacy JSON
mode: it adds an instruction to the prompt, repairs and validates the reply, and retries once
(`internal/modules/llm/service.go`). The schema is never sent to the engine, so it constrains
nothing at the source. The computer loop does not use it and should not: it would change the
prompt of the model that works.

**The shape (user).** One flat object with a required `type` enum (`actions`, `done`, `ask`)
ahead of the fields it selects, and each action's `kind` an enum: no union of four objects for
the grammar to choose between. Rules a flat schema can't state (a `done` needs a summary, a
`type` action needs text) stay as checks in code.

**What it needs.**
1. The LLM module passes a request's schema through to a local engine that can enforce it
   (llama-server takes a JSON schema and builds a grammar). Engines and remote providers that
   can't keep today's behaviour.
2. The computer loop sends its reply schema on every round.
3. A before/after on Gemma 4 12B and 26B-A4B, since grammar-constrained output can slow a
   model and can cost a small one in content: the three browser workflows, several runs each.
4. The repairs in `parse.go` stay underneath, for models whose engine takes no schema.

**Order.** Before Gemma 4 12B is offered in the install wizard: a model recommended for a
12 GB card should not depend on after-the-fact repairs to be read.

**Built 2026-11-17.** `llm.ResponseSchema` (`response_format` type `json_schema`) goes to a
local llama-server and, by the user's choice the same day, through to a remote that takes
llama-server's controls (`remote_thinking`: another Jarvis server, which hands it to its own
engine, or a llama-server). A Jarvis server from before this ignores the field and answers as
it did (checked against prod's build). OpenAI, Anthropic and Gemini get no schema: their own
schema modes refuse optional fields. The shape gained one thing over the flat object above: an
action's `kind` comes first and brings the fields that kind needs, because the flat version
still let through a `set_value` with no ref and an empty action list.

**Not measured yet.** Gemma 4 26B-A4B has never run with the schema: this box can't hold it,
and prod's build predates it. Check the form fill and the Craigslist sweep on prod as part of
the release that carries this.
